#!/usr/bin/env bash
# PHASE-7 — BUILD THE ACCEPTED FULL-SYSTEM SCRATCH DATABASE.
#
# TWO CLAIMS, KEPT APART ON PURPOSE. Collapsing them is how fixture rows end up inside a statement about what
# the appliance contains:
#
#   1. ACCEPTED SCHEMA FIDELITY -- the catalogue, privileges and definitions are the DEVELOPMENT appliance's
#      accepted as-built shape. Proven by a semantic digest over column types/nullability/defaults, full
#      constraint and index definitions, trigger definitions, function signatures and BODIES, table grants and
#      EFFECTIVE function privileges. It carries no rows and makes no claim about data.
#
#   2. DETERMINISTIC TEST FIXTURE -- the canonical tenant/site/guest-network/appliance/operator identifiers
#      that eleven gates address by literal uuid without seeding them. This is TEST DATA. It is not part of any
#      statement about appliance or production state, it is applied afterwards, and it is reported separately.
#
# WHY NOT REBUILD FROM MIGRATIONS. The earlier attempt composed data-plane 0001-0008 + iam_v2_scratch mg1-mg9 +
# data-plane 0009-0047, and a name-level fingerprint said it matched the appliance. The semantic digest said
# otherwise: 46 function BODIES differed and 6 foreign keys were missing. mg1-mg9 is the scratch equivalent of
# Phase 1A/1B, not the source the appliance's iam_v2 was built from, so that composition was an approximate
# hybrid -- precisely what a full-system re-acceptance must not diagnose against. The narrowest correct
# reconstruction is the appliance's own schema, captured with pg_dump --schema-only (a read; no Production is
# involved) and committed under accepted/ so the procedure reproduces without the appliance being reachable.
#
#   usage:  phase7_build_full_scratch.sh [database-name]      default: iam_accepted
set -uo pipefail
export PATH="$PATH:/c/Program Files/Docker/Docker/resources/bin"
HERE="$(cd "$(dirname "$0")" && pwd)"

C="${PHASE7_CONTAINER:-iamv2-p6}"
DB="${1:-${PHASE7_ACCEPTED_DB:-iam_accepted}}"
SCHEMA="$HERE/accepted/appliance-schema-20260816.sql"

# The accepted semantic digest, read from the DEVELOPMENT appliance on 2026-08-16 with the identical
# expression in phase7_fidelity.sql. Changing this is an edit somebody makes and justifies, never a value the
# script discovers and then agrees with.
EXPECT_DIGEST="58a92f656aaeab241d6b33b2b8d0673d parts=1848"

# Every role the dump's ownership and grant statements name. Missing one turns GRANT lines into errors and
# silently produces a database with a different privilege shape -- which the digest would catch, but only
# after wasting a diagnosis.
ROLES="stayconnect iam_v2_owner iam_v2_migrator iam_v2_svc_scd iam_v2_svc_edged iam_v2_svc_acctd \
iam_v2_svc_portald iam_v2_svc_hoteladm svc_scd svc_edged svc_acctd svc_netd sc_payment_runtime \
sc_payment_outcome sc_commerce_runtime sc_financial_operator sc_financial_readonly"

pass=0; fail=0
ok(){ printf '  [PASS] %s\n' "$1"; pass=$((pass+1)); }
no(){ printf '  [FAIL] %s :: %s\n' "$1" "${2:-}"; fail=$((fail+1)); }
q(){ docker exec -i "$C" psql -U postgres -d "$DB" -tAqc "$1" </dev/null 2>&1; }
eq(){ [ "$2" = "$3" ] && ok "$1" || no "$1" "expected '$3', got '$2'"; }

echo "== Phase 7: building the accepted full-system scratch database ($DB) =="

[ -f "$SCHEMA" ] || { echo "missing accepted schema: $SCHEMA"; exit 1; }

docker exec -i "$C" psql -U postgres -d postgres -qc "DROP DATABASE IF EXISTS $DB" </dev/null >/dev/null 2>&1
docker exec -i "$C" psql -U postgres -d postgres -qc "CREATE DATABASE $DB" </dev/null >/dev/null 2>&1 \
  || { echo "cannot create $DB"; exit 1; }

