#!/usr/bin/env bash
# Build a disposable PostgreSQL 16 carrying the authoritative chain through 0029, then run the Phase-5
# gates and the `integration && phase5` matrix against it. Self-contained: it creates and tears down its own
# container. No Production/appliance access, no PMS, no financial egress, no flag enablement.
#
# EXIT CODES (the CI retry policy depends on these):
#   0  every gate and test passed
#   1  a GATE or TEST failed — deterministic. CI must NOT retry: a second run that passes would hide a defect.
#   2  the disposable infrastructure could not be built. That IS transient, and is the only retryable case.
set -uo pipefail
export PATH="$PATH:/c/Program Files/Docker/Docker/resources/bin"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
C="${PHASE5_INTEG_CONTAINER:-iamv2-p5integ}"; DB=iam_scratch; PORT="${PHASE5_INTEG_PORT:-55439}"

cleanup(){ docker rm -f "$C" >/dev/null 2>&1 || true; }
trap cleanup EXIT
cleanup

echo "== disposable PG16 for Phase 5 (container=$C port=$PORT) =="
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
docker exec "$C" psql -U postgres -d "$DB" -tAqc \
  "CREATE TABLE IF NOT EXISTS public.schema_migrations(version text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now());" >/dev/null

# The chain, in order. 0007 is included because the post-stay throttle lives in public.auth_throttle_buckets
# and 0028 widens its CHECK: a chain without it cannot express the Phase-5 throttle at all.
for m in 0007_auth_throttle_buckets 0009_phase2_commerce 0010_phase3_stay_resolution \
         0011_phase4_financial_execution 0012_phase4_financial_hardening 0013_phase4_reversal_ledger \
         0014_phase4_payment_settlement 0015_phase4_payment_hardening 0016_phase4_payment_coherence \
         0017_phase4_least_privilege 0018_phase4_financial_identity_and_privilege \
         0019_phase4_financial_recovery 0020_phase4_financial_observability 0021_phase4_trust_boundary \
         0022_phase4_recovery_closure 0023_phase4_restore_generation \
         0024_phase4_outcome_authority_and_grant_kernel 0025_phase4_recovery_completion_and_compliance \
         0026_phase4_c35_failclosed_and_operator_retry \
         0027_phase5_poststay_and_transfer 0028_phase5_poststay_throttle_method \
         0029_phase5_reveal_is_at_mint \
         0050_pms_auth_freshness_follows_feed_health \
         0051_continuity_unknown_is_not_a_healthy_feed \
         0052_grace_config_lock_without_update_privilege \
         0053_local_mirror_authorizes_when_transport_is_down \
         0054_operator_resync_command_and_sync_progress \
         0055_request_full_resync_without_runtime_write; do
  # 0050 is out of numeric sequence with the rest of this list on purpose: this gate runs internal/authctx,
  # whose PMS arm now calls iam_v2.p3_feed_authorizes. Without it those tests fail with "function does not
  # exist" rather than on anything Phase 5 owns. It is applied last, after everything it redefines.
  if ! docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 -q \
       < "$ROOT/data-plane/migrations/$m.up.sql" >/dev/null 2>&1; then
    echo "MIGRATION FAILED: $m"
    docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 \
      < "$ROOT/data-plane/migrations/$m.up.sql" 2>&1 | tail -10
    exit 1
  fi
done

base="$(docker exec "$C" psql -U postgres -d "$DB" -tAqc \
  "SELECT count(*) FROM information_schema.tables WHERE table_schema='iam_v2' AND table_type='BASE TABLE';")"
if [ "${base:-0}" != "68" ]; then
  echo "INFRA: the chain did not build (iam_v2 base tables=$base, expected 68)"; exit 2
fi
echo "  chain built: 68 iam_v2 base tables through 0029"

fail=0
run_gate(){
  echo "== $1 =="
  if PHASE5_CONTAINER="$C" PHASE5_DB="$DB" bash "$2"; then echo "  -> PASS"; else echo "  -> FAIL"; fail=1; fi
}
run_gate "Phase-5 foundation + security"      "$ROOT/iam_v2_scratch/phase5_0027_foundation.sh"
run_gate "Phase-5 migration lifecycle"        "$ROOT/iam_v2_scratch/phase5_0027_lifecycle.sh"
run_gate "Phase-5 least privilege (derived)"  "$ROOT/iam_v2_scratch/phase5_least_privilege.sh"

echo "== Phase-5 integration matrix (integration && phase5) =="
if ! (cd "$ROOT/data-plane" && PHASE3_TEST_DSN="postgres://postgres:postgres@127.0.0.1:$PORT/$DB?sslmode=disable" \
      go test -tags "integration phase5" -count=1 -timeout 900s \
      ./internal/transfer/ ./internal/poststay/ ./internal/authctx/ ./internal/checkout/ ./cmd/edged/); then
  fail=1
fi

echo "== Phase-5 unit matrix (no database) =="
if ! (cd "$ROOT/data-plane" && go test -count=1 ./cmd/portald/ ./internal/iamv2/ ./internal/codegen/); then
  fail=1
fi

[ "$fail" -eq 0 ] || exit 1
echo "== every Phase-5 gate passed =="
exit 0
