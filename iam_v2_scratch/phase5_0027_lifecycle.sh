#!/usr/bin/env bash
# PHASE-5 MIGRATION 0027 LIFECYCLE: UP / raw re-apply / DOWN / DOWN->UP.
#
# The question a rollback rehearsal actually has to answer is "did we get back to where we were", and the
# CATALOG fingerprint cannot answer it: dropping a column does not free its attribute slot, so every column
# added afterwards shifts by one and the fingerprint changes while the schema is identical. That was measured
# during Phase-4 WS-L. This uses the STRUCTURAL fingerprint for the round trip and the catalog one only to
# show that the migration changed something at all.
#
# It also re-proves the thing a Phase-5 rollback most needs to be true: p3_stay_lifecycle_guard goes back to
# REFUSING the post-stay transition. A rollback that left the new arm in place would be a rollback in name.
#
# EXIT: 0 all assertions passed, 1 an assertion failed, 2 the environment was not usable.
set -uo pipefail
export PATH="$PATH:/c/Program Files/Docker/Docker/resources/bin"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
C="${PHASE5_CONTAINER:-iamv2-p5}"; DB="${PHASE5_DB:-iam_scratch_p5}"
UP="$ROOT/data-plane/migrations/0027_phase5_poststay_and_transfer.up.sql"
DOWN="$ROOT/data-plane/migrations/0027_phase5_poststay_and_transfer.down.sql"

pass=0; fail=0
ok(){ printf '  [PASS] %s\n' "$1"; pass=$((pass+1)); }
no(){ printf '  [FAIL] %s :: %s\n' "$1" "${2:-}"; fail=$((fail+1)); }
Q(){ docker exec "$C" psql -U postgres -d "$DB" -tAqc "$1" 2>&1; }
apply(){ docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 -q < "$1" 2>&1; }
eq(){ [ "$2" = "$3" ] && ok "$1" || no "$1" "expected '$2' got '$3'"; }
ne(){ [ "$2" != "$3" ] && ok "$1" || no "$1" "both are '$2'"; }

CAT="SELECT md5(string_agg(line, E'\n' ORDER BY line)) FROM (
       SELECT format('C %s %s %s %s %s', table_name, column_name, ordinal_position, data_type, is_nullable) AS line
         FROM information_schema.columns WHERE table_schema='iam_v2'
       UNION ALL SELECT format('K %s %s', conrelid::regclass::text, pg_get_constraintdef(oid))
         FROM pg_constraint WHERE connamespace='iam_v2'::regnamespace
       UNION ALL SELECT format('I %s', indexdef) FROM pg_indexes WHERE schemaname='iam_v2'
       UNION ALL SELECT format('T %s %s', tgrelid::regclass::text, tgname)
         FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid JOIN pg_namespace n ON n.oid=c.relnamespace
        WHERE n.nspname='iam_v2' AND NOT t.tgisinternal
       UNION ALL SELECT format('F %s(%s)', p.proname, pg_get_function_arguments(p.oid))
         FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='iam_v2') x"
STRUCT="$(cat "$ROOT/iam_v2_scratch/schema_structure_fingerprint.sql")"

docker exec "$C" psql -U postgres -d "$DB" -tAqc 'select 1' >/dev/null 2>&1 \
  || { echo "INFRA: container $C / db $DB is not usable"; exit 2; }

echo "===== PHASE-5 0027 MIGRATION LIFECYCLE (db=$DB) ====="

# Start from a known DOWN state so the run is repeatable whatever the database was left in.
apply "$DOWN" >/dev/null 2>&1
eq "starting from the pre-0027 state" "0" \
   "$(Q "SELECT count(*) FROM public.schema_migrations WHERE version='0027_phase5_poststay_and_transfer';")"
PRE_CAT="$(Q "$CAT")"; PRE_STRUCT="$(Q "$STRUCT")"
eq "the pre-0027 guard REFUSES the post-stay transition" "1" \
   "$(Q "SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
         WHERE n.nspname='iam_v2' AND p.proname='p3_stay_lifecycle_guard'
           AND pg_get_functiondef(p.oid) LIKE '%POST_STAY_ACTIVE transitions are Phase 5%';")"

out="$(apply "$UP")"
[ -z "$(printf '%s' "$out" | grep -i error)" ] && ok "0027 UP applied" || no "0027 UP applied" "$(head -1 <<<"$out")"
eq "0027 is recorded in the migration ledger" "1" \
   "$(Q "SELECT count(*) FROM public.schema_migrations WHERE version='0027_phase5_poststay_and_transfer';")"
