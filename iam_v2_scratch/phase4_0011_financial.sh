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
UP12="$ROOT/data-plane/migrations/0012_phase4_financial_hardening.up.sql"
UP13="$ROOT/data-plane/migrations/0013_phase4_reversal_ledger.up.sql"
UP14="$ROOT/data-plane/migrations/0014_phase4_payment_settlement.up.sql"
UP16="$ROOT/data-plane/migrations/0016_phase4_payment_coherence.up.sql"
DOWN16="$ROOT/data-plane/migrations/0016_phase4_payment_coherence.down.sql"
UP18="$ROOT/data-plane/migrations/0018_phase4_financial_identity_and_privilege.up.sql"
DOWN18="$ROOT/data-plane/migrations/0018_phase4_financial_identity_and_privilege.down.sql"
UP17="$ROOT/data-plane/migrations/0017_phase4_least_privilege.up.sql"
DOWN17="$ROOT/data-plane/migrations/0017_phase4_least_privilege.down.sql"
UP15="$ROOT/data-plane/migrations/0015_phase4_payment_hardening.up.sql"
DOWN15="$ROOT/data-plane/migrations/0015_phase4_payment_hardening.down.sql"
DOWN14="$ROOT/data-plane/migrations/0014_phase4_payment_settlement.down.sql"
DOWN13="$ROOT/data-plane/migrations/0013_phase4_reversal_ledger.down.sql"
DOWN12="$ROOT/data-plane/migrations/0012_phase4_financial_hardening.down.sql"
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
    if [ -z "$want" ] || printf '%s' "$out" | grep -qiE -- "$want"; then ok "$label"
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

echo "== 0012 hardening lifecycle: UP / DOWN / DOWN->UP =="
docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 < "$UP12" >/dev/null 2>&1 \
  && ok "0012 UP applied" || no "0012 UP applied" "the hardening migration did not apply"
UP12_FP="$(Q "$FP")"
[ "$UP_FP" != "$UP12_FP" ] && ok "0012 changed the catalog" || no "0012 changed the catalog" "catalog identical"
docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 < "$DOWN12" >/dev/null 2>&1 \
  && ok "0012 DOWN applied" || no "0012 DOWN applied" "down failed"
# Assert on the OBJECTS and the BEHAVIOUR rather than a byte-identical catalog hash: pg_get_functiondef
# normalises whitespace differently from the source text, so a hash comparison would be testing the
# formatter, not the rollback.
eq "0012 DOWN removed every 0012 object" "0" \
  "$(Q "SELECT (SELECT count(*) FROM pg_indexes WHERE schemaname='iam_v2' AND indexname='outbox_one_inflight_per_interface')
       + (SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='iam_v2' AND p.proname IN ('p4_posting_lifecycle_gate','p4_attempt_lifecycle_gate','p4_interface_decommission_gate','p4_fias_exponent_gate','p4_interface_freshness_block','p4_posting_freshness_gate','p4_attempt_freshness_gate','p4_consume_retry_authorization','p4_no_programmatic_reversal'))
       + (SELECT count(*) FROM information_schema.columns WHERE table_schema='iam_v2' AND table_name='posting_review_state' AND column_name='retry_authorization_consumed_at');")"
eq "0012 DOWN left every 0011 object intact" "5" \
  "$(Q "SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='iam_v2' AND p.proname IN ('p4_posting_currency_gate','p4_review_writer_only','record_posting_review_action','allocate_p_number','p4_attempt_retry_gate');")"
docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 < "$UP12" >/dev/null 2>&1 \
  && ok "0012 DOWN -> UP re-applies cleanly" || no "0012 DOWN -> UP re-applies cleanly" "re-up failed"
eq "0012 DOWN -> UP produces the SAME schema as the first UP" "$UP12_FP" "$(Q "$FP")"

echo "== 0013 reversal-ledger lifecycle: UP / DOWN / DOWN->UP =="
docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 < "$UP13" >/dev/null 2>&1   && ok "0013 UP applied" || no "0013 UP applied" "the reversal-ledger migration did not apply"
UP13_FP="$(Q "$FP")"
[ "$UP12_FP" != "$UP13_FP" ] && ok "0013 changed the catalog" || no "0013 changed the catalog" "identical"
docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 < "$DOWN13" >/dev/null 2>&1   && ok "0013 DOWN applied" || no "0013 DOWN applied" "down failed"
eq "0013 DOWN restores the 0012 blanket refusal (a MORE restrictive state, never a less safe one)" "1"   "$(Q "SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname=chr(105)||chr(97)||chr(109)||chr(95)||chr(118)||chr(50) AND p.proname='p4_no_programmatic_reversal';")"
docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 < "$UP13" >/dev/null 2>&1   && ok "0013 DOWN -> UP re-applies cleanly" || no "0013 DOWN -> UP re-applies cleanly" "re-up failed"
eq "0013 DOWN -> UP produces the SAME schema as the first UP" "$UP13_FP" "$(Q "$FP")"
eq "0013 is recorded in the migration ledger" "1"   "$(Q "SELECT count(*) FROM public.schema_migrations WHERE version='0013_phase4_reversal_ledger';")"

echo "== 0014 payment/settlement lifecycle: UP / DOWN / DOWN->UP =="
docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 < "$UP14" >/dev/null 2>&1 \
  && ok "0014 UP applied" || no "0014 UP applied" "the payment migration did not apply"
UP14_FP="$(Q "$FP")"
[ "$UP13_FP" != "$UP14_FP" ] && ok "0014 changed the catalog" || no "0014 changed the catalog" "identical"
docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 < "$DOWN14" >/dev/null 2>&1 \
  && ok "0014 DOWN applied" || no "0014 DOWN applied" "down failed"
eq "0014 DOWN restores the exact 0013 schema" "$UP13_FP" "$(Q "$FP")"
docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 < "$UP14" >/dev/null 2>&1 \
  && ok "0014 DOWN -> UP re-applies cleanly" || no "0014 DOWN -> UP re-applies cleanly" "re-up failed"
eq "0014 DOWN -> UP produces the SAME schema as the first UP" "$UP14_FP" "$(Q "$FP")"
eq "0014 is recorded in the migration ledger" "1" \
  "$(Q "SELECT count(*) FROM public.schema_migrations WHERE version='0014_phase4_payment_settlement';")"

echo "== 0015 payment-hardening lifecycle: UP / DOWN / DOWN->UP =="
docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 < "$UP15" >/dev/null 2>&1 \
  && ok "0015 UP applied" || no "0015 UP applied" "the hardening migration did not apply"
UP15_FP="$(Q "$FP")"
[ "$UP14_FP" != "$UP15_FP" ] && ok "0015 changed the catalog" || no "0015 changed the catalog" "identical"
docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 < "$DOWN15" >/dev/null 2>&1 \
  && ok "0015 DOWN applied" || no "0015 DOWN applied" "down failed"
# Object-level, not a catalog hash: pg_get_functiondef renormalises whitespace, so a hash comparison
# would be testing the formatter rather than the rollback.
eq "0015 DOWN removed every 0015 object" "0"   "$(Q "SELECT (SELECT count(*) FROM pg_indexes WHERE schemaname='iam_v2' AND indexname IN ('ptx_one_live_charge_per_settlement','ptx_event_provider_identity','ptx_provider_txn_ref_identity'))
       + (SELECT count(*) FROM information_schema.columns WHERE table_schema='iam_v2' AND table_name='payment_transactions' AND column_name IN ('provider_txn_ref','intent_created_at'))
       + (SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='iam_v2' AND p.proname IN ('apply_payment_callback_v2','p4_callback_evidence_safe','ns_payment_parent'));")"
