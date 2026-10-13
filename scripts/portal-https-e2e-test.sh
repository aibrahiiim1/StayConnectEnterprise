#!/usr/bin/env bash
# THE HTTPS CLIENT PORTAL, END TO END, FROM A GUEST DEVICE (run as root on the appliance).
#
# A network namespace with a veth into the guest bridge, leased by the appliance's own DHCP, behaves like a
# phone that has just joined: it resolves the portal hostname through the DNS its lease named, reaches the portal
# address only through the guest-network rules, and is checked on what really goes over the wire. Contract:
# docs/architecture/ONEGATE_HTTPS_PORTAL_AND_SOCIAL_LOGIN.md.
#
#   CA=<pem>   trust anchor for the portal certificate (a test CA such as Pebble's); default: the system roots.
#
# Read-only: nothing is created except the namespace, which is removed on exit. It signs nobody in.
set -uo pipefail
PASS=0; FAIL=0
ok(){ echo "  ok   $1"; PASS=$((PASS+1)); }
bad(){ echo "  FAIL $1"; FAIL=$((FAIL+1)); }
PSQL="docker exec stayconnect-pg psql -U stayconnect -d stayconnect_site -tAc"
N=oghttps; CAOPT=(); [ -n "${CA:-}" ] && CAOPT=(--cacert "$CA")
cleanup(){ ip netns exec $N dhclient -r -lf /var/lib/dhcp/dhclient-$N.leases -pf /run/dhclient-$N.pid ${N}c >/dev/null 2>&1
  ip netns del $N 2>/dev/null; ip link del ${N}a 2>/dev/null; rm -rf /etc/netns/$N /var/lib/dhcp/dhclient-$N.leases /etc/dhcp/$N.dhclient.conf; }
trap cleanup EXIT

GB=$($PSQL "SELECT bridge_name FROM guest_networks WHERE enabled ORDER BY created_at LIMIT 1")
GW=$($PSQL "SELECT host(gateway_ip) FROM guest_networks WHERE enabled ORDER BY created_at LIMIT 1")
read -r ACTIVE HOST VIP <<<"$($PSQL "SELECT s.https_active, coalesce(s.canonical_hostname,'-'), host(p.portal_ip)
  FROM portal_tls_state s, portal_https_settings p" | tr '|' ' ')"
echo "guest bridge $GB, gateway $GW, portal $HOST at $VIP, https_active=$ACTIVE"
[ "$ACTIVE" = t ] || { echo "HTTPS is not active on this appliance; nothing to test"; exit 2; }

cleanup; ip link add ${N}a type veth peer name ${N}c; ip link set ${N}a master "$GB"; ip link set ${N}a up
ip netns add $N; ip link set ${N}c netns $N; ip netns exec $N ip link set lo up; ip netns exec $N ip link set ${N}c up
mkdir -p /etc/netns/$N; : > /etc/netns/$N/resolv.conf
cat > /etc/dhcp/$N.dhclient.conf <<'EOF'
option captive-portal code 114 = text;
request subnet-mask, broadcast-address, routers, domain-name-servers, captive-portal;
EOF
sleep 1; ip netns exec $N timeout 25 dhclient -1 -cf /etc/dhcp/$N.dhclient.conf -lf /var/lib/dhcp/dhclient-$N.leases -pf /run/dhclient-$N.pid ${N}c >/dev/null 2>&1
CIP=$(ip netns exec $N ip -4 -br addr show ${N}c | awk '{print $3}' | cut -d/ -f1)
[ -n "$CIP" ] && ok "leased $CIP from the appliance" || { bad "no DHCP lease"; exit 1; }
DNS=$(grep -m1 'domain-name-servers' /var/lib/dhcp/dhclient-$N.leases | sed -E 's/.*servers ([0-9.]+).*/\1/')
echo "nameserver ${DNS:-$GW}" > /etc/netns/$N/resolv.conf
ip netns exec $N ping -c1 -W2 "$GW" >/dev/null 2>&1

echo "== DHCP option 114 (RFC 8910)"
O114=$(grep -m1 'captive-portal' /var/lib/dhcp/dhclient-$N.leases | sed -E 's/.*captive-portal "([^"]*)".*/\1/')
[ "$O114" = "https://$HOST/captive-portal/api" ] && ok "option 114 = $O114" || bad "option 114 is '$O114'"

