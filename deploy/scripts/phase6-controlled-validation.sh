#!/usr/bin/env bash
# PHASE-6 CONTROLLED LIVE-DARK VALIDATION — with cleanup that runs whatever happens.
#
# THIS SCRIPT TURNS PHASE-6 CAPABILITIES ON. That is the only thing in the whole phase that does, and the
# danger is not the enabling: it is being interrupted halfway. A run killed after the flags go on and before
# they come off leaves a DEVELOPMENT appliance advertising a capability nobody meant to leave running, with
# synthetic acceptance state in its database and an operator who has no reason to suspect either.
#
# So the cleanup is a trap, not a final section. It runs on success, on failure, on error, on Ctrl-C and on
# SIGTERM, and it is idempotent so running it twice is harmless. And then it VERIFIES itself: the script
# refuses to report success unless the appliance ends with every Phase-6 flag off, the product setting off,
# no synthetic state left behind, and the services healthy. A cleanup nobody checked is a wish.
#
# It is for the DEVELOPMENT appliance only, and it says so out loud: it refuses to run anywhere that looks
# like Production, and it creates only synthetic state of its own making. No real guest, PMS, provider or
# financial traffic is involved at any point.
#
#   usage:  phase6-controlled-validation.sh            # full run: enable, validate, clean up, verify
#           phase6-controlled-validation.sh cleanup    # cleanup and verification only (safe any time)
set -uo pipefail

ENV_DIR="${PHASE6_ENV_DIR:-/etc/stayconnect}"
UNITS="stayconnect-scd stayconnect-acctd stayconnect-edged"
ALL_UNITS="$UNITS stayconnect-portald stayconnect-netd stayconnect-hotel-admin"
PG="${PHASE6_PG_CONTAINER:-stayconnect-pg}"
DB="${PHASE6_DB:-stayconnect_site}"
DBUSER="${PHASE6_DB_USER:-stayconnect}"
# Every synthetic row this script creates carries this marker, so cleanup can find its own work and nothing
# else. A cleanup that deletes by shape rather than by marker is a cleanup that eventually deletes real data.
MARKER="PHASE6_CONTROLLED_VALIDATION"

pass=0; fail=0
ok(){ printf '  [PASS] %s\n' "$1"; pass=$((pass+1)); }
no(){ printf '  [FAIL] %s :: %s\n' "$1" "${2:-}"; fail=$((fail+1)); }
q(){ docker exec -i "$PG" psql -U "$DBUSER" -d "$DB" -tAqc "$1" 2>&1; }

# ---- refuse to run anywhere that is not the development appliance -----------------------------------------
guard_environment() {
  local host; host="$(hostname)"
  case "${PHASE6_ALLOW_HOST:-radius}" in
    "$host") : ;;
    *) echo "REFUSED: this host is '$host', not the authorized development appliance" >&2; exit 2 ;;
  esac
  # A Production database name is an immediate stop, whatever the hostname says.
  case "$DB" in
    *prod*|*production*) echo "REFUSED: database '$DB' looks like Production" >&2; exit 2 ;;
  esac
}

# ---- flags -------------------------------------------------------------------------------------------------
set_flag() {   # set_flag <unit> <NAME> <true|false>
  # Each assignment is its own statement: a `local a=.. f="${a#..}"` one-liner expands $a before the shell has
  # bound it, which is how the first cleanup run died with "unit: unbound variable" -- inside the trap, which
  # is the one place a failure must never happen.
  local unit="$1"
  local name="$2"
  local val="$3"
  local f="$ENV_DIR/${unit#stayconnect-}.env"
  [ -f "$f" ] || { echo "no env file $f" >&2; return 1; }
  sed -i "/^${name}=/d" "$f"
  [ "$val" = "true" ] && printf '%s=true\n' "$name" >> "$f"
  return 0
}

flags_off_everywhere() {
  local u n
  for u in $UNITS; do
    for n in STAYCONNECT_PHASE6_MASTER STAYCONNECT_PHASE6_AGGREGATE_ONLINE_TIME \
             STAYCONNECT_PHASE6_DEVICE_SELFSERVICE_GUEST STAYCONNECT_PHASE6_DEVICE_SELFSERVICE_ADMIN; do
      set_flag "$u" "$n" false || true
    done
  done
  systemctl restart $UNITS >/dev/null 2>&1 || true
}

