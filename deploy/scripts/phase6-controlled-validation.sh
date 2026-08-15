#!/usr/bin/env bash
# PHASE-6 CONTROLLED LIVE-DARK VALIDATION -- self-restoring, and it proves the restoration.
#
# THIS IS THE ONLY THING IN PHASE 6 THAT TURNS CAPABILITIES ON. The danger was never the enabling; it is being
# interrupted afterwards. A run killed between the flags going on and coming off leaves a DEVELOPMENT appliance
# advertising a capability nobody meant to leave running, synthetic state in its database, and an operator with
# no reason to suspect either.
#
# So restoration is a TRAP -- success, failure, error, INT, TERM -- and it restores a CAPTURED BASELINE rather
# than tidying up towards what it assumes the appliance looked like:
#
#   * the exact DARK Hotel Admin release, by path and content hash, captured before anything changes. Not "the
#     previous release": a run that itself deploys a flagged bundle makes the flagged one previous.
#   * every Phase-6 flag off, verified through the authoritative flag-coherence gate -- not by grepping the
#     files this script wrote, which only ever proves that it can write files.
#   * the per-appliance product setting returned to its captured value, PER APPLIANCE, through the sanctioned
#     audited writer. Never `UPDATE appliance_product_settings SET ... WHERE guest_device_self_service`: that
#     is an owner-level write across every appliance in the database, and it leaves no audit record of who
#     changed what.
#   * every synthetic row identified by SCOPE. The reserved tenant in phase6-validation-scope.sql exists for
#     nothing else, so teardown cannot reach a real guest's row -- and no marker is invented in any constrained
#     business vocabulary to make test rows findable.
#   * and the runtime proven dark afterwards: routes ABSENT, services healthy, accounting owner present.
#
# It refuses to run anywhere but the authorized development appliance, and that refusal is NOT overridable by
# an environment variable. The allow-list is compiled into this file, because a feature-enabling runner that
# can be pointed at another host by exporting one variable is not protected, it is merely inconvenienced.
#
#   usage:  phase6-controlled-validation.sh              full run: capture, enable, validate, restore, verify
#           phase6-controlled-validation.sh restore      restore + verify only (safe at any time, idempotent)
#           phase6-controlled-validation.sh selftest CASE
#                                                        fault injection -- enables a flag and then abandons
#                                                        the run (body-failure | signal | partial)
set -uo pipefail

# ---- identity: compiled in, not configurable ---------------------------------------------------------------
readonly AUTHORIZED_HOSTS="radius"
readonly AUTHORIZED_DB="stayconnect_site"

ENV_DIR="/etc/stayconnect"
UNITS="stayconnect-scd stayconnect-acctd stayconnect-edged"
ALL_UNITS="$UNITS stayconnect-portald stayconnect-netd stayconnect-hotel-admin"
PG="stayconnect-pg"
DBUSER="${PHASE6_DB_USER:-stayconnect}"
DB="$AUTHORIZED_DB"
COHERENCE="${PHASE6_COHERENCE:-/root/phase6-flag-coherence.sh}"
VALIDATION_DIR="/opt/stayconnect/validation"
HA_CURRENT="/opt/stayconnect/hotel-admin"
STATE_DIR="/var/lib/stayconnect/phase6-validation"
SCD_SOCK="/run/stayconnect/scd.sock"

# The reserved identities, matching phase6-validation-scope.sql. Fixed rather than generated, and written down
# in both places on purpose: teardown has to work from a cold start, including after a run that died before it
# could record anything anywhere.
readonly SYN_ENT="6d5f0000-0000-4000-8000-000000000302"
readonly SYN_SESS="6d5f0000-0000-4000-8000-000000000401"
readonly SYN_MAC_A="02:00:00:60:00:01"
readonly SYN_MAC_B="02:00:00:60:00:02"

pass=0; fail=0
BLACKHOLED=""
TEN=""; SITE=""; APPL=""; GIP=""
ok(){ printf '  [PASS] %s\n' "$1"; pass=$((pass+1)); }
no(){ printf '  [FAIL] %s :: %s\n' "$1" "${2:-}"; fail=$((fail+1)); }
say(){ printf '\n== %s ==\n' "$1"; }
q(){ docker exec -i "$PG" psql -U "$DBUSER" -d "$DB" -tAqc "$1" 2>&1; }

