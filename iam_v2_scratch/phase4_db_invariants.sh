#!/usr/bin/env bash
# PHASE-4 FINANCIAL DB INVARIANTS — BEHAVIOURAL PROOF, not catalog inspection.
#
# Every invariant below already exists in the schema (mg7 + 0010). None of them had ever been EXERCISED:
# iam_v2_scratch/guard_tests.sh covers the scratch-safety allowlist only. A constraint that appears in
# pg_catalog and a constraint that actually refuses the write are different claims, and only the second one
# is acceptance evidence — so each check here performs the forbidden write and asserts it is rejected.
#
# Disposable scratch PostgreSQL ONLY (the allowlist guard in lib.sh enforces that).
#
# Usage:  SCRATCH_ACK=I_UNDERSTAND_DISPOSABLE bash iam_v2_scratch/phase4_db_invariants.sh
HERE="$(cd "$(dirname "$0")" && pwd)"; source "$HERE/lib.sh"
set +e +o pipefail
require_scratch

PASSN=0; FAILN=0
ok(){ printf '  PASS  %-10s %s\n' "$1" "$2"; PASSN=$((PASSN+1)); }
no(){ printf '  FAIL  %-10s %s :: %s\n' "$1" "$2" "$3"; FAILN=$((FAILN+1)); }

q(){ docker exec -i "$SCRATCH_CONTAINER" psql -U postgres -d "$SCRATCH_DB" -qAt -c "$1" 2>&1; }

# rejects <C-row> <label> <sql> [expected substring]
rejects(){
  local c="$1" label="$2" sql="$3" want="${4:-}"
  local out; out="$(q "$sql")"
  if [ -n "$(printf '%s' "$out" | grep -i 'ERROR')" ]; then
    if [ -z "$want" ] || printf '%s' "$out" | grep -qi -- "$want"; then ok "$c" "$label"
    else no "$c" "$label" "rejected, but not for the expected reason: $(printf '%s' "$out" | head -1)"; fi
  else
    no "$c" "$label" "WRITE WAS ACCEPTED"
  fi
}
# accepts <C-row> <label> <sql>
accepts(){
  local c="$1" label="$2" sql="$3"
  local out; out="$(q "$sql")"
  if [ -n "$(printf '%s' "$out" | grep -i 'ERROR')" ]; then no "$c" "$label" "$(printf '%s' "$out" | head -1)"
  else ok "$c" "$label"; fi
}

T="11111111-1111-1111-1111-111111111111"   # tenant   (from seed)
S="22222222-2222-2222-2222-222222222222"   # site
IF1="aaaa0000-0000-0000-0000-000000000001" # pms interface

echo "===== PHASE-4 FINANCIAL DB INVARIANTS (behavioural) ====="

# Fixture context: a revision with a CONCRETE folio strategy, an IN_HOUSE postable stay and a folio, so the
# fail-closed paths below are proven to fail for the RIGHT reason rather than for a missing fixture.
# This suite is the BASELINE proof: every invariant here existed before migration 0011. It is run against
# BOTH chains - pre-0011 to record what was already true, and post-0011 to prove 0011 weakened none of it.
# Where 0011 adds a STRICTER refusal on the same write, the expected reason differs, so the few checks
# below that are affected branch on the schema instead of pretending the two chains are identical.
HAS_0011="$(q "SELECT count(*) FROM information_schema.columns WHERE table_schema='iam_v2' AND table_name='pms_interface_revisions' AND column_name='financial_base_currency';")"
if [ "$HAS_0011" = "1" ]; then
  # post-0011 a posting also needs a FINANCIALLY ONBOARDED revision, so the positive control must use one
  REV_OK="$(q "SELECT id FROM iam_v2.pms_interface_revisions WHERE pms_interface_id='$IF1' AND folio_identity_strategy <> 'UNSET' AND financial_base_currency IS NOT NULL ORDER BY revision_no DESC LIMIT 1;")"