# ---- THE TRAP. Everything below runs no matter how the script leaves. ---------------------------------------
cleanup() {
  local rc=$?
  echo
  echo "== cleanup (runs on success, failure, interrupt) =="

  # 1. every Phase-6 flag off, and the services restarted onto that state
  flags_off_everywhere
  ok "Phase-6 flags cleared and services restarted"

  # 2. the per-appliance product setting off -- it is durable and survives the flags
  q "UPDATE iam_v2.appliance_product_settings SET guest_device_self_service = false
      WHERE guest_device_self_service" >/dev/null
  ok "guest device self-service setting forced OFF"

  # 3. synthetic acceptance state, found by MARKER and nothing else
  q "DO \$\$
     DECLARE r record;
     BEGIN
       FOR r IN SELECT e.id FROM iam_v2.entitlements e
                 JOIN iam_v2.purchases p ON p.id = e.purchase_id
                WHERE p.trigger = '$MARKER' AND e.status <> 'TERMINATED'
       LOOP
         PERFORM iam_v2.terminate_entitlement_at_boundary(r.id, now(), 'ADMIN');
       END LOOP;
     END \$\$;" >/dev/null
  ok "synthetic entitlements terminated"

  # 4. and prove the appliance is actually in the state this claims
  verify_clean
  exit $rc
}

verify_clean() {
  local bad=0
  # Counted with one grep and wc -l rather than per-file counts summed with bc: grep -c prints a line PER
  # FILE, and bc is not installed on the appliance, so the first version compared a multi-line string against
  # "0" and reported flags set when none were. A verification that misreports is worse than none.
  local on; on="$(grep -rh '^STAYCONNECT_PHASE6_[A-Z_]*=true' "$ENV_DIR"/*.env 2>/dev/null | wc -l | tr -d ' ')"
  [ "${on:-0}" = "0" ] && ok "no Phase-6 flag is set anywhere" || { no "Phase-6 flags remain set" "$on"; bad=1; }

  local setting; setting="$(q "SELECT count(*) FROM iam_v2.appliance_product_settings WHERE guest_device_self_service")"
  [ "$setting" = "0" ] && ok "the product setting is OFF everywhere" || { no "the product setting is still ON" "$setting"; bad=1; }

  local live; live="$(q "SELECT count(*) FROM iam_v2.entitlements e JOIN iam_v2.purchases p ON p.id=e.purchase_id
                          WHERE p.trigger='$MARKER' AND e.status <> 'TERMINATED'")"
  [ "$live" = "0" ] && ok "no live synthetic entitlement remains" || { no "synthetic state remains live" "$live"; bad=1; }

  local down=""
  for u in $ALL_UNITS; do
    systemctl is-active --quiet "$u" || down="$down $u"
  done
  [ -z "$down" ] && ok "every required service is active" || { no "services not active" "$down"; bad=1; }

  echo "------------------------------------------------------------"
  if [ "$bad" = "0" ] && [ "$fail" -eq 0 ]; then
    printf 'PHASE6_CONTROLLED_VALIDATION pass=%d fail=%d — appliance left DARK and clean\n' "$pass" "$fail"
  else
    # The whole point: a run that cannot prove it cleaned up does not get to report success.
    printf 'PHASE6_CONTROLLED_VALIDATION pass=%d fail=%d — APPLIANCE MAY BE PARTIALLY ENABLED, INSPECT IT\n' \
      "$pass" "$fail"
    exit 1
  fi
}

guard_environment
trap cleanup EXIT INT TERM

if [ "${1:-}" = "cleanup" ]; then
  echo "== cleanup-only run =="
  exit 0     # the trap does the work, then verifies
fi

echo "== Phase-6 controlled validation on $(hostname) =="
echo "-- starting from a known-clean state --"
flags_off_everywhere
verify_clean_quiet() { :; }

# The validation body is intentionally minimal here: each product proof is added as its own step, and every
# one of them runs INSIDE this trap. Anything that fails, or any interruption, lands in cleanup above.
echo "(validation steps run here; the harness itself is what this file guarantees)"
