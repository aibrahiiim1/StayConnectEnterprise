#!/usr/bin/env bash
# PHASE-4 MIGRATION 0011 + FINANCIAL-CORE DB GATE.
#
# Self-contained: builds its own disposable PostgreSQL 16 on loopback, reproduces the authoritative
# pre-0011 chain, exercises 0011 UP / raw re-apply / DOWN / DOWN->UP, and then BEHAVIOURALLY proves every
# invariant 0011 adds — including the two that only a real concurrent PostgreSQL can prove: per-interface
# P# allocation under contention, and two reviewers racing for the same posting.
#
# It contacts no appliance, no Production database and no PMS. It sends no financial bytes anywhere: the
# only thing that ever "executes" here is an INSERT that the database is expected to refuse.
#
# EXIT CODES (the CI retry policy depends on these):
#   0  every assertion passed
#   1  an ASSERTION failed — deterministic. CI must NOT retry.
#   2  the disposable infrastructure could not be built — the only retryable condition.
set -uo pipefail
export PATH="$PATH:/c/Program Files/Docker/Docker/resources/bin"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
C="${PHASE4_GATE_CONTAINER:-iamv2-p4gate}"; DB=iam_scratch; PORT="${PHASE4_GATE_PORT:-55433}"
UP="$ROOT/data-plane/migrations/0011_phase4_financial_execution.up.sql"
DOWN="$ROOT/data-plane/migrations/0011_phase4_financial_execution.down.sql"
FIXTURE="$ROOT/iam_v2_scratch/phase4_financial_fixture.sql"

pass=0; fail=0
ok(){ printf '  [PASS] %s\n' "$1"; pass=$((pass+1)); }
no(){ printf '  [FAIL] %s :: %s\n' "$1" "${2:-}"; fail=$((fail+1)); }
Q(){ docker exec "$C" psql -U postgres -d "$DB" -tAqc "$1" 2>&1; }

# rejects <label> <sql> [expected substring]
rejects(){
  local label="$1" sql="$2" want="${3:-}" out
  out="$(Q "$sql")"
  if printf '%s' "$out" | grep -qi 'ERROR'; then
    if [ -z "$want" ] || printf '%s' "$out" | grep -qi -- "$want"; then ok "$label"
    else no "$label" "rejected for the wrong reason: $(printf '%s' "$out" | head -1)"; fi
  else
    no "$label" "WRITE WAS ACCEPTED"
  fi
}
accepts(){
  local label="$1" sql="$2" out
  out="$(Q "$sql")"
  if printf '%s' "$out" | grep -qi 'ERROR'; then no "$label" "$(printf '%s' "$out" | head -1)"; else ok "$label"; fi
}
eq(){ # eq <label> <expected> <actual>
  if [ "$2" = "$3" ]; then ok "$1"; else no "$1" "expected '$2', got '$3'"; fi
}

# full catalog fingerprint: columns + triggers + indexes + constraints
FP="SELECT md5(string_agg(x, E'\n' ORDER BY x)) FROM (
  SELECT 'C '||table_name||'.'||column_name||':'||data_type||':'||is_nullable||':'||coalesce(column_default,'') AS x FROM information_schema.columns WHERE table_schema='iam_v2'
  UNION ALL SELECT 'T '||c.relname||'.'||t.tgname FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='iam_v2' AND NOT t.tgisinternal
  UNION ALL SELECT 'I '||indexname||':'||indexdef FROM pg_indexes WHERE schemaname='iam_v2'
  UNION ALL SELECT 'K '||conname||':'||pg_get_constraintdef(con.oid) FROM pg_constraint con JOIN pg_class c ON c.oid=con.conrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='iam_v2'
  UNION ALL SELECT 'V '||viewname||':'||md5(definition) FROM pg_views WHERE schemaname='iam_v2'
  UNION ALL SELECT 'F '||p.proname||':'||md5(pg_get_functiondef(p.oid)) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='iam_v2'
) s;"

