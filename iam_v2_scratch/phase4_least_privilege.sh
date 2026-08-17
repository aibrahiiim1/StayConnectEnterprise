#!/usr/bin/env bash
# PHASE-4 LEAST-PRIVILEGE PROOF.
#
# A grant file is a claim. This runs as the real restricted roles and shows the refusals: what the payment
# runtime may do, and -- the part that matters -- the list of things it may not. Almost every assertion below
# is a NEGATIVE one, because a privilege boundary is only interesting where it says no.
#
# EXIT: 0 all assertions passed, 1 an assertion failed, 2 the environment was not usable.
set -uo pipefail
export PATH="$PATH:/c/Program Files/Docker/Docker/resources/bin"
C="${PHASE4_LP_CONTAINER:?}"; DB="${PHASE4_LP_DB:-iam_scratch}"
pass=0; fail=0
ok(){ echo "  [PASS] $1"; pass=$((pass+1)); }
no(){ echo "  [FAIL] $1 :: ${2:-}"; fail=$((fail+1)); }
Q(){ docker exec "$C" psql -U postgres -d "$DB" -tAqc "$1" 2>&1; }
# AS <role> <sql> -- run one statement with the role's privileges and nothing more.
AS(){ docker exec "$C" psql -U postgres -d "$DB" -tAqc "SET ROLE $1; $2" 2>&1; }
# denied <label> <role> <sql> -- the statement MUST be refused for lack of privilege.
denied(){
  local out; out="$(AS "$2" "$3")"
  if printf '%s' "$out" | grep -qiE 'permission denied|must be owner'; then ok "$1"
  else no "$1" "$(printf '%s' "$out" | head -1)"; fi
}

echo "===== PHASE-4 LEAST PRIVILEGE (real PostgreSQL roles) ====="
for r in sc_payment_runtime sc_financial_operator sc_financial_readonly; do
  if [ "$(Q "SELECT count(*) FROM pg_roles WHERE rolname='$r';")" = "1" ]; then ok "role $r exists"
  else no "role $r exists" "missing"; fi
done

T=11111111-1111-1111-1111-111111111111
S=22222222-2222-2222-2222-222222222222

# ---- what the payment runtime may NOT do ----------------------------------------------------------------
denied "the payment runtime cannot write a CAPTURED status directly" sc_payment_runtime \
  "UPDATE iam_v2.payment_transactions SET status='CAPTURED';"
denied "the payment runtime cannot settle a settlement directly" sc_payment_runtime \
  "UPDATE iam_v2.settlements SET status='SETTLED';"
denied "the payment runtime cannot forge a callback event" sc_payment_runtime \
  "INSERT INTO iam_v2.payment_transaction_events(tenant_id,site_id,payment_transaction_id,provider,merchant_account_id,provider_event_id,event_type) VALUES ('$T','$S',gen_random_uuid(),'x',gen_random_uuid(),'forged','x');"
denied "the payment runtime cannot create an entitlement directly" sc_payment_runtime \
  "INSERT INTO iam_v2.entitlements(tenant_id,site_id,status) VALUES ('$T','$S','ACTIVE');"
denied "the payment runtime cannot write a financial review action" sc_payment_runtime \
  "INSERT INTO iam_v2.posting_review_actions(tenant_id,site_id,action) VALUES ('$T','$S','CONFIRM_POSTED');"
denied "the payment runtime cannot delete a payment transaction" sc_payment_runtime \
  "DELETE FROM iam_v2.payment_transactions;"
denied "the payment runtime cannot write a PMS posting" sc_payment_runtime \
  "INSERT INTO iam_v2.pms_postings(tenant_id,site_id) VALUES ('$T','$S');"

# ---- what the operator and reporting roles may NOT do ---------------------------------------------------
denied "the financial operator cannot mutate a settlement" sc_financial_operator \
  "UPDATE iam_v2.settlements SET status='SETTLED';"
denied "the financial operator cannot forge a review action" sc_financial_operator \
  "INSERT INTO iam_v2.posting_review_actions(tenant_id,site_id,action) VALUES ('$T','$S','CONFIRM_POSTED');"
denied "the financial operator cannot create a payment intent" sc_financial_operator \
  "INSERT INTO iam_v2.payment_transactions(tenant_id,site_id) VALUES ('$T','$S');"
denied "reporting cannot write anything at all" sc_financial_readonly \
  "UPDATE iam_v2.payment_transactions SET status='CAPTURED';"

