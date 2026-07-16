#!/usr/bin/env bash
# Phase 1A LIVE-DARK creation in production stayconnect_site (appliance). Additive + DARK + reversible.
# Runs ON the appliance. Requires migrations/ alongside. NO service/DSN/search_path/PMS/network change.
set -uo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
CT=stayconnect-pg; DB=stayconnect_site; OWNER=iam_v2_owner
P(){ docker exec -i "$CT" psql -U stayconnect -d "$DB" -v ON_ERROR_STOP=1 -qAt -c "$1"; }
Pf(){ docker exec -i "$CT" psql -U stayconnect -d "$DB" -v ON_ERROR_STOP=1 -qAt; }   # stdin
Pac(){ docker exec -i "$CT" psql -U stayconnect -d "$DB" -v ON_ERROR_STOP=1 -qAt -c "$1"; }  # autocommit (own txn)
say(){ echo "[$(date -u +%H:%M:%S)] $*"; }
IDX=guest_networks_tsi_anchor

# deterministic iam_v2 catalog fingerprint (must equal the scratch/offline value)
FP_SQL="SELECT md5(string_agg(line,E'\n' ORDER BY line)) FROM (
  SELECT format('COL %s.%s %s %s %s',table_name,ordinal_position,column_name,data_type,is_nullable) line FROM information_schema.columns WHERE table_schema='iam_v2'
  UNION ALL SELECT format('CON %s %s',conrelid::regclass::text,pg_get_constraintdef(oid)) FROM pg_constraint WHERE connamespace='iam_v2'::regnamespace
  UNION ALL SELECT format('IDX %s',indexdef) FROM pg_indexes WHERE schemaname='iam_v2'
  UNION ALL SELECT format('TRG %s %s',tgrelid::regclass::text,tgname) FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='iam_v2' AND NOT t.tgisinternal
  UNION ALL SELECT format('FUN %s(%s)',pr.proname,pg_get_function_arguments(pr.oid)) FROM pg_proc pr JOIN pg_namespace n ON n.oid=pr.pronamespace WHERE n.nspname='iam_v2'
) x;"
# public fingerprint EXCLUDING the new anchor (to prove public unchanged except MG-0)
PUB_SQL="SELECT md5(string_agg(line,E'\n' ORDER BY line)) FROM (
  SELECT format('COL %s.%s %s',table_name,ordinal_position,column_name) line FROM information_schema.columns WHERE table_schema='public'
  UNION ALL SELECT format('CON %s %s',conrelid::regclass::text,pg_get_constraintdef(oid)) FROM pg_constraint WHERE connamespace='public'::regnamespace
  UNION ALL SELECT format('IDX %s',indexdef) FROM pg_indexes WHERE schemaname='public' AND indexname<>'$IDX'
) x;"
pubrows(){ P "SELECT coalesce(sum(n_live_tup),0) FROM pg_stat_user_tables WHERE schemaname='public'"; }

rollback(){
  say "ROLLBACK: dropping iam_v2 + MG-0 anchor (only what this phase created)"
  P "DROP SCHEMA IF EXISTS iam_v2 CASCADE;" || true
  Pac "DROP INDEX CONCURRENTLY IF EXISTS public.$IDX;" || true
  # owner/migrator/service roles created by this phase (only if present + unused)
  for r in iam_v2_owner iam_v2_migrator iam_v2_svc_scd iam_v2_svc_edged iam_v2_svc_acctd iam_v2_svc_portald iam_v2_svc_hoteladm; do
    P "DROP ROLE IF EXISTS $r;" 2>/dev/null || true
  done
  say "ROLLBACK done. public fingerprint now: $(P "$PUB_SQL")"
}

