#!/usr/bin/env bash
# Build a disposable PostgreSQL 16 with the accepted iam_v2 schema + migration 0010, then run the pmsd
# PG16 integration tests (build tag `integration`) against it. Self-contained: creates + tears down its own
# container. No Production/appliance access.
#
# EXIT CODES (the CI retry policy depends on these):
#   0  every test passed
#   1  a TEST failed — deterministic. CI must NOT retry: a second run that passes would be hiding a defect.
#   2  the disposable infrastructure could not be built (container, image, schema bootstrap). That IS
#      transient under runner load, and is the only condition CI may retry.
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
[ "$ready" = 1 ] || { echo "INFRA: postgres did not become ready"; docker logs "$C" 2>&1 | tail -20; exit 2; }
sleep 1

# accepted schema exactly as the migration gate builds it (fixture + mg0 + mg1..mg9)
runout="$(SCRATCH_CONTAINER="$C" SCRATCH_DB="$DB" SCRATCH_PORT_ALLOW="$PORT" SCRATCH_ACK=I_UNDERSTAND_DISPOSABLE \
  bash "$ROOT/iam_v2_scratch/run.sh" fresh 2>&1)" || { echo "INFRA: run.sh fresh FAILED:"; echo "$runout" | tail -20; exit 2; }
# ledger + 0009 baseline + 0010 via the authoritative runner
docker exec "$C" psql -U postgres -d "$DB" -tAqc "CREATE TABLE IF NOT EXISTS public.schema_migrations(version text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now());" >/dev/null
docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 < "$ROOT/data-plane/migrations/0009_phase2_commerce.up.sql" >/dev/null 2>&1
docker exec "$C" psql -U postgres -d "$DB" -tAqc "INSERT INTO public.schema_migrations(version) VALUES ('0009_phase2_commerce') ON CONFLICT DO NOTHING;" >/dev/null
export EDGE_PSQL="docker exec -i $C psql -U postgres -d $DB -v ON_ERROR_STOP=1"
bash "$ROOT/scripts/edge-migrate.sh" --only 0010_phase3_stay_resolution --expect-db "$DB" \
  --target-kind disposable --ack-target I_UNDERSTAND_DISPOSABLE_DATABASE --expect-sha256 "$UPSHA" >/dev/null 2>&1

# 0050 redefines a Phase-3 object this gate owns: issue_or_return_pms_context, plus the shared
# iam_v2.p3_stay_authorizable predicate the authctx suite exercises directly. The gate builds the schema Phase 3
# ships, and that now includes this. Applied with ON_ERROR_STOP so a broken migration fails here, loudly,
# rather than surfacing later as a missing-function mystery in an unrelated test.
if ! docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 \
     < "$ROOT/data-plane/migrations/0050_pms_auth_freshness_follows_feed_health.up.sql" >/dev/null 2>&1; then
  echo "0050 FAILED TO APPLY -- deterministic, not a flake"
  exit 1
fi
docker exec "$C" psql -U postgres -d "$DB" -tAqc \
  "INSERT INTO public.schema_migrations(version) VALUES ('0050_pms_auth_freshness_follows_feed_health') ON CONFLICT DO NOTHING;" >/dev/null

# 0051 narrows the same predicate's continuity term to CONTINUOUS only. The authctx suite asserts that UNKNOWN
# continuity cannot authorise, which is false against 0050 alone.
if ! docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 \
     < "$ROOT/data-plane/migrations/0051_continuity_unknown_is_not_a_healthy_feed.up.sql" >/dev/null 2>&1; then
  echo "0051 FAILED TO APPLY -- deterministic, not a flake"
  exit 1
fi
docker exec "$C" psql -U postgres -d "$DB" -tAqc \
  "INSERT INTO public.schema_migrations(version) VALUES ('0051_continuity_unknown_is_not_a_healthy_feed') ON CONFLICT DO NOTHING;" >/dev/null