eq "0015 DOWN restored the 0014 callback entry point" "1"   "$(Q "SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='iam_v2' AND p.proname='apply_payment_callback';")"
docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 < "$UP15" >/dev/null 2>&1 \
  && ok "0015 DOWN -> UP re-applies cleanly" || no "0015 DOWN -> UP re-applies cleanly" "re-up failed"
eq "0015 DOWN -> UP produces the SAME schema as the first UP" "$UP15_FP" "$(Q "$FP")"
eq "0015 is recorded in the migration ledger" "1" \
  "$(Q "SELECT count(*) FROM public.schema_migrations WHERE version='0015_phase4_payment_hardening';")"
eq "the duplicate-charge bound is now a UNIQUE INDEX, not a trigger count()" "1" \
  "$(Q "SELECT count(*) FROM pg_indexes WHERE schemaname='iam_v2' AND indexname='ptx_one_live_charge_per_settlement';")"
eq "provider events are deduplicated at the PROVIDER identity" "1" \
  "$(Q "SELECT count(*) FROM pg_indexes WHERE schemaname='iam_v2' AND indexname='ptx_event_provider_identity';")"
eq "the 0014 caller-nominated callback entry point is GONE" "0" \
  "$(Q "SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='iam_v2' AND p.proname='apply_payment_callback';")"
eq "0012 is recorded in the migration ledger" "1" \
  "$(Q "SELECT count(*) FROM public.schema_migrations WHERE version='0012_phase4_financial_hardening';")"


# ------------------------------------------------------------------ 0016 payment coherence
echo "== 0016 payment coherence =="
docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 < "$UP16" >/dev/null 2>&1 \
  && ok "0016 applies" || no "0016 applies" "up failed"
UP16_FP="$(Q "$FP")"
eq "the durable execution boundary exists" "1" \
  "$(Q "SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='iam_v2' AND p.proname='begin_payment_execution';")"
eq "the settlement admission gate exists" "1" \
  "$(Q "SELECT count(*) FROM pg_trigger WHERE tgname='p4_payment_admission_gate' AND NOT tgisinternal;")"

docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 < "$DOWN16" >/dev/null 2>&1 \
  && ok "0016 DOWN applies" || no "0016 DOWN applies" "down failed"
eq "0016 DOWN removed the durable execution boundary" "0" \
  "$(Q "SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='iam_v2' AND p.proname='begin_payment_execution';")"
docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 < "$UP16" >/dev/null 2>&1 \
  && ok "0016 DOWN -> UP re-applies cleanly" || no "0016 DOWN -> UP re-applies cleanly" "re-up failed"
eq "0016 DOWN -> UP produces the SAME schema as the first UP" "$UP16_FP" "$(Q "$FP")"
eq "0016 is recorded in the migration ledger" "1" \
  "$(Q "SELECT count(*) FROM public.schema_migrations WHERE version='0016_phase4_payment_coherence';")"

# ------------------------------------------------------------------ 0017 least privilege
echo "== 0017 least privilege =="
docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 < "$UP17" >/dev/null 2>&1 \
  && ok "0017 applies" || no "0017 applies" "up failed"
eq "the three financial roles exist" "3" \
  "$(Q "SELECT count(*) FROM pg_roles WHERE rolname IN ('sc_payment_runtime','sc_financial_operator','sc_financial_readonly');")"
eq "PUBLIC holds EXECUTE on no SECURITY DEFINER function" "0" \
  "$(Q "SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='iam_v2' AND p.prosecdef AND has_function_privilege('public', p.oid, 'EXECUTE');")"
docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 < "$DOWN17" >/dev/null 2>&1 \
  && ok "0017 DOWN applies" || no "0017 DOWN applies" "down failed"
eq "0017 DOWN removed the financial roles" "0" \
  "$(Q "SELECT count(*) FROM pg_roles WHERE rolname IN ('sc_payment_runtime','sc_financial_operator','sc_financial_readonly');")"
docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 < "$UP17" >/dev/null 2>&1 \
  && ok "0017 DOWN -> UP re-applies cleanly" || no "0017 DOWN -> UP re-applies cleanly" "re-up failed"
eq "0017 is recorded in the migration ledger" "1" \
  "$(Q "SELECT count(*) FROM public.schema_migrations WHERE version='0017_phase4_least_privilege';")"


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
eq "every phase-4 posting gate fires AFTER charge_gate (the folio refusal keeps precedence)" "charge_gate" \
  "$(Q "SELECT min(t.tgname) FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='iam_v2' AND c.relname='pms_postings' AND NOT t.tgisinternal AND t.tgtype & 4 = 4;")"
eq "the freshness gate fires LAST, so onboarding and currency reasons win over it" "p4_zz_posting_freshness_gate" \
  "$(Q "SELECT max(t.tgname) FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='iam_v2' AND c.relname='pms_postings' AND NOT t.tgisinternal AND t.tgtype & 4 = 4;")"
eq "no new EXECUTE granted to PUBLIC on the 0011 controlled functions" "false,false" \
  "$(Q "SELECT has_function_privilege('public','iam_v2.record_posting_review_action(uuid,text,uuid,text,jsonb,int,bigint)','EXECUTE')||','||has_function_privilege('public','iam_v2.allocate_p_number(uuid,uuid,uuid)','EXECUTE');")"

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

echo "== 0012: contract lifecycle, freshness axes, wire bounds and reversal =="
IFSTATE(){ Q "UPDATE iam_v2.pms_interfaces SET lifecycle_state='$1' WHERE id='$IF1';" >/dev/null; }
RT(){ Q "UPDATE iam_v2.pms_interface_runtime SET $1, updated_at=now() WHERE pms_interface_id='$IF1';" >/dev/null; }
RT_OK(){ RT "transport_status='CONNECTED', last_heartbeat_at=now(), continuity_status='CONTINUOUS', last_valid_event_at=now(), sync_status='IN_SYNC', last_complete_sync_at=now(), resync_generation_seq=0, published_resync_generation=0"; }

# 0013 corrects 0012: the contract REQUIRES the ledger row (sections 15 and 16) and FORBIDS the sender
# (section 9a rule 5, Gate 3B). Both halves are asserted here.
rejects "C25 a reversal ledger row cannot be written outside the audited CREATE_REVERSAL action" \
  "INSERT INTO iam_v2.pms_postings(tenant_id,site_id,pms_interface_id,settlement_id,purchase_id,stay_id,folio_id,posting_interface_revision_id,posting_type,reverses_posting_id,amount_minor,currency,currency_exponent,idempotency_key) VALUES ('$T','$S','$IF1','$SET1','$PUR1','$STAY1','$FOL1','$REV_OK','REVERSAL',gen_random_uuid(),100,'USD',2,'p4-revdirect');" \
  "REVERSAL_WRITER_ONLY"