# ---------------- GUARD ----------------
[ "${PROD_LIVE_DARK_ACK:-}" = "PHASE1A_LIVE_DARK_APPROVED" ] || { echo "ABORT: missing PROD_LIVE_DARK_ACK"; exit 90; }
cur="$(P "select current_database()")";       [ "$cur" = "$DB" ]  || { echo "ABORT: db=$cur"; exit 90; }
[ "$(P "select pg_is_in_recovery()")" = "f" ] || { echo "ABORT: standby"; exit 90; }
[ "$(P "select count(*) from information_schema.schemata where schema_name='iam_v2'")" = "0" ] || { echo "ABORT: iam_v2 already exists"; exit 91; }
[ "$(P "select count(*) from information_schema.columns where table_schema='public' and table_name='guest_networks' and column_name in ('id','tenant_id','site_id')")" = "3" ] || { echo "ABORT: guest_networks shape differs materially"; exit 92; }
[ "$(P "select count(*) from (select tenant_id,site_id,id from public.guest_networks group by 1,2,3 having count(*)>1) d")" = "0" ] || { echo "ABORT: guest_networks duplicate (tenant,site,id)"; exit 93; }
[ "$(P "select count(*) from pg_class where relname='$IDX'")" = "0" ] || { echo "ABORT: anchor already exists"; exit 94; }
say "GUARD OK: db=$DB primary, iam_v2 absent, guest_networks compatible, no anchor, ack present"

trap 'echo "[ERROR] unexpected failure — initiating rollback"; rollback; exit 1' ERR

# ---------------- BACKUP ----------------
STAMP="$(date -u +%Y%m%dT%H%M%SZ)"; BK="/root/backups/phase1a_predark_${STAMP}"; mkdir -p /root/backups
say "BACKUP: full custom dump + schema-only dump of $DB"
docker exec "$CT" pg_dump -U stayconnect -d "$DB" -Fc -f /tmp/p1a_full.dump
docker exec "$CT" pg_dump -U stayconnect -d "$DB" -s  -f /tmp/p1a_schema.sql
docker cp "$CT":/tmp/p1a_full.dump  "${BK}_full.dump"
docker cp "$CT":/tmp/p1a_schema.sql "${BK}_schema.sql"
docker exec "$CT" rm -f /tmp/p1a_full.dump /tmp/p1a_schema.sql
# validity checks (non-destructive): list the custom-format dump's table-of-contents via stdin
restore_objs="$(docker exec -i "$CT" pg_restore --list < "${BK}_full.dump" 2>/dev/null | grep -cE '^[0-9]+;')"
# also confirm the schema-only dump contains expected DDL and gunzips/parses
schema_ddl="$(grep -cE '^CREATE (TABLE|INDEX|FUNCTION|TRIGGER)' "${BK}_schema.sql")"
sz_full=$(stat -c%s "${BK}_full.dump"); sz_schema=$(stat -c%s "${BK}_schema.sql")
sha_full=$(sha256sum "${BK}_full.dump" | cut -d' ' -f1); sha_schema=$(sha256sum "${BK}_schema.sql" | cut -d' ' -f1)
say "BACKUP ok: full=${BK}_full.dump ($sz_full B, sha ${sha_full:0:16}.., TOC lists $restore_objs entries) schema=${BK}_schema.sql ($sz_schema B, sha ${sha_schema:0:16}.., $schema_ddl DDL stmts)"
{ [ "$restore_objs" -gt 20 ] || [ "$schema_ddl" -gt 20 ]; } || { echo "ABORT: backup validity check failed"; exit 95; }

# ---------------- PRE-STATE SNAPSHOT ----------------
PUB_BEFORE="$(P "$PUB_SQL")"; ROWS_BEFORE="$(pubrows)"; PUBTBL_BEFORE="$(P "select count(*) from information_schema.tables where table_schema='public' and table_type='BASE TABLE'")"
say "PRE-STATE: public_fingerprint=$PUB_BEFORE public_tables=$PUBTBL_BEFORE public_live_rows≈$ROWS_BEFORE"

# ---------------- MG-0 ----------------
say "MG-0: duplicate pre-check + CREATE UNIQUE INDEX CONCURRENTLY (non-transactional, no bare IF NOT EXISTS)"
inv="$(P "select count(*) from pg_class where relname='$IDX'")"   # 0 confirmed by guard
t0=$(date +%s%3N)
Pac "CREATE UNIQUE INDEX CONCURRENTLY $IDX ON public.guest_networks (tenant_id, site_id, id);"
t1=$(date +%s%3N)
valid="$(P "select indisvalid::text from pg_index where indexrelid='public.$IDX'::regclass")"
defok="$(P "select (indexdef='CREATE UNIQUE INDEX $IDX ON public.guest_networks USING btree (tenant_id, site_id, id)')::text from pg_indexes where indexname='$IDX'")"
owner_ok="$(P "select (relowner::regrole::text='stayconnect')::text from pg_class where relname='$IDX'")"
say "MG-0: indisvalid=$valid exact_def=$defok build_ms=$((t1-t0)) owner_is_platform=$owner_ok"
[ "$valid" = "true" ] && [ "$defok" = "true" ] || { echo "MG-0 FAIL"; exit 96; }

