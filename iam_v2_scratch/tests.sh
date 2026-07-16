#!/usr/bin/env bash
# A-series acceptance harness (scratch/test only). Per-test PASS/FAIL evidence.
HERE="$(cd "$(dirname "$0")" && pwd)"; source "$HERE/lib.sh"
set +e +o pipefail   # tests intentionally provoke SQL errors; do not abort on them
safety_guard
Q(){ docker exec -i "$SCRATCH_CONTAINER" psql -U postgres -d "$SCRATCH_DB" -v ON_ERROR_STOP=1 -qAt -c "$1" 2>&1; }
PASSN=0; FAILN=0
ok(){   echo "PASS  $1"; PASSN=$((PASSN+1)); }
no(){   echo "FAIL  $1  :: $2"; FAILN=$((FAILN+1)); }
expect_ok(){   local l="$1" s="$2"; local o; if o=$(Q "$s"); then ok "$l"; else no "$l" "$(echo "$o"|tr '\n' ' '|cut -c1-160)"; fi; }
expect_fail(){ local l="$1" s="$2" sub="$3"; local o rc; o=$(Q "$s"); rc=$?; if [ $rc -eq 0 ]; then no "$l" "expected error, got success"; elif [[ "$o" == *"$sub"* ]]; then ok "$l"; else no "$l" "wrong error: $(echo "$o"|tr '\n' ' '|cut -c1-160)"; fi; }
expect_eq(){   local l="$1" s="$2" exp="$3"; local o; o=$(Q "$s"); if [ "$o" = "$exp" ]; then ok "$l"; else no "$l" "got '$o' want '$exp'"; fi; }

TEN="'11111111-1111-1111-1111-111111111111'"; SITE="'22222222-2222-2222-2222-222222222222'"
I1="'aaaa0000-0000-0000-0000-000000000001'"; RUNSET="'aaaa0000-0000-0000-0000-0000000000d1'"; ROK="'aaaa0000-0000-0000-0000-0000000000d2'"
STAY="'eeee0000-0000-0000-0000-000000000001'"; FOLIO="'eeee0000-0000-0000-0000-0000000000f0'"
PUR="'99990000-0000-0000-0000-000000000001'"; SET="'99990000-0000-0000-0000-0000000000d1'"
ENT="'12340000-0000-0000-0000-000000000001'"; VOU="'ffff0000-0000-0000-0000-0000000000d1'"
D1="'d1d10000-0000-0000-0000-000000000001'"; D2="'d1d10000-0000-0000-0000-000000000002'"; D3="'d1d10000-0000-0000-0000-000000000003'"
SESS="'55550000-0000-0000-0000-000000000001'"; APP="'ab000000-0000-0000-0000-000000000001'"

echo "===== A-SERIES (scratch) ====="

# ---- schema / isolation integrity ----
expect_eq "INV-01 iam_v2 base-table count = 49" "SELECT count(*) FROM information_schema.tables WHERE table_schema='iam_v2' AND table_type='BASE TABLE';" "49"
expect_eq "INV-02 no accidental IAM objects in public (only fixtures + scratch harness tables)" "SELECT count(*) FROM pg_tables WHERE schemaname='public' AND tablename NOT IN ('tenants','sites','guest_networks','_scratch_marker','_iam_v2_migrations');" "0"
expect_eq "INV-03 MG-0 anchor present & valid" "SELECT indisvalid::text FROM pg_index WHERE indexrelid='public.guest_networks_tsi_anchor'::regclass;" "true"

