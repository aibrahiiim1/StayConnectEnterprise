#!/bin/bash
# 6. ROOM SIGN-IN (audit H-1, H-6; currency policy for room guests). Run as root on the appliance.
# ONE in-house stay from the live mirror, alone in its room, holding no access; never printed (id prefix only).
# Its surname is read into a variable for one request and never printed. Only a FREE fixture choice is taken;
# no room charge, no posting, no payment. The site's room sign-in mode is switched to room_lastname for the two
# requests that need it and restored EXACTLY (compared as text). The stay's test entitlement is ended at the end.
. "$(dirname "$0")/lib.sh"
DEFTIERS='[{"order":1,"grant":{}}]'; STAY=""
AM0=$($PSQL "SELECT auth_methods::text FROM tenants"); PMS0=$($PSQL "SELECT (auth_methods->'pms')::text FROM tenants")
restore_mode(){ code PUT /auth-methods "{\"pms\":$PMS0}" >/dev/null; }
room_cleanup(){
  [ "$($PSQL "SELECT auth_methods::text FROM tenants")" = "$AM0" ] || restore_mode
  if [ -n "$STAY" ]; then for e in $($PSQL "SELECT id FROM iam_v2.entitlements WHERE stay_id='$STAY' AND status IN ('PENDING','ACTIVE','SUSPENDED')"); do
    for s in $($PSQL "SELECT host(ip) FROM iam_v2.sessions WHERE entitlement_id='$e' AND ended IS NULL"); do curl -s --unix-socket /run/stayconnect/scd.sock -X POST -H 'Content-Type: application/json' -d "{\"ip\":\"$s\",\"reason\":\"admin\"}" http://x/v1/sessions/revoke >/dev/null; done
    $PSQL "SELECT iam_v2.apply_entitlement_transition('$e','TERMINATED',now(),'ADMIN')" >/dev/null 2>&1; done
    info "stay ${STAY:0:8}: entitlements left live = $($PSQL "SELECT count(*) FROM iam_v2.entitlements WHERE stay_id='$STAY' AND status IN ('PENDING','ACTIVE','SUSPENDED')")"; fi
  [ "$($PSQL "SELECT auth_methods::text FROM tenants")" = "$AM0" ] && info "auth_methods restored exactly" || echo "  FAIL  auth_methods NOT restored: $($PSQL "SELECT auth_methods::text FROM tenants")"; }
RESTORE_HOOK=room_cleanup
pms(){ ip netns exec $1 curl -s -H 'Content-Type: application/json' -d "$2" "$P/auth/pms/phase3"; }
rid(){ cat /proc/sys/kernel/random/uuid; }
lastroom(){ sleep 1; $PSQL "SELECT result||'/'||coalesce(reason_code,'-') FROM iam_v2.sign_in_attempts ORDER BY occurred_at DESC LIMIT 1" 2>/dev/null || $PSQL "SELECT result FROM iam_v2.sign_in_attempts ORDER BY occurred_at DESC LIMIT 1"; }
jf(){ printf '%s' "$1" | python3 -c "import json,sys;d=json.load(sys.stdin);print(d.get('$2',''))" 2>/dev/null; }