rejects "C7 the protel-fias posting path refuses any exponent but 2" \
  "$(mkposting c0120000-0000-0000-0000-000000000011 "$SET3" "$PUR3" "$REV_OK" USD 2 p4-exp2 | sed "s/,'USD',2,/,'USD',3,/")" \
  "FIAS_EXPONENT_UNSUPPORTED"

IFSTATE AUTH_DISABLED
accepts "C24 AUTH_DISABLED still permits posting (it disables guest AUTH, not the folio)" \
  "$(mkposting c0120000-0000-0000-0000-000000000012 "$SET1" "$PUR1" "$REV_OK" USD 2 p4-authdis)"
IFSTATE DRAINING
rejects "C24 DRAINING refuses NEW financial work" \
  "$(mkposting c0120000-0000-0000-0000-000000000013 "$SET1" "$PUR1" "$REV_OK" USD 2 p4-drain)" \
  "INTERFACE_NOT_ACCEPTING_WORK"
IFSTATE ACTIVE

RT "transport_status='DISCONNECTED'"
rejects "C32 axis 1: a DISCONNECTED interface fails closed before any financial work" \
  "$(mkposting c0120000-0000-0000-0000-000000000014 "$SET1" "$PUR1" "$REV_OK" USD 2 p4-ax1)" "INTERFACE_NOT_FRESH"
RT_OK
RT "last_heartbeat_at=now()-interval '30 minutes'"
rejects "C32 axis 1: a stale heartbeat fails closed" \
  "$(mkposting c0120000-0000-0000-0000-000000000015 "$SET1" "$PUR1" "$REV_OK" USD 2 p4-ax1b)" "TRANSPORT_HEARTBEAT_STALE"
RT_OK
RT "continuity_status='GAP_DETECTED'"
rejects "C32 axis 2: a discontinuous feed fails closed" \
  "$(mkposting c0120000-0000-0000-0000-000000000016 "$SET1" "$PUR1" "$REV_OK" USD 2 p4-ax2)" "CONTINUITY_GAP_DETECTED"
RT_OK
RT "sync_status='RESYNC_REQUIRED'"
rejects "C32 axis 3: an out-of-sync interface fails closed" \
  "$(mkposting c0120000-0000-0000-0000-000000000017 "$SET1" "$PUR1" "$REV_OK" USD 2 p4-ax3)" "SYNC_RESYNC_REQUIRED"
RT_OK
RT "resync_generation_seq=2, published_resync_generation=1"
rejects "C32 axis 4: a part-published resync generation fails closed" \
  "$(mkposting c0120000-0000-0000-0000-000000000018 "$SET1" "$PUR1" "$REV_OK" USD 2 p4-ax4)" "PIN_RESYNC_IN_FLIGHT"
RT_OK
accepts "C32 with all four axes green the charge is accepted again" \
  "$(mkposting c0120000-0000-0000-0000-000000000019 "$SET1" "$PUR1" "$REV_OK" USD 2 p4-axok)"

echo "== C22 per-interface lane serialization, across DIFFERENT postings =="
Q "INSERT INTO iam_v2.posting_outbox(tenant_id,site_id,pms_interface_id,posting_id,state) VALUES ('$T','$S','$IF1','c0120000-0000-0000-0000-000000000012','IN_FLIGHT');" >/dev/null
rejects "C22 a SECOND different posting cannot be IN_FLIGHT on the same interface" \
  "INSERT INTO iam_v2.posting_outbox(tenant_id,site_id,pms_interface_id,posting_id,state) VALUES ('$T','$S','$IF1','c0120000-0000-0000-0000-000000000019','IN_FLIGHT');" \
  "outbox_one_inflight_per_interface"
accepts "C23 a DIFFERENT interface may be in flight at the same time" \
  "INSERT INTO iam_v2.pms_postings(id,tenant_id,site_id,pms_interface_id,settlement_id,purchase_id,stay_id,folio_id,posting_interface_revision_id,posting_type,amount_minor,currency,currency_exponent,idempotency_key) VALUES ('c0120000-0000-0000-0000-00000000001a','$T','$S','$IF2','99990000-0000-0000-0000-0000000000d4','99990000-0000-0000-0000-000000000004','eeee0000-0000-0000-0000-000000000002','eeee0000-0000-0000-0000-0000000000f2','aaaa0000-0000-0000-0000-0000000002d1','CHARGE',100,'EUR',2,'p4-if2');
   INSERT INTO iam_v2.posting_outbox(tenant_id,site_id,pms_interface_id,posting_id,state) VALUES ('$T','$S','$IF2','c0120000-0000-0000-0000-00000000001a','IN_FLIGHT');"
Q "UPDATE iam_v2.posting_outbox SET state='DONE' WHERE pms_interface_id IN ('$IF1','$IF2');" >/dev/null

echo "== 0014: online payment and settlement execution =="
MERCH=aa000000-0000-0000-0000-000000000011
PPUR=77770000-0000-0000-0000-000000000001
PSET=77770000-0000-0000-0000-0000000000d1
Q "INSERT INTO iam_v2.purchases(id,tenant_id,site_id,package_revision_id,pms_interface_id,stay_id,settlement_mapping_id,trigger,amount_minor,currency,currency_exponent,state) VALUES ('$PPUR','$T','$S','cccc0000-0000-0000-0000-0000000000d1','$IF1','$STAY1','dddd0000-0000-0000-0000-000000000001','ADMIN_GRANT',100,'USD',2,'AWAITING_SETTLEMENT');" >/dev/null
Q "INSERT INTO iam_v2.settlements(id,tenant_id,site_id,purchase_id,method,status) VALUES ('$PSET','$T','$S','$PPUR','ONLINE_PAYMENT','REQUIRED');" >/dev/null
mkpay(){ echo "INSERT INTO iam_v2.payment_transactions(id,tenant_id,site_id,settlement_id,merchant_account_id,transaction_type,parent_transaction_id,provider,provider_ref,idempotency_key,amount_minor,currency,currency_exponent,status) VALUES ('$1','$T','$S','$PSET','$MERCH','$2',$3,'stripe','$4','$5',$6,'$7',$8,'$9');"; }
PARENT="'b0000000-0000-0000-0000-000000000002'"

rejects "C28 a client-chosen amount is refused; money comes from the pinned Purchase" \
  "$(mkpay b0000000-0000-0000-0000-000000000001 CHARGE NULL ref1 idem1 999 USD 2 CREATED)" \
  "PAYMENT_AMOUNT_NOT_SERVER_PINNED"
accepts "C28 the server-pinned amount is accepted" \
  "$(mkpay b0000000-0000-0000-0000-000000000002 CHARGE NULL ref2 idem2 100 USD 2 CREATED)"
rejects "C26 a second live CHARGE on one settlement is refused (0015: by the unique index)" \
  "$(mkpay b0000000-0000-0000-0000-000000000003 CHARGE NULL ref3 idem3 100 USD 2 CREATED)" \
  "ptx_one_live_charge_per_settlement"
rejects "C26 the payment status machine refuses CREATED -> CAPTURED" \
  "UPDATE iam_v2.payment_transactions SET status='CAPTURED' WHERE id=$PARENT;" \
  "PAYMENT_STATUS_TRANSITION"