# 0052 adds iam_v2.p3_lock_grace_config, which the Checkout Converter now calls instead of locking the grace
# config table directly. The checkout suite exercises that path, including its concurrency behaviour.
if ! docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 \
     < "$ROOT/data-plane/migrations/0052_grace_config_lock_without_update_privilege.up.sql" >/dev/null 2>&1; then
  echo "0052 FAILED TO APPLY -- deterministic, not a flake"
  exit 1
fi
docker exec "$C" psql -U postgres -d "$DB" -tAqc \
  "INSERT INTO public.schema_migrations(version) VALUES ('0052_grace_config_lock_without_update_privilege') ON CONFLICT DO NOTHING;" >/dev/null
# 0053 splits the same predicate's transport term in two: a live feed OR a trusted local mirror. Without it
# every offline case in the mirror-trust and local-mirror suites fails, because the database still refuses any
# guest whose PMS socket is down -- which is the exact behaviour those suites exist to prove is gone.
if ! docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1      < "$ROOT/data-plane/migrations/0053_local_mirror_authorizes_when_transport_is_down.up.sql" >/dev/null 2>&1; then
  echo "0053 FAILED TO APPLY -- deterministic, not a flake"
  exit 1
fi
docker exec "$C" psql -U postgres -d "$DB" -tAqc   "INSERT INTO public.schema_migrations(version) VALUES ('0053_local_mirror_authorizes_when_transport_is_down') ON CONFLICT DO NOTHING;" >/dev/null

# 0054 adds the operator resync command channel and the durable sync-progress columns. The pmsd suites claim
# commands and write stages against the real CHECK constraints, so without it they fail on missing columns.
if ! docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 \
     < "$ROOT/data-plane/migrations/0054_operator_resync_command_and_sync_progress.up.sql" >/dev/null 2>&1; then
  echo "0054 FAILED TO APPLY -- deterministic, not a flake"
  exit 1
fi
docker exec "$C" psql -U postgres -d "$DB" -tAqc \
  "INSERT INTO public.schema_migrations(version) VALUES ('0054_operator_resync_command_and_sync_progress') ON CONFLICT DO NOTHING;" >/dev/null

# 0055 replaces edged direct write access with a narrow SECURITY DEFINER function. The edged API suite calls
# the Full Resync endpoint, which fails with permission denied without it.
if ! docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 \
     < "$ROOT/data-plane/migrations/0055_request_full_resync_without_runtime_write.up.sql" >/dev/null 2>&1; then
  echo "0055 FAILED TO APPLY -- deterministic, not a flake"
  exit 1
fi
docker exec "$C" psql -U postgres -d "$DB" -tAqc \
  "INSERT INTO public.schema_migrations(version) VALUES ('0055_request_full_resync_without_runtime_write') ON CONFLICT DO NOTHING;" >/dev/null

# 0056 adds the materialization-readiness term and the partial index the authctx suites assert.
if ! docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 \
     < "$ROOT/data-plane/migrations/0056_materialization_readiness.up.sql" >/dev/null 2>&1; then
  echo "0056 FAILED TO APPLY -- deterministic, not a flake"
  exit 1
fi
docker exec "$C" psql -U postgres -d "$DB" -tAqc \
  "INSERT INTO public.schema_migrations(version) VALUES ('0056_materialization_readiness') ON CONFLICT DO NOTHING;" >/dev/null

# 0057 adds the scoped offer-lock helper the grant needs; without it the svc_scd grant test fails.
if ! docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 \
     < "$ROOT/data-plane/migrations/0057_lock_auth_context_offer.up.sql" >/dev/null 2>&1; then
  echo "0057 FAILED TO APPLY -- deterministic, not a flake"
  exit 1
fi
docker exec "$C" psql -U postgres -d "$DB" -tAqc \
  "INSERT INTO public.schema_migrations(version) VALUES ('0057_lock_auth_context_offer') ON CONFLICT DO NOTHING;" >/dev/null
