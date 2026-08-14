#!/usr/bin/env bash
# PHASE-5 LEAST-PRIVILEGE PROOF.
#
# The privilege model is DERIVED, not pre-committed: this measures what the delivered Phase-5 objects actually
# expose, and asserts the refusals. Almost every assertion is a negative one, because a privilege boundary is
# only interesting where it says no.
#
# The DARK claim being proved is precise: while Phase 5 is off, no service role holds ANY privilege on any
# Phase-5 table, and no role can reach the guarded writes even if it somehow held table grants.
#
# EXIT: 0 all assertions passed, 1 an assertion failed, 2 the environment was not usable.
set -uo pipefail
export PATH="$PATH:/c/Program Files/Docker/Docker/resources/bin"
C="${PHASE5_CONTAINER:-iamv2-p5}"; DB="${PHASE5_DB:-iam_scratch_p5}"
pass=0; fail=0
ok(){ printf '  [PASS] %s\n' "$1"; pass=$((pass+1)); }
no(){ printf '  [FAIL] %s :: %s\n' "$1" "${2:-}"; fail=$((fail+1)); }
Q(){ docker exec "$C" psql -U postgres -d "$DB" -tAqc "$1" 2>&1; }
AS(){ docker exec "$C" psql -U postgres -d "$DB" -tAqc "SET ROLE $1; $2" 2>&1; }
eq(){ [ "$2" = "$3" ] && ok "$1" || no "$1" "expected '$2' got '$3'"; }

docker exec "$C" psql -U postgres -d "$DB" -tAqc 'select 1' >/dev/null 2>&1 \
  || { echo "INFRA: container $C / db $DB unusable"; exit 2; }

echo "===== PHASE-5 LEAST PRIVILEGE (db=$DB) ====="

P5_TABLES="'post_stay_profiles','entitlement_transfers','stay_links'"

echo "== A. what the delivered objects actually expose =="
GRANTS="$(Q "SELECT COALESCE(string_agg(DISTINCT grantee||':'||table_name||':'||privilege_type, ', '), '')
             FROM information_schema.role_table_grants
             WHERE table_schema='iam_v2' AND table_name IN ($P5_TABLES)
               AND grantee NOT IN (current_user,'PUBLIC');")"
eq "no non-owner role holds ANY privilege on a Phase-5 table (dark)" "" "$GRANTS"

# The five Phase-4 runtime roles are the ones that exist on a delivered appliance. None may have gained
# anything from Phase 5 -- a phase that grants nothing must be measurable as granting nothing.
for role in sc_payment_runtime sc_payment_outcome sc_commerce_runtime sc_financial_operator sc_financial_readonly; do
  EXISTS="$(Q "SELECT count(*) FROM pg_roles WHERE rolname='$role';")"
  [ "$EXISTS" = "1" ] || { ok "$role is not present in this database (nothing to grant)"; continue; }
  N="$(Q "SELECT count(*) FROM information_schema.role_table_grants
          WHERE table_schema='iam_v2' AND table_name IN ($P5_TABLES) AND grantee='$role';")"
  eq "$role holds no privilege on any Phase-5 table" "0" "$N"
  F="$(Q "SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
          WHERE n.nspname='iam_v2' AND p.proname LIKE 'p5\_%'
            AND has_function_privilege('$role', p.oid, 'EXECUTE')
            AND p.proname <> 'p5_controlled_operation_open';")"
  eq "$role cannot execute any Phase-5 function except the open-checker" "0" "$F"
done

echo "== B. PUBLIC =="
for fn in "iam_v2.p5_begin_controlled_operation(text)" "iam_v2.p5_post_stay_profile_guard()" \
          "iam_v2.p5_entitlement_transfer_guard()" "iam_v2.p5_stay_link_guard()" \
          "iam_v2.p5_controlled_writer_only()" "iam_v2.p5_post_stay_authenticable(uuid,uuid,uuid)"; do
  eq "PUBLIC cannot execute $fn" "f" "$(Q "SELECT has_function_privilege('public','$fn','EXECUTE');")"
