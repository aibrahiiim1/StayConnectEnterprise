#!/usr/bin/env bash
# GUEST ACCESS, END TO END, ON THE APPLIANCE (run as root on PRE-LIVE).
#
# A real client on the live guest network -- a network namespace with a veth into the guest bridge, leased by
# the appliance's own DHCP -- walks the production path: captive portal, voucher or guest-account sign-in,
# package list, free acquisition (quote -> confirm -> entitlement), activation, netd enforcement, real internet,
# then disconnect. Every assertion is on what the SERVER holds (session row, entitlement row, voucher state,
# the nft enforcement set) or on real packets leaving the appliance -- never on a success page alone.
#
# Nothing here is fabricated: the voucher and account are issued through Hotel Admin's API exactly as an
# operator would, the package is the site's own published free package, and every credential this creates is
# revoked or deleted on exit. It performs no PMS, payment or financial action and asserts that none happened.
set -uo pipefail
PASS=0; FAIL=0
ok(){ echo "  ok   $1"; PASS=$((PASS+1)); }
bad(){ echo "  FAIL $1"; FAIL=$((FAIL+1)); }
R='--resolve hotel.stayconnect.local:443:127.0.0.1'
B=https://hotel.stayconnect.local/api/edge/v1
PSQL="docker exec stayconnect-pg psql -U stayconnect -d stayconnect_site -tAc"
CK=/tmp/ck.ga; NS=(); VIDS=(); ACCT=""
PROBE=http://connectivitycheck.gstatic.com/generate_204

cleanup() {
  for v in "${VIDS[@]:-}"; do [ -n "$v" ] && curl -sk $R -b $CK -o /dev/null -H 'Content-Type: application/json' \
      -d '{"password":"admin","reason":"guest-access e2e test cleanup"}' "$B/vouchers/$v/revoke"; done
  # An account that has signed in is referenced by its sign-in records, so it is disabled, not deleted.
  [ -n "$ACCT" ] && curl -sk $R -b $CK -o /dev/null -X PATCH -H 'Content-Type: application/json' -d '{"enabled":false}' "$B/guest-accounts/$ACCT"
  for n in "${NS[@]:-}"; do [ -n "$n" ] || continue
    ip netns exec "$n" dhclient -r -lf /tmp/$n.lease -pf /tmp/$n.pid ${n}c >/dev/null 2>&1
    ip netns del "$n" 2>/dev/null; ip link del ${n}a 2>/dev/null; rm -rf /etc/netns/$n; done
}
trap cleanup EXIT

GB=$($PSQL "SELECT bridge_name FROM guest_networks WHERE enabled ORDER BY created_at LIMIT 1")
GW=$($PSQL "SELECT host(gateway_ip) FROM guest_networks WHERE enabled ORDER BY created_at LIMIT 1")
PORTAL="http://$GW:8380"
counts(){ $PSQL "SELECT (SELECT count(*) FROM iam_v2.pms_postings)||'/'||(SELECT count(*) FROM iam_v2.posting_outbox)||'/'||(SELECT count(*) FROM iam_v2.payment_transactions)"; }
FIN0=$(counts)

# client <name>: a device on the guest bridge; sets CIP/CMAC.
client() {
  local n=$1; NS+=("$n"); ip netns del $n 2>/dev/null; ip link del ${n}a 2>/dev/null
  ip link add ${n}a type veth peer name ${n}c; ip link set ${n}a master "$GB"; ip link set ${n}a up
  ip netns add $n; ip link set ${n}c netns $n; ip netns exec $n ip link set lo up; ip netns exec $n ip link set ${n}c up
  sleep 1; ip netns exec $n timeout 25 dhclient -1 -lf /tmp/$n.lease -pf /tmp/$n.pid ${n}c >/dev/null 2>&1
  CIP=$(ip netns exec $n ip -4 -br addr show ${n}c | awk '{print $3}' | cut -d/ -f1)
  # The DNS server the lease handed out, as a real device would use it (ip netns exec mounts this file).
  local dns; dns=$(grep -o 'domain-name-servers [0-9.]*' /tmp/$n.lease | tail -1 | awk '{print $2}')
  mkdir -p /etc/netns/$n; echo "nameserver ${dns:-$GW}" > /etc/netns/$n/resolv.conf
  ip netns exec $n ping -c1 -W2 "$GW" >/dev/null 2>&1   # the portal refuses a device with no ARP entry
}
online(){ ip netns exec $1 curl -s -o /dev/null -w '%{http_code}' -m 8 "$PROBE"; }
inset(){ nft list set inet stayconnect phase3_auth_ipv4 2>/dev/null | grep -qE "\"?$GB\"? \. $1([^0-9]|\$)"; }
# signin <ns> <path> <form...>: POST a sign-in form; prints the Location header.
signin(){ local n=$1 p=$2; shift 2; rm -f /tmp/$n.jar
  ip netns exec $n curl -s -o /tmp/$n.html -D - -c /tmp/$n.jar -b /tmp/$n.jar "$@" "$PORTAL$p" | tr -d '\r' | awk 'tolower($1)=="location:"{print $2}'; }
