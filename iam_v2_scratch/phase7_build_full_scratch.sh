#!/usr/bin/env bash
# PHASE-7 — BUILD A COMPLETE PHASE-2-THROUGH-6 SCRATCH DATABASE, FROM THE AUTHORITATIVE LINEAGE.
#
# THE PROVENANCE QUESTION, ANSWERED BEFORE ANY SQL RUNS.
#
# There appeared to be two conflicting migration lineages. There are not; there are three ordered layers, and
# the earlier failure was applying them in the wrong order, not a conflict between them:
#
#   1. data-plane/migrations/0001..0008   the EDGE/public schema -- audit_log, guest_networks, appliances,
#                                         operators, sites, tenants, throttles, OTP keys.
#   2. iam_v2_scratch/mg0 + mg1..mg9      the iam_v2 schema itself. This is where `CREATE SCHEMA iam_v2` and
#                                         the Phase-1A/1B tables live; it is the only such source in the
#                                         repository, so it is authoritative rather than approximate. mg0 adds
#                                         the UNIQUE(tenant_id,site_id,id) anchor on public.guest_networks
#                                         that mg1's foreign keys require -- which is why mg1 failed with "no
#                                         unique constraint matching given keys" when it was run first.
#   3. data-plane/migrations/0009..0047   Phases 2 through 6, the same files the appliance runs.
#
# Layer 3 is byte-identical to what the DEVELOPMENT appliance has applied, so the result is the accepted
# as-built schema rather than a hybrid. The verification at the end is not decoration: it compares this
# database's catalogue against the appliance's accepted shape, and refuses to report success on a mismatch.
#
#   usage:  phase7_build_full_scratch.sh [database-name]      default: iam_full
#
# It DROPS and recreates the target database. It contacts no appliance and no Production system: the expected
# values it checks against are recorded here, having been read from the appliance once and written down.
set -uo pipefail
export PATH="$PATH:/c/Program Files/Docker/Docker/resources/bin"
HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/.." && pwd)"

C="${PHASE7_CONTAINER:-iamv2-p6}"
DB="${1:-${PHASE7_FULL_DB:-iam_full}}"

pass=0; fail=0
ok(){ printf '  [PASS] %s\n' "$1"; pass=$((pass+1)); }
no(){ printf '  [FAIL] %s :: %s\n' "$1" "${2:-}"; fail=$((fail+1)); }
q(){ docker exec -i "$C" psql -U postgres -d "$DB" -tAqc "$1" </dev/null 2>&1; }
eq(){ [ "$2" = "$3" ] && ok "$1" || no "$1" "expected '$3', got '$2'"; }

# THE ACCEPTED SHAPE, read from the DEVELOPMENT appliance and written down so this script needs no network.
# If the appliance's accepted schema changes, these change with it -- deliberately, as an edit somebody makes
# and explains, not as a number the script discovers and then agrees with.
EXPECT_IAMV2_TABLES=81
EXPECT_P6_FUNCTIONS=18
# The appliance's own catalogue+privilege fingerprint, computed by the identical expression below and read from
# the DEVELOPMENT appliance on 2026-08-16. Matching it is what makes this database the ACCEPTED as-built schema
# rather than merely a schema that applied without error -- counts can agree while shapes differ.
EXPECT_FINGERPRINT=03aef816b8541e7300d2570efe673f2f

echo "== Phase 7: building a complete Phase-2-through-6 scratch database ($DB) =="

apply(){ # apply <label> <file>
  local out
  out="$(docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 -q < "$2" 2>&1)"
  if [ $? -ne 0 ]; then
    no "apply $1" "$(printf '%s' "$out" | grep -i error | head -1 | cut -c1-140)"
    return 1
  fi
  return 0
}

docker exec -i "$C" psql -U postgres -d postgres -qc "DROP DATABASE IF EXISTS $DB" </dev/null >/dev/null 2>&1
docker exec -i "$C" psql -U postgres -d postgres -qc "CREATE DATABASE $DB" </dev/null >/dev/null 2>&1 \
  || { echo "cannot create $DB"; exit 1; }

# ---- layer 1: the edge/public schema -------------------------------------------------------------------------
n=0
for f in "$ROOT"/data-plane/migrations/000[1-8]_*.up.sql; do
  apply "$(basename "$f" .up.sql)" "$f" || exit 1
  n=$((n+1))
done
eq "layer 1: the edge/public schema applied (0001-0008)" "$n" "8"

# ---- layer 2: the iam_v2 schema (Phase 1A/1B) ------------------------------------------------------------------
# mg0 is a CONCURRENTLY index build and cannot run inside a transaction block, so it is issued directly rather
# than through the file applier. Its anchor is what mg1's foreign keys depend on.
docker exec -i "$C" psql -U postgres -d "$DB" -qc \
  "CREATE UNIQUE INDEX IF NOT EXISTS guest_networks_tsi_anchor ON public.guest_networks (tenant_id, site_id, id)" \
  </dev/null >/dev/null 2>&1
eq "layer 2: the guest-network anchor mg1 depends on exists" \
   "$(q "SELECT count(*) FROM pg_indexes WHERE indexname='guest_networks_tsi_anchor'")" "1"

n=0
for f in "$HERE"/migrations/mg[1-9]_*.sql; do
  apply "$(basename "$f" .sql)" "$f" || exit 1
  n=$((n+1))
