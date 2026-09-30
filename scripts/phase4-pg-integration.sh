#!/usr/bin/env bash
# Build a disposable PostgreSQL 16 carrying the FULL appliance schema -- exactly what a factory-clean appliance
# builds: Gate-P roles, the production baseline, Gate-P ownership, every migration above the baseline (0100:
# a room charge targets the reservation), Gate-P grants -- then run the Phase-4 financial-core integration
# matrix (build tags `integration phase4`) against it. Self-contained: it creates and tears down its own
# container. No Production/appliance access, no PMS, no financial egress.
#
# WHY THE FULL SCHEMA. This harness used to build the iam_v2 scratch chain plus migrations 0009-0026. That chain
# can never carry 0100, and the posting engine now reads what 0100 creates (posting_target_model, g_number,
# stay posting blocks, vendor-confirmed answers), so a matrix run on it tested a schema no appliance has. The
# build is shared with the other harnesses in scripts/lib-fullschema-testdb.sh.
#
# EXIT CODES (the CI retry policy depends on these):
#   0  every test passed
#   1  a TEST failed, or the schema did not apply — deterministic. CI must NOT retry: a second run that passes
#      would hide a defect.
#   2  the disposable infrastructure could not be built. That IS transient, and is the only retryable case.
set -uo pipefail
export PATH="$PATH:/c/Program Files/Docker/Docker/resources/bin"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
C="${PHASE4_INTEG_CONTAINER:-iamv2-p4integ}"; DB=iam_scratch; PORT="${PHASE4_INTEG_PORT:-55434}"
# shellcheck source=lib-fullschema-testdb.sh
. "$ROOT/scripts/lib-fullschema-testdb.sh"

cleanup(){ docker rm -f "$C" >/dev/null 2>&1 || true; }
trap cleanup EXIT
cleanup

echo "== disposable PG16, FULL appliance schema, for the phase-4 financial core (container=$C port=$PORT) =="
fullschema_build "$C" "$PORT" "$DB"; rc=$?
[ "$rc" = 0 ] || exit "$rc"

# The financial core's own objects, as named checks: a schema that built but lost one of them would otherwise
# surface as a confusing failure in an unrelated test.
have="$(docker exec "$C" psql -U postgres -d "$DB" -tAqc "SELECT count(*) FROM information_schema.columns WHERE table_schema='iam_v2' AND table_name='pms_interface_revisions' AND column_name='financial_base_currency';")"
if [ "${have:-0}" != "1" ]; then echo "0011's columns are missing - defect, not a flake"; exit 1; fi
hard="$(docker exec "$C" psql -U postgres -d "$DB" -tAqc "SELECT count(*) FROM pg_indexes WHERE schemaname='iam_v2' AND indexname='outbox_one_inflight_per_interface';")"
if [ "${hard:-0}" != "1" ]; then echo "0012's lane index is missing - defect, not a flake"; exit 1; fi
coh="$(docker exec "$C" psql -U postgres -d "$DB" -tAqc "SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='iam_v2' AND p.proname='begin_payment_execution';")"
if [ "${coh:-0}" != "1" ]; then echo "begin_payment_execution is missing - defect, not a flake"; exit 1; fi
echo "  full schema built: baseline + every migration above it + Gate-P"

export PHASE4_TEST_DSN="postgres://postgres:postgres@127.0.0.1:$PORT/$DB"
# The edged API contract tests use the Phase-3 DSN variable, because they are the same harness. Pointing it
# at THIS database is what lets the Manual Review routes be exercised against the financial schema.
export PHASE3_TEST_DSN="$PHASE4_TEST_DSN"
# The Phase-2 commerce suite runs against THIS database too. That is the point: the paid grant path reuses
# the Phase-2 writer, so the free path must be proven on the same schema the paid one runs on.
export PHASE2_TEST_DSN="$PHASE4_TEST_DSN"

# LOGIN roles that each hold exactly ONE production group role. The group roles are NOLOGIN by design -- a
# role that grants privileges should not also be a way in -- so the end-to-end restricted proofs need login
# principals that inherit them and nothing else. Disposable test infrastructure; no Production DSN or grant is
# touched.
fullschema_login "$C" "$DB" p4_runtime_login  runtimepw  sc_payment_runtime    || { echo "INFRA: runtime role"; exit 2; }
fullschema_login "$C" "$DB" p4_operator_login operatorpw sc_financial_operator || { echo "INFRA: operator role"; exit 2; }
export PHASE4_RUNTIME_DSN="postgres://p4_runtime_login:runtimepw@127.0.0.1:$PORT/$DB"
export PHASE4_OPERATOR_DSN="postgres://p4_operator_login:operatorpw@127.0.0.1:$PORT/$DB"

# The OUTCOME credential (0024) is a genuinely separate login holding only sc_payment_outcome. Sharing a
# login with the execution role would make every "the execution credential cannot assert an outcome" test
# vacuous, which is the whole property under test.
fullschema_login "$C" "$DB" p4_outcome_login  outcomepw  sc_payment_outcome  || { echo "INFRA: outcome role"; exit 2; }
fullschema_login "$C" "$DB" p4_commerce_login commercepw sc_commerce_runtime || { echo "INFRA: commerce role"; exit 2; }
export PHASE4_OUTCOME_DSN="postgres://p4_outcome_login:outcomepw@127.0.0.1:$PORT/$DB"
export PHASE4_COMMERCE_DSN="postgres://p4_commerce_login:commercepw@127.0.0.1:$PORT/$DB"

