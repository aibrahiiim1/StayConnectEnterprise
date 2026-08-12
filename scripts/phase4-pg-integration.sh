#!/usr/bin/env bash
# Build a disposable PostgreSQL 16 carrying the authoritative chain PLUS migration 0011, then run the
# Phase-4 financial-core integration matrix (build tag `integration`) against it. Self-contained: it
# creates and tears down its own container. No Production/appliance access, no PMS, no financial egress.
#
# EXIT CODES (the CI retry policy depends on these):
#   0  every test passed
#   1  a TEST failed — deterministic. CI must NOT retry: a second run that passes would hide a defect.
#   2  the disposable infrastructure could not be built. That IS transient, and is the only retryable case.
set -uo pipefail
export PATH="$PATH:/c/Program Files/Docker/Docker/resources/bin"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
C="${PHASE4_INTEG_CONTAINER:-iamv2-p4integ}"; DB=iam_scratch; PORT="${PHASE4_INTEG_PORT:-55434}"

cleanup(){ docker rm -f "$C" >/dev/null 2>&1 || true; }
trap cleanup EXIT
cleanup

echo "== disposable PG16 for the phase-4 financial core (container=$C port=$PORT) =="
docker run -d --name "$C" -e POSTGRES_PASSWORD=postgres -e POSTGRES_DB="$DB" \
  -p "127.0.0.1:$PORT:5432" postgres:16-alpine >/dev/null 2>&1 \
  || { echo "INFRA: could not start the disposable container"; exit 2; }
ready=0
for i in $(seq 1 60); do
  docker exec "$C" psql -U postgres -d "$DB" -tAqc 'select 1' >/dev/null 2>&1 && { ready=1; break; }
  sleep 1
done
[ "$ready" = 1 ] || { echo "INFRA: postgres did not become ready"; docker logs "$C" 2>&1 | tail -20; exit 2; }
sleep 1

runout="$(SCRATCH_CONTAINER="$C" SCRATCH_DB="$DB" SCRATCH_PORT_ALLOW="$PORT" SCRATCH_ACK=I_UNDERSTAND_DISPOSABLE \
  bash "$ROOT/iam_v2_scratch/run.sh" fresh 2>&1)" || { echo "INFRA: run.sh fresh FAILED:"; echo "$runout" | tail -20; exit 2; }
docker exec "$C" psql -U postgres -d "$DB" -tAqc "CREATE TABLE IF NOT EXISTS public.schema_migrations(version text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now());" >/dev/null
docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 < "$ROOT/data-plane/migrations/0009_phase2_commerce.up.sql" >/dev/null 2>&1
docker exec "$C" psql -U postgres -d "$DB" -tAqc "INSERT INTO public.schema_migrations(version) VALUES ('0009_phase2_commerce') ON CONFLICT DO NOTHING;" >/dev/null
docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 < "$ROOT/data-plane/migrations/0010_phase3_stay_resolution.up.sql" >/dev/null 2>&1

pre="$(docker exec "$C" psql -U postgres -d "$DB" -tAqc "SELECT count(*) FROM information_schema.tables WHERE table_schema='iam_v2';")"
if [ "${pre:-0}" != "63" ]; then echo "INFRA: pre-0011 chain did not build (iam_v2 tables=$pre)"; exit 2; fi

for m in 0011_phase4_financial_execution 0012_phase4_financial_hardening 0013_phase4_reversal_ledger 0014_phase4_payment_settlement 0015_phase4_payment_hardening 0016_phase4_payment_coherence 0017_phase4_least_privilege 0018_phase4_financial_identity_and_privilege 0019_phase4_financial_recovery 0020_phase4_financial_observability 0021_phase4_trust_boundary 0022_phase4_recovery_closure 0023_phase4_restore_generation 0024_phase4_outcome_authority_and_grant_kernel 0025_phase4_recovery_completion_and_compliance; do
  if ! docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 \
       < "$ROOT/data-plane/migrations/$m.up.sql" >/dev/null 2>&1; then
    # Deterministic: a broken migration fails the same way twice, so this is exit 1 and CI must not retry.
    echo "$m DID NOT APPLY - this is a defect, not a flake"
    docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 \
      < "$ROOT/data-plane/migrations/$m.up.sql" 2>&1 | tail -10
    exit 1
  fi
done
have="$(docker exec "$C" psql -U postgres -d "$DB" -tAqc "SELECT count(*) FROM information_schema.columns WHERE table_schema='iam_v2' AND table_name='pms_interface_revisions' AND column_name='financial_base_currency';")"
if [ "${have:-0}" != "1" ]; then echo "0011 applied but its columns are missing - defect, not a flake"; exit 1; fi
hard="$(docker exec "$C" psql -U postgres -d "$DB" -tAqc "SELECT count(*) FROM pg_indexes WHERE schemaname='iam_v2' AND indexname='outbox_one_inflight_per_interface';")"
if [ "${hard:-0}" != "1" ]; then echo "0012 applied but its lane index is missing - defect, not a flake"; exit 1; fi
coh="$(docker exec "$C" psql -U postgres -d "$DB" -tAqc "SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='iam_v2' AND p.proname='begin_payment_execution';")"
if [ "${coh:-0}" != "1" ]; then echo "0016 applied but begin_payment_execution is missing - defect, not a flake"; exit 1; fi
echo "  iam_v2 tables=$pre + 0011 + 0012 + 0013 + 0014 + 0015 + 0016 applied"