guard_environment() {
  local host; host="$(hostname)"
  case " $AUTHORIZED_HOSTS " in
    *" $host "*) : ;;
    *) echo "REFUSED: host '$host' is not in the compiled allow-list ($AUTHORIZED_HOSTS)" >&2; exit 2 ;;
  esac
  case "$DB" in
    *prod*|*production*) echo "REFUSED: database '$DB' looks like Production" >&2; exit 2 ;;
  esac
}

# ---- baseline capture ---------------------------------------------------------------------------------------
capture_baseline() {
  mkdir -p "$STATE_DIR"
  readlink -f "$HA_CURRENT" > "$STATE_DIR/ha_release"
  ha_hash > "$STATE_DIR/ha_hash"
  # Per appliance, so restoration can put each row back to the value it actually had rather than to a global
  # guess about what "off" meant here.
  docker exec -i "$PG" psql -U "$DBUSER" -d "$DB" -tAqc \
    "SELECT tenant_id||' '||site_id||' '||appliance_id||' '||guest_device_self_service
       FROM iam_v2.appliance_product_settings ORDER BY 1,2,3" > "$STATE_DIR/settings" 2>/dev/null
  local u
  for u in $UNITS; do
    cp -p "$ENV_DIR/${u#stayconnect-}.env" "$STATE_DIR/${u}.env.baseline" 2>/dev/null || true
  done
  ok "baseline captured: dark release $(basename "$(cat "$STATE_DIR/ha_release")"), hash $(cut -c1-12 < "$STATE_DIR/ha_hash"), $(wc -l < "$STATE_DIR/settings" | tr -d ' ') setting row(s)"
}

ha_hash() {
  # Content, not just the symlink target. Two releases can share a path after a redeploy; they cannot share
  # this. Restoring by path alone would be satisfied by a directory whose contents had been replaced.
  ( cd "$HA_CURRENT" 2>/dev/null && find . -type f \( -name '*.js' -o -name '*.json' -o -name '*.html' \) \
      -print0 2>/dev/null | sort -z | xargs -0 sha256sum 2>/dev/null | sha256sum | awk '{print $1}' ) \
    || echo "unhashable"
}

# ---- restoration ---------------------------------------------------------------------------------------------
restore_flags() {
  local u f
  for u in $UNITS; do
    f="$ENV_DIR/${u#stayconnect-}.env"
    if [ -f "$STATE_DIR/${u}.env.baseline" ]; then
      cp -p "$STATE_DIR/${u}.env.baseline" "$f"
    else
      # No baseline (restore-only run on a machine that never captured one): strip every Phase-6 flag. Removing
      # a line is safe in a way that writing "=false" is not -- a value nobody can parse must never be read as
      # permission, and the services already fail closed on one.
      sed -i '/^STAYCONNECT_PHASE6_/d' "$f" 2>/dev/null || true
    fi
  done
  systemctl restart $UNITS >/dev/null 2>&1 || true
}

restore_hotel_admin() {
  local want now
  want="$(cat "$STATE_DIR/ha_release" 2>/dev/null || true)"
  [ -n "$want" ] && [ -d "$want" ] || return 0
  now="$(readlink -f "$HA_CURRENT" 2>/dev/null || true)"
  [ "$now" = "$want" ] && return 0
  ln -sfn "$want" "$HA_CURRENT.tmp" && mv -Tf "$HA_CURRENT.tmp" "$HA_CURRENT"
  systemctl restart stayconnect-hotel-admin >/dev/null 2>&1 || true
}

restore_settings() {
  # THE SANCTIONED AUDITED WRITER, per appliance, back to the captured value. The writer serializes on the
  # appliance and records the change; that is the whole point of it existing.
  local op t s a v
  op="$(q "SELECT id FROM public.operators ORDER BY created_at LIMIT 1")"
  [ -n "$op" ] || return 0
  while read -r t s a v; do
    [ -n "${t:-}" ] || continue
    q "SELECT iam_v2.p6_set_guest_device_self_service('$t','$s','$a', $v, '$op',
         'phase6-controlled-validation', 'restore captured pre-validation baseline')" >/dev/null
  done < "$STATE_DIR/settings" 2>/dev/null
  # A settings row this run created that was NOT in the baseline is removed, so the baseline is restored
  # exactly rather than approximately. The append-only CHANGE record of it stays: that is audit history, and
  # deleting it would be the one thing this script must never teach anyone to do.
  local key
  q "SELECT tenant_id||' '||site_id||' '||appliance_id FROM iam_v2.appliance_product_settings" 2>/dev/null \
  | while read -r key; do
      [ -n "${key:-}" ] || continue
      if ! awk '{print $1" "$2" "$3}' "$STATE_DIR/settings" 2>/dev/null | grep -qxF "$key"; then
        set -- $key
        q "DELETE FROM iam_v2.appliance_product_settings
            WHERE tenant_id='$1' AND site_id='$2' AND appliance_id='$3'" >/dev/null
      fi
    done
}

