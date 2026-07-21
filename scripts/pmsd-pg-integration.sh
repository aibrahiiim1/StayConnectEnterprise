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
# robust readiness: a real query must succeed (pg_isready can pass during initdb's transient server, which
# then restarts -- running psql in that window fails with a socket error).
ready=0
for i in $(seq 1 60); do
  if docker exec "$C" psql -U postgres -d "$DB" -tAqc 'select 1' >/dev/null 2>&1; then ready=1; break; fi
  sleep 1
done
[ "$ready" = 1 ] || { echo "postgres did not become ready"; docker logs "$C" 2>&1 | tail -20; exit 1; }
sleep 1

# accepted schema exactly as the migration gate builds it (fixture + mg0 + mg1..mg9)
runout="$(SCRATCH_CONTAINER="$C" SCRATCH_DB="$DB" SCRATCH_PORT_ALLOW="$PORT" SCRATCH_ACK=I_UNDERSTAND_DISPOSABLE \
  bash "$ROOT/iam_v2_scratch/run.sh" fresh 2>&1)" || { echo "run.sh fresh FAILED:"; echo "$runout" | tail -20; exit 1; }
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
echo "== go test -tags integration ./internal/pmsd ./internal/stayengine ./internal/authctx ./internal/checkout ./internal/staygrant (Integration) =="
( cd "$ROOT/data-plane" && go test -tags integration -run Integration ./internal/pmsd/ ./internal/stayengine/ ./internal/authctx/ ./internal/checkout/ ./internal/staygrant/ -count=1 )
rc=$?
echo "PMSD_PG_INTEGRATION rc=$rc"
exit $rc