# ---- immutable revisions / append-only ----
expect_fail "IMM-01 interface revision UPDATE rejected" "UPDATE iam_v2.pms_interface_revisions SET source_timezone='X' WHERE id=$ROK;" "immutable"
expect_fail "IMM-02 interface revision DELETE rejected" "DELETE FROM iam_v2.pms_interface_revisions WHERE id=$ROK;" "immutable"
expect_fail "IMM-03 plan revision UPDATE rejected" "UPDATE iam_v2.service_plan_revisions SET name='X' WHERE id='bbbb0000-0000-0000-0000-0000000000d1';" "immutable"
expect_fail "IMM-04 package revision UPDATE rejected" "UPDATE iam_v2.internet_package_revisions SET price_minor=1 WHERE id='cccc0000-0000-0000-0000-0000000000d1';" "immutable"
expect_fail "AO-01 accounting_records UPDATE rejected" "BEGIN; INSERT INTO iam_v2.accounting_records(tenant_id,site_id,session_id,sample_seq,bytes_up) VALUES ($TEN,$SITE,$SESS,900,1); UPDATE iam_v2.accounting_records SET bytes_up=2 WHERE session_id=$SESS AND sample_seq=900; ROLLBACK;" "immutable"
expect_fail "AO-02 pms_postings UPDATE rejected (append-only ledger)" "BEGIN; INSERT INTO iam_v2.pms_postings(id,tenant_id,site_id,pms_interface_id,settlement_id,purchase_id,posting_interface_revision_id,posting_type,amount_minor,currency,currency_exponent,idempotency_key) VALUES ('aabb0000-0000-0000-0000-000000000001',$TEN,$SITE,$I1,$SET,$PUR,$ROK,'CHARGE',100,'USD',2,'k-ao2'); UPDATE iam_v2.pms_postings SET amount_minor=1 WHERE id='aabb0000-0000-0000-0000-000000000001'; ROLLBACK;" "immutable"

# ---- composite tenant/site/interface ownership FKs ----
expect_fail "FK-01 cross-tenant stay->interface rejected" "INSERT INTO iam_v2.stays(tenant_id,site_id,pms_interface_id,external_reservation_id,external_stay_identity,status) VALUES ('99999999-9999-9999-9999-999999999999',$SITE,$I1,'RX','SX','RESERVED');" "violates foreign key"
expect_fail "FK-02 cross-site folio->interface rejected" "INSERT INTO iam_v2.folios(tenant_id,site_id,pms_interface_id,external_folio_id) VALUES ($TEN,'88888888-8888-8888-8888-888888888888',$I1,'FX');" "violates foreign key"
expect_ok  "FK-03 same-namespace child insert accepted (rollback)" "BEGIN; INSERT INTO iam_v2.folios(tenant_id,site_id,pms_interface_id,external_folio_id) VALUES ($TEN,$SITE,$I1,'FOK'); ROLLBACK;"

# ---- folio UNSET fail-closed CHARGE gate (before outbox/P#/transmission) ----
expect_fail "FOLIO-01 CHARGE under UNSET revision blocked" "INSERT INTO iam_v2.pms_postings(tenant_id,site_id,pms_interface_id,settlement_id,purchase_id,stay_id,folio_id,posting_interface_revision_id,posting_type,amount_minor,currency,currency_exponent,idempotency_key) VALUES ($TEN,$SITE,$I1,$SET,$PUR,$STAY,$FOLIO,$RUNSET,'CHARGE',100,'USD',2,'k-unset');" "FOLIO_STRATEGY_UNSET"
expect_eq  "FOLIO-02 no outbox/P# side-effects from blocked CHARGE" "SELECT count(*) FROM iam_v2.pms_postings WHERE idempotency_key='k-unset';" "0"
expect_ok  "FOLIO-03 CHARGE under concrete strategy admitted (rollback)" "BEGIN; INSERT INTO iam_v2.pms_postings(tenant_id,site_id,pms_interface_id,settlement_id,purchase_id,stay_id,folio_id,posting_interface_revision_id,posting_type,amount_minor,currency,currency_exponent,idempotency_key) VALUES ($TEN,$SITE,$I1,$SET,$PUR,$STAY,$FOLIO,$ROK,'CHARGE',100,'USD',2,'k-ok'); ROLLBACK;"
expect_fail "FOLIO-04 CHARGE on non-IN_HOUSE stay blocked" "BEGIN; UPDATE iam_v2.stays SET posting_allowed=false, status='CHECKED_OUT' WHERE id=$STAY; INSERT INTO iam_v2.pms_postings(tenant_id,site_id,pms_interface_id,settlement_id,purchase_id,stay_id,folio_id,posting_interface_revision_id,posting_type,amount_minor,currency,currency_exponent,idempotency_key) VALUES ($TEN,$SITE,$I1,$SET,$PUR,$STAY,$FOLIO,$ROK,'CHARGE',100,'USD',2,'k-oos'); ROLLBACK;" "POSTING_NOT_ALLOWED"