# 0058 adds the scoped row-lock helpers. internal/authctx calls them on both the issue and the
# consume path, so without it every PMS auth test fails with "function does not exist".
if ! docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 \
     < "$ROOT/data-plane/migrations/0058_guest_auth_row_locks.up.sql" >/dev/null 2>&1; then
  echo "0058 FAILED TO APPLY -- deterministic, not a flake"
  exit 1
fi
docker exec "$C" psql -U postgres -d "$DB" -tAqc \
  "INSERT INTO public.schema_migrations(version) VALUES ('0058_guest_auth_row_locks') ON CONFLICT DO NOTHING;" >/dev/null

# 0059 adds service_plan_revisions.speed_allocation, which the shaping planner reads directly.
if ! docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 \
     < "$ROOT/data-plane/migrations/0059_speed_allocation.up.sql" >/dev/null 2>&1; then
  echo "0059 FAILED TO APPLY -- deterministic, not a flake"
  exit 1
fi
docker exec "$C" psql -U postgres -d "$DB" -tAqc \
  "INSERT INTO public.schema_migrations(version) VALUES ('0059_speed_allocation') ON CONFLICT DO NOTHING;" >/dev/null

# 0060 restates p3_feed_authorizes so a failed resync no longer invalidates the published roster.
if ! docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 \
     < "$ROOT/data-plane/migrations/0060_last_good_roster_survives_a_failed_resync.up.sql" >/dev/null 2>&1; then
  echo "0060 FAILED TO APPLY -- deterministic, not a flake"
  exit 1
fi
docker exec "$C" psql -U postgres -d "$DB" -tAqc \
  "INSERT INTO public.schema_migrations(version) VALUES ('0060_last_good_roster_survives_a_failed_resync') ON CONFLICT DO NOTHING;" >/dev/null

# 0061 makes the ingestion operation advance the Entitlement's own consumed_data_bytes. WITHOUT IT the data
# quota contract suite in ./internal/enforce fails on its first assertion, which is the point: that suite
# drives iam_v2.ingest_absolute_counters -- the operation acctd actually calls -- rather than inserting
# accounting rows directly, and before 0061 that operation left the entitlement's usage at zero forever.
if ! docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 \
     < "$ROOT/data-plane/migrations/0061_the_entitlement_records_what_it_spent.up.sql" >/dev/null 2>&1; then
  echo "0061 FAILED TO APPLY -- deterministic, not a flake"
  docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 \
    < "$ROOT/data-plane/migrations/0061_the_entitlement_records_what_it_spent.up.sql" 2>&1 | tail -10
  exit 1
fi
docker exec "$C" psql -U postgres -d "$DB" -tAqc \
  "INSERT INTO public.schema_migrations(version) VALUES ('0061_the_entitlement_records_what_it_spent') ON CONFLICT DO NOTHING;" >/dev/null

# 0062 lets a closing binding keep a sample landing on its own closing instant when nothing else covers it, so
# the crossing sample stays attributed to the Entitlement it exhausted. The data-quota contract suite asserts
# counter == derived AFTER a DATA termination, which is false without it.
if ! docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 \
     < "$ROOT/data-plane/migrations/0062_the_crossing_sample_still_belongs_to_the_entitlement_that_spent_it.up.sql" >/dev/null 2>&1; then
  echo "0062 FAILED TO APPLY -- deterministic, not a flake"
  docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 \
    < "$ROOT/data-plane/migrations/0062_the_crossing_sample_still_belongs_to_the_entitlement_that_spent_it.up.sql" 2>&1 | tail -10
  exit 1
fi
docker exec "$C" psql -U postgres -d "$DB" -tAqc \
  "INSERT INTO public.schema_migrations(version) VALUES ('0062_the_crossing_sample_still_belongs_to_the_entitlement_that_spent_it') ON CONFLICT DO NOTHING;" >/dev/null

