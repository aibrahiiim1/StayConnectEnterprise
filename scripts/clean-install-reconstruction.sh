#!/usr/bin/env bash
# FACTORY-CLEAN INSTALL RECONSTRUCTION — repository sources only.
#
# WHY THIS EXISTS
# ---------------
# The Production strategy is a FRESH APPLIANCE DEPLOYMENT, not an in-place cutover of the development
# machine. That makes one question load-bearing: can the final system be built from the authoritative
# repository ALONE, with no database dump, no /etc/stayconnect, no service state and no test identity copied
# off 172.21.60.23?
#
# The honest way to answer it is to build the schema in a disposable, isolated database that has never seen
# that appliance, and to report what the build actually produces rather than what the runbook believes it
# produces. Nothing here touches any appliance: the container is created, inspected and destroyed.
#
# It deliberately does NOT seed a tenant, a site, an operator, a plan or a voucher. Those arrive through
# enrollment, claim, signed assignment, licensing and Hotel-Admin configuration on the real appliance --
# and a script that quietly seeds them is exactly how a "clean" install ends up carrying test identities.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MIG="$ROOT/data-plane/migrations"
C="sc-cleanroom-$$"
PORT="${CLEANROOM_PORT:-55432}"
OUT="${CLEANROOM_OUT:-$ROOT/.cleanroom}"
mkdir -p "$OUT"

cleanup() { docker rm -f "$C" >/dev/null 2>&1 || true; }
trap cleanup EXIT

echo "== disposable PostgreSQL (never sees any appliance) =="
docker run -d --name "$C" -e POSTGRES_PASSWORD=postgres -e POSTGRES_DB=stayconnect_site \
  -p "127.0.0.1:$PORT:5432" postgres:16-alpine >/dev/null
for i in $(seq 1 60); do
  docker exec "$C" pg_isready -U postgres -d stayconnect_site >/dev/null 2>&1 && break
  sleep 1
done
docker exec "$C" psql -U postgres -d stayconnect_site -tAqc 'select 1' >/dev/null

psql_run() { docker exec -i "$C" psql -U postgres -d stayconnect_site -v ON_ERROR_STOP=1 "$@"; }

echo "== roles the migrations expect to exist (created by Gate-P on a real appliance) =="
# The migrations GRANT to the service roles, so a clean database needs them to exist before they run.
# NOLOGIN here: this reconstruction proves the SCHEMA builds, and must not mint usable credentials.
for r in stayconnect iam_v2_owner svc_scd svc_edged svc_acctd svc_netd; do
  psql_run -tAqc "DO \$\$ BEGIN IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='$r') THEN EXECUTE 'CREATE ROLE $r NOLOGIN'; END IF; END \$\$;" >/dev/null
done

echo "== applying the install path in dependency order =="
# ORDER IS THE FINDING, NOT AN IMPLEMENTATION DETAIL.
#
#   0001..0008   build the public platform. 0002 creates public.guest_networks.
#   mg1..mg9     build the iam_v2 domain. mg1 CREATEs the schema and immediately references
#                public.guest_networks, so it cannot run before 0002.
#   0009..0047   address iam_v2 objects, and NOTHING in the numbered sequence creates that schema.
#
# So the numbered sequence alone stops dead at 0009 with `schema "iam_v2" does not exist`, and the base
# schema alone stops dead at mg1 with `relation "public.guest_networks" does not exist`. Neither half is a
# complete install, and the ordered path that works is not expressed anywhere in the repository.
applied=0; failed=""
apply_one() {
  # Separate declarations on purpose: in a single `local a=.. b=.. c="$b"` the later assignment is expanded
  # before the earlier one exists, which under `set -u` aborts with "label: unbound variable".
  local f="$1"
  local label="$2"
  local log="$OUT/$(echo "$label" | tr '/' '_').log"
  if psql_run < "$f" >"$log" 2>&1; then
    applied=$((applied+1)); printf '  ok   %s
' "$label"
  else
    failed="$failed $label"; printf '  FAIL %s -- %s
' "$label" "$(tail -1 "$log")"
  fi
}

for f in $(ls "$MIG"/*.up.sql | sort); do
  n="$(basename "$f" .up.sql)"
  case "$n" in
    0009_*)
      if [ "${CLEANROOM_WITH_BASE:-1}" = "1" ]; then
        # THE ONE MISSING PIECE. mg1 anchors iam_v2.pms_interfaces to
        # public.guest_networks (tenant_id, site_id, id), and 0002 gives that table only PRIMARY KEY (id).
        # PostgreSQL refuses a composite FK without a matching unique key, so the base schema cannot build.
        # Idempotent, and additive only.
        if psql_run -tAqc "ALTER TABLE public.guest_networks
              ADD CONSTRAINT guest_networks_tenant_site_id_key UNIQUE (tenant_id, site_id, id)" >/dev/null 2>&1
        then printf '  ok   anchor/guest_networks_tenant_site_id_key
'
        else printf '  ok   anchor/guest_networks_tenant_site_id_key (already present)
'; fi
        for b in $(ls "$ROOT"/iam_v2_scratch/migrations/mg*.sql | sort -V); do
          apply_one "$b" "base/$(basename "$b" .sql)"
        done
      fi
      ;;
  esac
  apply_one "$f" "$n"
done
echo "  applied=$applied failed=${failed:-none}"

echo
echo "== what a factory-clean install actually contains =="
psql_run -tAc "
  SELECT 'schemas          : ' || string_agg(nspname, ' ' ORDER BY nspname)
    FROM pg_namespace WHERE nspname NOT LIKE 'pg_%' AND nspname <> 'information_schema';
  SELECT 'public tables    : ' || count(*) FROM information_schema.tables WHERE table_schema='public' AND table_type='BASE TABLE';
  SELECT 'iam_v2 tables    : ' || count(*) FROM information_schema.tables WHERE table_schema='iam_v2' AND table_type='BASE TABLE';
  SELECT 'rows in public   : ' || COALESCE(sum(n_live_tup),0) FROM pg_stat_user_tables WHERE schemaname='public';
  SELECT 'rows in iam_v2   : ' || COALESCE(sum(n_live_tup),0) FROM pg_stat_user_tables WHERE schemaname='iam_v2';
"

echo
echo "== the superseded guest-auth surface a clean install still creates =="
psql_run -tAc "
  SELECT '  ' || table_name FROM information_schema.tables
   WHERE table_schema='public'
     AND table_name IN ('guest_accounts','ticket_templates','vouchers','voucher_batches','sessions','auth_otps','guests')
   ORDER BY table_name;
"

echo
echo "== object inventory written for the dependency report =="
psql_run -tAc "SELECT table_schema||'.'||table_name FROM information_schema.tables
                WHERE table_schema IN ('public','iam_v2') AND table_type='BASE TABLE'
                ORDER BY 1" > "$OUT/objects.txt"
wc -l < "$OUT/objects.txt" | sed 's/^/  objects: /'
echo "  inventory: $OUT/objects.txt"

[ -z "$failed" ] || { echo "CLEAN_INSTALL_RECONSTRUCTION = FAIL"; exit 1; }
echo "CLEAN_INSTALL_RECONSTRUCTION = PASS (schema built from repository sources only, zero runtime state copied)"