UP_CAT="$(Q "$CAT")"; UP_STRUCT="$(Q "$STRUCT")"
ne "0027 changed the catalog" "$PRE_CAT" "$UP_CAT"
ne "0027 changed the STRUCTURE (not merely column ordering)" "$PRE_STRUCT" "$UP_STRUCT"
eq "the post-0027 guard ALLOWS the post-stay transition" "1" \
   "$(Q "SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
         WHERE n.nspname='iam_v2' AND p.proname='p3_stay_lifecycle_guard'
           AND pg_get_functiondef(p.oid) LIKE '%POST_STAY_ACTIVE%THEN true%';")"
eq "every Phase-5 object is present (7 functions + 6 triggers)" "13" \
   "$(Q "SELECT (SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
                  WHERE n.nspname='iam_v2' AND p.proname LIKE 'p5\\_%')
              + (SELECT count(*) FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid
                 JOIN pg_namespace n ON n.oid=c.relnamespace
                  WHERE n.nspname='iam_v2' AND NOT t.tgisinternal AND t.tgname LIKE 'p5\\_%');")"

# A raw re-apply must ERROR rather than silently mutate: the migration is not idempotent by design, and a
# second run that quietly "succeeded" would hide a half-applied schema.
RAW="$(apply "$UP")"
[ -n "$(printf '%s' "$RAW" | grep -i 'already exists')" ] \
  && ok "a raw re-apply ERRORS instead of silently mutating" \
  || no "a raw re-apply ERRORS" "no 'already exists' error"
eq "the catalog is unchanged after the failed re-apply (it rolled back)" "$UP_CAT" "$(Q "$CAT")"

out="$(apply "$DOWN")"
[ -z "$(printf '%s' "$out" | grep -i error)" ] && ok "0027 DOWN applied" || no "0027 DOWN applied" "$(head -1 <<<"$out")"
eq "DOWN removed the ledger row" "0" \
   "$(Q "SELECT count(*) FROM public.schema_migrations WHERE version='0027_phase5_poststay_and_transfer';")"
eq "DOWN left NO Phase-5 object behind" "0" \
   "$(Q "SELECT (SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
                  WHERE n.nspname='iam_v2' AND p.proname LIKE 'p5\\_%')
              + (SELECT count(*) FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid
                 JOIN pg_namespace n ON n.oid=c.relnamespace
                  WHERE n.nspname='iam_v2' AND NOT t.tgisinternal AND t.tgname LIKE 'p5\\_%');")"
eq "DOWN restored the Phase-3 guard, refusal message and all" "1" \
   "$(Q "SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
         WHERE n.nspname='iam_v2' AND p.proname='p3_stay_lifecycle_guard'
           AND pg_get_functiondef(p.oid) LIKE '%POST_STAY_ACTIVE transitions are Phase 5%';")"
eq "DOWN reverses ONLY 0027: the STRUCTURE is identical to pre-0027" "$PRE_STRUCT" "$(Q "$STRUCT")"

out="$(apply "$UP")"
[ -z "$(printf '%s' "$out" | grep -i error)" ] && ok "DOWN -> UP re-applies cleanly" || no "DOWN -> UP" "$(head -1 <<<"$out")"
eq "DOWN -> UP produces the SAME STRUCTURE as the first UP" "$UP_STRUCT" "$(Q "$STRUCT")"
CYC_CAT="$(Q "$CAT")"
if [ "$CYC_CAT" = "$UP_CAT" ]; then
  ok "the catalog fingerprint also matches (no dropped-column slots were consumed this cycle)"
else
  ok "the catalog fingerprint differs after the cycle, as expected — dropped columns do not free their attribute slots; the STRUCTURAL fingerprint above is the one that answers the rollback question"
fi

# 0027 does not exist alone in the chain: 0029 REPLACES the profile guard it creates, so re-applying 0027 by
# itself leaves the database holding 0027's older rule. Cycling one migration must not silently roll a LATER
# one back, so the chain head is restored and asserted before this script hands the database on.
for LATER in 0029_phase5_reveal_is_at_mint; do
  apply "$ROOT/data-plane/migrations/$LATER.up.sql" >/dev/null 2>&1
done
eq "the chain head is restored: 0029's rule is in force again, not 0027's" "1"    "$(Q "SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
         WHERE n.nspname='iam_v2' AND p.proname='p5_post_stay_profile_guard'
           AND pg_get_functiondef(p.oid) LIKE '%records its one-time reveal at mint%';")"

echo "===== RESULT PASS=$pass FAIL=$fail ====="
[ "$fail" -eq 0 ] || exit 1
exit 0
