#!/usr/bin/env bash
# Migration idempotency + deterministic-rebuild catalog equality.
HERE="$(cd "$(dirname "$0")" && pwd)"; source "$HERE/lib.sh"; set +e +o pipefail
export SCRATCH_ACK=I_UNDERSTAND_DISPOSABLE
PASSN=0; FAILN=0
ok(){ echo "PASS  $1"; PASSN=$((PASSN+1)); }
no(){ echo "FAIL  $1 :: $2"; FAILN=$((FAILN+1)); }
fp(){ docker exec -i "$SCRATCH_CONTAINER" psql -U postgres -d "$SCRATCH_DB" -At -c "
SELECT md5(string_agg(line, E'\n' ORDER BY line)) FROM (
  SELECT format('COL %s.%s %s %s %s', table_name, ordinal_position, column_name, data_type, is_nullable) AS line
    FROM information_schema.columns WHERE table_schema='iam_v2'
  UNION ALL SELECT format('CON %s %s', conrelid::regclass::text, pg_get_constraintdef(oid))
    FROM pg_constraint WHERE connamespace='iam_v2'::regnamespace
  UNION ALL SELECT format('IDX %s', indexdef) FROM pg_indexes WHERE schemaname='iam_v2'
  UNION ALL SELECT format('TRG %s %s', tgrelid::regclass::text, tgname)
    FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid JOIN pg_namespace n ON n.oid=c.relnamespace
    WHERE n.nspname='iam_v2' AND NOT t.tgisinternal
  UNION ALL SELECT format('FUN %s(%s)', p.proname, pg_get_function_arguments(p.oid))
    FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='iam_v2'
) x;"; }
tables(){ docker exec -i "$SCRATCH_CONTAINER" psql -U postgres -d "$SCRATCH_DB" -At -c "SELECT count(*) FROM information_schema.tables WHERE table_schema='iam_v2' AND table_type='BASE TABLE';"; }

echo "===== MIGRATION IDEMPOTENCY / CATALOG EQUALITY ====="
bash "$HERE/run.sh" fresh >/tmp/idem1.log 2>&1
F1="$(fp)"; T1="$(tables)"
[ "$T1" = "49" ] && ok "IDEM-01 fresh build -> 49 tables" || no "IDEM-01" "tables=$T1"

# apply the SET AGAIN WITHOUT down (must be a no-op via the ledger)
out="$(bash "$HERE/run.sh" up 2>&1)"; F2="$(fp)"; T2="$(tables)"
skips=$(echo "$out" | grep -c 'skip ')
[ "$skips" = "9" ] && ok "IDEM-02 second apply (no down) skips all 9 migrations (idempotent)" || no "IDEM-02" "skips=$skips"
[ "$F2" = "$F1" ] && ok "IDEM-03 catalog fingerprint unchanged after second apply" || no "IDEM-03" "F1=$F1 F2=$F2"
[ "$T2" = "49" ] && ok "IDEM-04 still 49 tables after second apply" || no "IDEM-04" "tables=$T2"

# down + rebuild -> deterministic (same fingerprint)
bash "$HERE/run.sh" fresh >/tmp/idem2.log 2>&1
F3="$(fp)"
[ "$F3" = "$F1" ] && ok "IDEM-05 exact catalog equality after fresh rebuild (deterministic)" || no "IDEM-05" "F1=$F1 F3=$F3"

echo "fingerprint=$F1"
echo "===== IDEMPOTENCY RESULT: PASS=$PASSN FAIL=$FAILN ====="
[ "$FAILN" = "0" ]