T=11111111-1111-1111-1111-111111111111
S=22222222-2222-2222-2222-222222222222
IF1=aaaa0000-0000-0000-0000-000000000001
IF2=aaaa0000-0000-0000-0000-000000000002
REV_OK=aaaa0000-0000-0000-0000-0000000000d3     # USD/2, GLOBALLY_UNIQUE  (financially onboarded)
REV_NOCUR=aaaa0000-0000-0000-0000-0000000000d4  # folio set, NO currency  (not financially onboarded)
REV_UNSET=aaaa0000-0000-0000-0000-0000000000d1  # folio UNSET
STAY1=eeee0000-0000-0000-0000-000000000001
FOL1=eeee0000-0000-0000-0000-0000000000f0
SET1=99990000-0000-0000-0000-0000000000d1; PUR1=99990000-0000-0000-0000-000000000001  # USD/2
SET2=99990000-0000-0000-0000-0000000000d2; PUR2=99990000-0000-0000-0000-000000000002  # EUR/2
SET3=99990000-0000-0000-0000-0000000000d3; PUR3=99990000-0000-0000-0000-000000000003  # USD/3

# mkposting <id> <settlement> <purchase> <revision> <currency> <exponent> <idem>
mkposting(){
  echo "INSERT INTO iam_v2.pms_postings(id,tenant_id,site_id,pms_interface_id,settlement_id,purchase_id,stay_id,folio_id,posting_interface_revision_id,posting_type,amount_minor,currency,currency_exponent,idempotency_key) VALUES ('$1','$T','$S','$IF1','$2','$3','$STAY1','$FOL1','$4','CHARGE',100,'$5',$6,'$7');"
}
# mkattempt <posting> <attempt_no> <p_number> <rn> <g#>
mkattempt(){
  echo "INSERT INTO iam_v2.posting_attempts(tenant_id,site_id,internal_posting_id,pms_interface_id,attempt_no,p_number,rn,g_number,sent_at) VALUES ('$T','$S','$1','$IF1',$2,'$3','$4','$5',now());"
}

# run the BASELINE financial suite (every invariant that existed before 0011) against whatever chain is
# currently built, in its own row namespace. Its result is folded into this gate's totals, so a baseline
# invariant that 0011 broke fails THIS gate.
run_baseline(){   # run_baseline <label>
  local label="$1" out tail
  out="$(SCRATCH_CONTAINER="$C" SCRATCH_DB="$DB" SCRATCH_PORT_ALLOW="$PORT" \
         SCRATCH_ACK=I_UNDERSTAND_DISPOSABLE bash "$ROOT/iam_v2_scratch/phase4_db_invariants.sh" 2>&1)"
  tail="$(printf '%s' "$out" | grep -E '^===== RESULT' | tail -1)"
  local p f
  p="$(printf '%s' "$tail" | sed -n 's/.*PASS=\([0-9]*\).*/\1/p')"
  f="$(printf '%s' "$tail" | sed -n 's/.*FAIL=\([0-9]*\).*/\1/p')"
  if [ -n "$p" ] && [ "${f:-1}" = "0" ]; then
    ok "$label — every pre-0011 financial invariant still holds ($p/$p)"
  else
    no "$label — pre-0011 financial invariants" "${tail:-suite produced no result}"
    printf '%s\n' "$out" | grep -E '^\s+FAIL' | head -10
  fi
}

cleanup(){ docker rm -f "$C" >/dev/null 2>&1 || true; }
trap cleanup EXIT
cleanup

echo "===== PHASE-4: Migration 0011 + financial-core DB gate (disposable PG16, container=$C port=$PORT) ====="
docker run -d --name "$C" -e POSTGRES_PASSWORD=postgres -e POSTGRES_DB="$DB" -p "127.0.0.1:$PORT:5432" postgres:16-alpine >/dev/null 2>&1 \
  || { echo "INFRA: could not start the disposable container"; exit 2; }
ready=0
for i in $(seq 1 60); do docker exec "$C" psql -U postgres -d "$DB" -tAqc 'select 1' >/dev/null 2>&1 && { ready=1; break; }; sleep 1; done
[ "$ready" = 1 ] || { echo "INFRA: postgres did not become ready"; docker logs "$C" 2>&1 | tail -20; exit 2; }
sleep 1

