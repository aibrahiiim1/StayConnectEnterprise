#!/usr/bin/env bash
# Appliance lifecycle consistency regression suite (docs/CENTRAL_CONTROL_PLANE.md §4).
#
# Exercises every lifecycle path against isolated zzz-* / ZZZ-* fixtures and asserts the invariants: no path
# leaves a current (active/suspended) licence bound to a retired or deleted appliance, credential state
# matches the lifecycle, licence operations never touch the appliance lifecycle, and the derived activation
# state is what the contract says. Idempotent (self-cleans); NEVER touches real customers or appliances.
#
# Needs the regtest helper (go build -o /tmp/regtest ./control-plane/cmd/regtest) for token-less registration.
#
# Run on Central:  ADMIN_EMAIL=... ADMIN_PASS=... bash lifecycle-regression.sh
set -u
API=${API:-http://127.0.0.1:8080}; CJ=/tmp/lifereg.txt; REG=${REG:-/tmp/regtest}
ADMIN_EMAIL=${ADMIN_EMAIL:?set ADMIN_EMAIL}; ADMIN_PASS=${ADMIN_PASS:?set ADMIN_PASS}
PSQL() { docker exec sc-central-pg psql -U stayconnect -d stayconnect -tA -c "$1"; }
jqget() { python3 -c "import sys,json;d=json.load(sys.stdin);print(eval('d'+sys.argv[1]))" "$1" 2>/dev/null; }
code() { curl -s --max-time 15 -b $CJ -c $CJ -o /tmp/lr.json -w "%{http_code}" -H 'Content-Type: application/json' "$@"; }
PASS=0; FAIL=0
ok()  { echo "  PASS  $1"; PASS=$((PASS+1)); }
bad() { echo "  FAIL  $1"; FAIL=$((FAIL+1)); }
curl -s --max-time 8 -c $CJ -o /dev/null -X POST $API/v1/auth/login -H 'Content-Type: application/json' \
  -d "{\"email\":\"$ADMIN_EMAIL\",\"password\":\"$ADMIN_PASS\"}"
reauth() { curl -s --max-time 8 -b $CJ -c $CJ -o /dev/null -X POST $API/v1/auth/reauth -H 'Content-Type: application/json' -d "{\"password\":\"$ADMIN_PASS\"}"; }
CAVER=$(PSQL "SELECT version FROM appliance_ca_versions ORDER BY version DESC LIMIT 1;")

clean() { PSQL "BEGIN;
 DELETE FROM licenses WHERE tenant_id IN (SELECT id FROM tenants WHERE slug LIKE 'zzz-%');
 DELETE FROM appliances WHERE tenant_id IN (SELECT id FROM tenants WHERE slug LIKE 'zzz-%') OR serial LIKE 'ZZZ-%';
 DELETE FROM sites WHERE tenant_id IN (SELECT id FROM tenants WHERE slug LIKE 'zzz-%');
 DELETE FROM operators WHERE tenant_id IN (SELECT id FROM tenants WHERE slug LIKE 'zzz-%');
 DELETE FROM tenants WHERE slug LIKE 'zzz-%';
 COMMIT;" >/dev/null 2>&1; }

# mkcustomer <sfx> -> TID SID
mkcustomer() {
  code -X POST $API/cloud/v1/customers -d "{\"name\":\"ZZZ $1\",\"slug\":\"zzz-$1\"}" >/dev/null; TID=$(jqget "['id']" </tmp/lr.json)
  code -X POST $API/cloud/v1/customers/$TID/sites -d "{\"name\":\"Site $1\",\"code\":\"$1\",\"timezone\":\"UTC\"}" >/dev/null; SID=$(jqget "['id']" </tmp/lr.json)
}
# register <serial> -> AID (a WAITING appliance, registered token-less by the regtest helper)
register() { AID=$($REG -base $API -serial "$1" | sed -n 's/^HTTP 200 //p' | jqget "['appliance_id']"); }
# mkfixture <sfx> -> TID SID AID, the appliance ACTIVATED at the site with a licence and a certificate
mkfixture() {
  mkcustomer "$1"; register "ZZZ-${1^^}-1"
  reauth; code -X POST $API/cloud/v1/appliances/$AID/activate \
    -d "{\"customer_id\":\"$TID\",\"site_id\":\"$SID\",\"license\":{\"max_concurrent_online_guests\":50,\"valid_days\":365}}" >/dev/null
  PSQL "INSERT INTO appliance_certificates (appliance_id,tenant_id,site_id,serial,cert_serial,fingerprint_sha256,public_key_fingerprint,ca_version,not_before,not_after,status,cert_pem) VALUES ('$AID','$TID','$SID','ZZZ-CERT','0A0B','deadbeef$RANDOM$RANDOM','pkf',$CAVER,now()-interval '1 day',now()+interval '360 days','active','PLACEHOLDER');" >/dev/null
}
lic()         { PSQL "SELECT status FROM licenses WHERE '$AID'=ANY(appliance_ids) ORDER BY license_version DESC LIMIT 1;"; }
cert_status() { PSQL "SELECT COALESCE((SELECT status FROM appliance_certificates WHERE appliance_id='$AID' ORDER BY created_at DESC LIMIT 1),'none');"; }
activation()  { code $API/cloud/v1/appliances/$AID >/dev/null; jqget "['activation']" </tmp/lr.json; }
global_orphans() { PSQL "SELECT count(*) FROM licenses l WHERE status IN ('active','suspended') AND cardinality(l.appliance_ids)>0
                         AND NOT EXISTS(SELECT 1 FROM appliances a WHERE a.id=ANY(l.appliance_ids) AND a.lifecycle_state='assigned');"; }

echo "=== pre-clean ==="; clean
[ -x "$REG" ] || { echo "regtest helper missing: go build -o $REG ./control-plane/cmd/regtest"; exit 2; }

echo ""; echo "### 1. DELETE a waiting appliance ###"
register "ZZZ-W1-1"
[ "$(activation)" = "waiting" ] && ok "registered appliance is waiting" || bad "activation=$(activation)"
reauth; c=$(code -X DELETE $API/cloud/v1/appliances/$AID -d '{"confirm_serial":"ZZZ-W1-1","reason":"reg"}')
[ "$c" = "204" ] && [ "$(PSQL "SELECT count(*) FROM appliances WHERE id='$AID';")" = "0" ] && ok "waiting appliance deleted" || bad "delete waiting c=$c"

echo ""; echo "### 2. DELETE refused for an activated appliance ###"
mkfixture d2
[ "$(activation)" = "activated" ] && ok "activated (assignment + licence + certificate)" || bad "activation=$(activation)"
reauth; c=$(code -X DELETE $API/cloud/v1/appliances/$AID -d '{"confirm_serial":"ZZZ-D2-1","reason":"reg"}')
[ "$c" = "409" ] && ok "delete of an activated appliance refused (409)" || bad "delete activated c=$c"

echo ""; echo "### 3. LICENCE SUSPEND never touches the lifecycle ###"
LID=$(PSQL "SELECT id FROM licenses WHERE '$AID'=ANY(appliance_ids) AND status='active';")
reauth; c=$(code -X POST $API/cloud/v1/licenses/$LID/suspend -d '{"reason":"reg"}')
[ "$c" = "200" ] && [ "$(PSQL "SELECT lifecycle_state FROM appliances WHERE id='$AID';")" = "assigned" ] && [ "$(activation)" = "activated" ] \
  && ok "suspend: lifecycle stays assigned, activation stays activated" || bad "suspend c=$c"

echo ""; echo "### 4. RETIRE (normal, two-phase): licence revoked now, certificate awaits the ack ###"
mkfixture d4
reauth; c=$(code -X POST $API/cloud/v1/appliances/$AID/retire -d '{"reason":"reg normal retire"}')
dstate=$(PSQL "SELECT delivery_state FROM appliance_terminal_delivery WHERE appliance_id='$AID';")
[ "$c" = "200" ] && [ "$(lic)" = "revoked" ] && ok "retire revoked the licence immediately" || bad "retire c=$c lic=$(lic)"
[ "$(cert_status)" = "active" ] && [ "$dstate" = "terminal_delivery_pending" ] && [ "$(activation)" = "retiring" ] \
  && ok "retire: certificate kept, delivery pending, activation=retiring" || bad "retire cert=$(cert_status) dstate=$dstate"

echo ""; echo "### 5. RETIRE (emergency): licence + certificate revoked at once ###"
mkfixture d5
reauth; c=$(code -X POST $API/cloud/v1/appliances/$AID/retire -d '{"reason":"reg emergency","emergency":true,"confirm_serial":"ZZZ-D5-1"}')
[ "$c" = "200" ] && [ "$(lic)" = "revoked" ] && [ "$(cert_status)" = "revoked" ] && [ "$(activation)" = "retired" ] \
  && ok "emergency: licence + certificate revoked, activation=retired" || bad "emergency c=$c lic=$(lic) cert=$(cert_status)"
reauth; c=$(code -X DELETE $API/cloud/v1/appliances/$AID -d '{"confirm_serial":"ZZZ-D5-1","reason":"reg"}')
[ "$c" = "204" ] && ok "a retired appliance's record deletes" || bad "delete retired c=$c"

echo ""; echo "### 6. REPLACE: the old appliance keeps its licence until the new one is activated ###"
mkfixture d6
reauth; c=$(code -X POST $API/cloud/v1/appliances/$AID/replace -d '{"reason":"reg replace"}')
OLD=$AID
[ "$c" = "200" ] && [ "$(lic)" = "active" ] && [ "$(PSQL "SELECT replacement_pending FROM appliances WHERE id='$AID';")" = "t" ] \
  && ok "replace: licence kept, replacement_pending set" || bad "replace c=$c lic=$(lic)"
register "ZZZ-D6-2"
reauth; code -X POST $API/cloud/v1/appliances/$AID/activate -d "{\"customer_id\":\"$TID\",\"site_id\":\"$SID\",\"license\":{\"valid_days\":30}}" >/dev/null
AID=$OLD
[ "$(lic)" = "revoked" ] && [ "$(activation)" = "retired" ] && ok "activating the replacement retired the old appliance" || bad "old lic=$(lic) act=$(activation)"

echo ""; echo "### 7. MOVE to another customer: the licence is revoked ###"
mkfixture d7; T7=$TID
mkcustomer d7b
reauth; c=$(code -X POST $API/cloud/v1/appliances/$AID/move -d "{\"customer_id\":\"$TID\",\"site_id\":\"$SID\",\"reason\":\"reg move\"}")
[ "$c" = "200" ] && [ "$(lic)" = "revoked" ] && ok "cross-customer move revoked the licence" || bad "move c=$c lic=$(lic)"

echo ""; echo "### 8. MOVE within the customer: the licence is re-issued, terms kept ###"
mkfixture d8
code -X POST $API/cloud/v1/customers/$TID/sites -d '{"name":"Annex d8","timezone":"UTC"}' >/dev/null; S2=$(jqget "['id']" </tmp/lr.json)
reauth; c=$(code -X POST $API/cloud/v1/appliances/$AID/move -d "{\"customer_id\":\"$TID\",\"site_id\":\"$S2\",\"reason\":\"reg move\"}")
[ "$c" = "200" ] && [ "$(lic)" = "active" ] && [ "$(PSQL "SELECT max_concurrent_online_guests FROM licenses WHERE '$AID'=ANY(appliance_ids) AND status='active';")" = "50" ] \
  && ok "same-customer move re-issued the licence with the same cap" || bad "move c=$c lic=$(lic)"

echo ""; echo "### 9. SITE DELETE refused while an appliance is there ###"
reauth; c=$(code -X DELETE $API/cloud/v1/sites/$S2 -d '{"confirm":"Annex d8","reason":"reg"}')
[ "$c" = "409" ] && ok "site delete refused (409)" || bad "site delete c=$c"

echo ""; echo "### INVARIANT: no current licence bound to an appliance that is not activated ###"
go=$(global_orphans); [ "$go" = "0" ] && ok "orphan current-licence count = 0" || bad "orphaned current licences: $go"
echo "  audit events this run: $(PSQL "SELECT string_agg(DISTINCT action, ',') FROM audit_log WHERE ts > now() - interval '5 minutes' AND (action LIKE 'appliance.%' OR action LIKE 'license.%');")"

echo ""; echo "=== cleanup ==="; clean; echo "zzz remaining: $(PSQL "SELECT count(*) FROM tenants WHERE slug LIKE 'zzz-%';")"
echo ""; echo "======== RESULT: PASS=$PASS FAIL=$FAIL ========"
rm -f $CJ
[ "$FAIL" = 0 ]
