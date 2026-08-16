#!/usr/bin/env bash
# PHASE-7 — REBUILD THE ACCEPTED SCHEMA FROM REPOSITORY-CONTROLLED SOURCES ALONE.
#
# The committed appliance snapshot is an ORACLE, not a source. This script must reach the same accepted
# semantic state without copying it, or the repository cannot rebuild its own system.
#
# THE ACCEPTED HISTORY, and the identity each layer ran under. That identity is not a detail: it decides
# ownership, and ownership decides what SECURITY DEFINER functions may do.
#
#   L1  data-plane/migrations/0001..0008     the edge/public schema            as the SUPERUSER
#   L2  iam_v2_scratch/migrations/mg1..mg9   Phase 1A: CREATE SCHEMA iam_v2    as IAM_V2_OWNER
#       (+ the mg0 guest_networks anchor, which mg1's foreign keys require)
#   L2b deploy/gatep/gatep-roles.sql         Phase 1B Gate P: the svc_* roles  as the SUPERUSER
#       deploy/gatep/gatep-grants.sql        Phase 1B Gate P: least privilege  as the SUPERUSER
#   L3  data-plane/migrations/0009..0047     Phases 2 through 6                as the SUPERUSER
#
# WHY L2 IS DIFFERENT, and how that was established rather than guessed: the Phase-1A Live-Dark Acceptance
# record states "MG-1..MG-9 applied as iam_v2_owner" and "Schema + all 49 objects owned by iam_v2_owner".
# Applying them as the superuser instead produced a schema owned by postgres -- the ONE residual difference
# against the oracle after every other cause was eliminated, and the reason the reconstruction's definer
# functions could not read their own schema.
#
# WHY L3 IS NOT: six migrations (0028, 0030, 0033, 0034, 0039, 0040) create roles and grant privileges the
# schema owner does not administer. They fail under SET ROLE iam_v2_owner and always did; on the appliance
# they ran as the superuser.
#
#   usage:  phase7_reconstruct_from_sources.sh [database-name]     default: iam_recon
set -uo pipefail
export PATH="$PATH:/c/Program Files/Docker/Docker/resources/bin"
HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/.." && pwd)"

C="${PHASE7_CONTAINER:-iamv2-p6}"
DB="${1:-${PHASE7_RECON_DB:-iam_recon}}"
SU="${PHASE7_SUPERUSER:-stayconnect}"

pass=0; fail=0
ok(){ printf '  [PASS] %s\n' "$1"; pass=$((pass+1)); }
no(){ printf '  [FAIL] %s :: %s\n' "$1" "${2:-}"; fail=$((fail+1)); }
q(){ docker exec -i "$C" psql -U postgres -d "$DB" -tAqc "$1" </dev/null 2>&1; }
eq(){ [ "$2" = "$3" ] && ok "$1" || no "$1" "expected '$3', got '$2'"; }

# apply_as <role|-> <file>   -- '-' means the bootstrap superuser (postgres) with no SET ROLE
apply_as(){
  local role="$1" file="$2" out
  if [ "$role" = "-" ]; then
    out="$(docker exec -i "$C" psql -U postgres -d "$DB" -q -v ON_ERROR_STOP=1 < "$file" 2>&1)"
  else
    out="$( { printf 'SET ROLE %s;\n' "$role"; cat "$file"; } \
            | docker exec -i "$C" psql -U postgres -d "$DB" -q -v ON_ERROR_STOP=1 2>&1 )"
  fi
  [ $? -eq 0 ] || { printf '%s' "$out" | grep -i '^ERROR' | head -1; return 1; }
  return 0
}

echo "== Phase 7: reconstructing the accepted schema from repository sources ($DB) =="

docker exec -i "$C" psql -U postgres -d postgres -qc "DROP DATABASE IF EXISTS $DB" </dev/null >/dev/null 2>&1
docker exec -i "$C" psql -U postgres -d postgres -qc "CREATE DATABASE $DB" </dev/null >/dev/null 2>&1 \
  || { echo "cannot create $DB"; exit 1; }