echo "== build the authoritative pre-0011 chain =="
SCRATCH_CONTAINER="$C" SCRATCH_DB="$DB" SCRATCH_PORT_ALLOW="$PORT" SCRATCH_ACK=I_UNDERSTAND_DISPOSABLE \
  bash "$ROOT/iam_v2_scratch/run.sh" fresh >/dev/null 2>&1 || { echo "INFRA: run.sh fresh failed"; exit 2; }
Q "CREATE TABLE IF NOT EXISTS public.schema_migrations(version text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now());" >/dev/null
docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 < "$ROOT/data-plane/migrations/0009_phase2_commerce.up.sql" >/dev/null 2>&1
Q "INSERT INTO public.schema_migrations(version) VALUES ('0009_phase2_commerce') ON CONFLICT DO NOTHING;" >/dev/null
docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 < "$ROOT/data-plane/migrations/0010_phase3_stay_resolution.up.sql" >/dev/null 2>&1
PRE_TABLES="$(Q "SELECT count(*) FROM information_schema.tables WHERE table_schema='iam_v2';")"
if [ "$PRE_TABLES" != "63" ]; then
  echo "INFRA: pre-0011 chain did not build (iam_v2 tables=$PRE_TABLES, expected 63)"; exit 2
fi
ok "pre-0011 chain reproduces the authoritative baseline (63 iam_v2 tables)"
docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 < "$ROOT/iam_v2_scratch/seed.sql" >/dev/null 2>&1 \
  || { echo "INFRA: seed failed"; exit 2; }
PRE_FP="$(Q "$FP")"; echo "  pre-0011 catalog md5 = $PRE_FP"

echo "== baseline financial invariants on the PRE-0011 chain =="
run_baseline "pre-0011"

# ------------------------------------------------------------------ migration lifecycle
echo "== 0011 lifecycle: UP / raw re-apply / DOWN / DOWN->UP =="
if docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 < "$UP" >/dev/null 2>&1; then
  ok "0011 UP applied"
else
  no "0011 UP applied" "the migration did not apply"; docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 < "$UP" 2>&1 | tail -5
fi
UP_FP="$(Q "$FP")"
[ "$PRE_FP" != "$UP_FP" ] && ok "0011 changed the catalog" || no "0011 changed the catalog" "catalog identical"
eq "0011 recorded in the migration ledger" "1" "$(Q "SELECT count(*) FROM public.schema_migrations WHERE version='0011_phase4_financial_execution';")"

RAW="$(docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 < "$UP" 2>&1)"
printf '%s' "$RAW" | grep -qi "already exists" && ok "raw re-apply ERRORS (as designed) instead of silently mutating" \
  || no "raw re-apply ERRORS" "no 'already exists' error"
eq "catalog unchanged after the failed raw re-apply (rolled back)" "$UP_FP" "$(Q "$FP")"

docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 < "$DOWN" >/dev/null 2>&1 \
  && ok "0011 DOWN applied" || no "0011 DOWN applied" "down migration failed"
eq "DOWN reverses ONLY 0011 (catalog is byte-identical to pre-0011)" "$PRE_FP" "$(Q "$FP")"
eq "DOWN removed the 0011 ledger row" "0" "$(Q "SELECT count(*) FROM public.schema_migrations WHERE version='0011_phase4_financial_execution';")"

docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 < "$UP" >/dev/null 2>&1 \
  && ok "DOWN -> UP re-applies cleanly" || no "DOWN -> UP re-applies cleanly" "re-up failed"
eq "DOWN -> UP produces the SAME schema as the first UP" "$UP_FP" "$(Q "$FP")"


# ------------------------------------------------------------------ nothing pre-existing was weakened
echo "== pre-existing enforcement is intact =="
eq "every iam_v2 trigger is still ENABLED (0 disabled)" "0" \
  "$(Q "SELECT count(*) FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='iam_v2' AND NOT t.tgisinternal AND t.tgenabled <> 'O';")"
