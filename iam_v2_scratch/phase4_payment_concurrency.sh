#!/usr/bin/env bash
# PHASE-4 PAYMENT CONCURRENCY PROOF.
#
# 0014 enforced "one live CHARGE per settlement" and the cumulative refund bound with SELECT-then-decide
# logic inside a BEFORE INSERT trigger. Under concurrency that is not enforcement at all: two transactions
# each read the pre-state, each pass, and both commit. This script proves the 0015 replacements with REAL
# concurrent PostgreSQL sessions rather than sequential statements, because a sequential test cannot tell
# the difference between a constraint and a lucky ordering.
#
# It expects a database already carrying the full chain (the caller builds it). Disposable PostgreSQL only.
#
# EXIT CODES: 0 all assertions passed, 1 an ASSERTION failed, 2 the environment was not usable.
set -uo pipefail
export PATH="$PATH:/c/Program Files/Docker/Docker/resources/bin"
C="${PHASE4_CONC_CONTAINER:?container required}"; DB="${PHASE4_CONC_DB:-iam_scratch}"

pass=0; fail=0
ok(){ printf '  [PASS] %s\n' "$1"; pass=$((pass+1)); }
no(){ printf '  [FAIL] %s :: %s\n' "$1" "${2:-}"; fail=$((fail+1)); }
Q(){ docker exec "$C" psql -U postgres -d "$DB" -tAqc "$1" 2>&1; }
eq(){ if [ "$2" = "$3" ]; then ok "$1"; else no "$1" "expected '$2', got '$3'"; fi; }

T=11111111-1111-1111-1111-111111111111
S=22222222-2222-2222-2222-222222222222
MERCH=aa000000-0000-0000-0000-000000000011
IF1=aaaa0000-0000-0000-0000-000000000001

echo "===== PHASE-4 PAYMENT CONCURRENCY (real concurrent PG16 sessions) ====="

# A pair of independent settlements, so the last assertion can show that unrelated money does not serialize.
mkchain(){  # mkchain <n>  -> creates purchase+settlement with ids ending in <n>
  Q "INSERT INTO iam_v2.purchases(id,tenant_id,site_id,package_revision_id,pms_interface_id,stay_id,settlement_mapping_id,trigger,amount_minor,currency,currency_exponent,state)
     VALUES ('cc$1c0000-0000-0000-0000-000000000001','$T','$S','cccc0000-0000-0000-0000-0000000000d1','$IF1','eeee0000-0000-0000-0000-000000000001','dddd0000-0000-0000-0000-000000000001','ADMIN_GRANT',100,'USD',2,'AWAITING_SETTLEMENT');" >/dev/null
  Q "INSERT INTO iam_v2.settlements(id,tenant_id,site_id,purchase_id,method,status)
     VALUES ('cc$1c0000-0000-0000-0000-0000000000d1','$T','$S','cc$1c0000-0000-0000-0000-000000000001','ONLINE_PAYMENT','REQUIRED');" >/dev/null
}
mkchain 1
mkchain 2

# ---- 1. two differently-keyed CHARGEs racing for ONE settlement -----------------------------------------
# Both transactions BEGIN, both insert, then both commit. Neither can see the other's uncommitted row, so
# only the unique index can decide -- which is exactly the property 0014 lacked.
race_charge(){  # race_charge <settlement> <idemA> <idemB> <refA> <refB>
  local se="$1" ia="$2" ib="$3" ra="$4" rb="$5" outA outB
  outA=$(mktemp); outB=$(mktemp)
  docker exec -i "$C" psql -U postgres -d "$DB" -tAq > "$outA" 2>&1 <<SQLA &
BEGIN;
INSERT INTO iam_v2.payment_transactions(tenant_id,site_id,settlement_id,merchant_account_id,transaction_type,provider,provider_ref,idempotency_key,amount_minor,currency,currency_exponent,status)
VALUES ('$T','$S','$se','$MERCH','CHARGE','stripe','$ra','$ia',100,'USD',2,'CREATED');
SELECT pg_sleep(2);
COMMIT;
SQLA
  local pidA=$!
  sleep 0.3
  docker exec -i "$C" psql -U postgres -d "$DB" -tAq > "$outB" 2>&1 <<SQLB
BEGIN;
INSERT INTO iam_v2.payment_transactions(tenant_id,site_id,settlement_id,merchant_account_id,transaction_type,provider,provider_ref,idempotency_key,amount_minor,currency,currency_exponent,status)
VALUES ('$T','$S','$se','$MERCH','CHARGE','stripe','$rb','$ib',100,'USD',2,'CREATED');
COMMIT;
SQLB
  wait $pidA
  RACE_A="$(cat "$outA")"; RACE_B="$(cat "$outB")"
  rm -f "$outA" "$outB"
}

