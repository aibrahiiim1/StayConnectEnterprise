#!/usr/bin/env bash
# Offline real-schema compatibility: build a SECOND disposable DB from the COMMITTED real platform
# migration chain (data-plane/migrations/0001..0006, schema-only, no rows, no secrets) and run
# MG-0..MG-9 on top. NO live database or appliance access.
HERE="$(cd "$(dirname "$0")" && pwd)"; source "$HERE/lib.sh"; set +e +o pipefail
export SCRATCH_ACK=I_UNDERSTAND_DISPOSABLE
C="$SCRATCH_CONTAINER"; RDB="iam_scratch_real"; REPO="$(cd "$HERE/.." && pwd)"
su(){ docker exec -i "$C" psql -U postgres -v ON_ERROR_STOP=1 -qAt "$@"; }
rdb(){ docker exec -i "$C" psql -U postgres -d "$RDB" -v ON_ERROR_STOP=1 -qAt "$@"; }
PASSN=0; FAILN=0; ok(){ echo "PASS  $1"; PASSN=$((PASSN+1)); }; no(){ echo "FAIL  $1 :: $2"; FAILN=$((FAILN+1)); }
fp(){ docker exec -i "$C" psql -U postgres -d "$RDB" -At -c "
SELECT md5(string_agg(line,E'\n' ORDER BY line)) FROM (
  SELECT format('COL %s.%s %s %s %s',table_name,ordinal_position,column_name,data_type,is_nullable) line FROM information_schema.columns WHERE table_schema='iam_v2'
  UNION ALL SELECT format('CON %s %s',conrelid::regclass::text,pg_get_constraintdef(oid)) FROM pg_constraint WHERE connamespace='iam_v2'::regnamespace
  UNION ALL SELECT format('IDX %s',indexdef) FROM pg_indexes WHERE schemaname='iam_v2'
  UNION ALL SELECT format('TRG %s %s',tgrelid::regclass::text,tgname) FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='iam_v2' AND NOT t.tgisinternal
  UNION ALL SELECT format('FUN %s(%s)',p.proname,pg_get_function_arguments(p.oid)) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='iam_v2'
) x;"; }
tabs(){ docker exec -i "$C" psql -U postgres -d "$RDB" -At -c "SELECT count(*) FROM information_schema.tables WHERE table_schema='iam_v2' AND table_type='BASE TABLE';"; }

echo "===== OFFLINE REAL-SCHEMA COMPATIBILITY ====="
su -c "DROP DATABASE IF EXISTS $RDB;" >/dev/null 2>&1
su -c "CREATE DATABASE $RDB;" >/dev/null
rdb -c "CREATE TABLE public._scratch_marker(marker text PRIMARY KEY); INSERT INTO public._scratch_marker VALUES ('DISPOSABLE_SCRATCH_ONLY');" >/dev/null

# 1) apply the committed REAL platform schema chain (schema-only; no row data)
allok=1
for m in 0001_edge_init 0002_edge_networking 0003_network_audit 0004_sysnet_audit_events 0005_appliance_service_health 0006_guest_accounts_and_voucher_gen; do
  if docker exec -i "$C" psql -U postgres -d "$RDB" -v ON_ERROR_STOP=1 -qAt < "$REPO/data-plane/migrations/$m.up.sql" >/tmp/real_$m.log 2>&1; then :; else allok=0; echo "  real migration FAILED: $m"; tail -3 /tmp/real_$m.log; fi
done
[ "$allok" = "1" ] && ok "OFR-01 committed real platform schema (0001..0006) applies clean (schema-only)" || no "OFR-01" "a real migration failed"
gn="$(rdb -c "SELECT count(*) FROM information_schema.columns WHERE table_schema='public' AND table_name='guest_networks' AND column_name IN ('tenant_id','site_id','id');")"
[ "$gn" = "3" ] && ok "OFR-02 real public.guest_networks exposes (tenant_id,site_id,id) for the anchor" || no "OFR-02" "cols=$gn"

# 2) MG-0 anchor on the REAL guest_networks, then MG-1..MG-9 (using the ledger); against RDB
SCRATCH_DB="$RDB" bash "$HERE/mg0.sh" >/tmp/real_mg0.log 2>&1 && ok "OFR-03 MG-0 anchor builds on real guest_networks (CONCURRENTLY)" || no "OFR-03" "$(tail -1 /tmp/real_mg0.log)"
SCRATCH_DB="$RDB" bash "$HERE/run.sh" up >/tmp/real_up.log 2>&1
t="$(tabs)"; [ "$t" = "49" ] && ok "OFR-04 MG-1..MG-9 build 49 iam_v2 tables on real schema" || no "OFR-04" "tables=$t"
F1="$(fp)"

# 3) second apply WITHOUT down (idempotent no-op)
out="$(SCRATCH_DB="$RDB" bash "$HERE/run.sh" up 2>&1)"; sk=$(echo "$out"|grep -c 'skip ')
[ "$sk" = "9" ] && ok "OFR-05 second apply on real schema skips all 9 (idempotent)" || no "OFR-05" "skips=$sk"
[ "$(fp)" = "$F1" ] && ok "OFR-06 real-schema catalog fingerprint stable on second apply" || no "OFR-06" "fingerprint drift"

# 4) down + reup on real schema
SCRATCH_DB="$RDB" bash "$HERE/run.sh" down >/dev/null 2>&1
t0="$(tabs)"; [ "$t0" = "0" ] && ok "OFR-07 down drops iam_v2 on real schema (0 tables)" || no "OFR-07" "tables=$t0"
SCRATCH_DB="$RDB" bash "$HERE/mg0.sh" >/dev/null 2>&1; SCRATCH_DB="$RDB" bash "$HERE/run.sh" up >/dev/null 2>&1
[ "$(fp)" = "$F1" ] && ok "OFR-08 re-up on real schema reproduces identical iam_v2 catalog" || no "OFR-08" "fingerprint mismatch after reup"

# 5) fingerprint parity vs the fixture build (iam_v2 identical regardless of what else is in public)
FIX_FP="$(docker exec -i "$C" psql -U postgres -d iam_scratch -At -c "
SELECT md5(string_agg(line,E'\n' ORDER BY line)) FROM (
  SELECT format('COL %s.%s %s %s %s',table_name,ordinal_position,column_name,data_type,is_nullable) line FROM information_schema.columns WHERE table_schema='iam_v2'
  UNION ALL SELECT format('CON %s %s',conrelid::regclass::text,pg_get_constraintdef(oid)) FROM pg_constraint WHERE connamespace='iam_v2'::regnamespace
  UNION ALL SELECT format('IDX %s',indexdef) FROM pg_indexes WHERE schemaname='iam_v2'
  UNION ALL SELECT format('TRG %s %s',tgrelid::regclass::text,tgname) FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='iam_v2' AND NOT t.tgisinternal
  UNION ALL SELECT format('FUN %s(%s)',p.proname,pg_get_function_arguments(p.oid)) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='iam_v2'
) x;")"
[ "$(fp)" = "$FIX_FP" ] && ok "OFR-09 iam_v2 catalog IDENTICAL on fixture DB and real-schema DB" || no "OFR-09" "real=$(fp) fixture=$FIX_FP"

echo "real-schema iam_v2 fingerprint: $F1"
su -c "DROP DATABASE IF EXISTS $RDB;" >/dev/null 2>&1   # dispose the real-schema DB
echo "===== OFFLINE-REAL RESULT: PASS=$PASSN FAIL=$FAILN ====="
[ "$FAILN" = "0" ]