else
  REV_OK="$(q "SELECT id FROM iam_v2.pms_interface_revisions WHERE pms_interface_id='$IF1' AND folio_identity_strategy <> 'UNSET' ORDER BY revision_no DESC LIMIT 1;")"
fi
REV_UNSET="$(q "SELECT id FROM iam_v2.pms_interface_revisions WHERE pms_interface_id='$IF1' AND folio_identity_strategy='UNSET' ORDER BY revision_no DESC LIMIT 1;")"
STAY="$(q "SELECT id FROM iam_v2.stays WHERE pms_interface_id='$IF1' LIMIT 1;")"
FOLIO="$(q "SELECT id FROM iam_v2.folios WHERE pms_interface_id='$IF1' LIMIT 1;")"
PUR="$(q "SELECT id FROM iam_v2.purchases LIMIT 1;")"
SET="$(q "SELECT id FROM iam_v2.settlements LIMIT 1;")"
echo "  schema: 0011_applied=$HAS_0011"
echo "  fixture: rev_ok=${REV_OK:0:8} rev_unset=${REV_UNSET:0:8} stay=${STAY:0:8} folio=${FOLIO:0:8} purchase=${PUR:0:8} settlement=${SET:0:8}"

mkposting(){ # $1=id $2=revision $3=idem
  echo "INSERT INTO iam_v2.pms_postings(id,tenant_id,site_id,pms_interface_id,settlement_id,purchase_id,stay_id,folio_id,posting_interface_revision_id,posting_type,amount_minor,currency,currency_exponent,idempotency_key) VALUES ('$1','$T','$S','$IF1','$SET','$PUR','$STAY','$FOLIO','$2','CHARGE',100,'USD',2,'$3');"
}

# ---- C5 folio strategy UNSET blocks CHARGE ---------------------------------------------------------------
if [ -n "$REV_UNSET" ]; then
  rejects "C5" "UNSET folio strategy blocks CHARGE fail-closed" \
    "$(mkposting 'e5e50000-0000-0000-0000-000000000001' "$REV_UNSET" 'inv-unset-1')" "FOLIO_STRATEGY_UNSET"
else
  echo "  SKIP  C5         no UNSET revision in fixture"
fi

# ---- C1 a CHARGE against a concrete strategy is ACCEPTED (positive control) -------------------------------
accepts "C1" "CHARGE accepted when every pinned object is in scope" \
  "$(mkposting 'c1c10000-0000-0000-0000-000000000001' "$REV_OK" 'inv-ok-1')"

# ---- pms_postings append-only ----------------------------------------------------------------------------
rejects "C-AO1" "pms_postings rejects UPDATE" \
  "UPDATE iam_v2.pms_postings SET amount_minor=999 WHERE id='c1c10000-0000-0000-0000-000000000001';"
rejects "C-AO1" "pms_postings rejects DELETE" \
  "DELETE FROM iam_v2.pms_postings WHERE id='c1c10000-0000-0000-0000-000000000001';"

# ---- idempotency uniqueness ------------------------------------------------------------------------------
rejects "C26" "duplicate idempotency_key rejected" \
  "$(mkposting 'c1c10000-0000-0000-0000-000000000002' "$REV_OK" 'inv-ok-1')" "idempotency"

# ---- C1 cross-scope composite FK pinning -----------------------------------------------------------------
rejects "C1" "folio from another tenant/site/interface rejected" \
  "INSERT INTO iam_v2.pms_postings(id,tenant_id,site_id,pms_interface_id,settlement_id,purchase_id,stay_id,folio_id,posting_interface_revision_id,posting_type,amount_minor,currency,currency_exponent,idempotency_key)
   VALUES ('c1c10000-0000-0000-0000-000000000003','$T','$S','$IF1','$SET','$PUR','$STAY','99999999-9999-9999-9999-999999999999','$REV_OK','CHARGE',100,'USD',2,'inv-badfolio');"

