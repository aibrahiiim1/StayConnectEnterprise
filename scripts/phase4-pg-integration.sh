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

for m in 0011_phase4_financial_execution 0012_phase4_financial_hardening 0013_phase4_reversal_ledger 0014_phase4_payment_settlement 0015_phase4_payment_hardening 0016_phase4_payment_coherence 0017_phase4_least_privilege 0018_phase4_financial_identity_and_privilege 0019_phase4_financial_recovery 0020_phase4_financial_observability; do
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
echo "== go test -tags integration -run IntegrationPosting ./internal/posting/ =="
( cd "$ROOT/data-plane" && go test -tags integration -run IntegrationPosting ./internal/posting/ -count=1 "$@" )
rc=$?
if [ "$rc" = 0 ]; then
  echo "== go test -tags integration -run IntegrationReviewAPI ./cmd/edged/ =="
  ( cd "$ROOT/data-plane" && go test -tags integration -run IntegrationReviewAPI ./cmd/edged/ -count=1 "$@" )
  rc=$?
fi
if [ "$rc" = 0 ]; then
  echo "== go test -tags integration -run IntegrationPayment ./internal/payment/ =="
  ( cd "$ROOT/data-plane" && go test -tags integration -run IntegrationPayment ./internal/payment/ -count=1 "$@" )
  rc=$?
fi
if [ "$rc" = 0 ]; then
  echo "== go test -tags integration -run "the phase-2 free grant path" ./internal/iamv2/ on this schema =="
  ( cd "$ROOT/data-plane" && # Narrowed to the free GRANT path deliberately. TestC2RollbackAtEveryBoundary seeds a fixed device MAC and
    # collides with itself when it shares a database with another suite; that is a pre-existing fixture defect
    # in a test unrelated to the grant writer, and widening this step to it would be testing the fixture.
    go test -tags integration -run "TestC2QuoteAndFreePurchase|TestC2ConcurrentSingleWinner|TestC4ImmutabilityAndPinTrigger" ./internal/iamv2/ -count=1 )
  rc=$?
fi
if [ "$rc" = 0 ]; then
  echo "== go test -tags integration -run IntegrationHealth ./internal/payment/ (observability + redaction) =="
  ( cd "$ROOT/data-plane" && go test -tags integration -run IntegrationHealth ./internal/payment/ -count=1 "$@" )
  rc=$?
fi
if [ "$rc" = 0 ]; then
  echo "== go test -tags integration -run IntegrationRecovery ./internal/payment/ (FINANCIAL_RECOVERY_MODE) =="
  ( cd "$ROOT/data-plane" && go test -tags integration -run IntegrationRecovery ./internal/payment/ -count=1 "$@" )
  rc=$?
fi
if [ "$rc" = 0 ]; then
  echo "== go test -tags integration -run IntegrationRestricted ./internal/payment/ (as sc_payment_runtime) =="
  ( cd "$ROOT/data-plane" && go test -tags integration -run IntegrationRestricted ./internal/payment/ -count=1 "$@" )
  rc=$?
fi
if [ "$rc" = 0 ]; then
  echo "== go test -tags integration -run IntegrationGrant ./internal/payment/ =="
  ( cd "$ROOT/data-plane" && go test -tags integration -run IntegrationGrant ./internal/payment/ -count=1 "$@" )
  rc=$?
fi
echo "PHASE4_PG_INTEGRATION rc=$rc"
exit $rc