# The two identities the accepted history used. Attributes matter: the appliance superuser really is a
# superuser, and a definer function owned by a non-superuser without schema USAGE cannot read its own tables.
docker exec -i "$C" psql -U postgres -d "$DB" -qc "
  DO \$\$ BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='$SU') THEN
      CREATE ROLE $SU SUPERUSER LOGIN INHERIT;
    ELSE ALTER ROLE $SU SUPERUSER LOGIN INHERIT; END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='iam_v2_owner') THEN
      CREATE ROLE iam_v2_owner NOSUPERUSER NOLOGIN INHERIT;
    ELSE ALTER ROLE iam_v2_owner NOSUPERUSER NOLOGIN INHERIT; END IF;
  END \$\$;" </dev/null >/dev/null 2>&1
docker exec -i "$C" psql -U postgres -d postgres -qc \
  "GRANT CREATE, CONNECT ON DATABASE $DB TO iam_v2_owner, $SU" </dev/null >/dev/null 2>&1
eq "the two accepted installation identities exist" \
   "$(q "SELECT count(*) FROM pg_roles WHERE rolname IN ('$SU','iam_v2_owner')")" "2"

# ---- L1: the edge/public schema, as the superuser ----------------------------------------------------------
n=0; bad=""
for f in "$ROOT"/data-plane/migrations/000[1-8]_*.up.sql; do
  if e="$(apply_as "$SU" "$f")"; then n=$((n+1)); else bad="$bad $(basename "$f" .up.sql):$e"; fi
done
eq "L1 the edge/public schema applied (0001-0008)" "$n" "8"
[ -z "$bad" ] || no "L1 failures" "$bad"

# MG-0, AS THE ACCEPTED HISTORY RAN IT. The Phase-1A record: "MG-0 (non-transactional): CREATE UNIQUE INDEX
# CONCURRENTLY guest_networks_tsi_anchor ON public.guest_networks (tenant_id, site_id, id)", executed by the
# superuser, and AC-03 records "public schema unchanged except the MG-0 anchor".
#
# THE PRIVILEGES ARE THE HISTORICAL MINIMUM, read from the appliance rather than chosen for convenience. It
# grants iam_v2_owner exactly REFERENCES on public.guest_networks and INSERT/SELECT on public.schema_migrations
# -- nothing else. An earlier version granted ALL ON SCHEMA public and ALL ON ALL TABLES to make the build
# pass; that is an over-grant which contradicts AC-03 and would have made the reconstruction a laxer system
# than the one it claims to reproduce.
docker exec -i "$C" psql -U postgres -d "$DB" -qc "
  SET ROLE $SU;
  CREATE UNIQUE INDEX IF NOT EXISTS guest_networks_tsi_anchor ON public.guest_networks (tenant_id, site_id, id);
  GRANT REFERENCES ON public.guest_networks TO iam_v2_owner;
  GRANT INSERT, SELECT ON public.schema_migrations TO iam_v2_owner;" </dev/null >/dev/null 2>&1
eq "L2 iam_v2_owner holds ONLY the historical minimum on public.guest_networks" \
   "$(q "SELECT string_agg(privilege_type, ',' ORDER BY privilege_type)
           FROM information_schema.role_table_grants
          WHERE table_schema='public' AND table_name='guest_networks' AND grantee='iam_v2_owner'")" "REFERENCES"
eq "L2 ...and only INSERT/SELECT on public.schema_migrations" \
   "$(q "SELECT string_agg(privilege_type, ',' ORDER BY privilege_type)
           FROM information_schema.role_table_grants
          WHERE table_schema='public' AND table_name='schema_migrations' AND grantee='iam_v2_owner'")" "INSERT,SELECT"
eq "L2 iam_v2_owner was NOT granted the public schema itself (AC-03: public unchanged)" \
   "$(q "SELECT (COALESCE(nspacl::text,'') NOT LIKE '%iam_v2_owner%')::text FROM pg_namespace WHERE nspname='public'")" "true"
eq "L2 the mg0 guest-network anchor exists" \
   "$(q "SELECT count(*) FROM pg_indexes WHERE indexname='guest_networks_tsi_anchor'")" "1"