# ---- C9 P# uniqueness per interface ----------------------------------------------------------------------
accepts "C9" "first attempt with P#=900001 accepted" \
  "INSERT INTO iam_v2.posting_attempts(id,tenant_id,site_id,internal_posting_id,pms_interface_id,attempt_no,p_number,rn,g_number,sent_at)
   VALUES ('a9a90000-0000-0000-0000-000000000001','$T','$S','c1c10000-0000-0000-0000-000000000001','$IF1',1,'900001','14215','G1',now());"
# A SECOND posting reusing the same P#. Deliberately a different posting rather than a second attempt on
# the same one: this check is about the per-interface P# namespace, and reusing the first posting would
# let the post-0011 retry gate answer first and leave P# uniqueness untested.
accepts "C9" "a second posting exists to contest the P# namespace" \
  "$(mkposting 'c1c10000-0000-0000-0000-000000000009' "$REV_OK" 'inv-ok-9')"
rejects "C9" "duplicate P# in the SAME interface rejected" \
  "INSERT INTO iam_v2.posting_attempts(id,tenant_id,site_id,internal_posting_id,pms_interface_id,attempt_no,p_number,rn,g_number,sent_at)
   VALUES ('a9a90000-0000-0000-0000-000000000002','$T','$S','c1c10000-0000-0000-0000-000000000009','$IF1',1,'900001','14215','G1',now());" "p_number"

# ---- attempt_no uniqueness per posting -------------------------------------------------------------------
# Pre-0011 the UNIQUE (internal_posting_id, attempt_no) index answers. Post-0011 the retry gate answers
# first and refuses it as an out-of-sequence attempt. Both refuse the duplicate; they say different things.
if [ "$HAS_0011" = "1" ]; then WANT8="ATTEMPT_SEQUENCE"; else WANT8="attempt_no"; fi
rejects "C8" "duplicate attempt_no for one posting rejected" \
  "INSERT INTO iam_v2.posting_attempts(id,tenant_id,site_id,internal_posting_id,pms_interface_id,attempt_no,p_number,rn,g_number,sent_at)
   VALUES ('a9a90000-0000-0000-0000-000000000003','$T','$S','c1c10000-0000-0000-0000-000000000001','$IF1',1,'900002','14215','G1',now());" "$WANT8"

# ---- C15 one-way outcome ---------------------------------------------------------------------------------
accepts "C15" "SENDING -> UNKNOWN allowed" \
  "UPDATE iam_v2.posting_attempts SET outcome='UNKNOWN' WHERE id='a9a90000-0000-0000-0000-000000000001';"
rejects "C15" "UNKNOWN -> SENDING rejected (one-way)" \
  "UPDATE iam_v2.posting_attempts SET outcome='SENDING' WHERE id='a9a90000-0000-0000-0000-000000000001';"
rejects "C15" "posting_attempts rejects DELETE" \
  "DELETE FROM iam_v2.posting_attempts WHERE id='a9a90000-0000-0000-0000-000000000001';"

# ---- C12 PA AS status catalog ----------------------------------------------------------------------------
rejects "C12" "invented PA AS status rejected" \
  "UPDATE iam_v2.posting_attempts SET pa_as_status='ZZ' WHERE id='a9a90000-0000-0000-0000-000000000001';" "pa_as_status"

# ---- C16 attempt events append-only ----------------------------------------------------------------------
accepts "C16" "attempt event insert accepted" \
  "INSERT INTO iam_v2.posting_attempt_events(id,tenant_id,site_id,posting_attempt_id,event_type,detail)
   VALUES ('e1e10000-0000-0000-0000-000000000001','$T','$S','a9a90000-0000-0000-0000-000000000001','SENT','{}');"
rejects "C16" "attempt event UPDATE rejected" \
  "UPDATE iam_v2.posting_attempt_events SET event_type='TAMPERED' WHERE id='e1e10000-0000-0000-0000-000000000001';"
rejects "C16" "attempt event DELETE rejected" \
  "DELETE FROM iam_v2.posting_attempt_events WHERE id='e1e10000-0000-0000-0000-000000000001';"

