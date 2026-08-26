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
# database and runs only the `prodprivilege` tag against it.
#
# THE BOOTSTRAP BELOW IS COPIED FROM scripts/factory-clean-baseline-verify.sh, deliberately and almost line for
# line. Three hand-written approximations failed here in a row — wrong image, wrong order, wrong readiness
# check — each costing a full CI round. The proven sequence is: the TimescaleDB image, readiness by "ready to
# accept connections" twice plus three clean queries, Gate-P roles, the stayconnect platform role,
# gatep-iam-roles BEFORE the baseline because the baseline's own privileges name iam_v2_owner, the baseline
# applied as the platform role, then IAM ownership and the real grant chain.
#
# NO TEST-ONLY PRIVILEGES ARE ADDED. Everything svc_scd holds here it holds because deploy/gatep says so. If a
# test fails for want of a privilege, the fix belongs in deploy/gatep, not in this file.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GATEP="$ROOT/deploy/gatep"
BASE="$ROOT/data-plane/migrations/baseline/0000_production_baseline.sql"
OUT="$(mktemp -d)"
C="sc-prodpriv-$$"
DB=stayconnect_site

cleanup() { docker rm -f "$C" >/dev/null 2>&1 || true; }
trap cleanup EXIT

[ -f "$BASE" ] || { echo "  FAIL: baseline missing"; exit 1; }

echo "== blank PostgreSQL cluster, port published for the Go suite =="
docker run -d --name "$C" -p 0:5432 -e POSTGRES_PASSWORD=postgres -e POSTGRES_DB="$DB" \
  "${CLEANROOM_IMAGE:-timescale/timescaledb:2.16.1-pg16}" >/dev/null
ready=0
for _ in $(seq 1 180); do
  if [ "$(docker logs "$C" 2>&1 | grep -c 'database system is ready to accept connections')" -ge 2 ]; then
    ok=0
    for _ in 1 2 3; do
      docker exec "$C" psql -U postgres -d "$DB" -tAqc 'select 1' >/dev/null 2>&1 && ok=$((ok+1))
      sleep 1
    done
    [ "$ok" = "3" ] && { ready=1; break; }
  fi
  sleep 1
done
[ "$ready" = "1" ] || { echo "  FAIL: database never became ready"; docker logs "$C" 2>&1 | tail -10; exit 1; }
psql_run() { docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 "$@"; }

echo "== Gate-P roles, then the baseline, then Gate-P ownership and grants =="
psql_run < "$GATEP/gatep-roles.sql" >"$OUT/roles.log" 2>&1 \
  || { echo "  FAIL gatep-roles.sql:"; tail -3 "$OUT/roles.log"; exit 1; }
psql_run -tAqc "DO \$\$ BEGIN CREATE ROLE stayconnect NOLOGIN SUPERUSER; EXCEPTION WHEN duplicate_object THEN NULL; END \$\$;" >/dev/null
docker cp "$GATEP" "$C:/tmp/gatep" >/dev/null

# BEFORE the baseline: the baseline carries its own privileges and those name iam_v2_owner.
MSYS_NO_PATHCONV=1 docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 \
  -f /tmp/gatep/gatep-iam-roles.sql >"$OUT/iam-roles-pre.log" 2>&1 \
  || { echo "  FAIL gatep-iam-roles.sql (pre):"; tail -3 "$OUT/iam-roles-pre.log"; exit 1; }

# The dump carries both schemas in one file, so it is applied as the platform role; Gate-P reasserts IAM
# ownership afterwards, which is what gatep-iam-ownership.sql is for and is proven idempotent.
if { printf 'SET ROLE stayconnect;\n'; cat "$BASE"; } | psql_run >"$OUT/baseline.log" 2>&1; then
  echo "  baseline applied"
else
  echo "  FAIL baseline:"; grep -iE '^ERROR|^psql:' "$OUT/baseline.log" | tail -5; tail -3 "$OUT/baseline.log"; exit 1
fi

for f in gatep-iam-ownership.sql gatep-iam-roles.sql gatep-grants.sql svc-edged-phase345-admin-grants.sql; do
  [ -f "$GATEP/$f" ] || continue
  MSYS_NO_PATHCONV=1 docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 \
    -f "/tmp/gatep/$f" >"$OUT/$f.log" 2>&1 \
    || { echo "  FAIL $f:"; tail -3 "$OUT/$f.log"; exit 1; }
  echo "  applied $f"
done

# Migrations published after the baseline snapshot. 0057 is the one this harness exists to exercise; the
# others keep the schema matching a current appliance.
for m in 0056_materialization_readiness 0057_lock_auth_context_offer; do
  f="$ROOT/data-plane/migrations/$m.up.sql"
  [ -f "$f" ] || continue
  psql_run < "$f" >"$OUT/$m.log" 2>&1 || { echo "  FAIL $m:"; tail -3 "$OUT/$m.log"; exit 1; }
  echo "  applied $m"
done