# 0015: the caller no longer nominates the transaction. It presents what the PROVIDER gave it and the
# database resolves the row itself.
# The CHARGE machine is EXACT: the only edge out of CREATED is PENDING (0016). The intent behind 'ref2' is
# genuinely CREATED at this point, so each widening is attempted against a real row rather than asserted.
REF2="$(Q "SELECT id FROM iam_v2.payment_transactions WHERE tenant_id='$T' AND provider_ref='ref2';")"
for st in CAPTURED FAILED CANCELLED EXPIRED UNKNOWN; do
  out="$(Q "UPDATE iam_v2.payment_transactions SET status='$st' WHERE id='$REF2';")"
  if printf '%s' "$out" | grep -q 'PAYMENT_STATUS_TRANSITION'; then
    ok "CREATED -> $st is refused; the only edge out of CREATED is PENDING"
  else
    no "CREATED -> $st is refused" "$(printf '%s' "$out" | head -1)"
  fi
done

# The durable execution boundary, which is what makes the settlement executable at all. Before 0016 the
# callback invented IN_PROGRESS for itself; now the boundary must be crossed BEFORE a provider is contacted,
# and a callback against a settlement that never began cannot settle anything.
eq "the durable execution boundary moves the intent and the settlement together" "EXECUTING"   "$(Q "SELECT iam_v2.begin_payment_execution('$REF2');")"
eq "the settlement is IN_PROGRESS once execution began" "IN_PROGRESS"   "$(Q "SELECT se.status FROM iam_v2.settlements se JOIN iam_v2.payment_transactions t ON t.settlement_id=se.id WHERE t.id='$REF2';")"
eq "re-entering the execution boundary is idempotent, not a second attempt" "ALREADY_EXECUTING"   "$(Q "SELECT iam_v2.begin_payment_execution('$REF2');")"

eq "C26 the capture callback is APPLIED" "APPLIED" \
  "$(Q "SELECT iam_v2.apply_payment_callback_v2('$T','stripe','$MERCH','ref2','evt-2','captured','CAPTURED');")"
eq "C26 a REPLAYED callback is a DUPLICATE and changes nothing" "DUPLICATE" \
  "$(Q "SELECT iam_v2.apply_payment_callback_v2('$T','stripe','$MERCH','ref2','evt-2','captured','CAPTURED');")"
rejects "C26 a callback that correlates to nothing is refused, not guessed" \
  "SELECT iam_v2.apply_payment_callback_v2('$T','stripe','$MERCH','no-such-ref','evt-9','captured','CAPTURED');" \
  "CALLBACK_UNCORRELATED"
rejects "a raw provider payload cannot enter the append-only callback ledger" \
  "SELECT iam_v2.apply_payment_callback_v2('$T','stripe','$MERCH','ref2','evt-10','x','PENDING',NULL,'{\"raw_body\":\"{}\"}'::jsonb);" \
  "CALLBACK_EVIDENCE_UNSAFE"
eq "the settlement reached SETTLED on the captured charge" "SETTLED" \
  "$(Q "SELECT status FROM iam_v2.settlements WHERE id='$PSET';")"
rejects "C26 a terminal payment status cannot move again" \
  "UPDATE iam_v2.payment_transactions SET status='FAILED' WHERE id=$PARENT;" \
  "PAYMENT_STATUS_TERMINAL"
rejects "a refund larger than the captured parent is refused" \
  "$(mkpay b0000000-0000-0000-0000-000000000004 REFUND $PARENT ref4 idem4 101 USD 2 CREATED)" \
  "PAYMENT_REFUND_EXCEEDS_CHARGE"
accepts "a partial refund within the bound is accepted" \
  "$(mkpay b0000000-0000-0000-0000-000000000005 REFUND $PARENT ref5 idem5 40 USD 2 CREATED)"
rejects "the CUMULATIVE refund bound is enforced" \
  "$(mkpay b0000000-0000-0000-0000-000000000006 REFUND $PARENT ref6 idem6 61 USD 2 CREATED)" \
  "PAYMENT_REFUND_EXCEEDS_CHARGE"
rejects "a refund in another currency is refused (no implicit FX)" \
  "$(mkpay b0000000-0000-0000-0000-000000000007 REFUND $PARENT ref7 idem7 10 EUR 2 CREATED)" \
  "PAYMENT_PARENT_CURRENCY_MISMATCH"
rejects "the PMS rail cannot take a provider charge" \
  "INSERT INTO iam_v2.payment_transactions(tenant_id,site_id,settlement_id,merchant_account_id,transaction_type,provider,provider_ref,idempotency_key,amount_minor,currency,currency_exponent,status) VALUES ('$T','$S','$SET1','$MERCH','CHARGE','stripe','refX','idemX',100,'USD',2,'CREATED');" \
  "PAYMENT_WRONG_RAIL"
rejects "a PMS settlement cannot be declared SETTLED by an unapproved transition" \
  "UPDATE iam_v2.settlements SET status='SETTLED' WHERE id='$SET1';" \
  "SETTLEMENT_TRANSITION"
rejects "0015: REQUIRED -> FAILED directly is no longer permitted (section 16)" \
  "UPDATE iam_v2.settlements SET status='FAILED' WHERE id='$SET1';" "SETTLEMENT_TRANSITION"
rejects "0015: REQUIRED -> MANUAL_REVIEW directly is no longer permitted (section 16)" \
  "UPDATE iam_v2.settlements SET status='MANUAL_REVIEW' WHERE id='$SET1';" "SETTLEMENT_TRANSITION"
rejects "0015/0016: a payment transaction cannot be inserted already CAPTURED (either gate is the right answer)" \
  "$(mkpay b0000000-0000-0000-0000-00000000000f CHARGE NULL refZ idemZ 100 USD 2 CAPTURED)" \
  "PAYMENT_MUST_START_CREATED|PAYMENT_SETTLEMENT_CLOSED"
rejects "the provider callback ledger is append-only" \
  "UPDATE iam_v2.payment_transaction_events SET event_type='TAMPERED';" "append-only"

echo "== C18/C20 review: evidence, the action/state matrix, single-use authorization =="
Q "INSERT INTO iam_v2.posting_attempts(tenant_id,site_id,internal_posting_id,pms_interface_id,attempt_no,p_number,rn,g_number,sent_at,outcome,pa_as_status,response_at) VALUES ('$T','$S','c0120000-0000-0000-0000-000000000019','$IF1',1,'7001','1421','5',now(),'ACKED','OK',now());" >/dev/null
rejects "C18 a terminal decision with no evidence is refused" \
  "SELECT iam_v2.record_posting_review_action('c0120000-0000-0000-0000-000000000019','CONFIRM_POSTED','$T','looks fine');" \
  "REVIEW_EVIDENCE_REQUIRED"
rejects "C20 a charge the PMS ACKed OK can NEVER be authorized for retry" \
  "SELECT iam_v2.record_posting_review_action('c0120000-0000-0000-0000-000000000019','CONFIRM_NOT_POSTED_RETRY','$T','operator believes it failed',jsonb_build_object('folio','verified'));" \
  "REVIEW_RETRY_REFUSED"
accepts "C18 a terminal decision WITH evidence is accepted" \
  "SELECT iam_v2.record_posting_review_action('c0120000-0000-0000-0000-000000000019','CONFIRM_POSTED','$T','folio verified',jsonb_build_object('folio','verified'));"


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
  "SELECT iam_v2.record_posting_review_action('$POK','APPROVE','$T','x',jsonb_build_object('folio','verified'));" "REVIEW_ACTION_UNKNOWN"
