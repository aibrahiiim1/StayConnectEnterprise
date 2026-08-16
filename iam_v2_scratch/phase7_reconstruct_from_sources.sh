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

# mg0's anchor: mg1's composite foreign keys reference public.guest_networks(tenant_id, site_id, id), and
# without a unique constraint on exactly those columns mg1 fails with "no unique constraint matching given
# keys". It is a CONCURRENTLY build in the original, which cannot run inside a transaction block.
docker exec -i "$C" psql -U postgres -d "$DB" -qc \
  "CREATE UNIQUE INDEX IF NOT EXISTS guest_networks_tsi_anchor ON public.guest_networks (tenant_id, site_id, id);
   GRANT ALL ON SCHEMA public TO iam_v2_owner;
   GRANT ALL ON ALL TABLES IN SCHEMA public TO iam_v2_owner;" </dev/null >/dev/null 2>&1
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

# ---- L3: Phases 2 through 6, as the superuser -----------------------------------------------------------------
n=0; bad=""
for f in "$ROOT"/data-plane/migrations/00[0-4][0-9]_*.up.sql; do
  case "$(basename "$f")" in 000[1-8]_*) continue ;; esac
  if e="$(apply_as "$SU" "$f")"; then n=$((n+1)); else bad="$bad $(basename "$f" .up.sql):$e"; fi
done
eq "L3 Phases 2-6 applied (0009-0047)" "$n" "39"
[ -z "$bad" ] || no "L3 failures" "$(printf '%s' "$bad" | cut -c1-200)"

echo
GOT="$(docker exec -i "$C" psql -U postgres -d "$DB" -tAq < "$HERE/phase7_fidelity.sql" 2>&1 | tail -1)"
echo "  reconstructed digest: $GOT"

echo "------------------------------------------------------------"
printf 'PHASE7_RECONSTRUCT db=%s pass=%d fail=%d\n' "$DB" "$pass" "$fail"
[ "$fail" -eq 0 ]