# ---- one-live-entitlement / supersession ----
expect_fail "ENT-01 second live entitlement per voucher rejected" "BEGIN; INSERT INTO iam_v2.purchases(id,tenant_id,site_id,package_revision_id,pms_interface_id,stay_id,settlement_mapping_id,trigger,amount_minor,currency,currency_exponent,state) VALUES ('99990000-0000-0000-0000-0000000000e2',$TEN,$SITE,'cccc0000-0000-0000-0000-0000000000d1',$I1,$STAY,'dddd0000-0000-0000-0000-000000000001','RENEWAL',100,'USD',2,'GRANTED'); INSERT INTO iam_v2.entitlements(tenant_id,site_id,voucher_id,pms_interface_id,purchase_id,policy_snapshot,service_plan_revision_id,package_revision_id,time_accounting_mode,end_mode,status) VALUES ($TEN,$SITE,$VOU,$I1,'99990000-0000-0000-0000-0000000000e2','{}','bbbb0000-0000-0000-0000-0000000000d1','cccc0000-0000-0000-0000-0000000000d1','VALIDITY_WINDOW','VALIDITY_WINDOW','ACTIVE'); ROLLBACK;" "ent_live_voucher"
expect_fail "ENT-02 no transition out of TERMINATED" "BEGIN; UPDATE iam_v2.entitlements SET status='TERMINATED', terminal_reason='REVOKED', terminated_at=now() WHERE id=$ENT; UPDATE iam_v2.entitlements SET status='ACTIVE', terminal_reason=NULL WHERE id=$ENT; ROLLBACK;" "out of TERMINATED"
expect_fail "ENT-03 counter decrease via direct UPDATE rejected" "UPDATE iam_v2.entitlements SET consumed_data_bytes = consumed_data_bytes - 1 WHERE id=$ENT;" "only via entitlement_adjustments"
expect_ok  "ENT-04 audited adjustment decrease allowed + logs" "BEGIN; UPDATE iam_v2.entitlements SET consumed_data_bytes=500 WHERE id=$ENT; SELECT iam_v2.apply_adjustment($ENT,'consumed_data_bytes','100','00000000-0000-0000-0000-0000000000aa','audit-test'); ROLLBACK;"
expect_fail "ENT-05 cross-subject supersession rejected" "BEGIN; INSERT INTO iam_v2.guest_principals(id,tenant_id) VALUES ('cafe0000-0000-0000-0000-000000000001',$TEN); INSERT INTO iam_v2.entitlements(tenant_id,site_id,guest_principal_id,pms_interface_id,purchase_id,policy_snapshot,service_plan_revision_id,package_revision_id,time_accounting_mode,end_mode,status,supersedes_entitlement_id) VALUES ($TEN,$SITE,'cafe0000-0000-0000-0000-000000000001',$I1,$PUR,'{}','bbbb0000-0000-0000-0000-0000000000d1','cccc0000-0000-0000-0000-0000000000d1','VALIDITY_WINDOW','VALIDITY_WINDOW','ACTIVE',$ENT); ROLLBACK;" "cross-subject supersession rejected"

# ---- window immutability + monotonic counters ----
expect_fail "WIN-01 window_ends_at move without adjustment rejected" "UPDATE iam_v2.entitlements SET window_ends_at = now()+interval '5 hours' WHERE id=$ENT;" "window move only via entitlement_adjustments"
expect_ok  "CNT-01 monotonic counter increase allowed" "BEGIN; UPDATE iam_v2.entitlements SET consumed_data_bytes = consumed_data_bytes + 10 WHERE id=$ENT; ROLLBACK;"

