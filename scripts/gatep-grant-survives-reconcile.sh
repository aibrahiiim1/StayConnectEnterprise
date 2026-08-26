#!/usr/bin/env bash
# A PRIVILEGE THE SERVICES NEED MUST SURVIVE A GATE-P RECONCILE.
#
# gatep-grants.sql is the whole reconcile, not half of it. It opens by REVOKEing ALL PRIVILEGES ON ALL
# FUNCTIONS IN SCHEMA iam_v2 from every service role, and then `\ir`-includes each per-service grant file, so
# the effective privilege set after it runs is EXACTLY what those files name. That is the design: the reconcile
# is the single authority on who may do what, and nothing can accumulate privilege quietly.
#
# The corollary is easy to miss. A grant written ONLY in a migration is temporary — real when the migration
# runs, and gone the first time anyone reconciles, permanently, because no per-service file re-grants it.
#
# That is what happened to iam_v2.p6_data_crossing. Migration 0041 grants EXECUTE on it to svc_acctd; a later
# reconcile removed it; and the PRE-LIVE appliance then logged "permission denied for function
# p6_data_crossing" on every expiry sweep — roughly once a second — with the sweep failing in its candidate
# query before it could evaluate a single entitlement. Nothing noticed, because a failing background sweep is
# quiet in every way except the log.
#
# This check proves BOTH halves, and the second is what stops it passing for the wrong reason:
#
#   1. POSITIVE — after a full reconcile, svc_acctd still holds EXECUTE on p6_data_crossing, because the
#      privilege is named in a per-service file rather than only in a migration.
#   2. CONTROL  — a migration-only grant does NOT survive the same reconcile. Without this, the positive half
#      would look identical whether the mechanism worked or the reconcile simply never revoked anything.
#
# EXIT CODES: 0 pass · 1 a check failed (deterministic, do not retry) · 2 the disposable cluster could not be
# built (transient, safe to retry).

set -uo pipefail
export PATH="$PATH:/c/Program Files/Docker/Docker/resources/bin"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GP="$ROOT/deploy/gatep"
C="gatep-reconcile-check-$$"
DB="stayconnect_site"

FAIL=0
ok(){  echo "  ok: $*"; }
bad(){ echo "  *** FAIL: $*"; FAIL=$((FAIL+1)); }

cleanup(){ docker rm -f "$C" >/dev/null 2>&1 || true; }
trap cleanup EXIT

q(){ docker exec "$C" psql -tAX -U postgres -d "$DB" -c "$1" 2>/dev/null | tr -d '[:space:]'; }
sql(){ docker exec "$C" psql -v ON_ERROR_STOP=1 -U postgres -d "$DB" -tAX -c "$1" >/dev/null 2>&1; }
holds(){ [ "$(q "SELECT has_function_privilege('$1','$2','EXECUTE')")" = "t" ]; }

# The reconcile runs from a copy of the directory INSIDE the container, with -f rather than on stdin:
# gatep-grants.sql `\ir`-includes its per-service files, and a relative include has no directory to resolve
# against when psql reads from a pipe.
#
# The doubled leading slash stops Git Bash rewriting an in-container path into a host one. Linux collapses it,
# so the identical string is correct on the CI runner; a global MSYS_NO_PATHCONV would fix this line and break
# the cleanroom build's own host paths, so the escape stays local to the argument that needs it.
reconcile(){ docker exec "$C" psql -v ON_ERROR_STOP=1 -U postgres -d "$DB" -q -f "//tmp/gatep/gatep-grants.sql" >/tmp/gr.out 2>&1; }

# Every privilege that must survive a reconcile, as "role|function". Each was, at some point, granted only by
# a migration and therefore silently removed by a reconcile; each is now named in a per-service file.
KEPT_PAIRS=(
  "svc_acctd|iam_v2.p6_data_crossing(uuid)"
  "svc_acctd|iam_v2.p6_expire_entitlement(uuid)"
  "svc_acctd|iam_v2.p6_due_terminal(uuid)"
  "svc_acctd|iam_v2.p6_suspend_over_budget(uuid, uuid)"
  "svc_acctd|iam_v2.p6_tick_online_time(uuid, uuid, timestamptz, int, uuid[], timestamptz[])"
  "svc_scd|iam_v2.p3_feed_authorizes(uuid, uuid, uuid, uuid, timestamptz)"
  "svc_scd|iam_v2.p3_cfg_secs(jsonb, text, int)"
  "svc_scd|iam_v2.record_auth_context_offer(uuid, uuid, uuid, uuid, integer, bigint, timestamptz)"
  "svc_scd|iam_v2.issue_or_return_pms_context(uuid, uuid, uuid, uuid, uuid, uuid, uuid, uuid, integer)"
  # The Room Login chain, added after the first real guest was refused at the grant. The offer-lock helper was
  # first granted only from gatep-grants.sql AFTER its COMMIT, which works once and is outside the reconcile's
  # own transaction; all six now live in the per-service files, and this proves they stay there.
  "svc_scd|iam_v2.lock_auth_context_offer(uuid, uuid, uuid, uuid)"
  "svc_scd|iam_v2.lock_pms_interface_runtime(uuid, uuid, uuid)"
  "svc_scd|iam_v2.lock_stay(uuid, uuid, uuid)"
  "svc_scd|iam_v2.lock_origin_stay(uuid, uuid, uuid)"
  "svc_scd|iam_v2.apply_entitlement_transition(uuid, text, timestamptz, text)"
  "svc_scd|iam_v2.authorize_entitlement_device(uuid, uuid, timestamptz)"
)