rejects "C18 a review decision with no reason is refused" \
  "SELECT iam_v2.record_posting_review_action('$POK','CONFIRM_POSTED','$T','   ',jsonb_build_object('folio','verified'));" "REVIEW_ACTOR_REASON_REQUIRED"
rejects "C21 a decision made against a stale version is refused" \
  "SELECT iam_v2.record_posting_review_action('$POK','CONFIRM_POSTED','$T','stale reviewer',jsonb_build_object('folio','verified'),7);" \
  "REVIEW_VERSION_STALE"

echo "== C21 under REAL concurrency: two reviewers, incompatible decisions, same posting =="
# Reviewer A opens a transaction, commits its decision, and holds the transaction open. Reviewer B starts
# while A still holds the lock: it MUST block, and then MUST be refused once A commits. This is the whole
# claim — a version column that nobody blocks on would let both of these commit.
docker exec -i "$C" psql -U postgres -d "$DB" -tAq > /tmp/p4_rev_a.log 2>&1 <<SQLA &
BEGIN;
SELECT iam_v2.record_posting_review_action('$POK','CONFIRM_NOT_POSTED_RETRY','$T','reviewer A: PMS shows no charge',jsonb_build_object('folio','verified'));
SELECT pg_sleep(4);
COMMIT;
SQLA
A_PID=$!
sleep 1
B_OUT="$(docker exec -i "$C" psql -U postgres -d "$DB" -tAq 2>&1 <<SQLB
BEGIN;
SELECT iam_v2.record_posting_review_action('$POK','CREATE_REVERSAL','$T','reviewer B: reverse it',jsonb_build_object('folio','verified'));
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
eq "C20 the retry authorization was CONSUMED by the attempt it authorized" "t" \
  "$(Q "SELECT (retry_authorized_attempt_no IS NULL AND retry_authorization_consumed_at IS NOT NULL) FROM iam_v2.posting_review_state WHERE posting_id='$POK';")"
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
  "SELECT iam_v2.record_posting_review_action('c0110000-0000-0000-0000-000000000009','CONFIRM_POSTED','$T','no attempt exists',jsonb_build_object('folio','verified'));" \
  "REVIEW_NOT_APPLICABLE"

# ------------------------------------------------------------------ 0018 financial identity + privilege
if docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 < "$UP18" >/dev/null 2>&1; then ok "0018 applies"; else no "0018 applies" "$(docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 < "$UP18" 2>&1 | tail -2 | tr "
" " ")"; fi
UP18_FP="$(Q "$FP")"
# Seed the account directly rather than reloading the whole fixture: the fixture is not idempotent, and
# re-running it here would duplicate the immutable PMS revisions it creates.
# The backfill records historical accounts DISABLED and non-default, which is deliberate: activating one is
# an operator decision, not a migration side effect. The gate performs that decision explicitly here, which
# is also what proves the backfilled row is a real, usable account rather than a placeholder.
ACCTSQL="INSERT INTO iam_v2.payment_provider_accounts(id,tenant_id,site_id,provider,merchant_account_ref,status,is_default) VALUES ('$MERCH','$T','$S','stripe','acct_fixture_0011','ACTIVE',true) ON CONFLICT (id) DO UPDATE SET status='ACTIVE', is_default=true;"
Q "$ACCTSQL" >/dev/null
# A dedicated ONLINE_PAYMENT settlement for the identity assertions. Reusing an earlier one would make the
# result depend on what previous assertions left behind, and an INSERT ... SELECT that matches no rows
# succeeds -- so a stale fixture turns a negative test into a vacuous pass.
I18PUR=78780000-0000-0000-0000-000000000001
I18SET=78780000-0000-0000-0000-0000000000d1
Q "INSERT INTO iam_v2.purchases(id,tenant_id,site_id,package_revision_id,pms_interface_id,stay_id,settlement_mapping_id,trigger,amount_minor,currency,currency_exponent,state) VALUES ('$I18PUR','$T','$S','cccc0000-0000-0000-0000-0000000000d1','$IF1','$STAY1','dddd0000-0000-0000-0000-000000000001','ADMIN_GRANT',100,'USD',2,'AWAITING_SETTLEMENT');" >/dev/null
Q "INSERT INTO iam_v2.settlements(id,tenant_id,site_id,purchase_id,method,status) VALUES ('$I18SET','$T','$S','$I18PUR','ONLINE_PAYMENT','REQUIRED');" >/dev/null
eq "the 0018 identity fixture settlement exists" "1"   "$(Q "SELECT count(*) FROM iam_v2.settlements WHERE id='$I18SET';")"

eq "the payment identity table exists" "1" \
  "$(Q "SELECT count(*) FROM information_schema.tables WHERE table_schema='iam_v2' AND table_name='payment_provider_accounts';")"
rejects "a placeholder provider identity cannot be configured" \
  "INSERT INTO iam_v2.payment_provider_accounts(tenant_id,site_id,provider,merchant_account_ref,status)
   VALUES ('$T','$S','none','x','ACTIVE');" "violates check constraint"
rejects "an unconfigured merchant account cannot appear in the financial record" \
  "INSERT INTO iam_v2.payment_transactions(tenant_id,site_id,settlement_id,merchant_account_id,transaction_type,provider,provider_ref,idempotency_key,amount_minor,currency,currency_exponent,status)
   SELECT '$T','$S',id,gen_random_uuid(),'CHARGE','stripe','ref-unconf','idem-unconf',100,'USD',2,'CREATED'
     FROM iam_v2.settlements WHERE id='$I18SET';" "violates foreign key|PAYMENT_ACCOUNT_UNKNOWN"
rejects "a payment cannot name a provider its configured account does not use" \
  "INSERT INTO iam_v2.payment_transactions(tenant_id,site_id,settlement_id,merchant_account_id,transaction_type,provider,provider_ref,idempotency_key,amount_minor,currency,currency_exponent,status)
   SELECT '$T','$S',id,'$MERCH','CHARGE','someone_else','ref-mm','idem-mm',100,'USD',2,'CREATED'
     FROM iam_v2.settlements WHERE id='$I18SET';" "PAYMENT_PROVIDER_MISMATCH"
eq "there is at most one default account per site" "1" \
  "$(Q "SELECT count(*) FROM pg_indexes WHERE schemaname='iam_v2' AND indexname='ppa_one_default_per_site';")"
eq "resolution returns the configured account" "1" \
  "$(Q "SELECT count(*) FROM iam_v2.p4_resolve_payment_account('$T','$S') WHERE provider='stripe';")"
eq "the controlled grant functions exist" "3" \
  "$(Q "SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='iam_v2' AND p.proname IN ('p4_insert_entitlement','p4_mark_purchase_granted','p4_terminate_live_entitlement_for_subject');")"
eq "every phase-4 enforcement trigger runs as the owner, not the caller" "0" \
  "$(Q "SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='iam_v2' AND p.proname IN ('p4_payment_admission_gate','p4_payment_identity_gate') AND NOT p.prosecdef;")"
eq "reporting sees only the redacted views" "3" \
  "$(Q "SELECT count(*) FROM information_schema.views WHERE table_schema='iam_v2' AND table_name IN ('v_financial_payments','v_financial_settlements','v_financial_review_queue');")"