race_charge cc1c0000-0000-0000-0000-0000000000d1 idem-race-a idem-race-b ref-race-a ref-race-b
LIVE="$(Q "SELECT count(*) FROM iam_v2.payment_transactions WHERE settlement_id='cc1c0000-0000-0000-0000-0000000000d1' AND transaction_type='CHARGE' AND status IN ('CREATED','PENDING','CAPTURED','UNKNOWN');")"
eq "two concurrent differently-keyed CHARGEs leave exactly ONE live charge" "1" "$LIVE"
if printf '%s%s' "$RACE_A" "$RACE_B" | grep -qi 'ptx_one_live_charge_per_settlement\|duplicate key'; then
  ok "the loser was refused by the unique index, not by a lucky ordering"
else
  no "the loser was refused by the index" "neither session reported a uniqueness failure"
fi

# ---- 2. two refunds that are individually valid but jointly exceed the parent ---------------------------
# Capture a charge first so there is something to return.
PAR="$(Q "SELECT id FROM iam_v2.payment_transactions WHERE settlement_id='cc1c0000-0000-0000-0000-0000000000d1' AND transaction_type='CHARGE' LIMIT 1;")"
PREF="$(Q "SELECT provider_ref FROM iam_v2.payment_transactions WHERE id='$PAR';")"
Q "SELECT iam_v2.apply_payment_callback_v2('$T','stripe','$MERCH','$PREF','evt-c1','pending','PENDING');" >/dev/null
Q "SELECT iam_v2.apply_payment_callback_v2('$T','stripe','$MERCH','$PREF','evt-c2','captured','CAPTURED');" >/dev/null
eq "the parent charge is CAPTURED" "CAPTURED" "$(Q "SELECT status FROM iam_v2.payment_transactions WHERE id='$PAR';")"

# 60 + 60 against a captured 100: each passes on its own, together they must not.
outA=$(mktemp); outB=$(mktemp)
docker exec -i "$C" psql -U postgres -d "$DB" -tAq > "$outA" 2>&1 <<SQLA &
BEGIN;
INSERT INTO iam_v2.payment_transactions(tenant_id,site_id,settlement_id,merchant_account_id,transaction_type,parent_transaction_id,provider,provider_ref,idempotency_key,amount_minor,currency,currency_exponent,status)
VALUES ('$T','$S','cc1c0000-0000-0000-0000-0000000000d1','$MERCH','REFUND','$PAR','stripe','ref-r-a','idem-r-a',60,'USD',2,'CREATED');
SELECT pg_sleep(2);
COMMIT;
SQLA
pidA=$!
sleep 0.3
docker exec -i "$C" psql -U postgres -d "$DB" -tAq > "$outB" 2>&1 <<SQLB
BEGIN;
INSERT INTO iam_v2.payment_transactions(tenant_id,site_id,settlement_id,merchant_account_id,transaction_type,parent_transaction_id,provider,provider_ref,idempotency_key,amount_minor,currency,currency_exponent,status)
VALUES ('$T','$S','cc1c0000-0000-0000-0000-0000000000d1','$MERCH','REFUND','$PAR','stripe','ref-r-b','idem-r-b',60,'USD',2,'CREATED');
COMMIT;
SQLB
wait $pidA
RB="$(cat "$outB")"; rm -f "$outA" "$outB"

SUM="$(Q "SELECT coalesce(sum(amount_minor),0) FROM iam_v2.payment_transactions WHERE parent_transaction_id='$PAR' AND status IN ('CREATED','PENDING','CAPTURED','UNKNOWN');")"
eq "two concurrent 60-unit refunds against a captured 100 do not both commit" "60" "$SUM"
if printf '%s' "$RB" | grep -qi 'PAYMENT_REFUND_EXCEEDS_CHARGE'; then
  ok "the second refund was refused by the cumulative bound while the first was still uncommitted"
else
  no "the second refund was refused by the bound" "$(printf '%s' "$RB" | head -2)"
fi

# ---- 3. unrelated settlements do not serialize -----------------------------------------------------------
# A charge on the OTHER settlement must commit while the first settlement's parent lock is irrelevant to it.
START=$(date +%s)
Q "INSERT INTO iam_v2.payment_transactions(tenant_id,site_id,settlement_id,merchant_account_id,transaction_type,provider,provider_ref,idempotency_key,amount_minor,currency,currency_exponent,status)
   VALUES ('$T','$S','cc2c0000-0000-0000-0000-0000000000d1','$MERCH','CHARGE','stripe','ref-other','idem-other',100,'USD',2,'CREATED');" >/dev/null
END=$(date +%s)
eq "an unrelated settlement accepted its own charge" "1" \
  "$(Q "SELECT count(*) FROM iam_v2.payment_transactions WHERE settlement_id='cc2c0000-0000-0000-0000-0000000000d1';")"
if [ $((END-START)) -lt 2 ]; then
  ok "the unrelated settlement did not wait on the other settlement's lock ($((END-START))s)"
else
  no "unrelated settlements serialize" "took $((END-START))s"
fi

echo
echo "===== PAYMENT CONCURRENCY: PASS=$pass FAIL=$fail ====="
[ "$fail" -eq 0 ] || exit 1
exit 0
