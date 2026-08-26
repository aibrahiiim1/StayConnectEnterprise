#!/usr/bin/env bash
# THE PRODUCTION PRIVILEGE HARNESS.
#
# The ordinary integration database asserts the DARK posture: svc_scd holds no runtime grants, and a test
# proves it. That assertion is correct and stays untouched. But it means the ordinary database can never
# exercise a privilege requirement, and the least-privilege tests skipped whenever the role was absent — which
# is how two defects reached production with CI green:
#
#   * svc_scd could not SELECT iam_v2.pms_interface_runtime, found only when the deployment ran;
#   * svc_scd could not lock iam_v2.auth_context_offers, found only when the first real guest tried to sign in
#     and was refused with package_not_offered_to_this_context.
#
# Both were invisible because every suite ran as a superuser. So this builds a SECOND, PRODUCTION-LIKE
# database — real Gate-P roles, real ownership, real grants — and runs only the `prodprivilege` tag against
# it. Neither posture has to pretend to be the other, and a skipped assertion stops being mistaken for a
# passing one.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
C="sc-prodpriv-$$"
DB="prodpriv"

cleanup() { docker rm -f "$C" >/dev/null 2>&1 || true; }
trap cleanup EXIT

echo "== disposable PostgreSQL 16 =="
docker run -d --name "$C" -p 0:5432 -e POSTGRES_PASSWORD=postgres -e POSTGRES_DB="$DB" postgres:16 >/dev/null
for _ in $(seq 1 60); do docker exec "$C" pg_isready -U postgres >/dev/null 2>&1 && break; sleep 1; done

echo "== factory-clean baseline =="
docker exec "$C" psql -U postgres -d "$DB" -tAqc \
  "CREATE TABLE IF NOT EXISTS public.schema_migrations(version text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now());" >/dev/null
if ! docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 \
     < "$ROOT/data-plane/migrations/baseline/0000_production_baseline.sql" >/tmp/prodpriv-baseline.log 2>&1; then
  echo "BASELINE FAILED TO APPLY:"; tail -20 /tmp/prodpriv-baseline.log; exit 1
fi

# THE REAL GATE-P CHAIN, in the order production applies it: roles, then ownership, then grants. Ownership
# matters here — it is what makes the grants mean anything, and it is precisely what the DARK harness must not
# have.
echo "== Gate-P roles, ownership and grants =="
for f in gatep-roles.sql gatep-iam-roles.sql gatep-iam-ownership.sql gatep-grants.sql \
         svc-edged-phase345-admin-grants.sql; do
  [ -f "$ROOT/deploy/gatep/$f" ] || continue
  if ! docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 \
       < "$ROOT/deploy/gatep/$f" >/tmp/prodpriv-$f.log 2>&1; then
    echo "GATE-P FILE FAILED: $f"; tail -15 /tmp/prodpriv-$f.log; exit 1
  fi
  echo "  applied $f"
done

# The whole point of this harness: if the role is not really here with real grants, the tests below would
# prove nothing, so refuse rather than run them.
have="$(docker exec "$C" psql -U postgres -d "$DB" -tAqc "SELECT count(*) FROM pg_roles WHERE rolname='svc_scd'")"
if [ "${have:-0}" != "1" ]; then
  echo "svc_scd IS NOT PROVISIONED -- this harness exists to exercise it; refusing to run"
  exit 1
fi
echo "  svc_scd present with the real Gate-P grants"

echo "== privilege model, before any test =="
docker exec "$C" psql -U postgres -d "$DB" -tAqc \
  "SELECT 'lock_auth_context_offer EXECUTE: ' ||
          has_function_privilege('svc_scd','iam_v2.lock_auth_context_offer(uuid,uuid,uuid,uuid)','EXECUTE') ||
          ' | auth_context_offers UPDATE: ' ||
          has_table_privilege('svc_scd','iam_v2.auth_context_offers','UPDATE')"

PORT="$(docker inspect -f '{{(index (index .NetworkSettings.Ports "5432/tcp") 0).HostPort}}' "$C")"

export PHASE3_TEST_DSN="postgres://postgres:postgres@127.0.0.1:${PORT}/${DB}?sslmode=disable"
echo "== go test -tags 'integration prodprivilege' ./cmd/scd =="
( cd "$ROOT/data-plane" && go test -tags "integration prodprivilege" -run Integration ./cmd/scd/ -count=1 )
echo "PROD_PRIVILEGE = PASS"
