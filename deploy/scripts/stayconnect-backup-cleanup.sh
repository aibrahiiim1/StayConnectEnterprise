#!/usr/bin/env bash
#
# stayconnect-backup-cleanup — safe, auditable retention for deployment and
# rollback artifacts on both Central and Appliance hosts.
#
# DEFAULT is DRY-RUN (prints the plan, deletes nothing). Pass --apply to execute.
# Idempotent, fail-safe (never deletes protected / current / previous / pinned /
# newest-full-backup material; aborts a group rather than guess if state is bad).
#
# Usage:
#   stayconnect-backup-cleanup            # dry-run: show KEEP / DELETE / PIN / PROTECTED
#   stayconnect-backup-cleanup --apply    # perform deletions
#   stayconnect-backup-cleanup --json     # print status JSON only
#
set -uo pipefail

CONF=/etc/stayconnect/backup-retention.conf
[ -r "$CONF" ] && . "$CONF"
: "${KEEP_BINARIES:=5}"      # per binary name (ctrlapi / scd / edged / acctd)
: "${KEEP_RELEASES:=5}"      # per UI app (cloud-admin / hotel-admin), on top of current+previous
: "${KEEP_DB:=7}"            # DB dumps, newest always kept regardless
: "${KEEP_CONFIG:=3}"        # config *.bak* files
: "${DISK_WARN:=80}"         # % used -> warn
: "${DISK_CRIT:=90}"         # % used -> critical

ROOT=/opt/stayconnect
PINS=/etc/stayconnect/backup-retention.pins
LOGDIR=/var/log/stayconnect
LOG="$LOGDIR/backup-cleanup.log"
STATUS="$ROOT/backup-retention-status.json"
TEXTFILE_DIRS=(/var/lib/node_exporter/textfile_collector /var/lib/prometheus/node-exporter)

APPLY=0; JSON_ONLY=0
for a in "$@"; do
  case "$a" in
    --apply) APPLY=1 ;;
    --json)  JSON_ONLY=1 ;;
    --dry-run) APPLY=0 ;;
  esac
done

mkdir -p "$LOGDIR"
NOW="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

# ---- role detection (fail-safe: unknown role deletes nothing) -----------------
# Discriminate by the UI release symlink each host owns: Central serves cloud-admin,
# the Appliance serves hotel-admin. Neither host has both. (A stray ctrlapi binary
# can exist on the Appliance, so binary presence alone is not reliable.)
ROLE=unknown
if [ -L "$ROOT/cloud-admin-current" ]; then ROLE=central
elif [ -L "$ROOT/hotel-admin" ]; then ROLE=appliance
fi

# ---- decision accumulators ---------------------------------------------------
declare -a R_DEL R_KEEP R_PIN R_PROT
FAILURES=0
FAIL_MSGS=""

fail() { FAILURES=$((FAILURES+1)); FAIL_MSGS="${FAIL_MSGS}${FAIL_MSGS:+; }$1"; }

is_pinned() {
  local p="$1"
  [ -r "$PINS" ] && grep -qxF "$p" "$PINS" 2>/dev/null && return 0
  [ -e "${p}.pinned" ] && return 0
  return 1
}

# retain_group LABEL KEEP_N ALWAYSKEEP_CSV <candidate paths...>
# Sorts candidates newest-first by mtime; keeps newest KEEP_N; anything in
# ALWAYSKEEP_CSV or pinned is always kept; everything else is deleted.
retain_group() {
  local label="$1" keep="$2" always="$3"; shift 3
  local -a cand=("$@")
  [ "${#cand[@]}" -eq 0 ] && return 0
  # newest-first
  local sorted
  sorted="$(ls -1dt "${cand[@]}" 2>/dev/null)"
  local kept=0 line
  while IFS= read -r line; do
    [ -z "$line" ] && continue
    if [[ ",$always," == *",$line,"* ]]; then
      R_KEEP+=("$label|$line|current/previous/required"); continue
    fi
    if is_pinned "$line"; then R_PIN+=("$label|$line|operator-pinned"); continue; fi
    if [ "$kept" -lt "$keep" ]; then
      R_KEEP+=("$label|$line|within newest $keep"); kept=$((kept+1)); continue
    fi
    R_DEL+=("$label|$line|older than newest $keep")
  done <<< "$sorted"
}