# ---- C17/C19 review action catalog + immutability ---------------------------------------------------------
if [ "$HAS_0011" = "1" ]; then
  # post-0011 the append-only ledger has exactly ONE writer, so the review goes through it
  accepts "C17" "CONFIRM_POSTED review action accepted (via the controlled writer)" \
    "SELECT iam_v2.record_posting_review_action('c1c10000-0000-0000-0000-000000000001','CONFIRM_POSTED','$T','FOLIO_VERIFIED',jsonb_build_object('folio','verified'));"
  rejects "C17" "generic APPROVE action rejected (no generic approve exists)" \
    "SELECT iam_v2.record_posting_review_action('c1c10000-0000-0000-0000-000000000001','APPROVE','$T','X',jsonb_build_object('folio','verified'));" "REVIEW_ACTION_UNKNOWN"
else
  accepts "C17" "CONFIRM_POSTED review action accepted" \
    "INSERT INTO iam_v2.posting_review_actions(id,tenant_id,site_id,posting_id,action,actor,reason,evidence)
     VALUES ('4a4a0000-0000-0000-0000-000000000001','$T','$S','c1c10000-0000-0000-0000-000000000001','CONFIRM_POSTED','$T','FOLIO_VERIFIED','{}');"
  rejects "C17" "generic APPROVE action rejected (no generic approve exists)" \
    "INSERT INTO iam_v2.posting_review_actions(id,tenant_id,site_id,posting_id,action,actor,reason,evidence)
     VALUES ('4a4a0000-0000-0000-0000-000000000002','$T','$S','c1c10000-0000-0000-0000-000000000001','APPROVE','$T','X','{}');" "action"
fi
# whichever way it was written, the recorded action is immutable
RACT="$(q "SELECT id FROM iam_v2.posting_review_actions WHERE posting_id='c1c10000-0000-0000-0000-000000000001' LIMIT 1;")"
rejects "C19" "review action UPDATE rejected" \
  "UPDATE iam_v2.posting_review_actions SET reason='CHANGED' WHERE id='$RACT';"
rejects "C19" "review action DELETE rejected" \
  "DELETE FROM iam_v2.posting_review_actions WHERE id='$RACT';"

# ---- C22 one active outbox row per posting -----------------------------------------------------------------
accepts "C22" "first QUEUED outbox row accepted" \
  "INSERT INTO iam_v2.posting_outbox(id,tenant_id,site_id,pms_interface_id,posting_id,state)
   VALUES ('0b0b0000-0000-0000-0000-000000000001','$T','$S','$IF1','c1c10000-0000-0000-0000-000000000001','QUEUED');"
rejects "C22" "second ACTIVE outbox row for the same posting rejected" \
  "INSERT INTO iam_v2.posting_outbox(id,tenant_id,site_id,pms_interface_id,posting_id,state)
   VALUES ('0b0b0000-0000-0000-0000-000000000002','$T','$S','$IF1','c1c10000-0000-0000-0000-000000000001','IN_FLIGHT');" "outbox_one_active"
accepts "C22" "a DONE row may coexist (partial index excludes terminal states)" \
  "UPDATE iam_v2.posting_outbox SET state='DONE' WHERE id='0b0b0000-0000-0000-0000-000000000001';
   INSERT INTO iam_v2.posting_outbox(id,tenant_id,site_id,pms_interface_id,posting_id,state)
   VALUES ('0b0b0000-0000-0000-0000-000000000003','$T','$S','$IF1','c1c10000-0000-0000-0000-000000000001','QUEUED');"

# ---- stay lifecycle gate ------------------------------------------------------------------------------------
q "UPDATE iam_v2.stays SET posting_allowed=false WHERE id='$STAY';" >/dev/null
rejects "C32" "posting_allowed=false blocks CHARGE" \
  "$(mkposting 'c3c30000-0000-0000-0000-000000000001' "$REV_OK" 'inv-noallow')" "POSTING_NOT_ALLOWED"