# The control: a function granted by a migration and named in NO per-service file, so it must NOT survive.
# Without it the positive results below would look identical whether the mechanism works or the reconcile
# simply never revokes anything.
CONTROL_ROLE=svc_netd
CONTROL_FN='iam_v2.p6_data_crossing(uuid)'

echo "== disposable cluster with the real schema and the real Gate-P roles =="
if ! CLEANROOM_KEEP=1 CLEANROOM_NAME="$C" bash "$ROOT/scripts/clean-install-reconstruction.sh" >/tmp/gr-build.log 2>&1; then
  echo "INFRA: could not build the cleanroom"; tail -20 /tmp/gr-build.log; exit 2
fi
docker inspect "$C" >/dev/null 2>&1 || { echo "INFRA: no cleanroom container"; exit 2; }
docker cp "$GP/." "$C:/tmp/gatep/" >/dev/null 2>&1 || { echo "INFRA: could not stage the Gate-P scripts"; exit 2; }

for pair in "${KEPT_PAIRS[@]}"; do
  fn="${pair#*|}"
  if [ "$(q "SELECT to_regprocedure('$fn') IS NOT NULL")" != "t" ]; then
    echo "  *** FAIL: $fn does not exist in the built schema; this check cannot mean anything"; exit 1
  fi
done
for role in svc_acctd svc_scd svc_netd; do
  if [ "$(q "SELECT count(*) FROM pg_roles WHERE rolname='$role'")" != "1" ]; then
    echo "INFRA: role $role was not created by the cleanroom build"; exit 2
  fi
done
ok "schema and roles present (${#KEPT_PAIRS[@]} privileges under test)"

# The control is only a control while no per-service file grants that function to that role.
if grep -l "p6_data_crossing" "$GP"/svc-netd-*.sql >/dev/null 2>&1; then
  echo "  *** FAIL: $CONTROL_FN is now named for $CONTROL_ROLE in a per-service file, so it can no longer"
  echo "            serve as the control. Choose another role/function pair granted only by a migration."
  exit 1
fi
ok "control pair is granted by no per-service file"

echo
echo "== a full reconcile =="
# Everything is granted the way a migration grants it, so the pairs differ only in whether a per-service file
# also names them.
for pair in "${KEPT_PAIRS[@]}"; do
  sql "GRANT EXECUTE ON FUNCTION ${pair#*|} TO ${pair%%|*}"
done
sql "GRANT EXECUTE ON FUNCTION $CONTROL_FN TO $CONTROL_ROLE"
if ! holds "$CONTROL_ROLE" "$CONTROL_FN"; then
  echo "  *** FAIL: the control grant did not take effect; the comparison proves nothing"; exit 1
fi
ok "all privileges granted the way a migration grants them"

if ! reconcile; then
  echo "INFRA: gatep-grants.sql failed to apply"; tail -20 /tmp/gr.out; exit 2
fi

if holds "$CONTROL_ROLE" "$CONTROL_FN"; then
  bad "the reconcile did NOT remove a migration-only grant — this check can no longer detect the defect it exists for, so the positive results below mean nothing"
else
  ok "CONTROL: a migration-only grant is gone after the reconcile, exactly as the appliance experienced"
fi

for pair in "${KEPT_PAIRS[@]}"; do
  role="${pair%%|*}"; fn="${pair#*|}"
  if holds "$role" "$fn"; then
    ok "$role holds EXECUTE on $fn"
  else
    bad "$role LOST EXECUTE on $fn — name it in a per-service file, not only in a migration"
  fi
done

# A privilege that survives the first reconcile and not the second is the same outage with a longer fuse.
echo
echo "== and again, because a reconcile is not a one-time event =="
reconcile || { echo "INFRA: second reconcile failed"; tail -20 /tmp/gr.out; exit 2; }
for pair in "${KEPT_PAIRS[@]}"; do
  role="${pair%%|*}"; fn="${pair#*|}"
  holds "$role" "$fn" || bad "$role lost EXECUTE on $fn on the SECOND reconcile"
done
[ "$FAIL" -eq 0 ] && ok "all ${#KEPT_PAIRS[@]} privileges still held after a second reconcile"

echo
if [ "$FAIL" -eq 0 ]; then
  echo "GATEP_GRANT_SURVIVES_RECONCILE = PASS"
  exit 0
fi
echo "GATEP_GRANT_SURVIVES_RECONCILE = FAIL ($FAIL)"
exit 1