for trg in ao_postings charge_gate ao_review ao_pa_events pa_oneway; do
  eq "mg7/mg9 trigger $trg still present" "1" \
    "$(Q "SELECT count(*) FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='iam_v2' AND t.tgname='$trg';")"
done
eq "charge_gate body is UNCHANGED by 0011" "1" \
  "$(Q "SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='iam_v2' AND p.proname='trg_posting_charge_gate' AND pg_get_functiondef(p.oid) LIKE '%FOLIO_STRATEGY_UNSET%' AND pg_get_functiondef(p.oid) LIKE '%POSTING_NOT_ALLOWED%';")"
eq "outbox_one_active partial unique index still present" "1" \
  "$(Q "SELECT count(*) FROM pg_indexes WHERE schemaname='iam_v2' AND indexname='outbox_one_active';")"
eq "0011 fires AFTER charge_gate on pms_postings (folio refusal keeps precedence)" "charge_gate p4_posting_currency_gate" \
  "$(Q "SELECT string_agg(t.tgname,' ' ORDER BY t.tgname) FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='iam_v2' AND c.relname='pms_postings' AND NOT t.tgisinternal AND t.tgtype & 4 = 4;")"
eq "no new EXECUTE granted to PUBLIC on the 0011 controlled functions" "false,false" \
  "$(Q "SELECT has_function_privilege('public','iam_v2.record_posting_review_action(uuid,text,uuid,text,jsonb,int)','EXECUTE')||','||has_function_privilege('public','iam_v2.allocate_p_number(uuid,uuid,uuid)','EXECUTE');")"

# ------------------------------------------------------------------ financial onboarding fixture
echo "== financial onboarding is a REVISION event (revisions stay immutable) =="
docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 < "$FIXTURE" >/dev/null 2>&1 \
  && ok "phase-4 financial fixture built" || { no "phase-4 financial fixture built" "fixture failed"; docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 < "$FIXTURE" 2>&1 | tail -5; }
rejects "an existing revision cannot be given a currency by UPDATE (immutable)" \
  "UPDATE iam_v2.pms_interface_revisions SET financial_base_currency='GBP' WHERE id='$REV_NOCUR';" "immutable"
rejects "a revision cannot carry a currency without an exponent" \
  "INSERT INTO iam_v2.pms_interface_revisions(tenant_id,site_id,pms_interface_id,revision_no,source_timezone,folio_identity_strategy,config,financial_base_currency) VALUES ('$T','$S','$IF1',90,'UTC','GLOBALLY_UNIQUE','{}','GBP');" \
  "pmsrev_financial_currency_pair"
rejects "a non-ISO currency code is rejected" \
  "INSERT INTO iam_v2.pms_interface_revisions(tenant_id,site_id,pms_interface_id,revision_no,source_timezone,folio_identity_strategy,config,financial_base_currency,financial_base_currency_exponent) VALUES ('$T','$S','$IF1',91,'UTC','GLOBALLY_UNIQUE','{}','usd',2);" \
  "pmsrev_financial_currency_iso"
rejects "an out-of-range minor-unit exponent is rejected" \
  "INSERT INTO iam_v2.pms_interface_revisions(tenant_id,site_id,pms_interface_id,revision_no,source_timezone,folio_identity_strategy,config,financial_base_currency,financial_base_currency_exponent) VALUES ('$T','$S','$IF1',92,'UTC','GLOBALLY_UNIQUE','{}','USD',9);" \
  "pmsrev_financial_currency_exponent_range"

# ------------------------------------------------------------------ G2 currency gate
echo "== G2: exact currency equality, no implicit FX =="
accepts "C6 CHARGE accepted when interface, purchase and package currency all match exactly" \
  "$(mkposting c0110000-0000-0000-0000-000000000001 "$SET1" "$PUR1" "$REV_OK" USD 2 p4-ok-1)"
rejects "C36 a revision with NO financial currency blocks the CHARGE (fail-closed onboarding)" \
  "$(mkposting c0110000-0000-0000-0000-000000000002 "$SET1" "$PUR1" "$REV_NOCUR" USD 2 p4-nocur)" \
  "INTERFACE_CURRENCY_NOT_ONBOARDED"