acquire(){ local n=$1 pkg=$2
  ip netns exec $n curl -s -o /tmp/$n.html -D - -c /tmp/$n.jar -b /tmp/$n.jar --data-urlencode "package_id=$pkg" "$PORTAL/packages/acquire" \
    | tr -d '\r' | awk 'tolower($1)=="location:"{print $2}'; }

curl -sk $R -c $CK -o /dev/null -H 'Content-Type: application/json' -d '{"email":"admin","password":"admin"}' $B/auth/login
PKGREV=$($PSQL "SELECT p.current_revision_id FROM iam_v2.internet_packages p JOIN iam_v2.internet_package_revisions r ON r.id=p.current_revision_id JOIN iam_v2.service_plan_revisions s ON s.id=r.service_plan_revision_id WHERE p.active AND NOT p.is_system AND r.price_minor=0 ORDER BY s.max_concurrent_devices ASC, r.revision_no DESC LIMIT 1")
PKGID=$($PSQL "SELECT package_id FROM iam_v2.internet_package_revisions WHERE id='$PKGREV'")
issue(){ local out; out=$(curl -sk $R -b $CK -H 'Content-Type: application/json' \
    -d "{\"count\":1,\"package_revision_id\":\"$PKGREV\",\"note\":\"guest-access e2e test\"}" "$B/vouchers/issue")
  CODE=$(printf '%s' "$out" | python3 -c 'import sys,json;print((json.load(sys.stdin).get("codes") or [""])[0])' 2>/dev/null)
  local b; b=$(printf '%s' "$out" | python3 -c 'import sys,json;print(json.load(sys.stdin).get("batch_id",""))' 2>/dev/null)
  VID=$($PSQL "SELECT id FROM iam_v2.vouchers WHERE batch_id='$b' LIMIT 1"); VIDS+=("$VID"); }

echo "== 0. setup: guest network $GB ($GW), package revision ${PKGREV:0:8} =="
issue; [ -n "$CODE" ] && [ -n "$VID" ] && ok "voucher issued through Hotel Admin (${VID:0:8})" || { bad "voucher issue failed"; exit 1; }
client ga1; CIP1=$CIP; [ -n "$CIP1" ] && ok "device 1 leased $CIP1" || { bad "device 1 got no lease"; exit 1; }

echo "== 1. before sign-in the device is captive =="
c=$(online ga1); [ "$c" != 204 ] && ok "no internet before sign-in (probe answered $c)" || bad "device was online before sign-in"
inset "$CIP1" && bad "device already in the enforcement set" || ok "device not in the enforcement set"

echo "== 2. invalid and revoked credentials fail =="
loc=$(signin ga1 /auth/voucher --data-urlencode "code=00000000")
[ -z "$loc" ] && grep -qiE "invalid|not valid|incorrect|not recogni|didn|wrong" /tmp/ga1.html && ok "an invalid voucher is refused on the portal" || bad "invalid voucher: location=$loc"
issue; RCODE=$CODE; RVID=$VID
curl -sk $R -b $CK -o /dev/null -H 'Content-Type: application/json' -d '{"password":"admin","reason":"e2e: revoked-credential check"}' "$B/vouchers/$RVID/revoke"
loc=$(signin ga1 /auth/voucher --data-urlencode "code=$RCODE")
[ -z "$loc" ] && ok "a revoked voucher is refused (state $($PSQL "SELECT state FROM iam_v2.vouchers WHERE id='$RVID'"))" || bad "revoked voucher redirected to $loc"