# ---- L2: Phase 1A, AS iam_v2_owner --------------------------------------------------------------------------
n=0; bad=""
for f in "$HERE"/migrations/mg[1-9]_*.sql; do
  if e="$(apply_as iam_v2_owner "$f")"; then n=$((n+1)); else bad="$bad $(basename "$f" .sql):$e"; fi
done
eq "L2 Phase 1A applied as iam_v2_owner (mg1-mg9)" "$n" "9"
[ -z "$bad" ] || no "L2 failures" "$bad"
eq "L2 the schema is owned by iam_v2_owner, as the Phase-1A record states" \
   "$(q "SELECT pg_get_userbyid(nspowner) FROM pg_namespace WHERE nspname='iam_v2'")" "iam_v2_owner"
eq "L2 Phase 1A left 49 iam_v2 tables, as the Phase-1A record states" \
   "$(q "SELECT count(*) FROM information_schema.tables WHERE table_schema='iam_v2'")" "49"

# ---- L2b: Phase 1B Gate P, as the superuser ------------------------------------------------------------------
for f in "$ROOT"/deploy/gatep/gatep-roles.sql "$ROOT"/deploy/gatep/gatep-grants.sql; do
  apply_as "$SU" "$f" >/dev/null 2>&1 || true    # idempotent; duplicate_object is caught inside the file
done
eq "L2b Gate-P created the runtime service roles" \
   "$(q "SELECT count(*) FROM pg_roles WHERE rolname IN ('svc_scd','svc_edged','svc_acctd','svc_netd')")" "4"

# ---- L3: Phases 2 through 6, and the identity CHANGES AT PHASE 5 --------------------------------------------
#
# Established from the appliance, not assumed. Its iam_v2 objects are owned in exactly two groups:
#
#   iam_v2_owner   68 tables, 111 functions   -- everything up to and including Phase 4
#   stayconnect     6 tables,  25 functions   -- every Phase-5 and Phase-6 object, by name:
#                                                appliance_product_settings, appliance_product_setting_changes,
#                                                guest_device_actions, entitlement_termination_evidence,
#                                                online_time_skipped_intervals, session_online_watermarks,
#                                                and the p5_*/p6_* functions
#
# So the first historical layer where the divergence appears is PHASE 5 (0027): from there on the migrations
# were applied as the superuser directly, while 0009-0026 were applied as the schema owner. That also explains
# the 182 "missing" grants without inventing anything -- their grantor is iam_v2_owner itself, so they are
# IMPLICIT OWNER privileges that appear simply because the owner owns the table. Nothing granted them, and a
# blanket reassignment to reproduce them would have been reproducing a symptom.
n=0; bad=""
for f in "$ROOT"/data-plane/migrations/00[0-2][0-9]_*.up.sql; do
  case "$(basename "$f")" in 000[1-8]_*) continue ;; esac
  case "$(basename "$f")" in 002[7-9]_*) continue ;; esac      # 0027+ belongs to L3b
  if e="$(apply_as iam_v2_owner "$f")"; then n=$((n+1)); else bad="$bad $(basename "$f" .up.sql):$e"; fi
done
eq "L3a Phases 2-4 applied as iam_v2_owner (0009-0026)" "$n" "18"
[ -z "$bad" ] || no "L3a failures" "$(printf '%s' "$bad" | cut -c1-200)"

n=0; bad=""
for f in "$ROOT"/data-plane/migrations/002[7-9]_*.up.sql "$ROOT"/data-plane/migrations/00[3-4][0-9]_*.up.sql; do
  if e="$(apply_as "$SU" "$f")"; then n=$((n+1)); else bad="$bad $(basename "$f" .up.sql):$e"; fi
done
eq "L3b Phases 5-6 applied as the superuser (0027-0047)" "$n" "21"
[ -z "$bad" ] || no "L3b failures" "$(printf '%s' "$bad" | cut -c1-200)"