rejects "C6 posting currency <> pinned interface currency is refused" \
  "$(mkposting c0110000-0000-0000-0000-000000000003 "$SET1" "$PUR1" "$REV_OK" EUR 2 p4-cur)" \
  "POSTING_CURRENCY_MISMATCH"
rejects "C6 purchase/package currency <> pinned interface currency is refused" \
  "$(mkposting c0110000-0000-0000-0000-000000000004 "$SET2" "$PUR2" "$REV_OK" USD 2 p4-pur)" \
  "PURCHASE_CURRENCY_MISMATCH"
rejects "C7 a currency EXPONENT mismatch is refused (USD/3 vs USD/2)" \
  "$(mkposting c0110000-0000-0000-0000-000000000005 "$SET3" "$PUR3" "$REV_OK" USD 2 p4-exp)" \
  "PURCHASE_CURRENCY_MISMATCH"
rejects "a posting that states no currency at all is refused" \
  "INSERT INTO iam_v2.pms_postings(id,tenant_id,site_id,pms_interface_id,settlement_id,purchase_id,stay_id,folio_id,posting_interface_revision_id,posting_type,amount_minor,idempotency_key) VALUES ('c0110000-0000-0000-0000-000000000006','$T','$S','$IF1','$SET1','$PUR1','$STAY1','$FOL1','$REV_OK','CHARGE',100,'p4-nocurcol');" \
  "POSTING_CURRENCY_UNSET"
rejects "C5 the folio-UNSET refusal still takes precedence over the currency refusal" \
  "$(mkposting c0110000-0000-0000-0000-000000000007 "$SET1" "$PUR1" "$REV_UNSET" USD 2 p4-unset)" \
  "FOLIO_STRATEGY_UNSET"

# ------------------------------------------------------------------ G1 RN + G#
echo "== G1: verified RN + G# on every attempt =="
POK=c0110000-0000-0000-0000-000000000001
rejects "C4 a NULL RN is refused"        "$(mkattempt "$POK" 1 100 '' 'G1' | sed "s/''/NULL/")" "attempt_rn_verified"
rejects "C4 a blank RN is refused"       "$(mkattempt "$POK" 1 100 '   ' 'G1')"                 "attempt_rn_verified"
rejects "C4 a NULL G# is refused"        "$(mkattempt "$POK" 1 100 '101' '' | sed "s/,''/,NULL/")" "attempt_gnumber_verified"
rejects "C4 a blank G# is refused"       "$(mkattempt "$POK" 1 100 '101' '  ')"                 "attempt_gnumber_verified"
rejects "C11 an RN containing the FIAS field delimiter is refused" "$(mkattempt "$POK" 1 100 '10|1' 'G1')" "attempt_rn_wire_safe"
rejects "C11 a G# containing a control character is refused" \
  "INSERT INTO iam_v2.posting_attempts(tenant_id,site_id,internal_posting_id,pms_interface_id,attempt_no,p_number,rn,g_number,sent_at) VALUES ('$T','$S','$POK','$IF1',1,'100','101',E'G\\x01',now());" \
  "attempt_gnumber_wire_safe"
rejects "C11 a non-numeric P# is refused" "$(mkattempt "$POK" 1 'P-1' '101' 'G1')" "attempt_pnumber_wire_safe"
eq "C3 Room Number is still EVIDENCE, not identity (no unique index or key on rn)" "0" \
  "$(Q "SELECT count(*) FROM pg_indexes WHERE schemaname='iam_v2' AND tablename='posting_attempts' AND indexdef ILIKE '%(rn%' AND indexdef ILIKE '%UNIQUE%';")"

# ------------------------------------------------------------------ P# allocator
echo "== P#: durable, atomic, per-interface =="
eq "first P# on a never-used interface is 1" "1" "$(Q "SELECT iam_v2.allocate_p_number('$T','$S','$IF2');")"
eq "the next P# on the same interface is 2"  "2" "$(Q "SELECT iam_v2.allocate_p_number('$T','$S','$IF2');")"
eq "a different interface has its OWN independent sequence" "1" "$(Q "SELECT iam_v2.allocate_p_number('$T','$S','$IF1');")"
rejects "P# cannot be allocated for an interface outside the tenant/site" \
  "SELECT iam_v2.allocate_p_number('$T','33333333-3333-3333-3333-333333333333','$IF1');" "PNUMBER_INTERFACE_UNKNOWN"
