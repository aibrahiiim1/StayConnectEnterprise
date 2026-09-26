#!/usr/bin/env bash
# GUEST ACCESS, END TO END, ON THE APPLIANCE (run as root on PRE-LIVE).
#
# Real clients on the live guest network -- network namespaces with a veth into the guest bridge, leased by the
# appliance's own DHCP -- walk the production path for every sign-in method: captive portal, sign-in, package
# list, free acquisition (quote -> confirm -> entitlement), activation, netd enforcement, real internet, then
# disconnect, rejoin, device limits, method switches and termination. Every assertion is on what the SERVER
# holds (session, entitlement, voucher, device authorization, the nft enforcement set) or on real packets
# leaving the appliance -- never on a success page alone.
#
# Nothing is fabricated: vouchers and accounts are issued through Hotel Admin's API, the packages are the
# site's own published free packages, and the PMS test uses one in-house stay from the live mirror, selected
# here and never printed (only a short stay id prefix is shown). Every credential created is revoked or
# disabled on exit, Hotel Admin's sign-in method settings are restored exactly, and the PMS test entitlement is
# ended so the stay holds no test access afterwards. No PMS posting, payment or financial action happens, and
# the run asserts that none did.
set -uo pipefail
PASS=0; FAIL=0
ok(){ echo "  ok   $1"; PASS=$((PASS+1)); }
bad(){ echo "  FAIL $1"; FAIL=$((FAIL+1)); }
R='--resolve hotel.stayconnect.local:443:127.0.0.1'
B=https://hotel.stayconnect.local/api/edge/v1
PSQL="docker exec stayconnect-pg psql -U stayconnect -d stayconnect_site -tAc"
CK=/tmp/ck.ga; NS=(); VIDS=(); ACCTS=(); PMS_ENT=""
PROBE=http://connectivitycheck.gstatic.com/generate_204
AM0=$($PSQL "SELECT auth_methods::text FROM tenants LIMIT 1")

cleanup() {
  for v in "${VIDS[@]:-}"; do [ -n "$v" ] && curl -sk $R -b $CK -o /dev/null -H 'Content-Type: application/json' \
      -d '{"password":"admin","reason":"guest-access e2e test cleanup"}' "$B/vouchers/$v/revoke"; done
  # An account that has signed in is referenced by its sign-in records, so it is disabled, not deleted.
  for a in "${ACCTS[@]:-}"; do [ -n "$a" ] && curl -sk $R -b $CK -o /dev/null -X PATCH -H 'Content-Type: application/json' \
      -d '{"enabled":false}' "$B/guest-accounts/$a"; done
  for n in "${NS[@]:-}"; do [ -n "$n" ] || continue
    ip netns exec "$n" curl -s -o /dev/null -m 5 -X POST "$PORTAL/logout" 2>/dev/null
    ip netns exec "$n" dhclient -r -lf /tmp/$n.lease -pf /tmp/$n.pid ${n}c >/dev/null 2>&1
    ip netns del "$n" 2>/dev/null; ip link del ${n}a 2>/dev/null; rm -rf /etc/netns/$n; done
  # The PMS test must leave the stay holding no test access.
  [ -n "$PMS_ENT" ] && $PSQL "SELECT iam_v2.apply_entitlement_transition('$PMS_ENT','TERMINATED',now(),'ADMIN')" >/dev/null 2>&1
  curl -sk $R -b $CK -o /dev/null -X PUT -H 'Content-Type: application/json' -d "$AM0" "$B/auth-methods"
  [ "$($PSQL "SELECT auth_methods::text FROM tenants LIMIT 1")" = "$AM0" ] && echo "  (Hotel Admin sign-in methods restored)" \
    || echo "  WARNING: sign-in methods NOT restored; original was $AM0"
}
trap cleanup EXIT

GB=$($PSQL "SELECT bridge_name FROM guest_networks WHERE enabled ORDER BY created_at LIMIT 1")
GW=$($PSQL "SELECT host(gateway_ip) FROM guest_networks WHERE enabled ORDER BY created_at LIMIT 1")
PORTAL="http://$GW:8380"
counts(){ $PSQL "SELECT (SELECT count(*) FROM iam_v2.pms_postings)||'/'||(SELECT count(*) FROM iam_v2.posting_outbox)||'/'||(SELECT count(*) FROM iam_v2.payment_transactions)"; }
FIN0=$(counts)