echo "== 3. voucher sign-in -> packages -> acquisition -> activation -> enforcement =="
issue
loc=$(signin ga1 /auth/voucher --data-urlencode "code=$CODE")
[ "$loc" = "/packages" ] && ok "voucher authenticated; portal moved to package selection" || bad "voucher sign-in went to '$loc'"
[ "$($PSQL "SELECT state FROM iam_v2.vouchers WHERE id='$VID'")" = UNUSED ] && ok "voucher still UNUSED after authentication alone" || bad "voucher changed state at authentication"
ip netns exec ga1 curl -s -o /tmp/ga1.pk -b /tmp/ga1.jar "$PORTAL/packages"
grep -q "value=\"$PKGID\"" /tmp/ga1.pk && ok "the voucher's package is offered" || bad "package $PKGID not offered"
N=$(grep -c 'name="package_id"' /tmp/ga1.pk); [ "$N" = 1 ] && ok "only the voucher-pinned package is offered ($N)" || bad "$N packages offered to a voucher pinned to one"
loc=$(acquire ga1 "$PKGID"); SID=$(printf '%s' "$loc" | sed -n 's/.*[?&]s=\([^&]*\).*/\1/p')
[ -n "$SID" ] && ok "acquisition redirected to success (session ${SID:0:8})" || { bad "acquire went to '$loc': $(grep -oE 'class="[^"]*(err|notice)[^"]*"[^<]*<[^>]*>[^<]*' /tmp/ga1.html | head -1)"; }
if [ -n "$SID" ]; then
  row=$($PSQL "SELECT s.state||'|'||e.status||'|'||coalesce(e.voucher_id::text,'')||'|'||pu.state||'|'||s.mac FROM iam_v2.sessions s JOIN iam_v2.entitlements e ON e.id=s.entitlement_id JOIN iam_v2.purchases pu ON pu.id=e.purchase_id WHERE s.id='$SID'")
  IFS='|' read -r SST EST EV PST SMAC <<<"$row"
  [ "$SST" = active ] && ok "session state is active (set by netd, not by the portal)" || bad "session state $SST"
  [ "$EST" = ACTIVE ] && [ "$EV" = "$VID" ] && ok "entitlement ACTIVE and bound to this voucher" || bad "entitlement $EST voucher=$EV"
  [ "$PST" = GRANTED ] && ok "purchase GRANTED (free, settlement NOT_REQUIRED)" || bad "purchase $PST"
  [ "$($PSQL "SELECT state FROM iam_v2.vouchers WHERE id='$VID'")" = REDEEMED ] && ok "voucher REDEEMED at acquisition (single use)" || bad "voucher not redeemed"
  [ "$($PSQL "SELECT count(*) FROM iam_v2.entitlement_device_authorizations WHERE entitlement_id=(SELECT entitlement_id FROM iam_v2.sessions WHERE id='$SID') AND deauthorized_at IS NULL")" = 1 ] && ok "exactly one device authorized on the entitlement" || bad "device authorization count wrong"
  inset "$CIP1" && ok "enforcement: ($GB . $CIP1) is in nft set phase3_auth_ipv4" || bad "device not in the enforcement set"
  c=$(online ga1); [ "$c" = 204 ] && ok "REAL INTERNET: $PROBE answered 204 from the guest device" || bad "no internet after activation (probe answered $c)"
  ip netns exec ga1 ping -c2 -W3 1.1.1.1 >/dev/null 2>&1 && ok "ICMP to 1.1.1.1 passes" || bad "ICMP to 1.1.1.1 fails"
fi

echo "== 4. the redeemed voucher cannot be reused, and the device limit holds =="
client ga2; CIP2=$CIP
loc=$(signin ga2 /auth/voucher --data-urlencode "code=$CODE")
[ -z "$loc" ] && ok "a second device cannot sign in with the redeemed voucher" || bad "redeemed voucher reused -> $loc"
c=$(online ga2); [ "$c" != 204 ] && ok "second device still captive" || bad "second device online"

echo "== 5. disconnect ends the session and enforcement =="
ip netns exec ga1 curl -s -o /dev/null -X POST "$PORTAL/logout"; sleep 6
[ "$($PSQL "SELECT state FROM iam_v2.sessions WHERE id='$SID'")" != active ] && ok "session no longer active after disconnect ($($PSQL "SELECT state||'/'||coalesce(end_reason,'') FROM iam_v2.sessions WHERE id='$SID'"))" || bad "session still active"
inset "$CIP1" && bad "device still in the enforcement set after disconnect" || ok "device removed from the enforcement set"
c=$(online ga1); [ "$c" != 204 ] && ok "internet blocked again after disconnect (probe answered $c)" || bad "still online after disconnect"