# a rolled-back allocation gives the number back — proving it is transactional, not a clock or a counter file
BEFORE_ROLLBACK="$(Q "SELECT next_p_number FROM iam_v2.pms_interface_pnumber_seq WHERE pms_interface_id='$IF1';")"
Q "BEGIN; SELECT iam_v2.allocate_p_number('$T','$S','$IF1'); ROLLBACK;" >/dev/null
eq "a rolled-back allocation consumes no P# (transactional)" "$BEFORE_ROLLBACK" \
  "$(Q "SELECT next_p_number FROM iam_v2.pms_interface_pnumber_seq WHERE pms_interface_id='$IF1';")"

echo "== P# under REAL concurrency (8 concurrent clients x 25 allocations on one interface) =="
IF2_BEFORE="$(Q "SELECT next_p_number FROM iam_v2.pms_interface_pnumber_seq WHERE pms_interface_id='$IF2';")"
TMPD="$(mktemp -d)"
for w in 1 2 3 4 5 6 7 8; do
  ( docker exec "$C" psql -U postgres -d "$DB" -tAqc \
      "SELECT iam_v2.allocate_p_number('$T','$S','$IF1') FROM generate_series(1,25);" > "$TMPD/w$w.txt" 2>&1 ) &
done
wait
cat "$TMPD"/w*.txt | grep -E '^[0-9]+$' | sort -n > "$TMPD/all.txt"
TOTAL="$(wc -l < "$TMPD/all.txt" | tr -d ' ')"
DISTINCT="$(sort -u "$TMPD/all.txt" | wc -l | tr -d ' ')"
MIN="$(head -1 "$TMPD/all.txt")"; MAX="$(tail -1 "$TMPD/all.txt")"
eq "200 concurrent allocations returned 200 values" "200" "$TOTAL"
eq "every concurrently allocated P# is DISTINCT (no duplicate allocation)" "200" "$DISTINCT"
eq "the concurrent allocations form one gapless range" "200" "$((MAX - MIN + 1))"
eq "the OTHER interface's sequence was untouched by the contention" "$IF2_BEFORE" \
  "$(Q "SELECT next_p_number FROM iam_v2.pms_interface_pnumber_seq WHERE pms_interface_id='$IF2';")"
rm -rf "$TMPD"

# ------------------------------------------------------------------ UNKNOWN safety
echo "== UNKNOWN is terminal without audited review =="
accepts "C13 the first attempt is accepted" "$(mkattempt "$POK" 1 1001 '101' 'G-1')"
rejects "C22 a second attempt while the first is SENDING is refused" "$(mkattempt "$POK" 2 1002 '101' 'G-1')" "ATTEMPT_IN_FLIGHT"
Q "UPDATE iam_v2.posting_attempts SET outcome='UNKNOWN', response_at=now() WHERE internal_posting_id='$POK' AND attempt_no=1;" >/dev/null
rejects "C14 an UNKNOWN attempt cannot be retried automatically" "$(mkattempt "$POK" 2 1002 '101' 'G-1')" "RETRY_REQUIRES_REVIEW"
rejects "C14 nor by skipping an attempt number" "$(mkattempt "$POK" 3 1003 '101' 'G-1')" "ATTEMPT_SEQUENCE"
rejects "C15 UNKNOWN cannot be walked back to SENDING" \
  "UPDATE iam_v2.posting_attempts SET outcome='SENDING' WHERE internal_posting_id='$POK' AND attempt_no=1;" "terminal"
eq "C14 the UNKNOWN posting consumed exactly ONE P#" "1" \
  "$(Q "SELECT count(*) FROM iam_v2.posting_attempts WHERE internal_posting_id='$POK';")"