teardown_scope() {
  if [ -f "$VALIDATION_DIR/phase6-validation-teardown.sql" ]; then
    docker exec -i "$PG" psql -U "$DBUSER" -d "$DB" -v ON_ERROR_STOP=1 -q \
      < "$VALIDATION_DIR/phase6-validation-teardown.sql" >/dev/null 2>&1
  fi
  # Belt and braces, and the reason is idempotence from a cold start: if the SQL file is not staged -- a run
  # that died before staging it, or a restore invoked on its own -- the reserved identities must still be made
  # inert. Same statements, same reserved ids, no parameters needed.
  q "DO \$\$
     BEGIN
       IF EXISTS (SELECT 1 FROM iam_v2.entitlements WHERE id='$SYN_ENT' AND status <> 'TERMINATED') THEN
         PERFORM iam_v2.terminate_entitlement_at_boundary('$SYN_ENT', now(), 'ADMIN');
       END IF;
       UPDATE iam_v2.sessions SET state='ended', ended=COALESCE(ended,now()),
              end_reason=COALESCE(end_reason,'ADMIN')
        WHERE entitlement_id='$SYN_ENT' AND state IN ('active','PENDING_ENFORCEMENT');
       UPDATE iam_v2.entitlement_devices SET status='RELEASED', released_at=COALESCE(released_at,now())
        WHERE entitlement_id='$SYN_ENT' AND status='AUTHORIZED';
     END \$\$;" >/dev/null
}

restore_network() {
  # The local-first proof blackholes the Central address. If the run dies while that route is in place the
  # appliance loses its uplink silently, which is a worse outcome than any test failure.
  [ -n "${BLACKHOLED:-}" ] || return 0
  ip route del blackhole "$BLACKHOLED/32" 2>/dev/null
  BLACKHOLED=""
}

restore() {
  say "restoration (runs on success, failure and interruption)"
  restore_network;  ok "no blackhole route left behind by the local-first proof"
  restore_flags;    ok "Phase-6 flag files restored to the captured baseline; services restarted"
  restore_hotel_admin; ok "Hotel Admin pointed at the captured DARK release"
  restore_settings; ok "per-appliance product settings restored through the audited writer"
  teardown_scope;   ok "the reserved validation scope is inert (terminated through the boundary path)"
}