eq "L3 ownership matches the appliance: Phase-6 tables belong to the superuser" \
   "$(q "SELECT pg_get_userbyid(relowner) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
          WHERE n.nspname='iam_v2' AND c.relname='appliance_product_settings'")" "$SU"
eq "L3 ownership matches the appliance: Phase-3 tables belong to the schema owner" \
   "$(q "SELECT pg_get_userbyid(relowner) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
          WHERE n.nspname='iam_v2' AND c.relname='stays'")" "iam_v2_owner"

# ---- L4: the accepted role graph -------------------------------------------------------------------------
#
# The Phase-1A record says "iam_v2_migrator is a member of the owner". That one membership accounts for TWO
# whole classes of difference: it is a MEMBER row, and because membership inherits EXECUTE it also puts
# iam_v2_migrator into the effective-privilege list of every owner-executable function -- 100 of them.
#
# An earlier version of this layer also reassigned EVERY iam_v2 object to iam_v2_owner, reading the Phase-1A
# invariant too widely. It holds for the 49 objects Phase 1A created, not for the Phase-2-to-6 tables, which
# the appliance leaves owned by the superuser that created them. Reassigning them invented 42 table grants
# the appliance does not have -- an owner holds every privilege implicitly, and information_schema reports it.
docker exec -i "$C" psql -U postgres -d "$DB" -qc "
  DO \$\$ BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='iam_v2_migrator') THEN
      CREATE ROLE iam_v2_migrator NOSUPERUSER NOLOGIN INHERIT;
    END IF;
    GRANT iam_v2_owner TO iam_v2_migrator;
  END \$\$;" </dev/null >/dev/null 2>&1
eq "L4 iam_v2_migrator is a member of iam_v2_owner, as the Phase-1A record states"    "$(q "SELECT count(*) FROM pg_auth_members am JOIN pg_roles m ON m.oid=am.member
          JOIN pg_roles g ON g.oid=am.roleid WHERE m.rolname='iam_v2_migrator' AND g.rolname='iam_v2_owner'")" "1"

# ---- WHAT THIS RECONSTRUCTION DOES NOT YET REPRODUCE, stated precisely rather than rounded off -------------
#
# Against the APPLIANCE ITSELF (not the dump-restored oracle, which under-reports implicit owner privileges),
# two related surfaces still differ and they have one shape:
#
#   GRT    182 grants the appliance has and this build does not -- iam_v2_owner on the Phase-2..6 TABLES.
#   FNEXEC  76 functions where the effective-privilege lists differ, for the same ownership reason.
#
# It is ONE question: on the appliance, iam_v2_owner holds privileges on the Phase-2-to-6 tables while those
# phases' FUNCTIONS remain owned by the superuser. A blanket reassignment of every object to iam_v2_owner was
# tried and is WRONG in the other direction -- it closes the 182 grants and then gives iam_v2_owner EXECUTE on
# 45 functions the appliance denies it, because on the appliance those functions belong to the superuser.
#
# So the remaining work is to find which accepted transition granted the owner those table privileges without
# transferring function ownership -- a migration GRANT, an ALTER DEFAULT PRIVILEGES, or an operational step --
# and add it additively. Until that is established, the fidelity claim is stated at its true scope:
#
#   PROVEN IDENTICAL: columns, constraints (with grouping preserved), indexes, triggers, function bodies and
#                     attributes including proconfig, role attributes, role memberships, schema ACL, and every
#                     svc_/sc_ runtime and financial table grant.
#   NOT YET IDENTICAL: iam_v2_owner's table grants on Phase-2..6 objects, and the 76 function
#                     effective-privilege entries that follow from the same ownership question.
#
# This is NOT "exactly semantically equal", and it is not called that anywhere.

echo
GOT="$(docker exec -i "$C" psql -U postgres -d "$DB" -tAq < "$HERE/phase7_fidelity.sql" 2>&1 | tail -1)"
echo "  reconstructed digest: $GOT"

echo "------------------------------------------------------------"
printf 'PHASE7_RECONSTRUCT db=%s pass=%d fail=%d\n' "$DB" "$pass" "$fail"
[ "$fail" -eq 0 ]