STAY=$($PSQL "SELECT s.id FROM iam_v2.stays s WHERE s.status='IN_HOUSE' AND s.external_reservation_id IS NOT NULL
  AND NOT EXISTS (SELECT 1 FROM iam_v2.entitlements e WHERE e.stay_id=s.id) AND NOT EXISTS (SELECT 1 FROM iam_v2.purchases pu WHERE pu.stay_id=s.id)
  AND (SELECT count(*) FROM iam_v2.stays o WHERE o.status='IN_HOUSE' AND o.normalized_room_number=s.normalized_room_number)=1
  AND (SELECT count(DISTINCT g.last_name_norm) FROM iam_v2.stay_guests g WHERE g.stay_id=s.id AND coalesce(g.last_name_norm,'')<>'')=1
  ORDER BY s.occupancy_evidence_at DESC NULLS LAST LIMIT 1")
[ -n "$STAY" ] || { blk "no suitable in-house stay (alone in its room, no access, one surname)"; exit 0; }
ROOM=$($PSQL "SELECT normalized_room_number FROM iam_v2.stays WHERE id='$STAY'"); RES=$($PSQL "SELECT external_reservation_id FROM iam_v2.stays WHERE id='$STAY'")
LN=$($PSQL "SELECT max(last_name_norm) FROM iam_v2.stay_guests WHERE stay_id='$STAY'")
info "test stay ${STAY:0:8} (in house, alone in its room, no access held); mode now: $(echo "$PMS0" | jget mode)"
PREV=$(mkplan "{\"code\":\"acc-room-$R4\",\"name\":\"Acc room 1 device\",\"down_kbps\":20000,\"up_kbps\":10000,\"max_concurrent_devices\":1,\"device_limit_policy\":\"REJECT_NEW_DEVICE\",\"time_accounting_mode\":\"VALIDITY_WINDOW\",\"speed_allocation\":\"PER_DEVICE\",\"create_only\":true}")
PK=$(mkpkg "{\"code\":\"acc-room1-$R4\",\"service_plan_revision_id\":\"$PREV\",\"display\":{\"name\":\"Acc room one device\"},\"duration_policy\":{\"end_mode\":\"VALIDITY_WINDOW\",\"duration_seconds\":3600},\"eligibility_rules\":[],\"grant_tiers\":$DEFTIERS,\"create_only\":true}")
case "$PK" in ERR*) bad "fixture package: $PK"; exit 1;; esac
PKREV=$($PSQL "SELECT current_revision_id FROM iam_v2.internet_packages WHERE id='$PK'")
USDREV=$($PSQL "SELECT current_revision_id FROM iam_v2.internet_packages WHERE id='$(cat /root/acceptance/usd.pkg 2>/dev/null)'" 2>/dev/null)
C1=r1$R4; C2=r2$R4; client $C1; client $C2

echo "== 1. room_any: the reservation number alone verifies"
j=$(pms $C1 "{\"room\":\"$ROOM\",\"verification\":\"$RES\",\"request_id\":\"$(rid)\"}")
printf '%s' "$j" | grep -q '"ok":true' && ok "room_any + reservation number -> verified ($(lastroom))" || bad "room_any verification refused: $(printf '%s' "$j" | head -c 160)"
ACX1=$(jf "$j" auth_context_id)
CH=$(printf '%s' "$j" | python3 -c 'import sys,json;print(json.dumps([(c.get("package_revision_id"),c.get("method"),c.get("currency") or (c.get("price") or {}).get("currency") if isinstance(c.get("price"),dict) else c.get("currency")) for c in json.load(sys.stdin).get("choices",[])]))' 2>/dev/null)
info "choices offered: $(printf '%s' "$CH" | python3 -c 'import json,sys;print(len(json.load(sys.stdin)))') (methods: $(printf '%s' "$j" | python3 -c 'import sys,json;print(sorted({c.get("method") for c in json.load(sys.stdin).get("choices",[])}))' 2>/dev/null))"
if [ -n "$USDREV" ]; then printf '%s' "$j" | python3 -c 'import sys,json;cs=json.load(sys.stdin).get("choices",[]);sys.exit(0 if any(c.get("package_revision_id")==sys.argv[1] and c.get("method")=="ONLINE_PAYMENT" for c in cs) else 1)' "$USDREV" \
  && ok "the USD card package is offered BY CARD to the room guest (provider converts)" || bad "USD card package not offered by card to the room guest"; fi