done
eq "layer 2: the iam_v2 schema applied (mg1-mg9)" "$n" "9"

# ---- layer 3: Phases 2 through 6 -------------------------------------------------------------------------------
n=0
for f in "$ROOT"/data-plane/migrations/00[0-4][0-9]_*.up.sql; do
  case "$(basename "$f")" in 000[1-8]_*) continue ;; esac
  apply "$(basename "$f" .up.sql)" "$f" || exit 1
  n=$((n+1))
done
eq "layer 3: Phases 2-6 applied (0009-0047)" "$n" "39"

# ---- and now: is this the accepted as-built shape, or something that merely ran? ---------------------------------
echo
echo "-- verifying against the accepted as-built shape --"
eq "iam_v2 table count matches the appliance" \
   "$(q "SELECT count(*) FROM information_schema.tables WHERE table_schema='iam_v2'")" "$EXPECT_IAMV2_TABLES"
eq "Phase-6 function count matches the appliance" \
   "$(q "SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
          WHERE n.nspname='iam_v2' AND p.proname LIKE 'p6\\_%'")" "$EXPECT_P6_FUNCTIONS"

# The service roles must exist and hold the shape the phases granted them. A schema without its roles is not
# the as-built system: every least-privilege proof in the matrix would pass vacuously against nothing.
for r in svc_scd svc_acctd svc_edged; do
  eq "role $r exists" "$(q "SELECT count(*) FROM pg_roles WHERE rolname='$r'")" "1"
done
eq "svc_scd can insert a device (0047)" \
   "$(q "SELECT has_table_privilege('svc_scd','iam_v2.devices','INSERT')")" "t"
eq "svc_scd can read entitlements (0047)" \
   "$(q "SELECT has_table_privilege('svc_scd','iam_v2.entitlements','SELECT')")" "t"
eq "svc_scd CANNOT write an entitlement" \
   "$(q "SELECT has_table_privilege('svc_scd','iam_v2.entitlements','UPDATE')")" "f"
eq "svc_acctd CANNOT write an entitlement" \
   "$(q "SELECT has_table_privilege('svc_acctd','iam_v2.entitlements','UPDATE')")" "f"

# PUBLIC EXPOSURE IS THE ONE THAT MUST NOT BE INTRODUCED BY THE BUILD -- but the rule is "no UNEXPECTED
# exposure", not "none at all", and the difference matters because the first version of this check asserted
# the stronger rule and failed against a decision the system made on purpose.
#
# iam_v2.p5_controlled_operation_open IS deliberately SECURITY DEFINER and deliberately granted to PUBLIC, and
# migration 0027 says why next to the grant: it reads controlled_operation_scope, which is closed to every
# role but the owner, while the guard trigger that calls it is NOT definer and runs as the WRITING role. So
# the writing role has to be able to call it, and what a caller learns is whether ITS OWN transaction has an
# open scope -- scoped to txid_current() and to a token the caller itself set. It already knew.
#
# The allow-list is one name long and every other definer function must still be closed. Widening it is an
# edit somebody makes and justifies; a new PUBLIC-executable definer function appearing on its own still fails
# here, which is the property worth keeping.
PUBLIC_DEFINER_ALLOWED="p5_controlled_operation_open"
pubfn="$(q "SELECT COALESCE(string_agg(p.proname, ',' ORDER BY p.proname), 'none') FROM pg_proc p
             JOIN pg_namespace n ON n.oid = p.pronamespace
            WHERE n.nspname='iam_v2' AND p.prosecdef AND has_function_privilege('public', p.oid, 'EXECUTE')")"
eq "only the justified definer function is PUBLIC-executable, and no other" "$pubfn" "$PUBLIC_DEFINER_ALLOWED"

# A catalogue fingerprint, so "rebuilding it twice produces the same result" is a comparison rather than a
# feeling. Ordered, and covering the things a privilege or shape regression would move.
FP="$(q "SELECT md5(string_agg(x, '|' ORDER BY x)) FROM (
   SELECT 'T:'||table_name FROM information_schema.tables WHERE table_schema='iam_v2'
   UNION ALL SELECT 'F:'||p.proname||':'||p.prosecdef::text FROM pg_proc p
     JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='iam_v2'
   UNION ALL SELECT 'C:'||conname FROM pg_constraint c
     JOIN pg_namespace n ON n.oid=c.connamespace WHERE n.nspname='iam_v2'
   UNION ALL SELECT 'I:'||indexname FROM pg_indexes WHERE schemaname='iam_v2'
   UNION ALL SELECT 'G:'||grantee||':'||table_name||':'||privilege_type
     FROM information_schema.role_table_grants WHERE table_schema='iam_v2' AND grantee LIKE 'svc\\_%'
 ) s(x)")"
eq "the catalogue+privilege fingerprint MATCHES the accepted appliance state" "$FP" "$EXPECT_FINGERPRINT"
echo "  catalogue fingerprint: $FP"
printf '%s' "$FP" > "${TMPDIR:-/tmp}/phase7_fullscratch_fingerprint.$DB"

echo "------------------------------------------------------------"
printf 'PHASE7_BUILD_FULL_SCRATCH db=%s pass=%d fail=%d\n' "$DB" "$pass" "$fail"
[ "$fail" -eq 0 ]