eq "G3 the read model reports UNKNOWN and awaiting_manual_review" "UNKNOWN|true|true" \
  "$(Q "SELECT execution_state||'|'||has_unknown_history||'|'||awaiting_manual_review FROM iam_v2.posting_execution_state WHERE posting_id='$POK';")"

# ------------------------------------------------------------------ C21 review
echo "== C21: the review ledger has exactly one writer =="
rejects "a direct INSERT into the append-only review ledger is refused" \
  "INSERT INTO iam_v2.posting_review_actions(tenant_id,site_id,posting_id,action,actor,reason) VALUES ('$T','$S','$POK','CONFIRM_POSTED','$T','bypass');" \
  "REVIEW_WRITER_ONLY"
rejects "C17 an invented review action is refused" \
  "SELECT iam_v2.record_posting_review_action('$POK','APPROVE','$T','x');" "REVIEW_ACTION_UNKNOWN"
rejects "C18 a review decision with no reason is refused" \
  "SELECT iam_v2.record_posting_review_action('$POK','CONFIRM_POSTED','$T','   ');" "REVIEW_ACTOR_REASON_REQUIRED"
rejects "C21 a decision made against a stale version is refused" \
  "SELECT iam_v2.record_posting_review_action('$POK','CONFIRM_POSTED','$T','stale reviewer','{}'::jsonb,7);" \
  "REVIEW_VERSION_STALE"

echo "== C21 under REAL concurrency: two reviewers, incompatible decisions, same posting =="
# Reviewer A opens a transaction, commits its decision, and holds the transaction open. Reviewer B starts
# while A still holds the lock: it MUST block, and then MUST be refused once A commits. This is the whole
# claim — a version column that nobody blocks on would let both of these commit.
docker exec -i "$C" psql -U postgres -d "$DB" -tAq > /tmp/p4_rev_a.log 2>&1 <<SQLA &
BEGIN;
SELECT iam_v2.record_posting_review_action('$POK','CONFIRM_NOT_POSTED_RETRY','$T','reviewer A: PMS shows no charge');
SELECT pg_sleep(4);
COMMIT;
SQLA
A_PID=$!
sleep 1
B_OUT="$(docker exec -i "$C" psql -U postgres -d "$DB" -tAq 2>&1 <<SQLB
BEGIN;
SELECT iam_v2.record_posting_review_action('$POK','CREATE_REVERSAL','$T','reviewer B: reverse it');
COMMIT;
SQLB
)"
wait $A_PID
printf '%s' "$B_OUT" | grep -qi 'REVIEW_CONFLICT' && ok "C21 the racing reviewer is refused with REVIEW_CONFLICT" \
  || no "C21 the racing reviewer is refused" "reviewer B was not refused: $(printf '%s' "$B_OUT" | head -2)"
eq "C21 exactly ONE terminal decision is recorded" "CONFIRM_NOT_POSTED_RETRY" \
  "$(Q "SELECT terminal_action FROM iam_v2.posting_review_state WHERE posting_id='$POK';")"
eq "C19 the append-only ledger holds exactly ONE action for that posting" "1" \
  "$(Q "SELECT count(*) FROM iam_v2.posting_review_actions WHERE posting_id='$POK';")"
eq "C20 the retry decision authorized exactly attempt 2" "2" \
  "$(Q "SELECT retry_authorized_attempt_no FROM iam_v2.posting_review_state WHERE posting_id='$POK';")"

echo "== the authorized retry, and only the authorized retry =="
rejects "C14 an attempt number the review did NOT authorize is still refused" "$(mkattempt "$POK" 3 1003 '101' 'G-1')" "ATTEMPT_SEQUENCE"
accepts "C20 the ONE authorized retry attempt is accepted" "$(mkattempt "$POK" 2 1002 '101' 'G-1')"
Q "UPDATE iam_v2.posting_attempts SET outcome='ACKED', pa_as_status='OK', response_at=now() WHERE internal_posting_id='$POK' AND attempt_no=2;" >/dev/null
rejects "C14 no further attempt is possible after the authorization was consumed" "$(mkattempt "$POK" 3 1003 '101' 'G-1')" "RETRY_REQUIRES_REVIEW"
eq "C26 the retry reused the SAME business idempotency key (one posting, two attempts)" "1" \
  "$(Q "SELECT count(DISTINCT idempotency_key) FROM iam_v2.pms_postings WHERE id='$POK';")"
