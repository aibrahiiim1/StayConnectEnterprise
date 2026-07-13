#!/usr/bin/env bash
# Regression: replacement overlap safety + factory-reset visibility.
# Isolated zzz-* fixtures only; never touches real customers/appliances.
# Run on Central: bash replacement-factoryreset-regression.sh
set -u
API=http://127.0.0.1:8080; CJ=/tmp/rfr.txt; REG=/tmp/regtest
PSQL() { docker exec sc-central-pg psql -U stayconnect -d stayconnect -tA -c "$1"; }
jqget() { python3 -c "import sys,json;d=json.load(sys.stdin);print(eval('d'+sys.argv[1]))" "$1" 2>/dev/null; }
code() { curl -s --max-time 15 -b $CJ -c $CJ -o /tmp/rb.json -w "%{http_code}" "$@"; }
PASS=0; FAIL=0; ok(){ echo "  PASS  $1"; PASS=$((PASS+1)); }; bad(){ echo "  FAIL  $1"; FAIL=$((FAIL+1)); }
curl -s --max-time 8 -c $CJ -o /dev/null -X POST $API/v1/auth/login -H 'Content-Type: application/json' -d '{"email":"admin@stayconnect.local","password":"ProofAdmin2026!"}'
reauth(){ curl -s --max-time 8 -b $CJ -c $CJ -o /dev/null -X POST $API/v1/auth/reauth -H 'Content-Type: application/json' -d '{"password":"ProofAdmin2026!"}'; }
CAVER=$(PSQL "SELECT version FROM appliance_ca_versions ORDER BY version DESC LIMIT 1;")
appid(){ PSQL "SELECT id::text FROM appliances WHERE serial='$1' ORDER BY updated_at DESC LIMIT 1;"; }
synthcert(){ PSQL "INSERT INTO appliance_certificates (appliance_id,tenant_id,site_id,serial,cert_serial,fingerprint_sha256,public_key_fingerprint,ca_version,not_before,not_after,status,cert_pem) VALUES ('$1','$TID','$SID','$2','0A','fp$RANDOM$RANDOM','pkf',$CAVER,now()-interval '1 day',now()+interval '360 days','active','X');" >/dev/null; }
clean(){ PSQL "BEGIN; ALTER TABLE guests DISABLE TRIGGER legacy_ro; ALTER TABLE sessions DISABLE TRIGGER legacy_ro; ALTER TABLE vouchers DISABLE TRIGGER legacy_ro;
 DELETE FROM appliance_security_alerts WHERE serial LIKE 'ZZZ-%';
 DELETE FROM appliance_certificates WHERE serial LIKE 'ZZZ-%';
 UPDATE appliances SET replaced_by=NULL, replacement_of=NULL WHERE serial LIKE 'ZZZ-%';
 DELETE FROM licenses WHERE tenant_id IN (SELECT id FROM tenants WHERE slug LIKE 'zzz-%');
 DELETE FROM appliance_bootstrap_tokens WHERE tenant_id IN (SELECT id FROM tenants WHERE slug LIKE 'zzz-%');
 DELETE FROM appliances WHERE tenant_id IN (SELECT id FROM tenants WHERE slug LIKE 'zzz-%') OR serial LIKE 'ZZZ-%';
 DELETE FROM tenant_subscriptions WHERE tenant_id IN (SELECT id FROM tenants WHERE slug LIKE 'zzz-%');
 DELETE FROM tenant_limit_overrides WHERE tenant_id IN (SELECT id FROM tenants WHERE slug LIKE 'zzz-%');
 DELETE FROM sites WHERE tenant_id IN (SELECT id FROM tenants WHERE slug LIKE 'zzz-%');
 DELETE FROM tenants WHERE slug LIKE 'zzz-%';
 ALTER TABLE guests ENABLE TRIGGER legacy_ro; ALTER TABLE sessions ENABLE TRIGGER legacy_ro; ALTER TABLE vouchers ENABLE TRIGGER legacy_ro; COMMIT;" >/dev/null 2>&1; }
licstat(){ PSQL "SELECT status FROM licenses WHERE '$1'=ANY(appliance_ids) ORDER BY created_at DESC LIMIT 1;"; }

echo "=== setup ==="; clean
code -X POST $API/v1/tenants -H 'Content-Type: application/json' -d '{"slug":"zzz-rfr","name":"ZZZ RFR"}' >/dev/null; TID=$(jqget "['id']" </tmp/rb.json)
PID=$(PSQL "SELECT id::text FROM plans WHERE is_active=true ORDER BY sort_order LIMIT 1;")
reauth; code -X POST "$API/v1/tenants/$TID/subscription-terms" -H 'Content-Type: application/json' -d "{\"plan_id\":\"$PID\",\"activation\":\"active\",\"reason\":\"t\"}" >/dev/null
reauth; code -X PUT "$API/v1/tenants/$TID/limit-overrides" -H 'Content-Type: application/json' -d '{"key":"max_appliances","value_type":"int","int_value":10,"reason":"t"}' >/dev/null
code -X POST "$API/v1/sites?tenant_id=$TID" -H 'Content-Type: application/json' -d '{"code":"rfr","name":"RFR Site","timezone":"UTC"}' >/dev/null; SID=$(jqget "['id']" </tmp/rb.json)
echo "TID=$TID SID=$SID"

echo ""
echo "############ REQUIREMENT 1: REPLACEMENT OVERLAP SAFETY ############"
echo "--- activate OLD appliance ---"
$REG --serial ZZZ-OLD >/dev/null; OLD=$(appid ZZZ-OLD); synthcert "$OLD" ZZZ-OLD
reauth; code -X POST "$API/cloud/v1/appliances-admin/$OLD/activate" -H 'Content-Type: application/json' -d "{\"tenant_id\":\"$TID\",\"site_id\":\"$SID\"}" >/dev/null
[ "$(licstat "$OLD")" = "active" ] && ok "OLD appliance activated + licensed" || bad "OLD not licensed ($(licstat "$OLD"))"

echo "--- start REPLACEMENT: old stays licensed (continuity), bounded deadline set ---"
reauth; c=$(code -X POST "$API/cloud/v1/appliances-admin/$OLD/replace" -H 'Content-Type: application/json' -d '{"reason":"reg replace"}')
pend=$(PSQL "SELECT replacement_pending FROM appliances WHERE id='$OLD';"); dl=$(PSQL "SELECT replacement_deadline IS NOT NULL FROM appliances WHERE id='$OLD';")
[ "$c" = "201" ] && [ "$pend" = "t" ] && [ "$dl" = "t" ] && [ "$(licstat "$OLD")" = "active" ] && ok "replace: old still licensed (continuity), replacement_pending + bounded deadline set" || bad "replace c=$c pend=$pend deadline=$dl lic=$(licstat "$OLD")"

echo "--- activate NEW at same site -> OLD auto-terminated ---"
$REG --serial ZZZ-NEW >/dev/null; NEW=$(appid ZZZ-NEW); synthcert "$NEW" ZZZ-NEW
reauth; code -X POST "$API/cloud/v1/appliances-admin/$NEW/activate" -H 'Content-Type: application/json' -d "{\"tenant_id\":\"$TID\",\"site_id\":\"$SID\"}" >/dev/null
os=$(PSQL "SELECT lifecycle_state FROM appliances WHERE id='$OLD';"); ol=$(licstat "$OLD"); rb=$(PSQL "SELECT replaced_by::text FROM appliances WHERE id='$OLD';"); ro=$(PSQL "SELECT replacement_of::text FROM appliances WHERE id='$NEW';"); nl=$(licstat "$NEW")
# old license is terminated: revoked, or superseded by the new appliance's license (same site) — both are non-active/terminal
{ [ "$os" = "decommissioned" ] && { [ "$ol" = "revoked" ] || [ "$ol" = "superseded" ]; }; } && ok "new Active -> OLD auto-decommissioned + license terminated ($ol)" || bad "old state=$os lic=$ol"
[ "$rb" = "$NEW" ] && [ "$ro" = "$OLD" ] && ok "old/new linked (replaced_by / replacement_of)" || bad "link rb=$rb ro=$ro"
[ "$nl" = "active" ] && ok "NEW appliance is licensed/active (service continuity preserved)" || bad "new lic=$nl"
oc=$(PSQL "SELECT status FROM appliance_certificates WHERE appliance_id='$OLD' ORDER BY created_at DESC LIMIT 1;")
[ "$oc" = "revoked" ] && ok "old credentials (certificate) revoked -> API/NATS access terminated" || bad "old cert=$oc"

echo "--- idempotency: re-activate NEW must not re-terminate / double-effect ---"
reauth; code -X POST "$API/cloud/v1/appliances-admin/$NEW/activate" -H 'Content-Type: application/json' -d "{\"tenant_id\":\"$TID\",\"site_id\":\"$SID\"}" >/dev/null
[ "$(PSQL "SELECT count(*) FROM appliances WHERE replacement_of='$OLD';")" -le 1 ] && ok "idempotent: no double replacement linkage" || bad "double linkage"

echo ""
echo "--- WINDOW EXPIRY: no completion within window -> alert + operator decision (NOT auto-terminated) ---"
$REG --serial ZZZ-EXP >/dev/null; EXPID=$(appid ZZZ-EXP); synthcert "$EXPID" ZZZ-EXP
reauth; code -X POST "$API/cloud/v1/appliances-admin/$EXPID/activate" -H 'Content-Type: application/json' -d "{\"tenant_id\":\"$TID\",\"site_id\":\"$SID\"}" >/dev/null
reauth; code -X POST "$API/cloud/v1/appliances-admin/$EXPID/replace" -H 'Content-Type: application/json' -d '{"reason":"reg expiry"}' >/dev/null
PSQL "UPDATE appliances SET replacement_deadline = now() - interval '1 hour' WHERE id='$EXPID';" >/dev/null
echo "  waiting for the 60s reconcile ticker..."; sleep 66
al=$(PSQL "SELECT count(*) FROM appliance_security_alerts WHERE appliance_id='$EXPID' AND kind='replacement_window_expired' AND resolved=false;")
es=$(PSQL "SELECT lifecycle_state FROM appliances WHERE id='$EXPID';"); el=$(licstat "$EXPID")
[ "$al" = "1" ] && ok "window elapsed -> visible replacement_window_expired alert raised" || bad "expiry alert count=$al"
[ "$es" != "decommissioned" ] && [ "$el" = "active" ] && ok "expiry: old NOT auto-terminated (still licensed, operator decision required)" || bad "expiry auto-terminated state=$es lic=$el"
sleep 3; al2=$(PSQL "SELECT count(*) FROM appliance_security_alerts WHERE appliance_id='$EXPID' AND kind='replacement_window_expired';")
[ "$al2" = "1" ] && ok "idempotent: alert not re-raised while open" || bad "alert re-raised count=$al2"

echo ""
echo "############ REQUIREMENT 2: FACTORY-RESET VISIBILITY ############"
echo "--- activate a box, then re-register SAME serial with a NEW identity (factory reset) ---"
$REG --serial ZZZ-FR >/dev/null; FR=$(appid ZZZ-FR); synthcert "$FR" ZZZ-FR
reauth; code -X POST "$API/cloud/v1/appliances-admin/$FR/activate" -H 'Content-Type: application/json' -d "{\"tenant_id\":\"$TID\",\"site_id\":\"$SID\"}" >/dev/null
FRSTATE=$(PSQL "SELECT lifecycle_state FROM appliances WHERE id='$FR';")
OUT=$($REG --serial ZZZ-FR); echo "  factory-reset re-register -> $OUT"
echo "$OUT" | grep -q "HTTP 403" && ok "factory-reset re-register REJECTED (403) — not auto-activated, no ownership transfer" || bad "expected 403, got: $OUT"
cnt=$(PSQL "SELECT count(*) FROM appliances WHERE serial='ZZZ-FR';")
[ "$cnt" = "1" ] && ok "no second/competing appliance row created (single identity system)" || bad "duplicate rows=$cnt"
det=$(PSQL "SELECT detail::text FROM appliance_security_alerts WHERE serial='ZZZ-FR' AND kind='hardware_reused' ORDER BY created_at DESC LIMIT 1;")
echo "  alert detail: $det"
echo "$det" | grep -q "factory_reset" && echo "$det" | grep -q "$FR" && echo "$det" | grep -q "same_hardware_serial" && ok "alert clearly links SAME hardware to previous appliance ($FR, state $FRSTATE)" || bad "alert missing old<->new linkage"
own=$(PSQL "SELECT COALESCE(tenant_id::text,'none') FROM appliances WHERE id='$FR';")
[ "$own" = "$TID" ] && ok "previous identity untouched (no auto-delete / auto-transfer / auto-activate)" || bad "previous ownership changed: $own"

echo ""
echo "############ INVARIANTS ############"
echo "  orphaned active/suspended licenses (missing appliance): $(PSQL "SELECT count(*) FROM licenses l WHERE status IN ('active','suspended') AND cardinality(l.appliance_ids)>0 AND NOT EXISTS(SELECT 1 FROM appliances a WHERE a.id=ANY(l.appliance_ids));")"
echo "  terminal-state appliance w/ active license: $(PSQL "SELECT count(*) FROM appliances a JOIN licenses l ON a.id=ANY(l.appliance_ids) WHERE a.lifecycle_state IN ('revoked','decommissioned') AND l.status IN ('active','suspended');")"
echo "  fleet-summary: $(curl -s --max-time 8 -b $CJ $API/cloud/v1/licenses/fleet-summary)"

echo ""; echo "=== cleanup ==="; clean; echo "zzz remaining: $(PSQL "SELECT count(*) FROM tenants WHERE slug LIKE 'zzz-%';")  real: $(PSQL "SELECT string_agg(slug,chr(44)) FROM tenants;")"
echo ""; echo "======== RESULT: PASS=$PASS FAIL=$FAIL ========"
rm -f $CJ
[ "$FAIL" = 0 ]