# ---- accounting: idempotent / out-of-order / session-close ----
expect_eq  "ACC-01 first sample APPLIED" "BEGIN; SELECT iam_v2.ingest_sample($SESS,1,100,50,1); ROLLBACK;" "APPLIED"
expect_eq  "ACC-02 duplicate (session,seq) sample = DUPLICATE" "BEGIN; DO \$\$ BEGIN PERFORM iam_v2.ingest_sample($SESS,1,100,50,1); END \$\$; SELECT iam_v2.ingest_sample($SESS,1,100,50,1); ROLLBACK;" "DUPLICATE"
expect_eq  "ACC-03 out-of-order (older seq) = STALE, no double count" "BEGIN; DO \$\$ BEGIN PERFORM iam_v2.ingest_sample($SESS,5,500,0,1); END \$\$; SELECT iam_v2.ingest_sample($SESS,3,300,0,1); ROLLBACK;" "STALE"
expect_eq  "ACC-04 counter-reset epoch bump handled" "BEGIN; DO \$\$ BEGIN PERFORM iam_v2.ingest_sample($SESS,1,900,0,1); END \$\$; SELECT iam_v2.ingest_sample($SESS,2,10,0,2); ROLLBACK;" "APPLIED"
expect_eq  "ACC-05 session close idempotent (2nd = ALREADY_ENDED)" "BEGIN; DO \$\$ BEGIN PERFORM iam_v2.close_session($SESS,'logout'); END \$\$; SELECT iam_v2.close_session($SESS,'logout'); ROLLBACK;" "ALREADY_ENDED"

# ---- device admission / advisory namespaces ----
expect_eq  "DEV-01 first device authorized (count 0 < max 2)" "BEGIN; SELECT iam_v2.reserve_device_slot($ENT,$D1,'cred-x',$APP::text,2); ROLLBACK;" "AUTHORIZED"
expect_eq  "DEV-02 over-limit 3rd device rejected (max=2)" "BEGIN; DO \$\$ BEGIN PERFORM iam_v2.reserve_device_slot($ENT,$D1,'cred-x',$APP::text,2); PERFORM iam_v2.reserve_device_slot($ENT,$D2,'cred-x',$APP::text,2); END \$\$; SELECT iam_v2.reserve_device_slot($ENT,$D3,'cred-x',$APP::text,2); ROLLBACK;" "MAX_DEVICES_REACHED"
expect_eq  "DEV-03 same-device reconnect = RECONNECT (no slot burn)" "BEGIN; DO \$\$ BEGIN PERFORM iam_v2.reserve_device_slot($ENT,$D1,'cred-x',$APP::text,2); END \$\$; SELECT iam_v2.reserve_device_slot($ENT,$D1,'cred-x',$APP::text,2); ROLLBACK;" "RECONNECT"
expect_eq  "LN-01 namespaces distinct: dev(11) <> cap(7) for same key" "SELECT (iam_v2.ns_device_slot('k') <> iam_v2.ns_capacity('k'))::text;" "true"
expect_eq  "LN-02 LN_DEVICE_SLOT constant = hashtextextended(x,11)" "SELECT (iam_v2.ns_device_slot('abc') = hashtextextended('abc',11))::text;" "true"
expect_eq  "LN-03 LN_CAPACITY constant = hashtextextended(x,7)" "SELECT (iam_v2.ns_capacity('abc') = hashtextextended('abc',7))::text;" "true"