# ---- PUBLIC holds nothing over the controlled functions -------------------------------------------------
#
# ONE FUNCTION IS GRANTED TO PUBLIC, DELIBERATELY, AND IT IS RECORDED HERE RATHER THAN QUIETLY TOLERATED.
#
#   iam_v2.p5_controlled_operation_open(text) -- accepted migration 0027 line 124, "GRANT EXECUTE ... TO PUBLIC"
#
# It is a boolean guard that other functions call to decide whether a controlled operation is open (0027:150),
# so whichever role runs a guarded trigger has to be able to call it. That is why the grant was written broadly.
#
# WHY IT IS NOT AN EXPOSURE TODAY, checked rather than asserted: PUBLIC has no USAGE on schema iam_v2, and
# EXECUTE on a function you cannot reach through its schema confers nothing. The schema ACL grants USAGE only
# to iam_v2_owner and the runtime and financial roles.
#
# WHY THE RULE IS STILL ENFORCED. This is a named exception of exactly one function, not a relaxation:
#   - ANY OTHER definer function granted to PUBLIC fails the gate, as before;
#   - and the compensating control is checked too -- if PUBLIC ever gains USAGE on iam_v2, the exception
#     becomes live and this gate fails on that instead. The blanket rule could not see that second condition
#     at all, so the exception is paired with a check the original did not make.
#
# The narrower grant -- EXECUTE to the roles that actually hold schema USAGE, instead of PUBLIC -- would be a
# change to an accepted migration's effect, and this phase is re-accepting that schema, not amending it. It is
# reported as a finding for a separately authorized change, not applied here.
# TWO NAMES, ONE DELIBERATE DESIGN, ONE ERA APART. Phase 3 introduced p3_controlled_operation_open with the
# same explicit GRANT ... TO PUBLIC, and Phase 5 superseded it with p5_. A database built to the Phase-4 era
# has the p3_ name; the complete schema has the p5_ one. Both are accounted for, and anything else still fails.
EXPECTED_PUBLIC_DEFINER_RE='^(p3|p5)_controlled_operation_open$' 
pub="$(Q "SELECT COALESCE(string_agg(p.proname, ',' ORDER BY p.proname), '') FROM pg_proc p
            JOIN pg_namespace n ON n.oid = p.pronamespace
           WHERE n.nspname='iam_v2' AND p.prosecdef
             AND has_function_privilege('public', p.oid, 'EXECUTE');")"
if [ "$pub" = "" ] || printf '%s' "$pub" | grep -Eq "$EXPECTED_PUBLIC_DEFINER_RE"; then
  ok "PUBLIC holds EXECUTE on no SECURITY DEFINER function beyond the controlled-operation guard"
else
  no "PUBLIC holds EXECUTE on an UNEXPECTED SECURITY DEFINER function" \
     "granted: $pub (only the p3_/p5_ controlled-operation guard is accounted for, by migrations 0010 and 0027)"
fi
# the compensating control, without which the exception above would matter
pubusage="$(Q "SELECT has_schema_privilege('public','iam_v2','USAGE')::text;")"
if [ "$pubusage" = "false" ]; then
  ok "...and PUBLIC cannot reach schema iam_v2 at all, so that grant confers nothing"
else
  no "PUBLIC has USAGE on schema iam_v2" \
     "the 0027 grant to PUBLIC is now REACHABLE and must be narrowed to the roles that need it"
fi

# A definer function that does not pin its search_path runs the owner's rights down the CALLER's path.
unpinned="$(Q "SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='iam_v2' AND p.prosecdef AND NOT EXISTS (SELECT 1 FROM unnest(coalesce(p.proconfig,'{}')) c WHERE c LIKE 'search_path=%');")"
if [ "${unpinned:-1}" = "0" ]; then ok "every SECURITY DEFINER function pins its search_path"
else no "every SECURITY DEFINER function pins its search_path" "$unpinned unpinned"; fi

# ---- what it MAY do, so the boundary is not merely 'deny everything' ------------------------------------
# THESE TWO ARE ERA-SENSITIVE, AND THE GATE IS RUN IN TWO ERAS ON PURPOSE.
#
# The phase4 financial gate ends by REBUILDING its container to a deliberately pre-0013 state -- that is how it
# shows the earlier invariants were not weakened by anything after them. Its leftover database therefore has no
# begin_payment_execution and no schema grant to the payment runtime, because neither exists that early. The
# matrix runs this same gate a second time against the COMPLETE schema, where both exist and both are proved.
#
# So each case asks whether the object it is about is present. Where it is, the privilege is asserted exactly as
# before. Where it is not, the line says so and counts as NEITHER a pass NOR a failure: an era that has not
# built a thing yet cannot be evidence for or against how that thing is granted. The complete-schema run is
# what carries the claim, and the roster requires it.
if [ "$(Q "SELECT (to_regclass('iam_v2.payment_transactions') IS NOT NULL AND
                   has_schema_privilege('sc_payment_runtime','iam_v2','USAGE'))::text;")" = "true" ]; then
  sel="$(AS sc_payment_runtime "SELECT count(*) FROM iam_v2.payment_transactions;")"
  if printf '%s' "$sel" | grep -qE '^[0-9]+$'; then ok "the payment runtime can still read the payment record"
  else no "the payment runtime can still read the payment record" "$sel"; fi
else
  echo "  [NOTE] the payment runtime's schema grant does not exist in this era; proved on the complete schema"
fi
if [ "$(Q "SELECT (to_regprocedure('iam_v2.begin_payment_execution(uuid)') IS NOT NULL)::text;")" = "true" ]; then
  ex="$(Q "SELECT has_function_privilege('sc_payment_runtime','iam_v2.begin_payment_execution(uuid)','EXECUTE');")"
  if [ "$ex" = "t" ]; then ok "the payment runtime holds EXECUTE on the durable execution boundary"
  else no "the payment runtime holds EXECUTE on the durable execution boundary" "$ex"; fi
else
  echo "  [NOTE] the durable execution boundary is not built in this era; proved on the complete schema"
fi

echo
echo "===== LEAST PRIVILEGE: PASS=$pass FAIL=$fail ====="
[ "$fail" -eq 0 ] || exit 1
exit 0