echo "== DNS"
A=$(ip netns exec $N getent ahostsv4 "$HOST" | awk 'NR==1{print $1}')
[ "$A" = "$VIP" ] && ok "$HOST resolves to the portal address $VIP" || bad "$HOST resolved to '$A'"

echo "== captive API (RFC 8908)"
H=$(ip netns exec $N curl -s "${CAOPT[@]}" -D - -o /tmp/$N.api "https://$HOST/captive-portal/api" | tr -d '\r')
echo "$H" | grep -qi '^content-type: application/captive+json' && ok "content type application/captive+json" || bad "content type"
echo "$H" | grep -qi '^cache-control: private, no-store' && ok "Cache-Control private, no-store" || bad "cache control"
grep -q '"captive":true' /tmp/$N.api && ok "captive=true for a device that has not signed in" || bad "captive flag: $(cat /tmp/$N.api)"
grep -q "\"user-portal-url\":\"https://$HOST/\"" /tmp/$N.api && ok "user-portal-url is the HTTPS portal" || bad "user-portal-url"
grep -q '"x-android-use-custom-tabs":361335020' /tmp/$N.api && ok "asks Android 12+ for a Custom Tab" || bad "custom tabs field"

echo "== TLS"
V=$(ip netns exec $N curl -s "${CAOPT[@]}" -o /dev/null -w '%{http_code} %{ssl_verify_result}' "https://$HOST/")
[ "$V" = "200 0" ] && ok "portal over HTTPS: 200, certificate verified" || bad "portal over HTTPS: $V"
ip netns exec $N curl -s "${CAOPT[@]}" -D - -o /dev/null "https://$HOST/" | grep -qi '^strict-transport-security' \
  && bad "HSTS is set (it would strand the HTTP fallback)" || ok "no HSTS header"
SUBJ=$(echo | ip netns exec $N openssl s_client -connect "$VIP:443" -servername "$HOST" 2>/dev/null | openssl x509 -noout -ext subjectAltName 2>/dev/null | tr -d ' \n')
echo "$SUBJ" | grep -q "DNS:$HOST" && ok "certificate names $HOST" || bad "certificate SAN: $SUBJ"

echo "== HTTP probes and pages redirect to HTTPS"
for u in "http://captive.apple.com/hotspot-detect.html" "http://connectivitycheck.gstatic.com/generate_204" "http://www.msftconnecttest.com/connecttest.txt"; do
  L=$(ip netns exec $N curl -s -o /dev/null -w '%{http_code} %{redirect_url}' -m 8 "$u")
  [ "$L" = "302 https://$HOST/" ] && ok "probe $u -> $L" || bad "probe $u -> $L"
done
L=$(ip netns exec $N curl -s -o /dev/null -w '%{http_code} %{redirect_url}' "http://$GW:8380/signin?x=1")
[ "$L" = "302 https://$HOST/signin?x=1" ] && ok "HTTP page keeps its path: $L" || bad "HTTP page: $L"
L=$(ip netns exec $N curl -s -o /dev/null -w '%{http_code} %{redirect_url}' "http://$HOST/")
[ "$L" = "302 https://$HOST/" ] && ok "http://$HOST/ -> HTTPS" || bad "http://$HOST/: $L"

echo "== nothing else is intercepted"
E=$(ip netns exec $N curl -s -o /dev/null -w '%{http_code}' -m 6 https://www.example.com/ 2>&1; echo " rc=$?")
case "$E" in *"rc=0"*) bad "an unauthenticated device reached https://www.example.com ($E)";; *) ok "arbitrary HTTPS before sign-in is refused, not served a portal certificate ($E)";; esac
R=$(ip netns exec $N curl -s -o /dev/null -w '%{http_code}' -m 4 "http://$VIP:22/" 2>&1; echo " rc=$?")
case "$R" in *"rc=0"*) bad "portal address answered on port 22";; *) ok "portal address refuses other ports (22)";; esac

echo; echo "PASS=$PASS FAIL=$FAIL"; [ "$FAIL" = 0 ]