# DB dumps: keep newest KEEP_DB, and the newest is ALWAYS kept (never-delete rule).
retain_db() {
  local label="$1" keep="$2"; shift 2
  local -a cand=("$@")
  [ "${#cand[@]}" -eq 0 ] && return 0
  local sorted kept=0 first=1 line
  sorted="$(ls -1dt "${cand[@]}" 2>/dev/null)"
  while IFS= read -r line; do
    [ -z "$line" ] && continue
    if [ "$first" = 1 ]; then R_KEEP+=("$label|$line|newest full backup (never delete)"); first=0; kept=$((kept+1)); continue; fi
    if is_pinned "$line"; then R_PIN+=("$label|$line|operator-pinned"); continue; fi
    if [ "$kept" -lt "$keep" ]; then R_KEEP+=("$label|$line|within newest $keep"); kept=$((kept+1)); continue; fi
    R_DEL+=("$label|$line|older than newest $keep")
  done <<< "$sorted"
}

symlink_target() { readlink -f "$1" 2>/dev/null; }

# ---- build plan per role -----------------------------------------------------
ROLLBACK_VALID=false
declare -a PROTECTED_DIRS

plan_central() {
  # Never-delete: live binary, current + previous release, PKI custody, migration
  # backups, trust anchors.
  PROTECTED_DIRS=("$ROOT/ca-ceremony-backup" "$ROOT/nats-migration-backup")
  for d in "${PROTECTED_DIRS[@]}"; do [ -e "$d" ] && R_PROT+=("pki/migration|$d|custody/recovery — never delete"); done
  for f in /etc/stayconnect/*.key /etc/stayconnect/*.pub /etc/stayconnect/*.crt; do
    [ -e "$f" ] && R_PROT+=("trust-anchor|$f|trust anchor — never delete"); done

  # ctrlapi binary backups
  retain_group "ctrlapi-binary" "$KEEP_BINARIES" "" $(ls -1d "$ROOT"/bin/ctrlapi.bak* "$ROOT"/bin/ctrlapi.prev* 2>/dev/null)

  # cloud-admin releases (protect current + previous symlink targets)
  local cur prev
  cur="$(symlink_target "$ROOT/cloud-admin-current")"
  prev="$(symlink_target "$ROOT/cloud-admin-current.previous")"
  if [ -d "$cur" ] && [ -d "$prev" ]; then ROLLBACK_VALID=true
  else fail "cloud-admin rollback path invalid (current=$cur previous=$prev)"; fi
  retain_group "cloud-admin-release" "$KEEP_RELEASES" "$cur,$prev" $(ls -1d "$ROOT"/releases/cloud-admin/*/ 2>/dev/null | sed 's:/$::')

  # DB dumps
  retain_db "db-dump" "$KEEP_DB" $(ls -1d /root/backups/*.sql /root/backups/*.sql.gz /root/backups/*.dump 2>/dev/null)

  # config backups
  retain_group "config-backup" "$KEEP_CONFIG" "" $(ls -1d /etc/netplan/*.bak* /etc/stayconnect/*.bak* 2>/dev/null)
}

plan_appliance() {
  # Protected: identity, certs, tls, assignment, license, generated, vendor key.
  for d in /etc/stayconnect/identity /etc/stayconnect/certs /etc/stayconnect/tls \
           /etc/stayconnect/assignment /etc/stayconnect/license /etc/stayconnect/generated; do
    [ -e "$d" ] && R_PROT+=("pki/identity|$d|identity/cert/trust — never delete"); done
  for f in /etc/stayconnect/*.key /etc/stayconnect/*.pub /etc/stayconnect/*.crt; do
    [ -e "$f" ] && R_PROT+=("trust-anchor|$f|key/cert material — never delete"); done

  # binary backups per service
  for svc in scd edged acctd netd portald; do
    retain_group "$svc-binary" "$KEEP_BINARIES" "" $(ls -1d "$ROOT"/bin/$svc.bak* "$ROOT"/bin/$svc.prev* 2>/dev/null)
  done

  # hotel-admin releases (protect current + previous)
  local cur prev
  cur="$(symlink_target "$ROOT/hotel-admin")"
  prev="$(symlink_target "$ROOT/hotel-admin.previous")"
  if [ -d "$cur" ] && [ -d "$prev" ]; then ROLLBACK_VALID=true
  else fail "hotel-admin rollback path invalid (current=$cur previous=$prev)"; fi
  retain_group "hotel-admin-release" "$KEEP_RELEASES" "$cur,$prev" $(ls -1d "$ROOT"/releases/hotel-admin/*/ 2>/dev/null | sed 's:/$::')

  # config backups
  retain_group "config-backup" "$KEEP_CONFIG" "" $(ls -1d /etc/netplan/*.bak* /etc/stayconnect/*.bak* 2>/dev/null)
}

case "$ROLE" in
  central)   plan_central ;;
  appliance) plan_appliance ;;
  *) fail "unknown host role — refusing to delete anything"; ROLLBACK_VALID=false ;;
esac

# ---- disk usage + alert ------------------------------------------------------
DISK_PCT="$(df --output=pcent / 2>/dev/null | tail -1 | tr -dc '0-9')"
: "${DISK_PCT:=0}"
DISK_ALERT=none
if   [ "$DISK_PCT" -ge "$DISK_CRIT" ]; then DISK_ALERT=critical
elif [ "$DISK_PCT" -ge "$DISK_WARN" ]; then DISK_ALERT=warning; fi

# nounset-safe array lengths (empty arrays trip `${#arr[@]}` under set -u on some bash).
set +u
N_DEL=${#R_DEL[@]}; N_KEEP=${#R_KEEP[@]}; N_PIN=${#R_PIN[@]}; N_PROT=${#R_PROT[@]}
set -u

# ---- execute (or dry-run) ----------------------------------------------------
DELETED=0; RECLAIMED_KB=0
if [ "$JSON_ONLY" = 0 ]; then
  echo "== stayconnect-backup-cleanup ($ROLE) $NOW  mode=$([ $APPLY = 1 ] && echo APPLY || echo DRY-RUN) =="
  echo "disk: ${DISK_PCT}% used (warn=$DISK_WARN crit=$DISK_CRIT) -> $DISK_ALERT"
  echo "rollback path valid: $ROLLBACK_VALID"
  [ "$FAILURES" -gt 0 ] && echo "FAILURES: $FAIL_MSGS"
  echo "-- PROTECTED (never delete) --"; printf '  %s\n' "${R_PROT[@]:-  (none)}"
  echo "-- PINNED --";                    printf '  %s\n' "${R_PIN[@]:-  (none)}"
  echo "-- KEEP --";                      printf '  %s\n' "${R_KEEP[@]:-  (none)}"
  echo "-- DELETE --";                    printf '  %s\n' "${R_DEL[@]:-  (none)}"
fi

# Fail-safe: if role unknown or rollback path invalid, never delete.
SAFE_TO_DELETE=1
[ "$ROLE" = "unknown" ] && SAFE_TO_DELETE=0
$ROLLBACK_VALID || SAFE_TO_DELETE=0

for entry in "${R_DEL[@]:-}"; do
  [ -z "$entry" ] && continue
  path="${entry#*|}"; path="${path%%|*}"
  sz=$(du -sk "$path" 2>/dev/null | cut -f1); : "${sz:=0}"
  if [ "$APPLY" = 1 ] && [ "$SAFE_TO_DELETE" = 1 ]; then
    if rm -rf -- "$path"; then
      DELETED=$((DELETED+1)); RECLAIMED_KB=$((RECLAIMED_KB+sz))
      echo "$NOW DELETED $path (${sz}KB)" >> "$LOG"
    else
      fail "delete failed: $path"
    fi
  fi
done

if [ "$APPLY" = 1 ] && [ "$SAFE_TO_DELETE" = 0 ] && [ "$N_DEL" -gt 0 ]; then
  fail "apply refused (unsafe: role=$ROLE rollback_valid=$ROLLBACK_VALID) — nothing deleted"
fi

# ---- write status JSON -------------------------------------------------------
json_arr() { local first=1; printf '['; for e in "$@"; do [ -z "$e" ] && continue; [ $first = 0 ] && printf ','; printf '"%s"' "$(printf '%s' "$e" | sed 's/"/\\"/g')"; first=0; done; printf ']'; }
{
  printf '{\n'
  printf '  "host_role": "%s",\n' "$ROLE"
  printf '  "last_run": "%s",\n' "$NOW"
  printf '  "mode": "%s",\n' "$([ $APPLY = 1 ] && echo apply || echo dry-run)"
  printf '  "disk_pct": %s,\n' "$DISK_PCT"
  printf '  "disk_alert": "%s",\n' "$DISK_ALERT"
  printf '  "disk_warn": %s, "disk_crit": %s,\n' "$DISK_WARN" "$DISK_CRIT"
  printf '  "rollback_path_valid": %s,\n' "$ROLLBACK_VALID"
  printf '  "failures": %s,\n' "$FAILURES"
  printf '  "failure_detail": "%s",\n' "$(printf '%s' "$FAIL_MSGS" | sed 's/"/\\"/g')"
  printf '  "retained": %s,\n' "$N_KEEP"
  printf '  "pinned": %s,\n' "$N_PIN"
  printf '  "protected": %s,\n' "$N_PROT"
  printf '  "delete_candidates": %s,\n' "$N_DEL"
  printf '  "deleted_last_run": %s,\n' "$DELETED"
  printf '  "reclaimed_kb": %s,\n' "$RECLAIMED_KB"
  printf '  "keep_binaries": %s, "keep_releases": %s, "keep_db": %s, "keep_config": %s,\n' "$KEEP_BINARIES" "$KEEP_RELEASES" "$KEEP_DB" "$KEEP_CONFIG"
  printf '  "protected_items": %s,\n' "$(json_arr "${R_PROT[@]:-}")"
  printf '  "pinned_items": %s,\n' "$(json_arr "${R_PIN[@]:-}")"
  printf '  "retained_items": %s,\n' "$(json_arr "${R_KEEP[@]:-}")"
  printf '  "delete_items": %s\n' "$(json_arr "${R_DEL[@]:-}")"
  printf '}\n'
} > "$STATUS.tmp" && mv "$STATUS.tmp" "$STATUS"

# ---- alert sinks (journald + prometheus textfile, best effort) ---------------
logger -t stayconnect-backup "role=$ROLE disk=${DISK_PCT}% alert=$DISK_ALERT rollback_valid=$ROLLBACK_VALID deleted=$DELETED failures=$FAILURES" 2>/dev/null || true
for td in "${TEXTFILE_DIRS[@]}"; do
  if [ -d "$td" ] && [ -w "$td" ]; then
    {
      echo "# HELP stayconnect_backup_disk_pct Root filesystem percent used."
      echo "stayconnect_backup_disk_pct ${DISK_PCT}"
      echo "stayconnect_backup_rollback_valid $($ROLLBACK_VALID && echo 1 || echo 0)"
      echo "stayconnect_backup_cleanup_failures ${FAILURES}"
      echo "stayconnect_backup_delete_candidates $N_DEL"
    } > "$td/stayconnect_backup.prom.tmp" && mv "$td/stayconnect_backup.prom.tmp" "$td/stayconnect_backup.prom"
    break
  fi
done

echo "$NOW RUN role=$ROLE mode=$([ $APPLY = 1 ] && echo apply || echo dry-run) disk=${DISK_PCT}% deleted=$DELETED reclaimed=${RECLAIMED_KB}KB failures=$FAILURES" >> "$LOG"

[ "$JSON_ONLY" = 1 ] && cat "$STATUS"
# Non-zero exit on failure or critical disk so callers/timers surface it.
[ "$FAILURES" -gt 0 ] && exit 2
[ "$DISK_ALERT" = critical ] && exit 3
exit 0