eq "the redacted payment view exposes no correlation handle" "0" \
  "$(Q "SELECT count(*) FROM information_schema.columns WHERE table_schema='iam_v2' AND table_name='v_financial_payments' AND column_name IN ('provider_ref','idempotency_key','provider_txn_ref');")"

docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 < "$DOWN18" >/dev/null 2>&1 \
  && ok "0018 DOWN applies" || no "0018 DOWN applies" "down failed"
eq "0018 DOWN removed the identity table" "0" \
  "$(Q "SELECT count(*) FROM information_schema.tables WHERE table_schema='iam_v2' AND table_name='payment_provider_accounts';")"
docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 < "$UP18" >/dev/null 2>&1 \
  && ok "0018 DOWN -> UP re-applies cleanly" || no "0018 DOWN -> UP re-applies cleanly" "re-up failed"
eq "0018 DOWN -> UP produces the SAME schema as the first UP" "$UP18_FP" "$(Q "$FP")"
Q "$ACCTSQL" >/dev/null
eq "0018 is recorded in the migration ledger" "1" \
  "$(Q "SELECT count(*) FROM public.schema_migrations WHERE version='0018_phase4_financial_identity_and_privilege';")"

# ------------------------------------------------------------------ 0019..0023 recovery, restore, trust boundary
# 0019 and 0020 are applied here rather than in a block of their own: 0021-0023 build directly on the
# recovery tables and the observability column, and a gate that tested them against a chain missing their
# dependencies would be measuring the dependency error rather than the migrations.
echo "== 0019/0020 recovery and observability =="
for M in 0019_phase4_financial_recovery 0020_phase4_financial_observability; do
  if docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1        < "$ROOT/data-plane/migrations/$M.up.sql" >/dev/null 2>&1; then ok "$M applies"
  else no "$M applies" "up failed"; fi
done
eq "the financial epoch table exists" "1"   "$(Q "SELECT count(*) FROM information_schema.tables WHERE table_schema='iam_v2' AND table_name='financial_epochs';")"

echo "== 0021/0022/0023 trust boundary, recovery closure and restore generation =="
for M in 0021_phase4_trust_boundary 0022_phase4_recovery_closure 0023_phase4_restore_generation; do
  if docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 \
       < "$ROOT/data-plane/migrations/$M.up.sql" >/dev/null 2>&1; then ok "$M applies"
  else no "$M applies" "$(docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 < "$ROOT/data-plane/migrations/$M.up.sql" 2>&1 | tail -2 | tr '\n' ' ')"; fi
done
UP23_FP="$(Q "$FP")"

eq "the high-level paid-grant operation exists" "1" \
  "$(Q "SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='iam_v2' AND p.proname='p4_grant_paid_entitlement';")"
eq "the narrowed provider-outcome operation exists" "1" \
  "$(Q "SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='iam_v2' AND p.proname='p4_apply_provider_outcome';")"
eq "the runtime holds NO low-level financial primitive" "0" \
  "$(Q "SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='iam_v2' AND p.prosecdef AND has_function_privilege('sc_payment_runtime',p.oid,'EXECUTE') AND p.proname IN ('apply_payment_callback_v2','p4_insert_entitlement','p4_terminate_live_entitlement_for_subject','p4_mark_purchase_granted','apply_entitlement_transition','record_posting_review_action');")"
eq "the outbox recovery gate exists" "1" \
  "$(Q "SELECT count(*) FROM pg_trigger WHERE tgname='p4_outbox_recovery_gate' AND NOT tgisinternal;")"
eq "the shared full-rail hold exists" "1" \
  "$(Q "SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='iam_v2' AND p.proname='p4_hold_financial_rails';")"
eq "a backfilled identity can never be live" "1" \
  "$(Q "SELECT count(*) FROM pg_constraint WHERE conname='ppa_unverified_is_never_live';")"
rejects "an unverified legacy account cannot be activated" \
  "INSERT INTO iam_v2.payment_provider_accounts(tenant_id,site_id,provider,merchant_account_ref,provenance,status,is_default)
   VALUES ('$T','$S','stripe',NULL,'BACKFILLED_UNVERIFIED','ACTIVE',true);" "ppa_unverified_is_never_live|ppa_default_is_active"
rejects "a CONFIGURED account still needs a real external reference" \
  "INSERT INTO iam_v2.payment_provider_accounts(tenant_id,site_id,provider,merchant_account_ref,provenance,status)
   VALUES ('$T','$S','stripe',NULL,'CONFIGURED','DISABLED');" "ppa_reference_matches_provenance"
eq "the restore generation column exists" "1" \
  "$(Q "SELECT count(*) FROM information_schema.columns WHERE table_schema='iam_v2' AND table_name='financial_epochs' AND column_name='restore_generation';")"
eq "the restore event ledger exists" "1" \
  "$(Q "SELECT count(*) FROM information_schema.tables WHERE table_schema='iam_v2' AND table_name='financial_restore_events';")"
eq "the financial actor assertion exists" "1" \
  "$(Q "SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='iam_v2' AND p.proname='p4_assert_financial_actor';")"
eq "PUBLIC still holds EXECUTE on no SECURITY DEFINER function" "0" \
  "$(Q "SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='iam_v2' AND p.prosecdef AND has_function_privilege('public', p.oid, 'EXECUTE');")"
eq "every phase-4 definer function still pins its search_path" "0" \
  "$(Q "SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='iam_v2' AND p.prosecdef AND NOT EXISTS (SELECT 1 FROM unnest(coalesce(p.proconfig,'{}')) c WHERE c LIKE 'search_path=%');")"

for M in 0023_phase4_restore_generation 0022_phase4_recovery_closure 0021_phase4_trust_boundary; do
  if docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 \
       < "$ROOT/data-plane/migrations/$M.down.sql" >/dev/null 2>&1; then ok "$M DOWN applies"
  else no "$M DOWN applies" "down failed"; fi
done
eq "0021 DOWN handed the primitives back" "1" \
  "$(Q "SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='iam_v2' AND p.proname='p4_insert_entitlement' AND has_function_privilege('sc_payment_runtime',p.oid,'EXECUTE');")"
for M in 0021_phase4_trust_boundary 0022_phase4_recovery_closure 0023_phase4_restore_generation; do
  if docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 \
       < "$ROOT/data-plane/migrations/$M.up.sql" >/dev/null 2>&1; then ok "$M DOWN -> UP re-applies cleanly"
  else no "$M DOWN -> UP re-applies cleanly" "re-up failed"; fi
done
eq "0021..0023 DOWN -> UP produces the SAME schema as the first UP" "$UP23_FP" "$(Q "$FP")"
eq "0023 is recorded in the migration ledger" "1" \
  "$(Q "SELECT count(*) FROM public.schema_migrations WHERE version='0023_phase4_restore_generation';")"

# ------------------------------------------------------------------ 0024/0025 final closure
echo "== 0024/0025 outcome authority, grant kernel, recovery completion, C27, C35 =="
for M in 0024_phase4_outcome_authority_and_grant_kernel 0025_phase4_recovery_completion_and_compliance; do
  if docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 \
       < "$ROOT/data-plane/migrations/$M.up.sql" >/dev/null 2>&1; then ok "$M applies"
  else no "$M applies" "$(docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 < "$ROOT/data-plane/migrations/$M.up.sql" 2>&1 | tail -2 | tr '\n' ' ')"; fi