# client <name>: a device on the guest bridge; sets CIP.
client() {
  local n=$1; NS+=("$n"); ip netns del $n 2>/dev/null; ip link del ${n}a 2>/dev/null
  ip link add ${n}a type veth peer name ${n}c; ip link set ${n}a master "$GB"; ip link set ${n}a up
  ip netns add $n; ip link set ${n}c netns $n; ip netns exec $n ip link set lo up; ip netns exec $n ip link set ${n}c up
  sleep 1; ip netns exec $n timeout 25 dhclient -1 -lf /tmp/$n.lease -pf /tmp/$n.pid ${n}c >/dev/null 2>&1
  CIP=$(ip netns exec $n ip -4 -br addr show ${n}c | awk '{print $3}' | cut -d/ -f1)
  # The DNS server the lease handed out, as a real device would use it (ip netns exec mounts this file).
  local dns; dns=$(grep -o 'domain-name-servers [0-9.]*' /tmp/$n.lease 2>/dev/null | tail -1 | awk '{print $2}')
  mkdir -p /etc/netns/$n; echo "nameserver ${dns:-$GW}" > /etc/netns/$n/resolv.conf
  ip netns exec $n ping -c1 -W2 "$GW" >/dev/null 2>&1   # the portal refuses a device with no ARP entry
}
online(){ ip netns exec $1 curl -s -o /dev/null -w '%{http_code}' -m 8 "$PROBE"; }
# inset <ip>: the device is in netd's enforcement set. The listing is captured BEFORE it is searched: under
# pipefail, `nft list ... | grep -q` fails on a match whenever grep exits first and nft takes the SIGPIPE.
inset(){ local s; s=$(nft list set inet stayconnect phase3_auth_ipv4 2>/dev/null); grep -qE "\"?$GB\"? \. $1([^0-9]|\$)" <<<"$s"; }
# signin <ns> <path> <form...>: POST a sign-in form; prints the Location header; the page lands in /tmp/<ns>.html.
signin(){ local n=$1 p=$2; shift 2; rm -f /tmp/$n.jar
  ip netns exec $n curl -s -o /tmp/$n.html -D - -c /tmp/$n.jar -b /tmp/$n.jar "$@" "$PORTAL$p" | tr -d '\r' | awk 'tolower($1)=="location:"{print $2}'; }
acquire(){ local n=$1 pkg=$2
  ip netns exec $n curl -s -o /tmp/$n.html -D - -c /tmp/$n.jar -b /tmp/$n.jar --data-urlencode "package_id=$pkg" "$PORTAL/packages/acquire" \
    | tr -d '\r' | awk 'tolower($1)=="location:"{print $2}'; }