done
# The one deliberate exception, and the reason it is one: the guard trigger runs as whichever role is
# writing, so that role must be able to ASK whether it has an open scope. What it learns is whether IT has
# one, in its OWN transaction -- which it already knows.
eq "PUBLIC CAN execute the open-checker (the guard runs as the writer and must be able to ask)" "t" \
   "$(Q "SELECT has_function_privilege('public','iam_v2.p5_controlled_operation_open(text)','EXECUTE');")"

echo "== C. a role WITH full table grants still cannot write =="
Q "DROP ROLE IF EXISTS p5_lp_probe;" >/dev/null
Q "CREATE ROLE p5_lp_probe NOLOGIN;
   GRANT USAGE ON SCHEMA iam_v2 TO p5_lp_probe;
   GRANT SELECT, INSERT, UPDATE, DELETE ON iam_v2.post_stay_profiles, iam_v2.entitlement_transfers,
         iam_v2.stay_links TO p5_lp_probe;" >/dev/null
HASH='$argon2id$v=19$m=65536,t=1,p=4$c2FsdHNhbHQ$aGFzaGhhc2hoYXNoaGFzaGhhc2hoYXNo'
# The ids are DELIBERATELY fictional. A BEFORE ROW trigger fires before foreign keys are checked, so the
# controlled-writer guard answers first and this gate needs no fixture at all — which matters because it runs
# on a freshly-built chain in CI where no Stay exists yet. Depending on another gate's leftovers would make
# this one's result depend on execution order.
FAKE_T=11111111-1111-1111-1111-111111111111
FAKE_S=22222222-2222-2222-2222-222222222222
FAKE_ST=eeee0000-0000-0000-0000-00000000dead
out="$(AS p5_lp_probe "INSERT INTO iam_v2.post_stay_profiles
  (tenant_id,site_id,origin_stay_id,origin_lifecycle_version,pin_hash,valid_until,issued_via,pin_revealed_at)
  VALUES ('$FAKE_T','$FAKE_S','$FAKE_ST',1,'$HASH', now()+interval '1 day','GUEST_AUTHENTICATED_SESSION', now());")"
[[ "$out" == *"require an open controlled operation"* ]] \
  && ok "full table grants are NOT enough to write a post-stay profile" \
  || no "granted role is refused" "$(head -1 <<<"$out")"
out="$(AS p5_lp_probe "SELECT iam_v2.p5_begin_controlled_operation('post_stay_identity');")"
[[ "$out" == *"permission denied"* ]] \
  && ok "...and it cannot open the operation to get around that" \
  || no "granted role cannot open the scope" "$(head -1 <<<"$out")"
# REVOKE BEFORE DROP, and assert it went. PostgreSQL refuses to drop a role that still holds privileges, and
# it refuses QUIETLY here because the output is discarded -- so a probe role survives, and the NEXT gate that
# measures "no non-owner role holds anything" reports this test's leftover as a privilege defect. That is
# exactly what happened, in both directions, between this gate and the foundation one.
Q "REVOKE ALL ON iam_v2.post_stay_profiles, iam_v2.entitlement_transfers, iam_v2.stay_links FROM p5_lp_probe;
   REVOKE USAGE ON SCHEMA iam_v2 FROM p5_lp_probe;
   DROP ROLE IF EXISTS p5_lp_probe;" >/dev/null
eq "the probe role is gone, so the next gate measures the real posture" "0"    "$(Q "SELECT count(*) FROM pg_roles WHERE rolname='p5_lp_probe';")"

echo "== D. the objects themselves =="
BAD="$(Q "SELECT COALESCE(string_agg(p.proname, ','),'') FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
          WHERE n.nspname='iam_v2' AND p.proname LIKE 'p5\_%'
            AND (p.proconfig IS NULL OR array_to_string(p.proconfig,',') NOT LIKE '%search_path=%');")"
eq "every Phase-5 function pins its search_path" "" "$BAD"
SECDEF="$(Q "SELECT COALESCE(string_agg(p.proname, ','),'') FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
             WHERE n.nspname='iam_v2' AND p.proname LIKE 'p5\_%' AND p.prosecdef
               AND p.proname NOT IN ('p5_begin_controlled_operation','p5_controlled_operation_open');")"
eq "only the two openers/checkers are SECURITY DEFINER" "" "$SECDEF"

echo "===== RESULT PASS=$pass FAIL=$fail ====="
[ "$fail" -eq 0 ] || exit 1
exit 0
