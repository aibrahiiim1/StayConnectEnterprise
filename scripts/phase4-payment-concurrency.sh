#!/usr/bin/env bash
# Self-contained payment-concurrency gate: builds its own disposable PG16, applies the full chain, and runs
# the real concurrent-session proof. It is a SEPARATE gate rather than a step inside the DB gate because the
# proof backgrounds psql sessions, and running it inside a command substitution changes their scheduling —
# which is exactly the kind of timing dependence this proof exists to rule out.
#
# EXIT: 0 pass, 1 assertion failure (never retry), 2 infrastructure (retryable).
set -uo pipefail
export PATH="$PATH:/c/Program Files/Docker/Docker/resources/bin"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
C="${PHASE4_CONC_CONTAINER:-iamv2-p4conc}"; DB=iam_scratch; PORT="${PHASE4_CONC_PORT:-55439}"

cleanup(){ docker rm -f "$C" >/dev/null 2>&1 || true; }
trap cleanup EXIT
cleanup
docker run -d --name "$C" -e POSTGRES_PASSWORD=postgres -e POSTGRES_DB="$DB" \
  -p "127.0.0.1:$PORT:5432" postgres:16-alpine >/dev/null 2>&1 || { echo "INFRA: container"; exit 2; }
for i in $(seq 1 60); do docker exec "$C" psql -U postgres -d "$DB" -tAqc 'select 1' >/dev/null 2>&1 && break; sleep 1; done
docker exec "$C" psql -U postgres -d "$DB" -tAqc 'select 1' >/dev/null 2>&1 || { echo "INFRA: not ready"; exit 2; }
SCRATCH_CONTAINER="$C" SCRATCH_DB="$DB" SCRATCH_PORT_ALLOW="$PORT" SCRATCH_ACK=I_UNDERSTAND_DISPOSABLE \
  bash "$ROOT/iam_v2_scratch/run.sh" fresh >/dev/null 2>&1 || { echo "INFRA: schema"; exit 2; }
docker exec "$C" psql -U postgres -d "$DB" -tAqc "CREATE TABLE IF NOT EXISTS public.schema_migrations(version text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now());" >/dev/null
for m in 0009_phase2_commerce 0010_phase3_stay_resolution 0011_phase4_financial_execution \
         0012_phase4_financial_hardening 0013_phase4_reversal_ledger 0014_phase4_payment_settlement \
         0015_phase4_payment_hardening 0016_phase4_payment_coherence 0017_phase4_least_privilege 0018_phase4_financial_identity_and_privilege 0019_phase4_financial_recovery 0020_phase4_financial_observability 0021_phase4_trust_boundary; do
  docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 < "$ROOT/data-plane/migrations/$m.up.sql" >/dev/null 2>&1 \
    || { echo "$m did not apply — a defect, not a flake"; exit 1; }
done
docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 < "$ROOT/iam_v2_scratch/seed.sql" >/dev/null 2>&1
docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 < "$ROOT/iam_v2_scratch/phase4_financial_fixture.sql" >/dev/null 2>&1
PHASE4_CONC_CONTAINER="$C" PHASE4_CONC_DB="$DB" bash "$ROOT/iam_v2_scratch/phase4_payment_concurrency.sh"