echo "== 5b. the success-page panel path (/api/commerce/*) also ends enforced, or not at all =="
issue; client ga3; CIP3=$CIP
loc=$(signin ga3 /auth/voucher --data-urlencode "code=$CODE")
j=$(ip netns exec ga3 curl -s -b /tmp/ga3.jar "$PORTAL/api/commerce/packages")
P3=$(printf '%s' "$j" | python3 -c 'import sys,json;print((json.load(sys.stdin).get("packages") or [{}])[0].get("package_id",""))' 2>/dev/null)
[ "$P3" = "$PKGID" ] && ok "panel lists the voucher's package" || bad "panel list: $(printf '%s' "$j" | head -c 160)"
q=$(ip netns exec ga3 curl -s -b /tmp/ga3.jar -H 'Content-Type: application/json' -d "{\"package_id\":\"$P3\"}" "$PORTAL/api/commerce/quote")
QID=$(printf '%s' "$q" | python3 -c 'import sys,json;print(json.load(sys.stdin).get("quote_id",""))' 2>/dev/null)
c=$(ip netns exec ga3 curl -s -b /tmp/ga3.jar -H 'Content-Type: application/json' -d "{\"quote_id\":\"$QID\"}" "$PORTAL/api/commerce/confirm")
ENF=$(printf '%s' "$c" | python3 -c 'import sys,json;d=json.load(sys.stdin);print(d.get("enforced"),d.get("session_id",""))' 2>/dev/null)
case "$ENF" in True\ ?*) ok "panel confirm answered only after enforcement (session ${ENF#True })";; *) bad "panel confirm: $(printf '%s' "$c" | head -c 200)";; esac
inset "$CIP3" && ok "panel device enforced in nft" || bad "panel device not enforced"
c=$(online ga3); [ "$c" = 204 ] && ok "REAL INTERNET for the panel device (204)" || bad "panel device probe $c"
ip netns exec ga3 curl -s -o /dev/null -X POST "$PORTAL/logout"; sleep 5
c=$(online ga3); [ "$c" != 204 ] && ok "panel device blocked after disconnect" || bad "panel device still online"

echo "== 6. guest account sign-in through the same chain =="
UN="e2e-$(date +%s)"; out=$(curl -sk $R -b $CK -H 'Content-Type: application/json' -d "{\"username\":\"$UN\",\"generate\":true,\"notes\":\"guest-access e2e test\"}" "$B/guest-accounts")
ACCT=$(printf '%s' "$out" | python3 -c 'import sys,json;d=json.load(sys.stdin);print(d.get("id") or (d.get("account") or {}).get("id",""))' 2>/dev/null)
PW=$(printf '%s' "$out" | python3 -c 'import sys,json;d=json.load(sys.stdin);print(d.get("password") or d.get("generated_password",""))' 2>/dev/null)
[ -n "$ACCT" ] && [ -n "$PW" ] && ok "guest account created through Hotel Admin" || bad "account create: $(printf '%s' "$out" | head -c 200)"
loc=$(signin ga2 /auth/credentials --data-urlencode "username=$UN" --data-urlencode "password=wrong-$PW")
[ -z "$loc" ] && ok "a wrong account password is refused" || bad "wrong password -> $loc"
loc=$(signin ga2 /auth/credentials --data-urlencode "username=$UN" --data-urlencode "password=$PW")
if [ "$loc" = "/packages" ]; then ok "guest account authenticated; package selection"
  ip netns exec ga2 curl -s -o /tmp/ga2.pk -b /tmp/ga2.jar "$PORTAL/packages"
  AP=$(grep -oE 'name="package_id" value="[^"]+"' /tmp/ga2.pk | head -1 | sed 's/.*value="//;s/"$//')
  if [ -n "$AP" ]; then ok "packages offered to the account ($(grep -c 'name="package_id"' /tmp/ga2.pk))"
    loc=$(acquire ga2 "$AP"); SID2=$(printf '%s' "$loc" | sed -n 's/.*[?&]s=\([^&]*\).*/\1/p')
    [ -n "$SID2" ] && [ "$($PSQL "SELECT state FROM iam_v2.sessions WHERE id='$SID2'")" = active ] && ok "account session active" || bad "account acquire went to '$loc'"
    inset "$CIP2" && ok "account device enforced in nft" || bad "account device not enforced"
    c=$(online ga2); [ "$c" = 204 ] && ok "REAL INTERNET for the account device (204)" || bad "account device probe $c"
    ip netns exec ga2 curl -s -o /dev/null -X POST "$PORTAL/logout"; sleep 5
    c=$(online ga2); [ "$c" != 204 ] && ok "account device blocked after disconnect" || bad "account device still online"
  else bad "no package offered to the account"; fi
else bad "account sign-in went to '$loc'"; fi

echo "== 7. no PMS posting, payment or financial traffic =="
[ "$(counts)" = "$FIN0" ] && ok "pms_postings/posting_outbox/payment_transactions unchanged ($FIN0)" || bad "financial tables changed: $FIN0 -> $(counts)"

echo "=================================================="
echo "GUEST_ACCESS_E2E pass=$PASS fail=$FAIL"
[ "$FAIL" = 0 ]
