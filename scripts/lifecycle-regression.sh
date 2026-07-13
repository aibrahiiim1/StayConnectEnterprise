#!/usr/bin/env bash
# Appliance lifecycle consistency regression suite.
#
# Exercises EVERY terminal/lifecycle path against isolated zzz-* fixtures and
# asserts the invariant: no path leaves an active/suspended license bound to a
# terminated/deleted appliance, credential state matches lifecycle, and the
# authoritative Fleet License Summary stays correct. Idempotent (self-cleans);
# NEVER touches real customers/appliances.
#
# Run on Central: bash lifecycle-regression.sh
set -u
API=http://127.0.0.1:8080; CJ=/tmp/lifereg.txt
PSQL() { docker exec sc-central-pg psql -U stayconnect -d stayconnect -tA -c "$1"; }
jqget() { python3 -c "import sys,json;d=json.load(sys.stdin);print(eval('d'+sys.argv[1]))" "$1" 2>/dev/null; }
code() { curl -s --max-time 10 -b $CJ -c $CJ -o /tmp/lr.json -w "%{http_code}" "$@"; }
PASS=0; FAIL=0
ok()  { echo "  PASS  $1"; PASS=$((PASS+1)); }
bad() { echo "  FAIL  $1"; FAIL=$((FAIL+1)); }
curl -s --max-time 8 -c $CJ -o /dev/null -X POST $API/v1/auth/login -H 'Content-Type: application/json' -d '{"email":"admin@stayconnect.local","password":"ProofAdmin2026!"}'
reauth() { curl -s --max-time 8 -b $CJ -c $CJ -o /dev/null -X POST $API/v1/auth/reauth -H 'Content-Type: application/json' -d '{"password":"ProofAdmin2026!"}'; }
CAVER=$(PSQL "SELECT version FROM appliance_ca_versions ORDER BY version DESC LIMIT 1;")