# 0057's own grant is guarded on the role existing. Reassert the Gate-P chain afterwards so the test proves
# the grant survives a RECONCILE, not merely a fresh migration — the failure mode that has bitten twice.
MSYS_NO_PATHCONV=1 docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 \
  -f /tmp/gatep/gatep-grants.sql >"$OUT/grants-recheck.log" 2>&1 \
  || { echo "  FAIL gatep-grants.sql (reconcile):"; tail -3 "$OUT/grants-recheck.log"; exit 1; }

# BASELINE-COMPATIBLE FIXTURE DEFAULTS, for this disposable database only.
#
# The shared Go fixture seeds public.tenants(id) alone, which the DARK schema accepts because its tenants
# table carries only id. The REAL baseline declares slug and name NOT NULL, so the same fixture cannot seed
# here. The DARK fixture must not change — it is exercising a different schema on purpose — so the
# accommodation lives here instead: every NOT NULL column without a default on the platform tables the fixture
# touches gets a synthetic default.
#
# This is schema convenience on a throwaway database, NOT a privilege. Nothing below grants anything, and the
# privilege model under test is untouched: it still comes entirely from deploy/gatep.
docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 >/dev/null <<'FIXDEF'
DO $$
DECLARE r record;
BEGIN
  FOR r IN
    SELECT c.table_name, c.column_name, c.data_type
      FROM information_schema.columns c
     WHERE c.table_schema='public'
       AND c.table_name IN ('tenants','sites','guest_networks','appliances','operators')
       AND c.is_nullable='NO'
       AND c.column_default IS NULL
       AND c.column_name <> 'id'
  LOOP
    -- A value shaped for the column's type; uniqueness matters for slug-like text columns because the
    -- fixture runs many times against one database.
    IF r.data_type IN ('text','character varying') THEN
      EXECUTE format('ALTER TABLE public.%I ALTER COLUMN %I SET DEFAULT (%L || substr(gen_random_uuid()::text,1,8))',
                     r.table_name, r.column_name, 'fixture-');
    ELSIF r.data_type IN ('boolean') THEN
      EXECUTE format('ALTER TABLE public.%I ALTER COLUMN %I SET DEFAULT false', r.table_name, r.column_name);
    ELSIF r.data_type IN ('integer','bigint','smallint','numeric') THEN
      EXECUTE format('ALTER TABLE public.%I ALTER COLUMN %I SET DEFAULT 0', r.table_name, r.column_name);
    ELSIF r.data_type LIKE 'timestamp%' THEN
      EXECUTE format('ALTER TABLE public.%I ALTER COLUMN %I SET DEFAULT now()', r.table_name, r.column_name);
    ELSIF r.data_type = 'jsonb' THEN
      EXECUTE format('ALTER TABLE public.%I ALTER COLUMN %I SET DEFAULT ''{}''::jsonb', r.table_name, r.column_name);
    ELSIF r.data_type = 'uuid' THEN
      EXECUTE format('ALTER TABLE public.%I ALTER COLUMN %I SET DEFAULT gen_random_uuid()', r.table_name, r.column_name);
    END IF;
  END LOOP;
END $$;
FIXDEF
echo "  fixture defaults applied to the platform tables"

have="$(docker exec "$C" psql -U postgres -d "$DB" -tAqc "SELECT count(*) FROM pg_roles WHERE rolname='svc_scd'")"
[ "${have:-0}" = "1" ] || { echo "  FAIL: svc_scd is not provisioned; this harness exists to exercise it"; exit 1; }

echo "== the privilege model under test =="
docker exec "$C" psql -U postgres -d "$DB" -tAqc \
  "SELECT '  lock helper EXECUTE by svc_scd: ' ||
          has_function_privilege('svc_scd','iam_v2.lock_auth_context_offer(uuid,uuid,uuid,uuid)','EXECUTE') ||
          '  |  auth_context_offers UPDATE by svc_scd: ' ||
          has_table_privilege('svc_scd','iam_v2.auth_context_offers','UPDATE') ||
          '  |  lock helper EXECUTE by PUBLIC: ' ||
          has_function_privilege('public','iam_v2.lock_auth_context_offer(uuid,uuid,uuid,uuid)','EXECUTE')"

PORT="$(docker inspect -f '{{(index (index .NetworkSettings.Ports "5432/tcp") 0).HostPort}}' "$C")"
export PHASE3_TEST_DSN="postgres://postgres:postgres@127.0.0.1:${PORT}/${DB}?sslmode=disable"
# ONLY the prodprivilege tests. -run Integration pulled in every other cmd/scd integration suite, whose
# fixtures build their own schema rather than the baseline and so fail on constraints the real schema has
# (tenants.slug NOT NULL, among others). Those suites are the DARK harness's job; this one exists solely to
# exercise the real service role against the real Gate-P grants.
echo "== go test -run TestIntegration_Phase3Grant_ -tags 'integration prodprivilege' ./cmd/scd =="
( cd "$ROOT/data-plane" && go test -tags "integration prodprivilege" -run 'TestIntegration_Phase3Grant_' ./cmd/scd/ -count=1 )
echo "PROD_PRIVILEGE = PASS"