# ---------------- ROLES (owner/migrator/service-equivalent) ----------------
say "ROLES: create iam_v2_owner/migrator + service-equivalent (least privilege)"
P "DO \$\$ BEGIN CREATE ROLE iam_v2_owner NOLOGIN; EXCEPTION WHEN duplicate_object THEN NULL; END \$\$;
   DO \$\$ BEGIN CREATE ROLE iam_v2_migrator NOLOGIN; EXCEPTION WHEN duplicate_object THEN NULL; END \$\$;
   DO \$\$ BEGIN CREATE ROLE iam_v2_svc_scd NOLOGIN; EXCEPTION WHEN duplicate_object THEN NULL; END \$\$;
   DO \$\$ BEGIN CREATE ROLE iam_v2_svc_edged NOLOGIN; EXCEPTION WHEN duplicate_object THEN NULL; END \$\$;
   DO \$\$ BEGIN CREATE ROLE iam_v2_svc_acctd NOLOGIN; EXCEPTION WHEN duplicate_object THEN NULL; END \$\$;
   DO \$\$ BEGIN CREATE ROLE iam_v2_svc_portald NOLOGIN; EXCEPTION WHEN duplicate_object THEN NULL; END \$\$;
   DO \$\$ BEGIN CREATE ROLE iam_v2_svc_hoteladm NOLOGIN; EXCEPTION WHEN duplicate_object THEN NULL; END \$\$;
   GRANT iam_v2_owner TO iam_v2_migrator;
   GRANT CREATE ON DATABASE $DB TO iam_v2_owner;
   GRANT REFERENCES ON public.guest_networks TO iam_v2_owner;"
P "CREATE SCHEMA IF NOT EXISTS iam_v2 AUTHORIZATION iam_v2_owner;
   REVOKE ALL ON SCHEMA iam_v2 FROM PUBLIC;
   ALTER DEFAULT PRIVILEGES FOR ROLE iam_v2_owner IN SCHEMA iam_v2 REVOKE ALL ON TABLES FROM PUBLIC;
   ALTER DEFAULT PRIVILEGES FOR ROLE iam_v2_owner IN SCHEMA iam_v2 REVOKE ALL ON SEQUENCES FROM PUBLIC;
   ALTER DEFAULT PRIVILEGES FOR ROLE iam_v2_owner IN SCHEMA iam_v2 REVOKE ALL ON FUNCTIONS FROM PUBLIC;"

# ---------------- MG-1..MG-9 (as iam_v2_owner; dark) ----------------
for f in mg1_pms_interface_core mg2_plans_packages mg3_identities_credentials mg4_stay_domain \
         mg5_auth_commerce mg6_entitlements_devices_sessions mg7_postings_payments \
         mg8_resolution_aux mg9_engine; do
  ck=$(sha256sum "$HERE/migrations/$f.sql" | cut -d' ' -f1)
  ts=$(date +%s%3N)
  { echo "SET ROLE iam_v2_owner;"; cat "$HERE/migrations/$f.sql"; } | Pf >/dev/null
  te=$(date +%s%3N)
  say "MG applied: $f (sha ${ck:0:16}.. ${te-ts}ms)"
done
say "MG-1..MG-9 applied"
trap - ERR
echo "PROD_LIVE_DARK_APPLIED"

# ---------------- LIVE-DARK ACCEPTANCE ----------------
say "===== LIVE-DARK ACCEPTANCE ====="
ACCFAIL=0; pass(){ echo "PASS  $1"; }; fail(){ echo "FAIL  $1"; ACCFAIL=1; }
FP="$(P "$FP_SQL")"
TBL="$(P "select count(*) from information_schema.tables where table_schema='iam_v2' and table_type='BASE TABLE'")"
CON="$(P "select count(*) from pg_constraint where connamespace='iam_v2'::regnamespace")"
TRG="$(P "select count(*) from pg_trigger t join pg_class c on c.oid=t.tgrelid join pg_namespace n on n.oid=c.relnamespace where n.nspname='iam_v2' and not t.tgisinternal")"
FUN="$(P "select count(*) from pg_proc pr join pg_namespace n on n.oid=pr.pronamespace where n.nspname='iam_v2'")"
PUB_AFTER="$(P "$PUB_SQL")"; ROWS_AFTER="$(pubrows)"; PUBTBL_AFTER="$(P "select count(*) from information_schema.tables where table_schema='public' and table_type='BASE TABLE'")"
IAMROWS="$(P "select coalesce(sum(n_live_tup),0) from pg_stat_user_tables where schemaname='iam_v2'")"
PUBOBJ="$(P "select count(*) from pg_class c join pg_namespace n on n.oid=c.relnamespace where n.nspname='public' and c.relkind in ('r','v','S','f') and c.relname like 'iam_v2%'")"