for r in $ROLES; do
  docker exec -i "$C" psql -U postgres -d "$DB" -qc \
    "DO \$\$ BEGIN IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='$r') THEN EXECUTE 'CREATE ROLE $r NOLOGIN'; END IF; END \$\$;" \
    </dev/null >/dev/null 2>&1
done
eq "every role the accepted schema references exists" \
   "$(q "SELECT count(*) FROM pg_roles WHERE rolname IN ('iam_v2_owner','svc_scd','svc_acctd','svc_edged','svc_netd')")" "5"

err="$(docker exec -i "$C" psql -U postgres -d "$DB" -q < "$SCHEMA" 2>&1 >/dev/null)"
# Two errors are expected in a scratch container and are NAMED rather than filtered blindly: `schema "public"
# already exists` (the dump recreates it) and `_timescaledb_functions does not exist` (the appliance runs
# TimescaleDB, the scratch container does not, and nothing under iam_v2 depends on it).
unexpected="$(printf '%s' "$err" | grep -i '^ERROR' \
  | grep -viE 'schema "public" already exists|_timescaledb_functions' | head -3)"
[ -z "$unexpected" ] && ok "the accepted schema restored with no unexpected error" \
  || no "restore produced unexpected errors" "$(printf '%s' "$unexpected" | head -1 | cut -c1-120)"

# ---- CLAIM 1: accepted schema fidelity ---------------------------------------------------------------------
echo
echo "-- claim 1: accepted schema fidelity --"
GOT="$(docker exec -i "$C" psql -U postgres -d "$DB" -tAq < "$HERE/phase7_fidelity.sql" 2>&1 | tail -1)"
eq "the SEMANTIC digest matches the accepted appliance schema" "$GOT" "$EXPECT_DIGEST"
echo "  digest: $GOT"

# ---- CLAIM 2: the deterministic test fixture ----------------------------------------------------------------
echo
echo "-- claim 2: deterministic TEST FIXTURE (test data; no claim about appliance state) --"
fixerr="$(docker exec -i "$C" psql -U postgres -d "$DB" -q < "$HERE/phase7_fixture.sql" 2>&1 >/dev/null \
          | grep -i '^ERROR' | head -1)"
[ -z "$fixerr" ] && ok "the deterministic Phase-7 fixture applied" \
  || no "the platform fixture failed" "$(printf '%s' "$fixerr" | cut -c1-120)"

# The eleven gates that address these identifiers by literal uuid without seeding them are why this exists. A
# gate failing for want of these rows is a MISSING FIXTURE, not a product regression, and the two must never
# be reported as the same thing.
eq "fixture tenant present"    "$(q "SELECT count(*) FROM public.tenants    WHERE id='11111111-1111-1111-1111-111111111111'")" "1"
eq "fixture site present"      "$(q "SELECT count(*) FROM public.sites      WHERE id='22222222-2222-2222-2222-222222222222'")" "1"
eq "fixture appliance present" "$(q "SELECT count(*) FROM public.appliances WHERE id='44444444-4444-4444-4444-444444444444'")" "1"
eq "fixture guest network present" \
   "$(q "SELECT count(*) FROM public.guest_networks WHERE id='33333333-3333-3333-3333-333333333333'")" "1"
eq "at least one fixture operator present" \
   "$(q "SELECT (count(*) > 0)::text FROM public.operators")" "true"

# Fixture rows must not have disturbed the SCHEMA claim: data is not shape, and if inserting rows moved the
# digest then the digest is fingerprinting the wrong thing.
AFTER="$(docker exec -i "$C" psql -U postgres -d "$DB" -tAq < "$HERE/phase7_fidelity.sql" 2>&1 | tail -1)"
eq "the fixture changed no schema definition (digest unmoved)" "$AFTER" "$EXPECT_DIGEST"

echo "------------------------------------------------------------"
printf 'PHASE7_BUILD_FULL_SCRATCH db=%s pass=%d fail=%d\n' "$DB" "$pass" "$fail"
[ "$fail" -eq 0 ]