logout(){ ip netns exec $1 curl -s -o /dev/null -X POST "$PORTAL/logout"; sleep 5; }
sidof(){ printf '%s' "$1" | sed -n 's/.*[?&]s=\([^&]*\).*/\1/p'; }
# says <ns> <text>: the message the portal RENDERED for the guest (#server-error). The page also embeds every
# translated sentence in its script, so searching the whole page would match anything.
says(){ python3 -c '
import re, sys, html
t = open("/tmp/%s.html" % sys.argv[1], encoding="utf-8", errors="replace").read()
m = re.search(r"id=\"server-error\"[^>]*>(.*?)</div>", t, re.S)
sys.exit(0 if m and sys.argv[2].lower() in html.unescape(m.group(1)).lower() else 1)' "$1" "$2"; }
methods(){ curl -sk $R -b $CK -o /dev/null -X PUT -H 'Content-Type: application/json' -d "$1" "$B/auth-methods"; }
# roomsignin <ns> <room> <verification>: the JSON the portal page posts, including its fresh request id.
roomsignin(){ ip netns exec $1 curl -s -H 'Content-Type: application/json' \
  -d "$(python3 -c 'import json,sys,uuid;print(json.dumps({"room":sys.argv[1],"verification":sys.argv[2],"request_id":str(uuid.uuid4())}))' "$2" "$3")" "$PORTAL/auth/pms/phase3"; }
# lastroom: the result the operator's sign-in record holds for the most recent room attempt.
lastroom(){ sleep 1; $PSQL "SELECT result FROM iam_v2.sign_in_attempts ORDER BY occurred_at DESC LIMIT 1"; }

curl -sk $R -c $CK -o /dev/null -H 'Content-Type: application/json' -d '{"email":"admin","password":"admin"}' $B/auth/login
pkgrev(){ $PSQL "SELECT p.current_revision_id FROM iam_v2.internet_packages p JOIN iam_v2.internet_package_revisions r ON r.id=p.current_revision_id JOIN iam_v2.service_plan_revisions s ON s.id=r.service_plan_revision_id WHERE p.active AND NOT p.is_system AND r.price_minor=0 AND s.max_concurrent_devices $1 ORDER BY r.revision_no DESC LIMIT 1"; }
REV1=$(pkgrev "= 1"); PKG1=$($PSQL "SELECT package_id FROM iam_v2.internet_package_revisions WHERE id='$REV1'")
REVN=$(pkgrev "> 1"); PKGN=$($PSQL "SELECT package_id FROM iam_v2.internet_package_revisions WHERE id='$REVN'")
issue(){ local out b; out=$(curl -sk $R -b $CK -H 'Content-Type: application/json' \
    -d "{\"count\":1,\"package_revision_id\":\"${1:-$REV1}\",\"note\":\"guest-access e2e test\"}" "$B/vouchers/issue")
  CODE=$(printf '%s' "$out" | python3 -c 'import sys,json;print((json.load(sys.stdin).get("codes") or [""])[0])' 2>/dev/null)
  b=$(printf '%s' "$out" | python3 -c 'import sys,json;print(json.load(sys.stdin).get("batch_id",""))' 2>/dev/null)
  VID=$($PSQL "SELECT id FROM iam_v2.vouchers WHERE batch_id='$b' LIMIT 1"); VIDS+=("$VID"); }
account(){ local out; UN="e2e-$(date +%s%N | cut -c10-16)"
  out=$(curl -sk $R -b $CK -H 'Content-Type: application/json' -d "{\"username\":\"$UN\",\"generate\":true,\"notes\":\"guest-access e2e test\"}" "$B/guest-accounts")
  ACCT=$(printf '%s' "$out" | python3 -c 'import sys,json;d=json.load(sys.stdin);print(d.get("id") or (d.get("account") or {}).get("id",""))' 2>/dev/null)
  PW=$(printf '%s' "$out" | python3 -c 'import sys,json;d=json.load(sys.stdin);print(d.get("password") or d.get("generated_password",""))' 2>/dev/null)
  ACCTS+=("$ACCT"); }
ent_of_session(){ $PSQL "SELECT entitlement_id FROM iam_v2.sessions WHERE id='$1'"; }
devs_on(){ $PSQL "SELECT count(*) FROM iam_v2.entitlement_device_authorizations WHERE entitlement_id='$1' AND deauthorized_at IS NULL"; }

echo "== 0. setup: guest network $GB ($GW); 1-device package ${REV1:0:8}, multi-device package ${REVN:0:8} =="
issue; [ -n "$CODE" ] && [ -n "$VID" ] && ok "voucher issued through Hotel Admin (${VID:0:8})" || { bad "voucher issue failed"; exit 1; }
client ga1; CIP1=$CIP; [ -n "$CIP1" ] && ok "device 1 leased $CIP1" || { bad "device 1 got no lease"; exit 1; }

echo "== 1. before sign-in the device is captive =="
c=$(online ga1); [ "$c" != 204 ] && ok "no internet before sign-in (probe answered $c)" || bad "device was online before sign-in"
inset "$CIP1" && bad "device already in the enforcement set" || ok "device not in the enforcement set"

echo "== 2. invalid and revoked credentials fail =="
loc=$(signin ga1 /auth/voucher --data-urlencode "code=00000000")
[ -z "$loc" ] && ok "an invalid voucher is refused" || bad "invalid voucher -> $loc"
issue; RVID=$VID; RCODE=$CODE
curl -sk $R -b $CK -o /dev/null -H 'Content-Type: application/json' -d '{"password":"admin","reason":"e2e: revoked-credential check"}' "$B/vouchers/$RVID/revoke"
loc=$(signin ga1 /auth/voucher --data-urlencode "code=$RCODE")
[ -z "$loc" ] && ok "a revoked voucher is refused (state $($PSQL "SELECT state FROM iam_v2.vouchers WHERE id='$RVID'"))" || bad "revoked voucher -> $loc"
pj=$(roomsignin ga1 00000 e2e-no-such-guest)
! printf '%s' "$pj" | grep -q '"ok":true' && [ "$(lastroom)" = ROOM_NOT_IN_MIRROR ] && ok "room sign-in refused for a room that does not exist (recorded ROOM_NOT_IN_MIRROR)" || bad "non-existent room: $(lastroom) $pj"
issue   # the voucher the rest of the run uses (the first one is spent on nothing; it is revoked on exit)

echo "== 3. Hotel Admin's method switches are enforced by the backend, not only hidden =="
methods '{"voucher":{"enabled":false}}'
loc=$(signin ga1 /auth/voucher --data-urlencode "code=$CODE")
[ -z "$loc" ] && says ga1 "not available" && ok "voucher switched off: a valid voucher is refused ('not available')" || bad "disabled voucher method -> '$loc'"
[ "$($PSQL "SELECT state FROM iam_v2.vouchers WHERE id='$VID'")" = UNUSED ] && ok "the refused voucher was not touched" || bad "voucher state changed"
methods "$AM0"
account; U1=$UN; P1=$PW
methods '{"guest_account":{"enabled":false}}'
loc=$(signin ga1 /auth/credentials --data-urlencode "username=$U1" --data-urlencode "password=$P1")
[ -z "$loc" ] && says ga1 "not available" && ok "accounts switched off: correct credentials are refused ('not available')" || bad "disabled account method -> '$loc'"
methods '{"pms":{"enabled":false,"mode":"room_any","provider":"protel-fias"}}'
pj=$(roomsignin ga1 00000 x)
printf '%s' "$pj" | grep -q '"ok":false' && [ "$(lastroom)" = SERVICE_UNAVAILABLE ] && ok "room sign-in switched off: refused before any room lookup (recorded SERVICE_UNAVAILABLE)" || bad "disabled PMS method: $(lastroom) $pj"
methods "$AM0"; methods '{"guest_account":{"enabled":true}}'   # accounts on for the account tests below

echo "== 4. voucher: sign-in -> packages -> acquisition -> activation -> enforcement -> internet =="
loc=$(signin ga1 /auth/voucher --data-urlencode "code=$CODE")
[ "$loc" = "/packages" ] && ok "voucher authenticated; package selection" || bad "voucher sign-in went to '$loc'"
[ "$($PSQL "SELECT state FROM iam_v2.vouchers WHERE id='$VID'")" = UNUSED ] && ok "voucher still UNUSED after authentication alone" || bad "voucher changed state at authentication"
ip netns exec ga1 curl -s -o /tmp/ga1.pk -b /tmp/ga1.jar "$PORTAL/packages"
N=$(grep -c 'name="package_id"' /tmp/ga1.pk); grep -q "value=\"$PKG1\"" /tmp/ga1.pk && [ "$N" = 1 ] && ok "exactly the voucher's own package is offered" || bad "$N packages offered; own offered: $(grep -c "$PKG1" /tmp/ga1.pk)"
loc=$(acquire ga1 "$PKG1"); SID=$(sidof "$loc")
[ -n "$SID" ] && ok "acquisition redirected to success only after activation" || bad "acquire went to '$loc'"
VENT=$(ent_of_session "$SID")
row=$($PSQL "SELECT s.state||'|'||e.status||'|'||pu.state FROM iam_v2.sessions s JOIN iam_v2.entitlements e ON e.id=s.entitlement_id JOIN iam_v2.purchases pu ON pu.id=e.purchase_id WHERE s.id='$SID'")
[ "$row" = "active|ACTIVE|GRANTED" ] && ok "session active (set by netd), entitlement ACTIVE, purchase GRANTED" || bad "state $row"
[ "$($PSQL "SELECT state FROM iam_v2.vouchers WHERE id='$VID'")" = REDEEMED ] && ok "voucher REDEEMED at acquisition" || bad "voucher not redeemed"
[ "$(devs_on "$VENT")" = 1 ] && ok "one device authorized on the entitlement" || bad "device authorizations: $(devs_on "$VENT")"
inset "$CIP1" && ok "enforcement: device in nft set phase3_auth_ipv4" || bad "device not enforced"
c=$(online ga1); [ "$c" = 204 ] && ok "REAL INTERNET: $PROBE answered 204" || bad "no internet after activation ($c)"
ip netns exec ga1 ping -c2 -W3 1.1.1.1 >/dev/null 2>&1 && ok "ICMP to 1.1.1.1 passes" || bad "ICMP fails"

echo "== 5. voucher: device limit, disconnect, rejoin, termination =="
client ga2; CIP2=$CIP
loc=$(signin ga2 /auth/voucher --data-urlencode "code=$CODE")
[ -z "$loc" ] && says ga2 "device limit" && ok "a second device on a 1-device voucher is refused at the device limit" || bad "second device -> '$loc'"
c=$(online ga2); [ "$c" != 204 ] && ok "the refused device stays captive" || bad "refused device online"
c=$(online ga1); [ "$c" = 204 ] && ok "the first device stayed online" || bad "first device knocked offline ($c)"
logout ga1
[ "$($PSQL "SELECT state FROM iam_v2.sessions WHERE id='$SID'")" != active ] && ok "disconnect ended the session" || bad "session still active"
inset "$CIP1" && bad "still enforced after disconnect" || ok "enforcement removed on disconnect"
c=$(online ga1); [ "$c" != 204 ] && ok "captive again after disconnect ($c)" || bad "online after disconnect"
loc=$(signin ga1 /auth/voucher --data-urlencode "code=$CODE"); SID2=$(sidof "$loc")
[ -n "$SID2" ] && [ "$(ent_of_session "$SID2")" = "$VENT" ] && ok "the same voucher reconnects the device to its SAME entitlement, straight to success" || bad "voucher reconnect -> '$loc'"
c=$(online ga1); [ "$c" = 204 ] && ok "online again after reconnect (204)" || bad "reconnect probe $c"
[ "$($PSQL "SELECT count(*) FROM iam_v2.entitlements WHERE voucher_id='$VID'")" = 1 ] && ok "still exactly one entitlement for the voucher (no fresh quota)" || bad "voucher has several entitlements"
$PSQL "SELECT iam_v2.apply_entitlement_transition('$VENT','TERMINATED',now(),'ADMIN')" >/dev/null; sleep 6
c=$(online ga1); [ "$c" != 204 ] && ok "ending the entitlement takes the device offline ($c)" || bad "still online after the entitlement ended"
loc=$(signin ga1 /auth/voucher --data-urlencode "code=$CODE")
[ -z "$loc" ] && ok "a redeemed voucher whose access ended cannot sign in again (single use)" || bad "ended voucher -> '$loc'"

echo "== 6. the success-page panel path (/api/commerce/*) ends enforced, or not at all =="
issue; client ga3; CIP3=$CIP
signin ga3 /auth/voucher --data-urlencode "code=$CODE" >/dev/null
j=$(ip netns exec ga3 curl -s -b /tmp/ga3.jar "$PORTAL/api/commerce/packages")
P3=$(printf '%s' "$j" | python3 -c 'import sys,json;print((json.load(sys.stdin).get("packages") or [{}])[0].get("package_id",""))' 2>/dev/null)
q=$(ip netns exec ga3 curl -s -b /tmp/ga3.jar -H 'Content-Type: application/json' -d "{\"package_id\":\"$P3\"}" "$PORTAL/api/commerce/quote")
QID=$(printf '%s' "$q" | python3 -c 'import sys,json;print(json.load(sys.stdin).get("quote_id",""))' 2>/dev/null)
c=$(ip netns exec ga3 curl -s -b /tmp/ga3.jar -H 'Content-Type: application/json' -d "{\"quote_id\":\"$QID\"}" "$PORTAL/api/commerce/confirm")
ENF=$(printf '%s' "$c" | python3 -c 'import sys,json;print(json.load(sys.stdin).get("enforced"))' 2>/dev/null)
[ "$ENF" = True ] && inset "$CIP3" && [ "$(online ga3)" = 204 ] && ok "panel confirm answered only after enforcement; device online (204)" || bad "panel path: $c"
logout ga3

echo "== 7. guest account: purchase once, second device joins, limit holds, reconnect rejoins =="
account; U=$UN; P=$PW; A=$ACCT
loc=$(signin ga2 /auth/credentials --data-urlencode "username=$U" --data-urlencode "password=wrong-$P")
[ -z "$loc" ] && ok "a wrong account password is refused" || bad "wrong password -> $loc"
loc=$(signin ga2 /auth/credentials --data-urlencode "username=$U" --data-urlencode "password=$P")
[ "$loc" = "/packages" ] && ok "account authenticated; package selection (no access held yet)" || bad "account sign-in -> '$loc'"
ip netns exec ga2 curl -s -o /tmp/ga2.pk -b /tmp/ga2.jar "$PORTAL/packages"
grep -q "value=\"$PKGN\"" /tmp/ga2.pk && ok "the multi-device package is offered to the account" || bad "multi-device package not offered"
loc=$(acquire ga2 "$PKGN"); ASID=$(sidof "$loc"); AENT=$(ent_of_session "$ASID")
[ -n "$ASID" ] && inset "$CIP2" && [ "$(online ga2)" = 204 ] && ok "account device A online on the multi-device package (204)" || bad "account acquire -> '$loc'"
MAXD=$($PSQL "SELECT sp.max_concurrent_devices FROM iam_v2.entitlements e JOIN iam_v2.service_plan_revisions sp ON sp.id=e.service_plan_revision_id WHERE e.id='$AENT'")
client ga4; CIP4=$CIP
loc=$(signin ga4 /auth/credentials --data-urlencode "username=$U" --data-urlencode "password=$P"); BSID=$(sidof "$loc")
[ -n "$BSID" ] && [ "$(ent_of_session "$BSID")" = "$AENT" ] && ok "device B JOINED the account's live entitlement (no purchase page, no new grant)" || bad "device B -> '$loc'"
[ "$(online ga4)" = 204 ] && [ "$(online ga2)" = 204 ] && ok "both devices online at once (limit $MAXD)" || bad "A=$(online ga2) B=$(online ga4)"
[ "$($PSQL "SELECT count(*) FROM iam_v2.entitlements WHERE guest_account_id='$A'")" = 1 ] && [ "$(devs_on "$AENT")" = 2 ] && ok "one entitlement, two authorized devices" || bad "entitlements/devices wrong"
logout ga2
loc=$(signin ga2 /auth/credentials --data-urlencode "username=$U" --data-urlencode "password=$P"); RSID=$(sidof "$loc")
[ -n "$RSID" ] && [ "$(ent_of_session "$RSID")" = "$AENT" ] && [ "$(online ga2)" = 204 ] && ok "account device A reconnects to the same entitlement (204)" || bad "account reconnect -> '$loc'"
logout ga2; logout ga4
# The device limit, on the 1-device package with a fresh account.
account; U=$UN; P=$PW
signin ga2 /auth/credentials --data-urlencode "username=$U" --data-urlencode "password=$P" >/dev/null
loc=$(acquire ga2 "$PKG1")
[ -n "$(sidof "$loc")" ] && [ "$(online ga2)" = 204 ] && ok "second account online on the 1-device package" || bad "1-device acquire -> '$loc'"
loc=$(signin ga4 /auth/credentials --data-urlencode "username=$U" --data-urlencode "password=$P")
[ -z "$loc" ] && says ga4 "device limit" && [ "$(online ga4)" != 204 ] && ok "another device is refused at the 1-device limit and stays captive" || bad "over-limit device -> '$loc'"
[ "$(online ga2)" = 204 ] && ok "the device holding the slot stays online" || bad "slot holder knocked offline"
logout ga2

echo "== 8. PMS room sign-in: one in-house stay from the live mirror (not printed) =="
methods '{"guest_account":{"enabled":false}}'
STAY=$($PSQL "SELECT s.id FROM iam_v2.stays s WHERE s.status='IN_HOUSE' AND s.external_reservation_id IS NOT NULL
  AND NOT EXISTS (SELECT 1 FROM iam_v2.entitlements e WHERE e.stay_id=s.id AND e.status IN ('PENDING','ACTIVE','SUSPENDED'))
  AND (SELECT count(*) FROM iam_v2.stays o WHERE o.status='IN_HOUSE' AND o.normalized_room_number=s.normalized_room_number)=1
  ORDER BY s.occupancy_evidence_at DESC NULLS LAST LIMIT 1")
ROOM=$($PSQL "SELECT normalized_room_number FROM iam_v2.stays WHERE id='$STAY'"); RES=$($PSQL "SELECT external_reservation_id FROM iam_v2.stays WHERE id='$STAY'")
pms(){ roomsignin $1 "$ROOM" "$2"; }
pmschoose(){ local n=$1 j=$2 acx prv
  if printf '%s' "$j" | grep -q '"needs_choice":true'; then
    acx=$(printf '%s' "$j" | python3 -c 'import sys,json;print(json.load(sys.stdin)["auth_context_id"])')
    prv=$(printf '%s' "$j" | python3 -c 'import sys,json;c=json.load(sys.stdin)["choices"];print(c[-1]["package_revision_id"])')
    j=$(ip netns exec $n curl -s -H 'Content-Type: application/json' -d "{\"auth_context_id\":\"$acx\",\"package_revision_id\":\"$prv\"}" "$PORTAL/auth/pms/phase3")
  fi
  printf '%s' "$j"; }
if [ -n "$STAY" ]; then
  ok "test stay selected (${STAY:0:8}): in house, alone in its room, no access held"
  j=$(pms ga2 "wrong-$RES"); ! printf '%s' "$j" | grep -q '"ok":true' && [ "$(lastroom)" = CREDENTIAL_MISMATCH ] && ok "the right room with the wrong verification is refused (CREDENTIAL_MISMATCH)" || bad "wrong verification: $(lastroom)"
  j=$(pmschoose ga2 "$(pms ga2 "$RES")")
  PSID=$(printf '%s' "$j" | python3 -c 'import sys,json;print(json.load(sys.stdin).get("session_id",""))' 2>/dev/null)
  PMS_ENT=$(ent_of_session "$PSID")
  [ -n "$PSID" ] && [ "$($PSQL "SELECT stay_id FROM iam_v2.entitlements WHERE id='$PMS_ENT'")" = "$STAY" ] && ok "room sign-in granted the stay's entitlement and a session" || bad "PMS grant: $(printf '%s' "$j" | head -c 200)"
  inset "$CIP2" && [ "$(online ga2)" = 204 ] && ok "room device enforced and online (204)" || bad "room device probe $(online ga2)"
  PMAX=$($PSQL "SELECT sp.max_concurrent_devices FROM iam_v2.entitlements e JOIN iam_v2.service_plan_revisions sp ON sp.id=e.service_plan_revision_id WHERE e.id='$PMS_ENT'")
  j=$(pmschoose ga4 "$(pms ga4 "$RES")")
  if [ "${PMAX:-1}" -gt 1 ]; then
    PSID2=$(printf '%s' "$j" | python3 -c 'import sys,json;print(json.load(sys.stdin).get("session_id",""))' 2>/dev/null)
    [ -n "$PSID2" ] && [ "$(ent_of_session "$PSID2")" = "$PMS_ENT" ] && [ "$(online ga4)" = 204 ] && [ "$(online ga2)" = 204 ] \
      && ok "a second device of the same room JOINED the stay's entitlement; both online (limit $PMAX)" || bad "second room device: $(printf '%s' "$j" | head -c 200)"
  else
    printf '%s' "$j" | grep -qi "device limit" && [ "$(online ga4)" != 204 ] && ok "a second room device is refused at the stay's 1-device limit" || bad "second room device: $(printf '%s' "$j" | head -c 200)"
  fi
  [ "$($PSQL "SELECT count(*) FROM iam_v2.entitlements WHERE stay_id='$STAY'")" = 1 ] && ok "still one entitlement for the stay" || bad "stay has several entitlements"
  logout ga2; logout ga4
  c=$(online ga2); [ "$c" != 204 ] && ok "room device captive again after disconnect" || bad "room device online after disconnect"
else
  bad "no in-house stay suitable for the positive room test"
fi

echo "== 9. no PMS posting, payment or financial traffic =="
[ "$(counts)" = "$FIN0" ] && ok "pms_postings/posting_outbox/payment_transactions unchanged ($FIN0)" || bad "financial tables changed: $FIN0 -> $(counts)"

echo "=================================================="
echo "GUEST_ACCESS_E2E pass=$PASS fail=$FAIL"
[ "$FAIL" = 0 ]