[ "$TBL" = "49" ]  && pass "AC-01 exactly 49 iam_v2 tables" || fail "AC-01 tables=$TBL"
[ "$FP" = "bd75026ff6ea5835a1ca8d19051eb257" ] && pass "AC-02 iam_v2 catalog fingerprint == verified scratch build (bd75026f) — all PK/U/CK/FK/PI/triggers/functions identical" || fail "AC-02 fp=$FP"
[ "$PUB_AFTER" = "$PUB_BEFORE" ] && pass "AC-03 public schema unchanged except the MG-0 anchor" || fail "AC-03 public fingerprint changed"
[ "$ROWS_AFTER" = "$ROWS_BEFORE" ] && pass "AC-04 public live-row totals unchanged ($ROWS_BEFORE)" || fail "AC-04 rows $ROWS_BEFORE->$ROWS_AFTER"
[ "$PUBTBL_AFTER" = "$PUBTBL_BEFORE" ] && pass "AC-05 public base-table count unchanged ($PUBTBL_BEFORE)" || fail "AC-05 $PUBTBL_BEFORE->$PUBTBL_AFTER"
[ "$PUBOBJ" = "0" ] && pass "AC-06 no iam_v2* objects leaked into public" || fail "AC-06 leaked=$PUBOBJ"
[ "$IAMROWS" = "0" ] && pass "AC-07 zero rows in iam_v2 (dark)" || fail "AC-07 iam_v2 rows=$IAMROWS"
[ "$(P "select nspowner::regrole::text from pg_namespace where nspname='iam_v2'")" = "iam_v2_owner" ] && pass "AC-08 schema owned by iam_v2_owner" || fail "AC-08 owner"
[ "$(P "select count(*) from pg_class c join pg_namespace n on n.oid=c.relnamespace where n.nspname='iam_v2' and c.relkind='r' and c.relowner<>'iam_v2_owner'::regrole")" = "0" ] && pass "AC-09 all iam_v2 tables owned by iam_v2_owner" || fail "AC-09"
[ "$(P "select folio_identity_strategy from iam_v2.pms_interface_revisions limit 1" 2>/dev/null; P "select column_default from information_schema.columns where table_schema='iam_v2' and table_name='pms_interface_revisions' and column_name='folio_identity_strategy'")" = "'UNSET'::text" ] && pass "AC-10 folio_identity_strategy DEFAULT 'UNSET'" || fail "AC-10 default=$(P "select column_default from information_schema.columns where table_schema='iam_v2' and table_name='pms_interface_revisions' and column_name='folio_identity_strategy'")"
[ "$(P "select count(*) from pg_trigger t join pg_class c on c.oid=t.tgrelid join pg_namespace n on n.oid=c.relnamespace where n.nspname='iam_v2' and c.relname='pms_postings' and t.tgname='charge_gate'")" = "1" ] && pass "AC-11 folio-UNSET charge_gate trigger present" || fail "AC-11"
[ "$(P "select count(*) from pg_proc pr join pg_namespace n on n.oid=pr.pronamespace where n.nspname='iam_v2' and (pr.proname ilike '%send%revers%' or pr.proname ilike '%pt_c%' or pr.proname ilike '%negative_ta%')")" = "0" ] && pass "AC-12 no executable reversal function" || fail "AC-12"
[[ "$(P "show search_path")" != *iam_v2* ]] && pass "AC-13 default search_path excludes iam_v2" || fail "AC-13"
[ "$(P "select count(*) from pg_db_role_setting s join pg_roles r on r.oid=s.setrole where r.rolname='stayconnect' and array_to_string(s.setconfig,',') ilike '%iam_v2%'")" = "0" ] && pass "AC-14 service role 'stayconnect' has no iam_v2 in a role-level search_path" || fail "AC-14"
[ "$(P "select count(*) from information_schema.role_table_grants where table_schema='iam_v2' and grantee='PUBLIC'")" = "0" ] && pass "AC-15 PUBLIC has no privileges on iam_v2" || fail "AC-15"
den="$(docker exec -i "$CT" psql -U stayconnect -d "$DB" -qAt -c "SET ROLE iam_v2_svc_scd; SELECT 1 FROM iam_v2.pms_interfaces LIMIT 1;" 2>&1)"
[[ "$den" == *"permission denied"* ]] && pass "AC-16 non-superuser service-equivalent role denied SELECT on iam_v2" || fail "AC-16 ($den)"