eq "G3 the read model now reports POSTED from the latest attempt" "POSTED|2|true" \
  "$(Q "SELECT execution_state||'|'||latest_attempt_no||'|'||has_unknown_history FROM iam_v2.posting_execution_state WHERE posting_id='$POK';")"
eq "G3 the read model is DERIVED — pms_postings still has no status column" "0" \
  "$(Q "SELECT count(*) FROM information_schema.columns WHERE table_schema='iam_v2' AND table_name='pms_postings' AND column_name IN ('status','state','execution_state');")"

# ------------------------------------------------------------------ ESCALATE is not terminal
echo "== ESCALATE is explicitly NOT a terminal decision =="
accepts "C17 a second posting can be escalated" \
  "$(mkposting c0110000-0000-0000-0000-000000000009 "$SET1" "$PUR1" "$REV_OK" USD 2 p4-ok-9)"
accepts "an ESCALATE with no attempt yet is allowed" \
  "SELECT iam_v2.record_posting_review_action('c0110000-0000-0000-0000-000000000009','ESCALATE','$T','needs finance');"
accepts "ESCALATE can repeat (it decides nothing)" \
  "SELECT iam_v2.record_posting_review_action('c0110000-0000-0000-0000-000000000009','ESCALATE','$T','still needs finance');"
eq "ESCALATE left the posting undecided" "" \
  "$(Q "SELECT coalesce(terminal_action,'') FROM iam_v2.posting_review_state WHERE posting_id='c0110000-0000-0000-0000-000000000009';")"
eq "ESCALATE was counted" "2" \
  "$(Q "SELECT escalation_count FROM iam_v2.posting_review_state WHERE posting_id='c0110000-0000-0000-0000-000000000009';")"
rejects "a terminal decision still needs something to decide about" \
  "SELECT iam_v2.record_posting_review_action('c0110000-0000-0000-0000-000000000009','CONFIRM_POSTED','$T','no attempt exists');" \
  "REVIEW_NOT_APPLICABLE"

# ------------------------------------------------------------------ the baseline suite, after 0011
# On a FRESHLY rebuilt database, so the second pass is judged on its own rows and not on what the first
# pass left behind in append-only tables. If 0011 weakened any pre-0011 financial invariant, it fails here.
echo "== the same baseline invariants AFTER 0011, on a clean rebuild (nothing was weakened) =="
SCRATCH_CONTAINER="$C" SCRATCH_DB="$DB" SCRATCH_PORT_ALLOW="$PORT" SCRATCH_ACK=I_UNDERSTAND_DISPOSABLE \
  bash "$ROOT/iam_v2_scratch/run.sh" fresh >/dev/null 2>&1 || { echo "INFRA: rebuild failed"; exit 2; }
Q "CREATE TABLE IF NOT EXISTS public.schema_migrations(version text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now());" >/dev/null
docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 < "$ROOT/data-plane/migrations/0009_phase2_commerce.up.sql" >/dev/null 2>&1
docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 < "$ROOT/data-plane/migrations/0010_phase3_stay_resolution.up.sql" >/dev/null 2>&1
docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 < "$ROOT/iam_v2_scratch/seed.sql" >/dev/null 2>&1
docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 < "$UP" >/dev/null 2>&1 \
  || { echo "INFRA: 0011 did not re-apply on the rebuild"; exit 2; }
docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 < "$FIXTURE" >/dev/null 2>&1 \
  || { echo "INFRA: fixture did not apply on the rebuild"; exit 2; }
eq "the rebuild carries 0011" "1" "$(Q "SELECT count(*) FROM information_schema.columns WHERE table_schema='iam_v2' AND table_name='pms_interface_revisions' AND column_name='financial_base_currency';")"
run_baseline "post-0011"

echo
echo "===== PHASE-4 0011 GATE: PASS=$pass FAIL=$fail ====="
[ "$fail" -eq 0 ] || exit 1
exit 0