built="$(docker exec "$C" psql -U postgres -d "$DB" -tAqc "SELECT count(*) FROM information_schema.tables WHERE table_schema='iam_v2';")"
if [ "${built:-0}" -lt 40 ]; then echo "INFRA: SCHEMA BUILD FAILED (iam_v2 tables=$built)"; exit 2; fi
runtime_cols="$(docker exec "$C" psql -U postgres -d "$DB" -tAqc "SELECT count(*) FROM information_schema.columns WHERE table_schema='iam_v2' AND table_name='pms_interface_runtime' AND column_name='pinned_secret_generation_id';")"
if [ "${runtime_cols:-0}" != "1" ]; then
  # Deterministic: the migration itself did not apply. Exit 1 so CI does NOT retry -- a broken migration
  # fails the same way twice, and a retry that passed would mean something non-deterministic was hiding.
  echo "0010 NOT APPLIED (pinned_secret_generation_id missing) -- the migration did not apply; this is a defect, not a flake"
  exit 1
fi
echo "  iam_v2 tables=$built + 0010 applied"

export PHASE3_TEST_DSN="postgres://postgres:postgres@127.0.0.1:$PORT/$DB"
# ---------------------------------------------------------------------------------------------------------
# TEST OWNERSHIP GUARD.
#
# This gate builds a database that stops at migration 0010, because that is the schema Phase 3 ships. Phase-4
# integration tests live in the SAME ./cmd/edged package and, while they carried only the `integration` tag,
# they compiled into this run and failed against a schema in which `financial_base_currency` and
# `p4_declare_financial_recovery` cannot exist. Seven red tests that said nothing about Phase 3, and would
# have hidden anything that did.
#
# They now carry `//go:build integration && phase4`, so this build simply does not contain them. The check
# below is the part that keeps it true: it asserts that no file compiled into THIS gate's packages mentions a
# Phase-4-only object. A future test dropped into cmd/edged without the tag fails here, loudly, instead of
# failing later as a mystery about a missing column.
PKGS="./internal/pmsd/ ./internal/stayengine/ ./internal/authctx/ ./internal/checkout/ ./internal/staygrant/ ./internal/pmsresolve/ ./internal/enforce/ ./internal/writerguard/ ./cmd/edged/ ./cmd/acctd/ ./cmd/scd/"
echo "== test-ownership guard: no Phase-4-schema test may compile into the Phase-3 gate =="
leak=0
while IFS= read -r gofile; do
  [ -n "$gofile" ] || continue
  # Phase-4-only database objects. A Phase-3 test cannot legitimately reference these: they do not exist
  # until migration 0011 or later.
  if grep -qE 'iam_v2\.p4_|financial_base_currency|p4_declare_financial_recovery|p4_entitlement_grant_kernel' "$gofile"; then
    echo "  LEAK: $gofile compiles into the Phase-3 gate but references Phase-4-only schema"
    leak=1
  fi
done <<EOF
$( cd "$ROOT/data-plane" && go list -tags integration -f '{{$d := .Dir}}{{range .TestGoFiles}}{{$d}}/{{.}}
{{end}}{{range .XTestGoFiles}}{{$d}}/{{.}}
{{end}}' $PKGS 2>/dev/null )
EOF
if [ "$leak" != 0 ]; then
  echo "  A Phase-4 test must carry //go:build integration && phase4 so this gate does not build it."
  exit 1
fi
echo "  ok: every test compiled into this gate is Phase-3-owned"

echo "== go test -tags integration ./internal/pmsd ./internal/stayengine ./internal/authctx ./internal/checkout ./internal/staygrant ./internal/pmsresolve ./internal/enforce ./internal/writerguard ./cmd/edged ./cmd/acctd ./cmd/netd ./cmd/scd (Integration) =="
( cd "$ROOT/data-plane" && go test -tags integration -run Integration ./internal/pmsd/ ./internal/stayengine/ ./internal/authctx/ ./internal/checkout/ ./internal/staygrant/ ./internal/pmsresolve/ ./internal/enforce/ ./internal/writerguard/ ./cmd/edged/ ./cmd/acctd/ ./cmd/netd/ ./cmd/scd/ -count=1 )
rc=$?
echo "PMSD_PG_INTEGRATION rc=$rc"
exit $rc