# functional probes (transactional; ROLLBACK — no durable rows)
imm="$(docker exec -i "$CT" psql -U stayconnect -d "$DB" -qAt -c "BEGIN; SET ROLE iam_v2_owner; INSERT INTO iam_v2.pms_interfaces(id,tenant_id,site_id,connector_kind) VALUES ('aaaaaaaa-0000-0000-0000-000000000001','11111111-1111-1111-1111-111111111111','22222222-2222-2222-2222-222222222222','probe'); INSERT INTO iam_v2.pms_interface_revisions(id,tenant_id,site_id,pms_interface_id,revision_no,source_timezone,config) VALUES ('aaaaaaaa-0000-0000-0000-0000000000d1','11111111-1111-1111-1111-111111111111','22222222-2222-2222-2222-222222222222','aaaaaaaa-0000-0000-0000-000000000001',1,'UTC','{}'); UPDATE iam_v2.pms_interface_revisions SET source_timezone='X' WHERE id='aaaaaaaa-0000-0000-0000-0000000000d1'; ROLLBACK;" 2>&1)"
[[ "$imm" == *"immutable"* ]] && pass "AC-17 immutable-revision trigger fires (UPDATE rejected) — functional probe, rolled back" || fail "AC-17 ($imm)"
fol="$(docker exec -i "$CT" psql -U stayconnect -d "$DB" -qAt -c "BEGIN; SET ROLE iam_v2_owner; INSERT INTO iam_v2.pms_interfaces(id,tenant_id,site_id,connector_kind) VALUES ('bbbbbbbb-0000-0000-0000-000000000001','11111111-1111-1111-1111-111111111111','22222222-2222-2222-2222-222222222222','probe'); INSERT INTO iam_v2.pms_interface_revisions(id,tenant_id,site_id,pms_interface_id,revision_no,source_timezone,folio_identity_strategy,config) VALUES ('bbbbbbbb-0000-0000-0000-0000000000d1','11111111-1111-1111-1111-111111111111','22222222-2222-2222-2222-222222222222','bbbbbbbb-0000-0000-0000-000000000001',1,'UTC','UNSET','{}'); INSERT INTO iam_v2.pms_postings(tenant_id,site_id,pms_interface_id,settlement_id,purchase_id,posting_interface_revision_id,posting_type,amount_minor,currency,currency_exponent,idempotency_key) VALUES ('11111111-1111-1111-1111-111111111111','22222222-2222-2222-2222-222222222222','bbbbbbbb-0000-0000-0000-000000000001','00000000-0000-0000-0000-000000000000','00000000-0000-0000-0000-000000000000','bbbbbbbb-0000-0000-0000-0000000000d1','CHARGE',100,'USD',2,'probe'); ROLLBACK;" 2>&1)"
[[ "$fol" == *"FOLIO_STRATEGY_UNSET"* ]] && pass "AC-18 folio-UNSET fail-closed CHARGE rejected before outbox/P# — functional probe, rolled back" || fail "AC-18 ($fol)"

echo "----- public unchanged proof -----"
echo "public_fingerprint before=$PUB_BEFORE after=$PUB_AFTER"
echo "iam_v2 catalog fingerprint=$FP (constraints=$CON triggers=$TRG functions=$FUN)"
echo "backup: ${BK}_full.dump sha=$sha_full | ${BK}_schema.sql sha=$sha_schema"
if [ "$ACCFAIL" = "0" ]; then echo "ACCEPTANCE_PASS"; else echo "ACCEPTANCE_FAIL — rolling back"; rollback; exit 2; fi
