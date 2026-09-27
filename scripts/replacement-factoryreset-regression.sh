#!/usr/bin/env bash
# Regression: replacement overlap safety + factory-reset visibility (docs/CENTRAL_CONTROL_PLANE.md §4).
# Isolated zzz-* / ZZZ-* fixtures only; never touches real customers/appliances.
# Needs the regtest helper (go build -o /tmp/regtest ./control-plane/cmd/regtest).
# Run on Central:  ADMIN_EMAIL=... ADMIN_PASS=... bash replacement-factoryreset-regression.sh
set -u
API=${API:-http://127.0.0.1:8080}; CJ=/tmp/rfr.txt; REG=${REG:-/tmp/regtest}
ADMIN_EMAIL=${ADMIN_EMAIL:?set ADMIN_EMAIL}; ADMIN_PASS=${ADMIN_PASS:?set ADMIN_PASS}
PSQL() { docker exec sc-central-pg psql -U stayconnect -d stayconnect -tA -c "$1"; }
jqget() { python3 -c "import sys,json;d=json.load(sys.stdin);print(eval('d'+sys.argv[1]))" "$1" 2>/dev/null; }
code() { curl -s --max-time 15 -b $CJ -c $CJ -o /tmp/rb.json -w "%{http_code}" -H 'Content-Type: application/json' "$@"; }
PASS=0; FAIL=0; ok(){ echo "  PASS  $1"; PASS=$((PASS+1)); }; bad(){ echo "  FAIL  $1"; FAIL=$((FAIL+1)); }
curl -s --max-time 8 -c $CJ -o /dev/null -X POST $API/v1/auth/login -H 'Content-Type: application/json' \
  -d "{\"email\":\"$ADMIN_EMAIL\",\"password\":\"$ADMIN_PASS\"}"
reauth(){ curl -s --max-time 8 -b $CJ -c $CJ -o /dev/null -X POST $API/v1/auth/reauth -H 'Content-Type: application/json' -d "{\"password\":\"$ADMIN_PASS\"}"; }
CAVER=$(PSQL "SELECT version FROM appliance_ca_versions ORDER BY version DESC LIMIT 1;")
appid(){ PSQL "SELECT id::text FROM appliances WHERE serial='$1' ORDER BY updated_at DESC LIMIT 1;"; }
synthcert(){ PSQL "INSERT INTO appliance_certificates (appliance_id,tenant_id,site_id,serial,cert_serial,fingerprint_sha256,public_key_fingerprint,ca_version,not_before,not_after,status,cert_pem) VALUES ('$1','$TID','$SID','$2','0A','fp$RANDOM$RANDOM','pkf',$CAVER,now()-interval '1 day',now()+interval '360 days','active','X');" >/dev/null; }
activate(){ reauth; code -X POST "$API/cloud/v1/appliances/$1/activate" \
  -d "{\"customer_id\":\"$TID\",\"site_id\":\"$SID\",\"license\":{\"max_concurrent_online_guests\":20,\"valid_days\":365}}"; }
clean(){ PSQL "BEGIN;
 DELETE FROM appliance_security_alerts WHERE serial LIKE 'ZZZ-%';
 UPDATE appliances SET replaced_by=NULL, replacement_of=NULL WHERE serial LIKE 'ZZZ-%';
 DELETE FROM licenses WHERE tenant_id IN (SELECT id FROM tenants WHERE slug LIKE 'zzz-%');
 DELETE FROM appliances WHERE tenant_id IN (SELECT id FROM tenants WHERE slug LIKE 'zzz-%') OR serial LIKE 'ZZZ-%';
 DELETE FROM sites WHERE tenant_id IN (SELECT id FROM tenants WHERE slug LIKE 'zzz-%');
 DELETE FROM tenants WHERE slug LIKE 'zzz-%';
 COMMIT;" >/dev/null 2>&1; }
licstat(){ PSQL "SELECT status FROM licenses WHERE '$1'=ANY(appliance_ids) ORDER BY license_version DESC LIMIT 1;"; }

echo "=== setup ==="; clean
[ -x "$REG" ] || { echo "regtest helper missing: go build -o $REG ./control-plane/cmd/regtest"; exit 2; }
code -X POST $API/cloud/v1/customers -d '{"slug":"zzz-rfr","name":"ZZZ RFR"}' >/dev/null; TID=$(jqget "['id']" </tmp/rb.json)
code -X POST $API/cloud/v1/customers/$TID/sites -d '{"code":"rfr","name":"RFR Site","timezone":"UTC"}' >/dev/null; SID=$(jqget "['id']" </tmp/rb.json)
echo "TID=$TID SID=$SID"

echo ""
echo "############ REQUIREMENT 1: REPLACEMENT OVERLAP SAFETY ############"
$REG -base $API -serial ZZZ-OLD >/dev/null; OLD=$(appid ZZZ-OLD)
activate "$OLD" >/dev/null; synthcert "$OLD" ZZZ-OLD
[ "$(licstat "$OLD")" = "active" ] && ok "OLD appliance activated + licensed" || bad "OLD not licensed ($(licstat "$OLD"))"

reauth; c=$(code -X POST "$API/cloud/v1/appliances/$OLD/replace" -d '{"reason":"reg replace"}')
pend=$(PSQL "SELECT replacement_pending FROM appliances WHERE id='$OLD';"); dl=$(PSQL "SELECT replacement_deadline IS NOT NULL FROM appliances WHERE id='$OLD';")
[ "$c" = "200" ] && [ "$pend" = "t" ] && [ "$dl" = "t" ] && [ "$(licstat "$OLD")" = "active" ] \
  && ok "replace: old still licensed (continuity), replacement_pending + bounded deadline set" || bad "replace c=$c pend=$pend deadline=$dl lic=$(licstat "$OLD")"

$REG -base $API -serial ZZZ-NEW >/dev/null; NEW=$(appid ZZZ-NEW)
activate "$NEW" >/dev/null
os=$(PSQL "SELECT lifecycle_state FROM appliances WHERE id='$OLD';"); ol=$(licstat "$OLD"); rb=$(PSQL "SELECT replaced_by::text FROM appliances WHERE id='$OLD';"); ro=$(PSQL "SELECT replacement_of::text FROM appliances WHERE id='$NEW';"); nl=$(licstat "$NEW")
[ "$os" = "decommissioned" ] && [ "$ol" = "revoked" ] && ok "new activated -> OLD retired + licence revoked" || bad "old state=$os lic=$ol"
[ "$rb" = "$NEW" ] && [ "$ro" = "$OLD" ] && ok "old/new linked (replaced_by / replacement_of)" || bad "link rb=$rb ro=$ro"
[ "$nl" = "active" ] && ok "NEW appliance is licensed (service continuity preserved)" || bad "new lic=$nl"
oc=$(PSQL "SELECT status FROM appliance_certificates WHERE appliance_id='$OLD' ORDER BY created_at DESC LIMIT 1;")
[ "$oc" = "revoked" ] && ok "old credentials (certificate) revoked" || bad "old cert=$oc"
ot=$(PSQL "SELECT state FROM appliance_signed_assignments WHERE appliance_id='$OLD';")
[ "$ot" = "decommissioned" ] && ok "old appliance holds a signed terminal assignment" || bad "old assignment state=$ot"

c=$(activate "$NEW")
[ "$c" = "409" ] && [ "$(PSQL "SELECT count(*) FROM appliances WHERE replacement_of='$OLD';")" -le 1 ] \
  && ok "re-activating NEW is refused (409); no double replacement linkage" || bad "re-activate c=$c"

echo ""
echo "--- WINDOW EXPIRY: no completion within window -> alert + operator decision (NOT auto-terminated) ---"
$REG -base $API -serial ZZZ-EXP >/dev/null; EXPID=$(appid ZZZ-EXP)
activate "$EXPID" >/dev/null
reauth; code -X POST "$API/cloud/v1/appliances/$EXPID/replace" -d '{"reason":"reg expiry"}' >/dev/null
PSQL "UPDATE appliances SET replacement_deadline = now() - interval '1 hour' WHERE id='$EXPID';" >/dev/null
echo "  waiting for the 60s reconcile ticker..."; sleep 66
al=$(PSQL "SELECT count(*) FROM appliance_security_alerts WHERE appliance_id='$EXPID' AND kind='replacement_window_expired' AND resolved=false;")
es=$(PSQL "SELECT lifecycle_state FROM appliances WHERE id='$EXPID';"); el=$(licstat "$EXPID")
[ "$al" = "1" ] && ok "window elapsed -> visible replacement_window_expired alert raised" || bad "expiry alert count=$al"
[ "$es" = "assigned" ] && [ "$el" = "active" ] && ok "expiry: old NOT auto-terminated (still licensed, operator decision required)" || bad "expiry auto-terminated state=$es lic=$el"
sleep 3; al2=$(PSQL "SELECT count(*) FROM appliance_security_alerts WHERE appliance_id='$EXPID' AND kind='replacement_window_expired';")
[ "$al2" = "1" ] && ok "idempotent: alert not re-raised while open" || bad "alert re-raised count=$al2"

echo ""
echo "############ REQUIREMENT 2: FACTORY-RESET VISIBILITY ############"
$REG -base $API -serial ZZZ-FR >/dev/null; FR=$(appid ZZZ-FR)
activate "$FR" >/dev/null
OUT=$($REG -base $API -serial ZZZ-FR); echo "  factory-reset re-register -> $OUT"
echo "$OUT" | grep -q "HTTP 403" && ok "factory-reset re-register of an ACTIVATED box REJECTED (403) — no auto-transfer" || bad "expected 403, got: $OUT"
cnt=$(PSQL "SELECT count(*) FROM appliances WHERE serial='ZZZ-FR';")
[ "$cnt" = "1" ] && ok "no second/competing appliance row created" || bad "duplicate rows=$cnt"
det=$(PSQL "SELECT detail::text FROM appliance_security_alerts WHERE serial='ZZZ-FR' AND kind='hardware_reused' ORDER BY created_at DESC LIMIT 1;")
echo "$det" | grep -q "factory_reset" && echo "$det" | grep -q "$FR" && ok "alert links the SAME hardware to the previous appliance ($FR)" || bad "alert missing old<->new linkage: $det"
own=$(PSQL "SELECT COALESCE(tenant_id::text,'none') FROM appliances WHERE id='$FR';")
[ "$own" = "$TID" ] && ok "previous identity untouched" || bad "previous ownership changed: $own"

echo "--- after the operator retires it (emergency), the same hardware re-registers as a clean WAITING appliance ---"
reauth; code -X POST "$API/cloud/v1/appliances/$FR/retire" -d '{"reason":"reset box","emergency":true,"confirm_serial":"ZZZ-FR"}' >/dev/null
OUT=$($REG -base $API -serial ZZZ-FR)
st=$(PSQL "SELECT lifecycle_state || '/' || COALESCE(tenant_id::text,'none') FROM appliances WHERE id='$FR';")
echo "$OUT" | grep -q "HTTP 200" && [ "$st" = "pending_approval/none" ] && ok "retired hardware re-registers on the same row as WAITING, unowned" || bad "re-register after retire: $OUT state=$st"

echo ""
echo "############ INVARIANTS ############"
echo "  retired appliance with a current licence: $(PSQL "SELECT count(*) FROM appliances a JOIN licenses l ON a.id=ANY(l.appliance_ids) WHERE a.lifecycle_state IN ('revoked','decommissioned') AND l.status IN ('active','suspended');")"

echo ""; echo "=== cleanup ==="; clean; echo "zzz remaining: $(PSQL "SELECT count(*) FROM tenants WHERE slug LIKE 'zzz-%';")"
echo ""; echo "======== RESULT: PASS=$PASS FAIL=$FAIL ========"
rm -f $CJ
[ "$FAIL" = 0 ]