# ROOM CHARGE ACROSS THE REAL BOUNDARY (D45). The posting worker and pmsd each connect as their REAL Gate-P
# service role -- svc_posting and svc_pmsd, with exactly the privileges deploy/gatep gives them -- so the
# hand-off suite proves each side needs nothing more. Only a password is added, inside this disposable DB.
fullschema_service_password "$C" "$DB" svc_posting postingpw || { echo "INFRA: svc_posting"; exit 2; }
fullschema_service_password "$C" "$DB" svc_pmsd    pmsdpw    || { echo "INFRA: svc_pmsd"; exit 2; }
export ROOMCHARGE_TEST_DSN="$PHASE4_TEST_DSN"
export ROOMCHARGE_WORKER_DSN="postgres://svc_posting:postingpw@127.0.0.1:$PORT/$DB"
export ROOMCHARGE_PMSD_DSN="postgres://svc_pmsd:pmsdpw@127.0.0.1:$PORT/$DB"
# Charge health and Recovery read and act through edged's own login. The finops step proves it as svc_edged.
fullschema_service_password "$C" "$DB" svc_edged   edgedpw   || { echo "INFRA: svc_edged"; exit 2; }
export EDGED_ROLE_TEST_DSN="postgres://svc_edged:edgedpw@127.0.0.1:$PORT/$DB"
# ---------------------------------------------------------------- the suite
#
# Every step goes through run_step, for two reasons that a chain of copy-pasted `if [ "$rc" = 0 ]` blocks
# could not give:
#
#   * a step name is quoted ONCE, in a variable, so a label containing quotes cannot leak into the shell.
#     The previous form embedded a quoted phrase inside an already-quoted echo and a comment after a `&&`,
#     which produced a real `No such file or directory` INSIDE an otherwise green run. A deterministic
#     command error sitting quietly in a passing gate is worse than a failure: it trains a reader to
#     ignore the output.
#   * the FIRST failure stops the suite and is reported by name, so "which step failed" never has to be
#     inferred from where the output stops.
#
# PHASE4_SELFTEST_BREAK exists so the gate can be shown to fail on demand. A gate nobody has watched fail
# is a gate nobody has tested.
FAILED_STEP=""
run_step() {
  local name="$1"; shift
  [ -n "$FAILED_STEP" ] && return 0
  echo "== $name =="
  if [ "${PHASE4_SELFTEST_BREAK:-}" = "$name" ]; then
    echo "   (PHASE4_SELFTEST_BREAK: deliberately breaking this step to prove the gate reports it)"
    set -- /nonexistent-harness-command-for-selftest
  fi
  if ! ( cd "$ROOT/data-plane" && "$@" ); then
    FAILED_STEP="$name"
    echo "   STEP FAILED: $name"
  fi
}

# The `phase4` tag marks the tests that need the financial schema. Every harness now builds the full
# appliance schema, so the tag no longer separates schemas; it still keeps these suites out of the Phase-3
# gate's run, which is a scope decision, and this harness asks for it explicitly.
GO=(go test -tags "integration phase4" -count=1)

run_step "posting core"            "${GO[@]}" -run IntegrationPosting ./internal/posting/ "$@"
# The real boundary (D45): engine -> unix socket -> pmsd relay -> FIAS adapter -> fake PMS, with the worker and
# pmsd on their own Gate-P service roles (ROOMCHARGE_*_DSN above).
run_step "room charge hand-off"    "${GO[@]}" -run TestHandoff_ ./internal/posting/ "$@"
# Amendment A1 (D46) and financial freshness (D48): the room-move races, answer meanings, blocks, and all six
# room-charge stages agreeing on freshness -- on the worker's and pmsd's own logins.
run_step "room charge A1 + D48"   "${GO[@]}" -run "TestA1_|TestD48_|TestRoomCharge" ./internal/posting/ "$@"
run_step "review + finops API"     "${GO[@]}" -run "IntegrationReviewAPI|IntegrationFinOpsAPI|IntegrationZeroAttemptAPI" ./cmd/edged/ "$@"
run_step "payment runtime"         "${GO[@]}" -run IntegrationPayment ./internal/payment/ "$@"
# Narrowed to the free GRANT path deliberately: TestC2RollbackAtEveryBoundary seeds a fixed device MAC and
# collides with itself when it shares a database with another suite. That is a pre-existing fixture defect
# in a test unrelated to the grant writer, and widening this step would be testing the fixture.
run_step "phase-2 free grant path" "${GO[@]}"   -run "TestC2QuoteAndFreePurchase|TestC2ConcurrentSingleWinner|TestC4ImmutabilityAndPinTrigger"   ./internal/iamv2/
run_step "observability"           "${GO[@]}" -run IntegrationHealth ./internal/payment/ "$@"
run_step "recovery"                "${GO[@]}" -run IntegrationRecovery ./internal/payment/ "$@"
run_step "definer abuse"           "${GO[@]}" -run IntegrationDefinerAbuse ./internal/payment/ "$@"
run_step "restricted role"         "${GO[@]}" -run IntegrationRestricted ./internal/payment/ "$@"
run_step "entitlement grant"       "${GO[@]}" -run IntegrationGrant ./internal/payment/ "$@"
run_step "final closure"           "${GO[@]}" -run IntegrationClosure ./internal/payment/ "$@"
run_step "C35 fail-closed"         "${GO[@]}" -run IntegrationC35 ./internal/payment/ "$@"

if [ -n "$FAILED_STEP" ]; then
  echo "PHASE4_PG_INTEGRATION rc=1 (failed step: $FAILED_STEP)"
  exit 1
fi
echo "PHASE4_PG_INTEGRATION rc=0"
exit 0