printf '%s' "$j" | python3 -c 'import sys,json;cs=json.load(sys.stdin).get("choices",[]);sys.exit(0 if any(c.get("package_revision_id")==sys.argv[1] and c.get("method")=="NOT_REQUIRED" for c in cs) else 1)' "$PKREV" && ok "the free 1-device fixture is offered to the room guest" || bad "fixture not among the choices"

echo "== 2. room_lastname (H-1): reservation-only is refused, the surname verifies"
NEWPMS=$(echo "$PMS0" | python3 -c 'import json,sys;d=json.load(sys.stdin);d["mode"]="room_lastname";print(json.dumps(d))')
c=$(code PUT /auth-methods "{\"pms\":$NEWPMS}"); [ "$c" = 200 ] && ok "site mode switched to room_lastname via the API" || bad "mode switch -> $c $(last)"
sleep 2
j=$(pms $C2 "{\"room\":\"$ROOM\",\"reservation_number\":\"$RES\",\"request_id\":\"$(rid)\"}")
printf '%s' "$j" | grep -q '"ok":true' && bad "H-1: reservation_number-only VERIFIED under room_lastname" || ok "reservation_number-only REFUSED under room_lastname ($(lastroom); '$(jf "$j" message | cut -c1-70)')"
j=$(pms $C2 "{\"room\":\"$ROOM\",\"last_name\":\"$LN\",\"request_id\":\"$(rid)\"}"); unset LN
printf '%s' "$j" | grep -q '"ok":true' && ok "the stay's surname VERIFIED under room_lastname ($(lastroom))" || bad "surname refused: $(printf '%s' "$j" | head -c 140)"
restore_mode; [ "$($PSQL "SELECT auth_methods::text FROM tenants")" = "$AM0" ] && ok "room_any restored exactly (auth_methods text identical)" || bad "auth_methods differ after restore"

echo "== 3. device 1 takes the FREE 1-device fixture; device 2 is offered the replacement page (H-6)"
j=$(pms $C1 "{\"auth_context_id\":\"$ACX1\",\"package_revision_id\":\"$PKREV\",\"request_id\":\"$(rid)\"}")
printf '%s' "$j" | grep -q '"ok":true' && ok "free choice granted to device 1" || bad "grant: $(printf '%s' "$j" | head -c 160)"
waiton $C1 && ok "device 1 online (204)" || bad "device 1 not online"
j=$(pms $C2 "{\"room\":\"$ROOM\",\"verification\":\"$RES\",\"request_id\":\"$(rid)\"}")
if printf '%s' "$j" | grep -q '"needs_choice":true'; then a2=$(jf "$j" auth_context_id); j=$(pms $C2 "{\"auth_context_id\":\"$a2\",\"package_revision_id\":\"$PKREV\",\"request_id\":\"$(rid)\"}"); fi
rt=$(jf "$j" redirect_to); [ "$rt" = /devices/replace ] && ok "device 2 at the 1-device limit -> redirect_to /devices/replace (H-6)" || bad "H-6: device 2 answer $(printf '%s' "$j" | head -c 200)"
[ "$(online $C2)" != 204 ] && ok "device 2 is not admitted silently" || bad "device 2 online over the limit"
ip netns exec $C2 curl -s -o /tmp/$C2.rep -c /tmp/$C2.jar -b /tmp/$C2.jar "$P/devices/replace"; info "replacement page: $(pagetext /tmp/$C2.rep 400 | grep -o -i 'replace[^.]*\.' | head -1 | cut -c1-120)"
[ "$($PSQL "SELECT count(*) FROM iam_v2.entitlements WHERE stay_id='$STAY'")" = 1 ] && ok "one entitlement for the stay" || bad "stay has $($PSQL "SELECT count(*) FROM iam_v2.entitlements WHERE stay_id='$STAY'") entitlements"
[ "$($PSQL "SELECT count(*) FROM iam_v2.purchases WHERE stay_id='$STAY' AND amount_minor>0")" = 0 ] && ok "no priced purchase for the stay" || bad "a priced purchase exists for the stay"