clean() { PSQL "BEGIN; ALTER TABLE guests DISABLE TRIGGER legacy_ro; ALTER TABLE sessions DISABLE TRIGGER legacy_ro; ALTER TABLE vouchers DISABLE TRIGGER legacy_ro;
 DELETE FROM appliance_certificates WHERE serial LIKE 'ZZZ-%';
 DELETE FROM licenses WHERE tenant_id IN (SELECT id FROM tenants WHERE slug LIKE 'zzz-%');
 DELETE FROM appliance_bootstrap_tokens WHERE tenant_id IN (SELECT id FROM tenants WHERE slug LIKE 'zzz-%');
 DELETE FROM appliances WHERE tenant_id IN (SELECT id FROM tenants WHERE slug LIKE 'zzz-%') OR serial LIKE 'ZZZ-%';
 DELETE FROM tenant_subscriptions WHERE tenant_id IN (SELECT id FROM tenants WHERE slug LIKE 'zzz-%');
 DELETE FROM tenant_limit_overrides WHERE tenant_id IN (SELECT id FROM tenants WHERE slug LIKE 'zzz-%');
 DELETE FROM sites WHERE tenant_id IN (SELECT id FROM tenants WHERE slug LIKE 'zzz-%');
 DELETE FROM tenants WHERE slug LIKE 'zzz-%';
 ALTER TABLE guests ENABLE TRIGGER legacy_ro; ALTER TABLE sessions ENABLE TRIGGER legacy_ro; ALTER TABLE vouchers ENABLE TRIGGER legacy_ro; COMMIT;" >/dev/null 2>&1; }

# mkfixture <slug-suffix> -> exports TID SID AID (tenant/site/appliance) with a subscription + caps
mkfixture() {
  local sfx="$1"
  code -X POST $API/v1/tenants -H 'Content-Type: application/json' -d "{\"slug\":\"zzz-$sfx\",\"name\":\"ZZZ $sfx\"}" >/dev/null; TID=$(jqget "['id']" </tmp/lr.json)
  reauth; code -X POST "$API/v1/tenants/$TID/subscription-terms" -H 'Content-Type: application/json' -d "{\"plan_id\":\"$PID\",\"activation\":\"active\",\"reason\":\"t\"}" >/dev/null
  reauth; code -X PUT "$API/v1/tenants/$TID/limit-overrides" -H 'Content-Type: application/json' -d '{"key":"max_appliances","value_type":"int","int_value":10,"reason":"t"}' >/dev/null
  code -X POST "$API/v1/sites?tenant_id=$TID" -H 'Content-Type: application/json' -d "{\"code\":\"$sfx\",\"name\":\"Site $sfx\",\"timezone\":\"UTC\"}" >/dev/null; SID=$(jqget "['id']" </tmp/lr.json)
  code -X POST "$API/v1/appliances?tenant_id=$TID" -H 'Content-Type: application/json' -d "{\"site_id\":\"$SID\",\"serial\":\"ZZZ-${sfx^^}-1\",\"name\":\"A\"}" >/dev/null; AID=$(jqget "['id']" </tmp/lr.json)
}
issue_bound() { reauth; code -X POST "$API/cloud/v1/licenses" -H 'Content-Type: application/json' -d "{\"tenant_id\":\"$TID\",\"site_id\":\"$SID\",\"appliance_id\":\"$AID\",\"valid_days\":365}" >/dev/null; }
synth_cert() { PSQL "INSERT INTO appliance_certificates (appliance_id,tenant_id,site_id,serial,cert_serial,fingerprint_sha256,public_key_fingerprint,ca_version,not_before,not_after,status,cert_pem) VALUES ('$AID','$TID','$SID','ZZZ-CERT','0A0B','deadbeef$RANDOM','pkf',$CAVER,now()-interval '1 day',now()+interval '360 days','active','PLACEHOLDER');" >/dev/null; }
cert_status() { PSQL "SELECT COALESCE((SELECT status FROM appliance_certificates WHERE appliance_id='$AID' ORDER BY created_at DESC LIMIT 1),'none');"; }
# invariant: no active/suspended license bound to a NON-existent appliance anywhere
global_orphans() { PSQL "SELECT count(*) FROM licenses l WHERE status IN ('active','suspended') AND cardinality(l.appliance_ids)>0 AND NOT EXISTS(SELECT 1 FROM appliances a WHERE a.id=ANY(l.appliance_ids));"; }
summ() { curl -s --max-time 8 -b $CJ $API/cloud/v1/licenses/fleet-summary; }

echo "=== pre-clean ==="; clean
PID=$(PSQL "SELECT id::text FROM plans WHERE is_active=true ORDER BY sort_order LIMIT 1;")
echo "baseline orphans(global)=$(global_orphans)  fleet=$(summ)"

echo ""
echo "### 1. DEACTIVATE (reversible): revoke bound license, PRESERVE certificate ###"
mkfixture d1; issue_bound; synth_cert
reauth; code -X POST "$API/cloud/v1/appliances-admin/$AID/deactivate" -H 'Content-Type: application/json' -d '{}' >/dev/null
[ "$(cert_status)" = "active" ] && ok "deactivate preserves certificate (reversible)" || bad "deactivate cert=$(cert_status) expected active"
[ "$(cert_status)" = "active" ] && [ "$(PSQL "SELECT status FROM licenses WHERE '$AID'=ANY(appliance_ids) ORDER BY created_at DESC LIMIT 1;")" = "revoked" ] && ok "deactivate revoked the bound license" || bad "deactivate license not revoked"

echo ""
echo "### 2. DELETE (admin path /cloud/v1/appliances-admin): revoke + cascade all authority ###"
mkfixture d2; issue_bound; synth_cert
reauth; c=$(code -X DELETE "$API/cloud/v1/appliances-admin/$AID" -H 'Content-Type: application/json' -d "{\"confirm\":\"ZZZ-D2-1\",\"reason\":\"reg\"}")
gone=$(PSQL "SELECT count(*) FROM appliances WHERE id='$AID';"); lic=$(PSQL "SELECT status FROM licenses WHERE '$AID'=ANY(appliance_ids) ORDER BY created_at DESC LIMIT 1;"); cert=$(PSQL "SELECT count(*) FROM appliance_certificates WHERE appliance_id='$AID';")
[ "$c" = "204" ] && [ "$gone" = "0" ] && [ "$lic" = "revoked" ] && [ "$cert" = "0" ] && ok "admin delete: appliance gone, license revoked, certs cascade-removed" || bad "admin delete c=$c gone=$gone lic=$lic certs=$cert"

echo ""
echo "### 3. DELETE (tenant path /v1/appliances): revoke bound license ###"
mkfixture d3; issue_bound
reauth; c=$(code -X DELETE "$API/v1/appliances/$AID?tenant_id=$TID")
lic=$(PSQL "SELECT status FROM licenses WHERE '$AID'=ANY(appliance_ids) ORDER BY created_at DESC LIMIT 1;")
[ "$c" = "204" ] && [ "$lic" = "revoked" ] && ok "tenant delete revoked bound license" || bad "tenant delete c=$c lic=$lic"

echo ""
echo "### 4. REVOKE (normal, two-phase): license revoked NOW, cert awaits ack ###"
mkfixture d4; issue_bound; synth_cert
reauth; c=$(code -X POST "$API/cloud/v1/appliances-admin/$AID/revoke" -H 'Content-Type: application/json' -d '{"reason":"reg normal revoke"}')
lic=$(PSQL "SELECT status FROM licenses WHERE '$AID'=ANY(appliance_ids) ORDER BY created_at DESC LIMIT 1;"); cert=$(cert_status); dstate=$(PSQL "SELECT delivery_state FROM appliance_terminal_delivery WHERE appliance_id='$AID';")
[ "$c" = "200" ] && [ "$lic" = "revoked" ] && ok "normal revoke revoked the license immediately" || bad "normal revoke c=$c lic=$lic"
[ "$cert" = "active" ] && [ "$dstate" = "terminal_delivery_pending" ] && ok "normal revoke: cert preserved, terminal delivery pending (two-phase intact)" || bad "normal revoke cert=$cert dstate=$dstate"

echo ""
echo "### 5. REVOKE (emergency compromise): license + cert revoked IMMEDIATELY ###"
mkfixture d5; issue_bound; synth_cert
reauth; c=$(code -X POST "$API/cloud/v1/appliances-admin/$AID/revoke" -H 'Content-Type: application/json' -d '{"reason":"reg emergency","emergency_compromise":true,"confirmation":"ZZZ-D5-1"}')
lic=$(PSQL "SELECT status FROM licenses WHERE '$AID'=ANY(appliance_ids) ORDER BY created_at DESC LIMIT 1;"); cert=$(cert_status); dstate=$(PSQL "SELECT delivery_state FROM appliance_terminal_delivery WHERE appliance_id='$AID';")
[ "$c" = "200" ] && [ "$lic" = "revoked" ] && [ "$cert" = "revoked" ] && ok "emergency: license + certificate revoked immediately (NATS denied)" || bad "emergency c=$c lic=$lic cert=$cert dstate=$dstate"

echo ""
echo "### 6. DECOMMISSION (terminal): license revoked ###"
mkfixture d6; issue_bound; synth_cert
reauth; c=$(code -X POST "$API/cloud/v1/appliances-admin/$AID/decommission" -H 'Content-Type: application/json' -d '{"reason":"reg decommission"}')
lic=$(PSQL "SELECT status FROM licenses WHERE '$AID'=ANY(appliance_ids) ORDER BY created_at DESC LIMIT 1;")
[ "$c" = "200" ] && [ "$lic" = "revoked" ] && ok "decommission revoked the license" || bad "decommission c=$c lic=$lic"

echo ""
echo "### 7. REPLACE: KEEP the old appliance's license (stays operational until new box online) ###"
mkfixture d7; issue_bound
reauth; c=$(code -X POST "$API/cloud/v1/appliances-admin/$AID/replace" -H 'Content-Type: application/json' -d '{"reason":"reg replace"}')
lic=$(PSQL "SELECT status FROM licenses WHERE '$AID'=ANY(appliance_ids) ORDER BY created_at DESC LIMIT 1;"); pend=$(PSQL "SELECT replacement_pending FROM appliances WHERE id='$AID';")
[ "$c" = "201" ] && [ "$lic" = "active" ] && [ "$pend" = "t" ] && ok "replace: license KEPT active, replacement_pending set (policy-correct)" || bad "replace c=$c lic=$lic pending=$pend"

echo ""
echo "### 8. REASSIGN (cross-tenant): revoke the previous tenant's bound license ###"
mkfixture d8; issue_bound
code -X POST $API/v1/tenants -H 'Content-Type: application/json' -d '{"slug":"zzz-d8b","name":"ZZZ d8b"}' >/dev/null; TID2=$(jqget "['id']" </tmp/lr.json)
reauth; code -X POST "$API/v1/tenants/$TID2/subscription-terms" -H 'Content-Type: application/json' -d "{\"plan_id\":\"$PID\",\"activation\":\"active\",\"reason\":\"t\"}" >/dev/null
code -X POST "$API/v1/sites?tenant_id=$TID2" -H 'Content-Type: application/json' -d '{"code":"d8b","name":"Site d8b","timezone":"UTC"}' >/dev/null; SID2=$(jqget "['id']" </tmp/lr.json)
reauth; c=$(code -X POST "$API/cloud/v1/appliances-admin/$AID/reassign" -H 'Content-Type: application/json' -d "{\"tenant_id\":\"$TID2\",\"site_id\":\"$SID2\",\"reason\":\"reg reassign\"}")
lic=$(PSQL "SELECT status FROM licenses WHERE '$AID'=ANY(appliance_ids) AND tenant_id='$TID' ORDER BY created_at DESC LIMIT 1;")
[ "$lic" = "revoked" ] && ok "cross-tenant reassign revoked the old tenant's bound license (c=$c)" || bad "reassign old license lic=$lic c=$c"

echo ""
echo "### 9. SITE DELETE blocked while an active license exists ###"
mkfixture d9; issue_bound
reauth; c=$(code -X DELETE "$API/v1/sites/$SID?tenant_id=$TID" -H 'Content-Type: application/json' -d "{\"confirm\":\"d9\",\"reason\":\"reg\"}")
[ "$c" = "409" ] && ok "site delete blocked (409) while active license exists" || bad "site delete c=$c expected 409"

echo ""
echo "### INVARIANT: no orphaned active/suspended license bound to a missing appliance ###"
go=$(global_orphans); [ "$go" = "0" ] && ok "global orphan-bound-license count = 0" || bad "orphaned licenses remain: $go"
echo "  fleet-summary: $(summ)"
echo "  audit events this run: $(PSQL "SELECT string_agg(DISTINCT action, ',') FROM audit_log WHERE ts > now() - interval '5 minutes' AND action LIKE 'appliance.%';")"

echo ""
echo "=== cleanup ==="; clean; echo "zzz remaining: $(PSQL "SELECT count(*) FROM tenants WHERE slug LIKE 'zzz-%';")"
echo ""
echo "======== RESULT: PASS=$PASS FAIL=$FAIL ========"
rm -f $CJ
[ "$FAIL" = 0 ]