q "UPDATE iam_v2.stays SET posting_allowed=true WHERE id='$STAY';" >/dev/null
# ---- Phase-3 lifecycle boundary (a SEPARATE claim from the financial gate) ----------------------------------
# A raw status edit is refused because IN_HOUSE->CHECKED_OUT must SET effective_checkout_at in the same
# statement. This proves the lifecycle guard; it proves NOTHING about charge_gate, so it is scored on its own.
# Two independent structural refusals, scored separately because they prove different things.
rejects "P3-LC" "checkout leaving posting_allowed=true refused (posting_only_in_house)" \
  "UPDATE iam_v2.stays SET status='CHECKED_OUT', effective_checkout_at=now() WHERE id='$STAY';" "posting_only_in_house"
rejects "P3-LC" "checkout without effective_checkout_at refused (checkedout_needs_boundary CHECK)" \
  "UPDATE iam_v2.stays SET status='CHECKED_OUT', posting_allowed=false WHERE id='$STAY';" "checkedout_needs_boundary"

# ---- C32 non-IN_HOUSE blocks CHARGE — via the APPROVED lifecycle transition ---------------------------------
# The stay is checked out the way the Phase-3 contract says a checkout happens: status and
# effective_checkout_at move together, guard enabled, nothing bypassed. Only then is the CHARGE attempted, so a
# rejection is genuine evidence about charge_gate rather than a side effect of a refused fixture edit.
# A real checkout moves all three together: status, effective_checkout_at and posting_allowed. The
# posting_only_in_house CHECK makes posting_allowed=true with a non-IN_HOUSE status structurally
# impossible, so the fixture must satisfy it exactly as production would.
q "UPDATE iam_v2.stays SET status='CHECKED_OUT', effective_checkout_at=now(), posting_allowed=false WHERE id='$STAY';" >/dev/null
NOWST="$(q "SELECT status FROM iam_v2.stays WHERE id='$STAY';")"
if [ "$NOWST" = "CHECKED_OUT" ]; then
  ok "P3-LC" "approved checkout applied (status=CHECKED_OUT, effective_checkout_at set)"
  rejects "C32" "genuinely non-IN_HOUSE stay blocks CHARGE" \
    "$(mkposting 'c3c30000-0000-0000-0000-000000000002' "$REV_OK" 'inv-notinhouse')" "POSTING_NOT_ALLOWED"
  # Reinstate through the approved path: CHECKED_OUT->IN_HOUSE must CLEAR effective_checkout_at and increment
  # lifecycle_version exactly once. Restoring the fixture by any other route would itself be a bypass.
  q "UPDATE iam_v2.stays SET status='IN_HOUSE', effective_checkout_at=NULL, posting_allowed=true, lifecycle_version=lifecycle_version+1 WHERE id='$STAY';" >/dev/null
  BACK="$(q "SELECT status FROM iam_v2.stays WHERE id='$STAY';")"
  if [ "$BACK" = "IN_HOUSE" ]; then ok "P3-LC" "approved reinstatement restored the fixture (CHECKED_OUT->IN_HOUSE)"
  else no "P3-LC" "approved reinstatement" "stay left in $BACK"; fi
else
  no "C32" "genuinely non-IN_HOUSE stay blocks CHARGE" \
     "could not reach CHECKED_OUT through the approved transition (status=$NOWST) - the financial gate was NOT exercised"
fi

# ---- guards are still ENABLED (not silently disabled by this suite) -----------------------------------------
DIS="$(q "SELECT count(*) FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='iam_v2' AND NOT t.tgisinternal AND t.tgenabled='D';")"
if [ "$DIS" = "0" ]; then ok "GUARDS" "every iam_v2 trigger is still ENABLED (0 disabled)"; else no "GUARDS" "triggers disabled" "$DIS disabled"; fi

echo "===== RESULT: PASS=$PASSN FAIL=$FAILN ====="
[ "$FAILN" = "0" ]
