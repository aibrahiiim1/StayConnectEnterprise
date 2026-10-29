#!/usr/bin/env bash
# Self-test for the retention sweep in stayconnect-backup-cleanup.sh on an APPLIANCE (GDPR final mission: one backup system).
#
# Runs the REAL script against a sandbox (SC_CLEANUP_* overrides) and asserts:
#   - the sweep NEVER removes a database backup: scheduled, manual, pre-change or pre-restore, clear or encrypted, however old and
#     however many -- scd's backup system is the only thing that rotates them, so there is no second retention policy to disagree;
#   - the nightly sets the retired site-backup job wrote are EARLIER BACKUPS: every one is kept, whatever its age or number, and the
#     sweep says so;
#   - nothing else in the site-backup directory is touched: subdirectories, look-alike names, other files;
#   - every earlier-backup artefact ends up 0600 and the directory 0700 (permissions are repaired; nothing is deleted);
#   - old policy settings (SITE_BACKUP_*, DB_BACKUP_DAYS) in the file have no effect;
#   - what the sweep does sweep (config backups beyond KEEP_CONFIG, orphan verification markers) still works;
#   - dry-run deletes nothing.
# Needs bash, GNU coreutils and GNU findutils. Touches nothing outside its temporary directory.
set -uo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
SCRIPT="$HERE/stayconnect-backup-cleanup.sh"
T="$(mktemp -d)"; trap 'rm -rf "$T"' EXIT
fail=0
ok()  { echo "PASS: $*"; }
bad() { echo "FAIL: $*"; fail=1; }

ROOT="$T/opt"; SITE="$T/backups"; mkdir -p "$ROOT/backups/db" "$ROOT/bin" "$ROOT/releases/hotel-admin/r1" "$ROOT/releases/hotel-admin/r2" "$SITE/compliance" "$T/log"
ln -s "$ROOT/releases/hotel-admin/r2" "$ROOT/hotel-admin"
ln -s "$ROOT/releases/hotel-admin/r1" "$ROOT/hotel-admin.previous"

# 12 nightly sets, from today back to 60 days. All must survive.
days=(0 1 2 35 36 37 38 40 41 44 50 60)
for d in "${days[@]}"; do
  stamp="$(date -u -d "-$d days" +%Y%m%d-%H%M%S)"
  e=""; case "$d" in 1|44) e=".age";; esac
  for f in "site-$stamp.dump$e" "site-$stamp.dump.meta.json" "site-$stamp-etc.tgz$e"; do
    echo x > "$SITE/$f"; chmod 0644 "$SITE/$f"; touch -d "-$d days" "$SITE/$f"
  done
done
echo keep > "$SITE/compliance/old.json"; touch -d "-400 days" "$SITE/compliance/old.json"
echo keep > "$SITE/site-latest.dump";     touch -d "-400 days" "$SITE/site-latest.dump"
echo keep > "$SITE/phase1b.dump";         touch -d "-400 days" "$SITE/phase1b.dump"

# database backups of every kind, from fresh to a year old. The sweep must remove none of them.
dbstamp() { date -u -d "-$1 days" +%Y%m%dT%H%M%SZ; }
DB="$ROOT/backups/db"
mk() { echo x > "$DB/$1"; touch -d "-$2 days" "$DB/$1"; }
mk "db-$(dbstamp 0).sql.gz.age" 0
mk "db-$(dbstamp 3).sql.gz.age" 3
mk "db-$(dbstamp 3).meta.json" 3
mk "db-$(dbstamp 40).sql.gz" 40
mk "db-safety-$(dbstamp 45).sql.gz.age" 45
mk "db-safety-$(dbstamp 365).sql.gz" 365
for i in 1 2 3 4 5 6 7 8 9 10; do mk "db-$(dbstamp $((100 + i))).sql.gz.age" $((100 + i)); done
n_db_before="$(ls "$DB" | wc -l)"
# an orphan verification marker is still swept
echo '{}' > "$DB/db-gone.sql.gz.age.verified"
# config backups beyond KEEP_CONFIG are still swept (the sandbox has none: only prove the sweep still runs cleanly)

