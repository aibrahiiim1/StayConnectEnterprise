#!/usr/bin/env bash
# Build a disposable PostgreSQL 16 with the accepted iam_v2 schema + migration 0010, then run the pmsd
# PG16 integration tests (build tag `integration`) against it. Self-contained: creates + tears down its own
# container. No Production/appliance access.
set -uo pipefail
export PATH="$PATH:/c/Program Files/Docker/Docker/resources/bin"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
C=iamv2-scratch; DB=iam_scratch; PORT="${PMSD_INTEG_PORT:-55432}"
UPSHA="$(sha256sum "$ROOT/data-plane/migrations/0010_phase3_stay_resolution.up.sql" | awk '{print $1}')"

cleanup(){ docker rm -f "$C" >/dev/null 2>&1 || true; }
trap cleanup EXIT
cleanup
echo "== disposable PG16 for pmsd integration (container=$C port=$PORT) =="
docker run -d --name "$C" -e POSTGRES_PASSWORD=postgres -e POSTGRES_DB="$DB" -p 127.0.0.1:$PORT:5432 postgres:16-alpine >/dev/null
for i in $(seq 1 30); do docker exec "$C" pg_isready -U postgres -d "$DB" >/dev/null 2>&1 && break; sleep 1; done

# accepted schema exactly as the migration gate builds it (fixture + mg0 + mg1..mg9)
SCRATCH_CONTAINER="$C" SCRATCH_DB="$DB" SCRATCH_PORT_ALLOW="$PORT" SCRATCH_ACK=I_UNDERSTAND_DISPOSABLE \
  bash "$ROOT/iam_v2_scratch/run.sh" fresh >/dev/null 2>&1
# ledger + 0009 baseline + 0010 via the authoritative runner
docker exec "$C" psql -U postgres -d "$DB" -tAqc "CREATE TABLE IF NOT EXISTS public.schema_migrations(version text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now());" >/dev/null
docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 < "$ROOT/data-plane/migrations/0009_phase2_commerce.up.sql" >/dev/null 2>&1
docker exec "$C" psql -U postgres -d "$DB" -tAqc "INSERT INTO public.schema_migrations(version) VALUES ('0009_phase2_commerce') ON CONFLICT DO NOTHING;" >/dev/null
export EDGE_PSQL="docker exec -i $C psql -U postgres -d $DB -v ON_ERROR_STOP=1"
bash "$ROOT/scripts/edge-migrate.sh" --only 0010_phase3_stay_resolution --expect-db "$DB" \
  --target-kind disposable --ack-target I_UNDERSTAND_DISPOSABLE_DATABASE --expect-sha256 "$UPSHA" >/dev/null 2>&1

built="$(docker exec "$C" psql -U postgres -d "$DB" -tAqc "SELECT count(*) FROM information_schema.tables WHERE table_schema='iam_v2';")"
if [ "${built:-0}" -lt 40 ]; then echo "SCHEMA BUILD FAILED (iam_v2 tables=$built)"; exit 1; fi
runtime_cols="$(docker exec "$C" psql -U postgres -d "$DB" -tAqc "SELECT count(*) FROM information_schema.columns WHERE table_schema='iam_v2' AND table_name='pms_interface_runtime' AND column_name='pinned_secret_generation_id';")"
if [ "${runtime_cols:-0}" != "1" ]; then echo "0010 NOT APPLIED (pinned_secret_generation_id missing)"; exit 1; fi
echo "  iam_v2 tables=$built + 0010 applied"

export PHASE3_TEST_DSN="postgres://postgres:postgres@127.0.0.1:$PORT/$DB"
echo "== go test -tags integration ./internal/pmsd (Integration) =="
( cd "$ROOT/data-plane" && go test -tags integration -run Integration ./internal/pmsd/ -count=1 )
rc=$?
echo "PMSD_PG_INTEGRATION rc=$rc"
exit $rc