done
UP25_FP="$(Q "$FP")"

# --- F1: the outcome authority is a DIFFERENT role from the execution authority
eq "the outcome role exists" "1" \
  "$(Q "SELECT count(*) FROM pg_roles WHERE rolname='sc_payment_outcome';")"
eq "the EXECUTION role cannot assert a provider outcome" "f" \
  "$(Q "SELECT has_function_privilege('sc_payment_runtime','iam_v2.p4_apply_provider_outcome(text,text,text,text,text,jsonb)','EXECUTE');")"
eq "the OUTCOME role can" "t" \
  "$(Q "SELECT has_function_privilege('sc_payment_outcome','iam_v2.p4_apply_provider_outcome(text,text,text,text,text,jsonb)','EXECUTE');")"
eq "the outcome role cannot begin an execution" "f" \
  "$(Q "SELECT has_function_privilege('sc_payment_outcome','iam_v2.begin_payment_execution(uuid)','EXECUTE');")"
eq "the outcome role cannot grant" "f" \
  "$(Q "SELECT has_function_privilege('sc_payment_outcome','iam_v2.p4_grant_paid_entitlement(uuid,uuid,uuid)','EXECUTE');")"

# --- F2: ONE grant kernel, reachable only through the two authorized entry points
eq "the shared grant kernel exists" "1" \
  "$(Q "SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='iam_v2' AND p.proname='p4_entitlement_grant_kernel';")"
eq "no runtime role can call the kernel directly" "0" \
  "$(Q "SELECT count(*) FROM (VALUES ('sc_payment_runtime'),('sc_commerce_runtime'),('sc_payment_outcome'),('sc_financial_operator'),('sc_financial_readonly')) r(x) WHERE has_function_privilege(r.x,'iam_v2.p4_entitlement_grant_kernel(uuid,uuid,uuid,uuid,uuid,uuid,jsonb,uuid,uuid)','EXECUTE');")"
eq "the free entry point exists and belongs to commerce" "t" \
  "$(Q "SELECT has_function_privilege('sc_commerce_runtime','iam_v2.p4_grant_quoted_entitlement(uuid,uuid,uuid)','EXECUTE');")"
eq "the payment runtime cannot use the FREE entry point" "f" \
  "$(Q "SELECT has_function_privilege('sc_payment_runtime','iam_v2.p4_grant_quoted_entitlement(uuid,uuid,uuid)','EXECUTE');")"
eq "commerce cannot use the PAID entry point" "f" \
  "$(Q "SELECT has_function_privilege('sc_commerce_runtime','iam_v2.p4_grant_paid_entitlement(uuid,uuid,uuid)','EXECUTE');")"

# --- F3: the zero-attempt authorization and the marker cases
eq "the zero-attempt retry authorization exists" "1" \
  "$(Q "SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='iam_v2' AND p.proname='p4_authorize_zero_attempt_retry';")"
eq "it belongs to the financial operator and to nobody else" "1" \
  "$(Q "SELECT count(*) FROM (VALUES ('sc_financial_operator'),('sc_payment_runtime'),('sc_payment_outcome'),('sc_commerce_runtime')) r(x) WHERE has_function_privilege(r.x,'iam_v2.p4_authorize_zero_attempt_retry(uuid,uuid,text,jsonb)','EXECUTE');")"

# --- C27
eq "C27: one external merchant account belongs to one customer" "1" \
  "$(Q "SELECT count(*) FROM pg_indexes WHERE schemaname='iam_v2' AND indexname='ppa_merchant_ref_globally_unique';")"

# --- C35
eq "C35: the compliance archive recorder exists" "1" \
  "$(Q "SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='iam_v2' AND p.proname='p4_record_compliance_archive';")"
eq "C35: the purge gate exists" "1" \
  "$(Q "SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='iam_v2' AND p.proname='p4_assert_compliance_archived';")"
rejects "C35: a purge with no archive is refused" \
  "SELECT iam_v2.p4_assert_compliance_archived('$T'::uuid);" "COMPLIANCE_ARCHIVE_MISSING"
eq "C35: the missing external receipt authority is recorded rather than defaulted" "1" \
  "$(Q "SELECT count(*) FROM information_schema.columns WHERE table_schema='iam_v2' AND table_name='compliance_archives' AND column_name='receipt_blocked_reason';")"

# --- posture, re-asserted after the new definer functions
eq "PUBLIC still holds EXECUTE on no SECURITY DEFINER function" "0" \
  "$(Q "SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='iam_v2' AND p.prosecdef AND has_function_privilege('public', p.oid, 'EXECUTE');")"
eq "every phase-4 definer function still pins its search_path" "0" \
  "$(Q "SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='iam_v2' AND p.prosecdef AND NOT EXISTS (SELECT 1 FROM unnest(coalesce(p.proconfig,'{}')) c WHERE c LIKE 'search_path=%');")"

for M in 0025_phase4_recovery_completion_and_compliance 0024_phase4_outcome_authority_and_grant_kernel; do
  if docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 \
       < "$ROOT/data-plane/migrations/$M.down.sql" >/dev/null 2>&1; then ok "$M DOWN applies"
  else no "$M DOWN applies" "down failed"; fi
done
eq "0024 DOWN handed outcome authority back to the execution role" "t" \
  "$(Q "SELECT has_function_privilege('sc_payment_runtime','iam_v2.p4_apply_provider_outcome(text,text,text,text,text,jsonb)','EXECUTE');")"
for M in 0024_phase4_outcome_authority_and_grant_kernel 0025_phase4_recovery_completion_and_compliance; do
  if docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 \
       < "$ROOT/data-plane/migrations/$M.up.sql" >/dev/null 2>&1; then ok "$M DOWN -> UP re-applies cleanly"
  else no "$M DOWN -> UP re-applies cleanly" "re-up failed"; fi
done
eq "0024..0025 DOWN -> UP produces the SAME schema as the first UP" "$UP25_FP" "$(Q "$FP")"
eq "0025 is recorded in the migration ledger" "1" \
  "$(Q "SELECT count(*) FROM public.schema_migrations WHERE version='0025_phase4_recovery_completion_and_compliance';")"

# ------------------------------------------------------------------ 0026 closure
# C35 FAILING CLOSED, and the read model the zero-attempt operator path needs.
#
# The 0025 gate passed as soon as ANY archive row existed, so the export the appliance wrote about itself
# authorized the deletion. These assertions are about the corrected property and, just as importantly,
# about it not being routable AROUND: the flag cannot be set without external evidence even by the owner.
echo "== 0026 C35 fail-closed + zero-attempt read model =="
if docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 \
     < "$ROOT/data-plane/migrations/0026_phase4_c35_failclosed_and_operator_retry.up.sql" >/dev/null 2>&1; then
  ok "0026 applies"
else
  no "0026 applies" "$(docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 \
      < "$ROOT/data-plane/migrations/0026_phase4_c35_failclosed_and_operator_retry.up.sql" 2>&1 | tail -2 | tr '\n' ' ')"
fi
UP26_FP="$(Q "$FP")"