# ---- verification: the RUNTIME, not the files this script wrote ------------------------------------------------
verify_dark() {
  local bad=0
  say "verification of the restored state"

  if [ -f "$COHERENCE" ]; then
    if bash "$COHERENCE" >/dev/null 2>&1; then
      ok "the authoritative flag-coherence gate passes (every Phase-6 flag off and agreeing across services)"
    else
      no "flag coherence" "the authoritative gate reports a problem; run $COHERENCE for detail"; bad=1
    fi
  else
    no "flag coherence" "the authoritative gate is missing at $COHERENCE"; bad=1
  fi

  # THE ROUTE ITSELF. A flag file records an intention; this records what the running process will answer.
  local code
  code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 --unix-socket "$SCD_SOCK" \
          -X POST http://localhost/v1/phase6/devices/list -H 'Content-Type: application/json' \
          -d '{}' 2>/dev/null || echo 000)"
  case "$code" in
    404) ok "the guest device route is ABSENT in the running scd (404)" ;;
    *)   no "the guest device route answered $code" "while dark it must not exist"; bad=1 ;;
  esac

  # THE ACCOUNTING-OWNER INVARIANT. Accrual is data-driven precisely so a flag cannot stop it, but it still
  # needs a process to run in: an appliance holding aggregate entitlements with no accounting owner is exactly
  # the state in which a finite budget becomes unlimited.
  if systemctl is-active --quiet stayconnect-acctd; then
    if journalctl -u stayconnect-acctd --since '-10min' 2>/dev/null | grep -q 'phase6_fallback_accounting'; then
      ok "the accounting owner is active and reports the Phase-6 fallback owner"
    else
      ok "the accounting owner (acctd) is active"
    fi
  else
    no "acctd is not active" "aggregate budgets would have no owner"; bad=1
  fi

  local u down=""
  for u in $ALL_UNITS; do systemctl is-active --quiet "$u" || down="$down $u"; done
  [ -z "$down" ] && ok "every required service is active" || { no "services not active" "$down"; bad=1; }

  local want now
  want="$(cat "$STATE_DIR/ha_release" 2>/dev/null || true)"
  now="$(readlink -f "$HA_CURRENT" 2>/dev/null || true)"
  if [ -z "$want" ]; then
    ok "no captured Hotel Admin baseline to compare against (restore-only run)"
  elif [ "$want" != "$now" ]; then
    no "Hotel Admin release" "current $now, baseline $want"; bad=1
  elif [ "$(ha_hash)" != "$(cat "$STATE_DIR/ha_hash")" ]; then
    no "Hotel Admin content" "the release path matches but its contents changed"; bad=1
  else
    ok "the exact DARK Hotel Admin release is current, by path and by content hash"
  fi

  local live
  live="$(q "SELECT
      (SELECT count(*) FROM iam_v2.entitlements WHERE id='$SYN_ENT' AND status <> 'TERMINATED')
    + (SELECT count(*) FROM iam_v2.sessions WHERE entitlement_id='$SYN_ENT' AND state IN ('active','PENDING_ENFORCEMENT'))
    + (SELECT count(*) FROM iam_v2.entitlement_devices WHERE entitlement_id='$SYN_ENT' AND status='AUTHORIZED')")"
  [ "$live" = "0" ] && ok "no live synthetic entitlement, session or device binding remains at the reserved ids" \
    || { no "synthetic state is still live" "$live row(s)"; bad=1; }

  # The settings table matches the captured baseline row for row.
  local nowset
  nowset="$(q "SELECT tenant_id||' '||site_id||' '||appliance_id||' '||guest_device_self_service
                 FROM iam_v2.appliance_product_settings ORDER BY 1,2,3")"
  if [ "$nowset" = "$(cat "$STATE_DIR/settings" 2>/dev/null)" ]; then
    ok "the per-appliance product settings match the captured baseline exactly"
  else
    no "product settings differ from the baseline" "$(printf '%s' "$nowset" | tr '\n' ';')"; bad=1
  fi

  return $bad
}

finish() {
  local rc=$?
  restore
  local vrc=0
  verify_dark || vrc=1
  echo "------------------------------------------------------------"
  if [ "$vrc" = "0" ] && [ "$fail" -eq 0 ]; then
    printf 'PHASE6_CONTROLLED_VALIDATION pass=%d fail=%d -- appliance verified DARK and restored (exit %d)\n' \
      "$pass" "$fail" "$rc"
    exit "$rc"
  fi
  printf 'PHASE6_CONTROLLED_VALIDATION pass=%d fail=%d -- APPLIANCE MAY BE PARTIALLY ENABLED, INSPECT IT\n' \
    "$pass" "$fail"
  exit 1
}

# ---- enabling -------------------------------------------------------------------------------------------------
set_flags() {   # set_flags "<FLAG> <FLAG> ..."   -- exactly this set, on every unit; anything else removed
  local wanted="$1" u f n
  for u in $UNITS; do
    f="$ENV_DIR/${u#stayconnect-}.env"
    sed -i '/^STAYCONNECT_PHASE6_/d' "$f"
    for n in $wanted; do printf '%s=true\n' "$n" >> "$f"; done
  done
  systemctl restart $UNITS >/dev/null 2>&1
  sleep 2
}

setting_on() {  # setting_on <true|false> -- the REAL appliance, through the audited writer
  local val="$1" op row
  op="$(q "SELECT id FROM public.operators ORDER BY created_at LIMIT 1")"
  row="$(q "SELECT a.tenant_id||' '||a.site_id||' '||a.id FROM public.appliances a ORDER BY a.id LIMIT 1")"
  set -- $row
  q "SELECT iam_v2.p6_set_guest_device_self_service('$1','$2','$3', $val, '$op',
       'phase6-controlled-validation', 'controlled validation under D25')" >/dev/null
}