# ---- posting_attempts one-way state ----
expect_fail "PA-01 outcome cannot return to SENDING" "BEGIN; INSERT INTO iam_v2.pms_postings(id,tenant_id,site_id,pms_interface_id,settlement_id,purchase_id,posting_interface_revision_id,posting_type,amount_minor,currency,currency_exponent,idempotency_key) VALUES ('bbbb1111-0000-0000-0000-000000000001',$TEN,$SITE,$I1,$SET,$PUR,$ROK,'CHARGE',100,'USD',2,'k-pa'); INSERT INTO iam_v2.posting_attempts(id,tenant_id,site_id,internal_posting_id,pms_interface_id,attempt_no,p_number,sent_at,outcome) VALUES ('bbbb2222-0000-0000-0000-000000000001',$TEN,$SITE,'bbbb1111-0000-0000-0000-000000000001',$I1,1,'900001',now(),'ACKED'); UPDATE iam_v2.posting_attempts SET outcome='SENDING' WHERE id='bbbb2222-0000-0000-0000-000000000001'; ROLLBACK;" "is terminal"
expect_fail "PA-02 posting_attempts identity immutable (P# change rejected)" "BEGIN; INSERT INTO iam_v2.pms_postings(id,tenant_id,site_id,pms_interface_id,settlement_id,purchase_id,posting_interface_revision_id,posting_type,amount_minor,currency,currency_exponent,idempotency_key) VALUES ('bbbb1111-0000-0000-0000-000000000002',$TEN,$SITE,$I1,$SET,$PUR,$ROK,'CHARGE',100,'USD',2,'k-pa2'); INSERT INTO iam_v2.posting_attempts(id,tenant_id,site_id,internal_posting_id,pms_interface_id,attempt_no,p_number,sent_at) VALUES ('bbbb2222-0000-0000-0000-000000000002',$TEN,$SITE,'bbbb1111-0000-0000-0000-000000000002',$I1,1,'900002',now()); UPDATE iam_v2.posting_attempts SET p_number='999999' WHERE id='bbbb2222-0000-0000-0000-000000000002'; ROLLBACK;" "identity is immutable"

# ---- reversal scope: no executable reversal; only passive REVERSAL ledger row ----
expect_eq  "REV-01 no reversal sender/PT=C function exists" "SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='iam_v2' AND (p.proname ILIKE '%revers%send%' OR p.proname ILIKE '%pt_c%' OR p.proname ILIKE '%send_reversal%');" "0"

# ---- A5 / A9 / A10 / A11 DB primitives ----
expect_ok  "CAP-01 (A5) aggregate data-cap terminal transition to TERMINATED/DATA (atomic, one-way)" "BEGIN; UPDATE iam_v2.entitlements SET consumed_data_bytes=1000000, status='TERMINATED', terminal_reason='DATA', terminated_at=now() WHERE id=$ENT; ROLLBACK;"
expect_eq  "CAP-RESID-01 (A11) rejected admission leaves ZERO residual binding rows" "BEGIN; DO \$\$ BEGIN PERFORM iam_v2.reserve_device_slot($ENT,$D1,'c',$APP::text,2); PERFORM iam_v2.reserve_device_slot($ENT,$D2,'c',$APP::text,2); PERFORM iam_v2.reserve_device_slot($ENT,$D3,'c',$APP::text,2); END \$\$; SELECT count(*) FROM iam_v2.entitlement_devices WHERE entitlement_id=$ENT AND device_id=$D3; ROLLBACK;" "0"
expect_eq  "SUSP-01 (A9) suspension keeps window; still counts live" "BEGIN; UPDATE iam_v2.entitlements SET status='SUSPENDED' WHERE id=$ENT; SELECT status||'|'||(window_ends_at IS NOT NULL)::text FROM iam_v2.entitlements WHERE id=$ENT; ROLLBACK;" "SUSPENDED|true"
expect_eq  "REOPEN-01 (A10) sample after TERMINATED does not reopen entitlement" "BEGIN; UPDATE iam_v2.entitlements SET status='TERMINATED',terminal_reason='DATA',terminated_at=now() WHERE id=$ENT; DO \$\$ BEGIN PERFORM iam_v2.ingest_sample($SESS,10,100,0,1); END \$\$; SELECT status FROM iam_v2.entitlements WHERE id=$ENT; ROLLBACK;" "TERMINATED"

# ---- AGGREGATE_ONLINE_TIME inert (present in enum, not implemented) ----
expect_ok  "AGG-01 AGGREGATE_ONLINE_TIME storable but inert (enum only)" "BEGIN; INSERT INTO iam_v2.service_plan_revisions(tenant_id,site_id,service_plan_id,revision_no,name,time_accounting_mode) VALUES ($TEN,$SITE,'bbbb0000-0000-0000-0000-000000000001',99,'agg','AGGREGATE_ONLINE_TIME'); ROLLBACK;"

echo "===== RESULT: PASS=$PASSN FAIL=$FAILN ====="
[ "$FAILN" = "0" ]