# an archive the appliance wrote about itself, digest and all
ARCH="$(Q "SELECT iam_v2.p4_record_compliance_archive('$T'::uuid,'$S'::uuid,repeat('b',64),'/var/backups/x.json','{\"iam_v2.purchases\":1}'::jsonb)::text;")"
eq "C35: a locally written archive does not claim a verified receipt" "f" \
  "$(Q "SELECT receipt_verified FROM iam_v2.compliance_archives WHERE id='$ARCH';")"
rejects "C35: a local archive with no external receipt cannot authorize a purge" \
  "SELECT iam_v2.p4_assert_compliance_archived('$T'::uuid);" "COMPLIANCE_RECEIPT_UNVERIFIED"
rejects "C35: the receipt flag cannot be set by hand, even as the owner" \
  "UPDATE iam_v2.compliance_archives SET receipt_verified=true WHERE id='$ARCH';" \
  "ca_receipt_evidence_matches_flag"
rejects "C35: a blank authority does not satisfy the evidence constraint" \
  "UPDATE iam_v2.compliance_archives SET receipt_verified=true, receipt_authority='  ', receipt_reference='x', receipt_verified_at=now() WHERE id='$ARCH';" \
  "ca_receipt_evidence_matches_flag"
rejects "C35: an archive cannot be INSERTed already verified" \
  "INSERT INTO iam_v2.compliance_archives(tenant_id,site_id,manifest_sha256,receipt_verified,purpose) VALUES ('$T','$S',repeat('c',64),true,'CROSS_CUSTOMER_PURGE');" \
  "ca_receipt_evidence_matches_flag"
rejects "C35: a receipt with no reference is refused outright" \
  "SELECT iam_v2.p4_record_compliance_receipt('$ARCH'::uuid,'an-authority','');" \
  "COMPLIANCE_RECEIPT_EVIDENCE_REQUIRED"
eq "C35: no role can record a receipt -- there is no authority to have heard from" "0" \
  "$(Q "SELECT count(*) FROM (VALUES ('sc_payment_runtime'),('sc_payment_outcome'),('sc_commerce_runtime'),('sc_financial_operator'),('sc_financial_readonly'),('public')) r(x) WHERE has_function_privilege(r.x,'iam_v2.p4_record_compliance_receipt(uuid,text,text)','EXECUTE');")"
# ...and the gate is not merely "always refuse": with real external evidence it opens. Recorded as the
# OWNER, standing in for the day an authority exists.
accepts "C35: a receipt with full external evidence is recordable by the owner" \
  "SELECT iam_v2.p4_record_compliance_receipt('$ARCH'::uuid,'gate-archival-authority','receipt-0001');"
accepts "C35: a VERIFIED receipt opens the same gate" \
  "SELECT iam_v2.p4_assert_compliance_archived('$T'::uuid);"
rejects "C35: custody cannot be acknowledged twice" \
  "SELECT iam_v2.p4_record_compliance_receipt('$ARCH'::uuid,'someone-else','receipt-0002');" \
  "COMPLIANCE_RECEIPT_ALREADY_RECORDED"

eq "0026: the zero-attempt read model exists" "1" \
  "$(Q "SELECT count(*) FROM pg_views WHERE schemaname='iam_v2' AND viewname='v_zero_attempt_recovery_queue';")"
eq "0026: it belongs to the financial operator and to no runtime role" "1" \
  "$(Q "SELECT count(*) FROM (VALUES ('sc_financial_operator'),('sc_payment_runtime'),('sc_payment_outcome'),('sc_commerce_runtime')) r(x) WHERE has_table_privilege(r.x,'iam_v2.v_zero_attempt_recovery_queue','SELECT');")"
eq "0026: PUBLIC still holds EXECUTE on no SECURITY DEFINER function" "0" \
  "$(Q "SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='iam_v2' AND p.prosecdef AND has_function_privilege('public', p.oid, 'EXECUTE');")"
eq "0026: every phase-4 definer function still pins its search_path" "0" \
  "$(Q "SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='iam_v2' AND p.prosecdef AND NOT EXISTS (SELECT 1 FROM unnest(coalesce(p.proconfig,'{}')) c WHERE c LIKE 'search_path=%');")"

if docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 \
     < "$ROOT/data-plane/migrations/0026_phase4_c35_failclosed_and_operator_retry.down.sql" >/dev/null 2>&1; then
  ok "0026 DOWN applies"
else no "0026 DOWN applies" "down failed"; fi
# The receipt columns SURVIVE the DOWN on purpose: an acknowledgement by an outside party is a fact about
# the world, and a migration rolling back is not a reason to forget who acknowledged what.
eq "0026 DOWN keeps the recorded external evidence" "gate-archival-authority" \
  "$(Q "SELECT receipt_authority FROM iam_v2.compliance_archives WHERE id='$ARCH';")"
if docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 \
     < "$ROOT/data-plane/migrations/0026_phase4_c35_failclosed_and_operator_retry.up.sql" >/dev/null 2>&1; then
  ok "0026 DOWN -> UP re-applies cleanly"
else no "0026 DOWN -> UP re-applies cleanly" "re-up failed"; fi
eq "0026 DOWN -> UP produces the SAME schema as the first UP" "$UP26_FP" "$(Q "$FP")"
eq "0026 is recorded in the migration ledger" "1" \
  "$(Q "SELECT count(*) FROM public.schema_migrations WHERE version='0026_phase4_c35_failclosed_and_operator_retry';")"



# ------------------------------------------------------------------ the baseline suite, after 0011
# On a FRESHLY rebuilt database, so the second pass is judged on its own rows and not on what the first
# pass left behind in append-only tables. If 0011 weakened any pre-0011 financial invariant, it fails here.
echo "== the same baseline invariants AFTER 0011 + 0012, on a clean rebuild (nothing was weakened) =="
SCRATCH_CONTAINER="$C" SCRATCH_DB="$DB" SCRATCH_PORT_ALLOW="$PORT" SCRATCH_ACK=I_UNDERSTAND_DISPOSABLE \
  bash "$ROOT/iam_v2_scratch/run.sh" fresh >/dev/null 2>&1 || { echo "INFRA: rebuild failed"; exit 2; }
Q "CREATE TABLE IF NOT EXISTS public.schema_migrations(version text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now());" >/dev/null
docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 < "$ROOT/data-plane/migrations/0009_phase2_commerce.up.sql" >/dev/null 2>&1
docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 < "$ROOT/data-plane/migrations/0010_phase3_stay_resolution.up.sql" >/dev/null 2>&1
docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 < "$ROOT/iam_v2_scratch/seed.sql" >/dev/null 2>&1
docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 < "$UP" >/dev/null 2>&1 \
  || { echo "INFRA: 0011 did not re-apply on the rebuild"; exit 2; }
docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 < "$UP12" >/dev/null 2>&1 \
  || { echo "INFRA: 0012 did not re-apply on the rebuild"; exit 2; }
docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 < "$FIXTURE" >/dev/null 2>&1 \
  || { echo "INFRA: fixture did not apply on the rebuild"; exit 2; }
eq "the rebuild carries 0011 + 0012" "1" "$(Q "SELECT count(*) FROM information_schema.columns WHERE table_schema='iam_v2' AND table_name='pms_interface_revisions' AND column_name='financial_base_currency';")"
run_baseline "post-0011+0012+0013+0014+0015"

echo "===== PHASE-4 0011 GATE: PASS=$pass FAIL=$fail ====="
[ "$fail" -eq 0 ] || exit 1
exit 0