# A backup made with cp -a keeps its source's modification time, so copies of an unchanged file tie on mtime. The newest COPY must
# still be the one kept (retain_group orders by change time). The function is lifted out of the script and run on its own.
(
  R_KEEP=(); R_DEL=(); R_PIN=()
  is_pinned() { return 1; }
  eval "$(sed -n '/^retain_group() {/,/^}/p' "$SCRIPT")"
  d="$T/cfg"; mkdir -p "$d"
  for i in 1 2 3 4 5; do
    echo "copy $i" > "$d/ctrlapi.env.bak-$i"; sleep 1.1; touch -d '2020-01-01' "$d/ctrlapi.env.bak-$i"
  done
  retain_group "config-backup" 3 "" "$d"/ctrlapi.env.bak-*
  digits() { for e in "$@"; do e="${e##*ctrlapi.env.bak-}"; printf '%s' "${e%%|*}"; done | fold -w1 | sort | tr -d '\n'; }
  kept="$(digits "${R_KEEP[@]}")"
  del="$(digits "${R_DEL[@]}")"
  if [ "$kept" = 345 ] && [ "$del" = 12 ]; then echo "PASS: of copies that tie on modification time, the newest three are kept"
  else echo "FAIL: config backups that tie on mtime: kept '$kept', swept '$del' (want 345 / 12)"; exit 1; fi
) || fail=1

run() {
  SC_CLEANUP_CONF="$T/conf" SC_CLEANUP_ROOT="$ROOT" SC_CLEANUP_PINS="$T/pins" SC_CLEANUP_LOGDIR="$T/log" \
    SC_CLEANUP_SITE_DIR="$SITE" bash "$SCRIPT" "$@" >"$T/out" 2>&1
}

# a policy file carrying the RETIRED settings: they must have no effect at all
printf 'SITE_BACKUP_DAYS=1\nSITE_BACKUP_KEEP=1\nDB_BACKUP_DAYS=1\nKEEP_DB=1\n' > "$T/conf"

run   # dry-run
n_before="$(ls "$SITE" | grep -c '^site-[0-9]')"
[ "$n_before" = 36 ] && ok "dry-run deleted nothing" || bad "dry-run changed the directory ($n_before files)"

run --apply
[ "$(ls "$SITE" | grep -c '^site-[0-9]')" = 36 ] && ok "every earlier nightly set was kept, however old or many" || bad "an earlier nightly set was removed"
[ "$(ls "$DB" | grep -vc 'gone')" = "$n_db_before" ] && ok "no database backup was removed (all $n_db_before files)" || bad "the sweep removed a database backup: $(ls "$DB" | wc -l) files left of $n_db_before"
[ ! -e "$DB/db-gone.sql.gz.age.verified" ] && ok "an orphan verification marker was swept" || bad "an orphan verification marker survived"
for f in compliance/old.json site-latest.dump phase1b.dump; do
  [ -f "$SITE/$f" ] && ok "untouched: $f" || bad "deleted something that is not a nightly set: $f"
done
perm_bad="$(find "$SITE" -maxdepth 1 -type f -name 'site-2*' ! -perm 0600 | wc -l)"
[ "$perm_bad" = 0 ] && ok "every earlier-backup artefact is 0600" || bad "$perm_bad artefact(s) are not 0600"
[ "$(stat -c %a "$SITE")" = 700 ] && ok "the directory is 0700" || bad "the directory is $(stat -c %a "$SITE")"
[ "$(stat -c %a "$SITE/phase1b.dump")" = 644 ] && ok "permissions of other files untouched" || bad "another file's mode was changed"
grep -q 'earlier-backup|' "$T/out" && ok "the sweep reports the earlier backups as kept" || bad "the earlier backups are not reported"
grep -q 'newest full backup (never delete)' "$T/out" && ok "the newest database backup is reported as never deleted" || bad "the newest database backup is not reported"
if sed -n '/-- DELETE --/,$p' "$T/out" | grep -qE 'sql\.gz(\.age)?\||earlier-backup'; then bad "a backup is on the delete list"; else ok "no backup of any kind is on the delete list"; fi

[ "$fail" = 0 ] && { echo "ALL PASS"; exit 0; }
echo "--- script output ---"; cat "$T/out"; exit 1