# public.operators comes from migration 0001 (the appliance's own schema), which the iam_v2 scratch chain
# does not apply. The financial actor assertion added in 0021 checks the recorded author against it, so the
# disposable database needs the same shape -- otherwise the tests would be exercising an assertion that
# always errors rather than one that discriminates.
docker exec "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 -tAqc   "CREATE TABLE IF NOT EXISTS public.operators (
     id uuid PRIMARY KEY DEFAULT gen_random_uuid(), tenant_id uuid, email text NOT NULL,
     display_name text, password_hash text, status text NOT NULL DEFAULT 'active',
     created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
     auth_method text NOT NULL DEFAULT 'local');" >/dev/null || { echo "INFRA: operators"; exit 2; }

export PHASE4_TEST_DSN="postgres://postgres:postgres@127.0.0.1:$PORT/$DB"
# The edged API contract tests use the Phase-3 DSN variable, because they are the same harness. Pointing it
# at THIS database is what lets the Manual Review routes be exercised against 0011+0012+0013.
export PHASE3_TEST_DSN="$PHASE4_TEST_DSN"
# The Phase-2 commerce suite runs against THIS database too. That is the point: the paid grant path reuses
# the Phase-2 writer, so the free path must be proven on the same schema the paid one runs on -- a Phase-2
# suite that only ever sees the pre-0010 chain cannot catch a writer that later schema rejects.
export PHASE2_TEST_DSN="$PHASE4_TEST_DSN"

# A LOGIN role that holds ONLY sc_payment_runtime. The migration's roles are NOLOGIN by design -- a role
# that grants privileges should not also be a way in -- so the end-to-end restricted proof needs a login
# principal that inherits it and nothing else. This is disposable test infrastructure; no Production DSN or
# grant is touched anywhere in this milestone.
docker exec "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 -tAqc   "DO \$\$ BEGIN
     IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='p4_runtime_login') THEN
       CREATE ROLE p4_runtime_login LOGIN PASSWORD 'runtimepw' INHERIT;
     END IF;
   END \$\$;
   GRANT sc_payment_runtime TO p4_runtime_login;
   GRANT USAGE ON SCHEMA public TO p4_runtime_login;
   DO \$\$ BEGIN
     IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='p4_operator_login') THEN
       CREATE ROLE p4_operator_login LOGIN PASSWORD 'operatorpw' INHERIT;
     END IF;
   END \$\$;
   GRANT sc_financial_operator TO p4_operator_login;
   GRANT USAGE ON SCHEMA public TO p4_operator_login;" >/dev/null || { echo "INFRA: runtime role"; exit 2; }
export PHASE4_RUNTIME_DSN="postgres://p4_runtime_login:runtimepw@127.0.0.1:$PORT/$DB"
export PHASE4_OPERATOR_DSN="postgres://p4_operator_login:operatorpw@127.0.0.1:$PORT/$DB"

# The OUTCOME credential (0024) is a genuinely separate login holding only sc_payment_outcome. Sharing a
# login with the execution role would make every "the execution credential cannot assert an outcome" test
# vacuous, which is the whole property under test.
docker exec "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 -tAqc   "DO \$\$ BEGIN
     IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='p4_outcome_login') THEN
       CREATE ROLE p4_outcome_login LOGIN PASSWORD 'outcomepw' INHERIT;
     END IF;
     IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='p4_commerce_login') THEN
       CREATE ROLE p4_commerce_login LOGIN PASSWORD 'commercepw' INHERIT;
     END IF;
   END \$\$;
   GRANT sc_payment_outcome TO p4_outcome_login;
   GRANT sc_commerce_runtime TO p4_commerce_login;
   GRANT USAGE ON SCHEMA public TO p4_outcome_login, p4_commerce_login;" >/dev/null   || { echo "INFRA: outcome role"; exit 2; }
export PHASE4_OUTCOME_DSN="postgres://p4_outcome_login:outcomepw@127.0.0.1:$PORT/$DB"
export PHASE4_COMMERCE_DSN="postgres://p4_commerce_login:commercepw@127.0.0.1:$PORT/$DB"
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

GO=(go test -tags integration -count=1)

run_step "posting core"            "${GO[@]}" -run IntegrationPosting ./internal/posting/ "$@"
run_step "review + finops API"     "${GO[@]}" -run "IntegrationReviewAPI|IntegrationFinOpsAPI" ./cmd/edged/ "$@"
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

if [ -n "$FAILED_STEP" ]; then
  echo "PHASE4_PG_INTEGRATION rc=1 (failed step: $FAILED_STEP)"
  exit 1
fi
echo "PHASE4_PG_INTEGRATION rc=0"
exit 0