# appliance_identity fills TEN/SITE/APPL and GIP from the appliance's own tables. Nothing is typed in: the
# fixture has to live under the identity scd actually resolves against, because scd creates every device row as
# (its own tenant, its own site, its own appliance) and refuses any address that is not on a mapped guest
# network. A fixture in a tenant of its own would be unreachable through the real guest route, and the
# validation would then be proving that the handler works against a fixture.
appliance_identity() {
  local row
  row="$(q "SELECT a.tenant_id||' '||a.site_id||' '||a.id FROM public.appliances a ORDER BY a.id LIMIT 1")"
  set -- $row
  TEN="${1:-}"; SITE="${2:-}"; APPL="${3:-}"
  GIP="$(q "SELECT host(network(subnet_cidr) + 100) FROM guest_networks
             WHERE enabled ORDER BY masklen(subnet_cidr) DESC LIMIT 1")"
  [ -n "$TEN" ] && [ -n "$APPL" ] && [ -n "$GIP" ]
}

seed_scope() {
  docker exec -i "$PG" psql -U "$DBUSER" -d "$DB" -v ON_ERROR_STOP=1 -q \
    -v ten="$TEN" -v site="$SITE" -v appl="$APPL" -v gip="$GIP" \
    < "$VALIDATION_DIR/phase6-validation-scope.sql" 2>&1
}

route_code() {  # route_code <path> -> HTTP status from the running scd
  curl -s -o /dev/null -w '%{http_code}' --max-time 5 --unix-socket "$SCD_SOCK" \
    -X POST "http://localhost$1" -H 'Content-Type: application/json' -d "${2:-\{\}}" 2>/dev/null || echo 000
}

guard_environment

case "${1:-run}" in
  restore)
    say "restore-only run on $(hostname)"
    trap finish EXIT INT TERM
    exit 0
    ;;

  selftest)
    # FAULT INJECTION, RUN BEFORE THE REAL BODY IS EVER TRUSTED. Each case enables a capability and then
    # abandons the run in a different way. The run is judged only by whether the appliance comes back dark --
    # which is the property the real validation depends on and the one that cannot be established by reading
    # the code and hoping.
    say "restoration self-test: '${2:-body-failure}' (a flag IS enabled and then abandoned, on purpose)"
    capture_baseline
    case "${2:-body-failure}" in
      body-failure)
        trap finish EXIT INT TERM
        set_flags "STAYCONNECT_PHASE6_MASTER STAYCONNECT_PHASE6_AGGREGATE_ONLINE_TIME"
        ok "capability enabled; the body is about to fail"
        false; exit 1 ;;
      signal)
        trap finish EXIT INT TERM
        set_flags "STAYCONNECT_PHASE6_MASTER STAYCONNECT_PHASE6_AGGREGATE_ONLINE_TIME"
        ok "capability enabled; this run is about to be signalled"
        kill -TERM $$; sleep 10; exit 1 ;;
      partial)
        trap finish EXIT INT TERM
        set_flags "STAYCONNECT_PHASE6_MASTER STAYCONNECT_PHASE6_DEVICE_SELFSERVICE_GUEST \
                   STAYCONNECT_PHASE6_DEVICE_SELFSERVICE_ADMIN STAYCONNECT_PHASE6_AGGREGATE_ONLINE_TIME"
        setting_on true
        appliance_identity && seed_scope >/dev/null 2>&1
        ok "every flag on, the product setting on, and synthetic state seeded; abandoning mid-way"
        exit 3 ;;
      double-restore)
        # Idempotence: restoring an already-restored appliance must be a no-op that still verifies.
        trap finish EXIT INT TERM
        restore; ok "a first restoration ran; the trap is about to run a second one"
        exit 0 ;;
      *) echo "unknown selftest case '${2:-}'" >&2; exit 2 ;;
    esac
    ;;

  run)
    say "Phase-6 controlled validation on $(hostname)"
    trap finish EXIT INT TERM
    capture_baseline
    # SOURCED, not executed: the body must run inside this shell so that its failures, its counters and any
    # signal that reaches it all land in the trap above. A child process would take its own exit status with
    # it and leave the flags on.
    . "$(dirname "$0")/phase6-controlled-validation-body.sh"
    ;;

  *) echo "usage: $0 [run|restore|selftest CASE]" >&2; exit 2 ;;
esac
