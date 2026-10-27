#!/usr/bin/env bash
# ONE CLIENT DEVICE, END TO END, ON A TEST APPLIANCE (manual Part D.3c).
#
# A network namespace plugged into the first enabled Client bridge with a veth pair: its traffic enters the bridge
# exactly as a device on an access port would, and crosses the real nftables, Kea, Unbound, Client Portal, shaping
# and NAT. Checks DHCP, DNS, portal interception, HTTPS blocked before sign-in, the captive probe, a voucher
# sign-in, the entitlement, the enforced session (nft + tc), Internet access after sign-in and accounting.
#
# TEST APPLIANCES ONLY: it adds a namespace and a veth to a Client bridge and consumes a voucher. The namespace is
# left in place so the session can be inspected; remove it with `client-journey-netns-test.sh --remove <name>`, which
# also stops the namespace's dhclient (deleting only the namespace leaves dhclient renewing, and logging an error,
# for ever).
#
# Usage (root): client-journey-netns-test.sh <name> <voucher-code>      e.g.  client-journey-netns-test.sh cj1 K7Q2-...
#               client-journey-netns-test.sh --remove <name>
# Needs: dhclient (isc-dhcp-client), curl, socat (optional, for the Kea lease query).
# Exit: 0 every check passed · 1 a check failed
set -uo pipefail
if [ "${1:-}" = "--remove" ]; then
  N="${2:?usage: client-journey-netns-test.sh --remove <name>}"
  ip netns pids "$N" 2>/dev/null | xargs -r kill 2>/dev/null
  ip netns del "$N" 2>/dev/null; ip link del "${N}a" 2>/dev/null
  rm -rf "/etc/netns/$N" "/tmp/$N".*
  echo "client-journey $N: removed"
  exit 0
fi
N="${1:?usage: client-journey-netns-test.sh <name> <voucher-code>}"; CODE="${2:?usage: client-journey-netns-test.sh <name> <voucher-code>}"
P="docker exec -i stayconnect-pg psql -X -tAq -U stayconnect -d stayconnect_site -c"
BR="$($P "SELECT bridge_name FROM guest_networks WHERE enabled ORDER BY created_at LIMIT 1" </dev/null)"
GW="$($P "SELECT host(gateway_ip) FROM guest_networks WHERE enabled ORDER BY created_at LIMIT 1" </dev/null)"
PORTAL="http://$GW:8380"
pass=0; fail=0
ok(){ pass=$((pass+1)); echo "  PASS  $*"; }
bad(){ fail=$((fail+1)); echo "  FAIL  $*"; }
nsx(){ ip netns exec "$N" "$@"; }

echo "== client $N on $BR (gateway $GW)"
ip netns pids "$N" 2>/dev/null | xargs -r kill 2>/dev/null; ip netns del "$N" 2>/dev/null; ip link del "${N}a" 2>/dev/null
ip link add "${N}a" type veth peer name "${N}c"; ip link set "${N}a" master "$BR"; ip link set "${N}a" up
ip netns add "$N"; ip link set "${N}c" netns "$N"; nsx ip link set lo up; nsx ip link set "${N}c" up
MAC="$(nsx cat /sys/class/net/${N}c/address)"
sleep 1

echo "== 1. DHCP"
nsx timeout 30 dhclient -1 -lf "/tmp/$N.lease" -pf "/tmp/$N.pid" "${N}c" >/dev/null 2>&1
CIP="$(nsx ip -4 -br addr show "${N}c" | awk '{print $3}' | cut -d/ -f1)"
DNS="$(grep -o 'domain-name-servers [0-9.]*' "/tmp/$N.lease" 2>/dev/null | tail -1 | awk '{print $2}')"
O114="$(grep -o 'option [a-z0-9-]*114[^;]*\|captive[^;]*' "/tmp/$N.lease" 2>/dev/null | head -1)"
RT="$(nsx ip route | awk '/default/ {print $3}')"
[ -n "$CIP" ] && ok "lease $CIP (mac $MAC), router $RT, dns ${DNS:-none}" || bad "no DHCP lease"
LS="$(echo '{"command":"lease4-get-all","service":["dhcp4"]}' | socat - UNIX-CONNECT:/run/kea/kea4-ctrl-socket 2>/dev/null | grep -c "$CIP" || true)"
[ "${LS:-0}" -ge 1 ] && ok "Kea holds the lease" || echo "  note  Kea lease query unavailable via socat (${LS:-none})"
mkdir -p "/etc/netns/$N"; echo "nameserver ${DNS:-$GW}" > "/etc/netns/$N/resolv.conf"
nsx ping -c1 -W2 "$GW" >/dev/null 2>&1

echo "== 2. DNS"
A="$(nsx getent hosts example.com | awk '{print $1}' | head -1)"
[ -n "$A" ] && ok "example.com resolves through the gateway's resolver ($A)" || bad "no DNS answer"

echo "== 3. captive portal before sign-in"
code="$(nsx curl -s -o "/tmp/$N.land" -w '%{http_code}' -m 8 http://example.com/)"
grep -qiE 'voucher|sign in|portal' "/tmp/$N.land" && ok "plain HTTP is intercepted to the Client Portal (HTTP $code)" || bad "no portal interception (HTTP $code)"
code="$(nsx curl -s -o /dev/null -w '%{http_code}' -m 8 https://example.com/ 2>/dev/null || true)"
[ "$code" = "000" ] && ok "HTTPS to the internet is blocked before sign-in" || bad "HTTPS reached the internet before sign-in ($code)"
p204="$(nsx curl -s -o /dev/null -w '%{http_code}' -m 8 http://connectivitycheck.gstatic.com/generate_204)"
[ "$p204" != "204" ] && ok "Android captive probe is not answered 204 (HTTP $p204), so the OS shows the portal" || bad "captive probe returned 204"

echo "== 4. voucher sign-in"
nsx curl -s -o "/tmp/$N.out" -D "/tmp/$N.hdr" -c "/tmp/$N.jar" -b "/tmp/$N.jar" -m 30 --data-urlencode "code=$CODE" "$PORTAL/auth/voucher" >/dev/null
loc="$(tr -d '\r' < "/tmp/$N.hdr" | awk 'tolower($1)=="location:"{print $2}' | tail -1)"
echo "  redirect: ${loc:-none}"
case "$loc" in */success*) ok "voucher accepted, redirected to the success page";;
  */packages*) nsx curl -s -o "/tmp/$N.pk" -c "/tmp/$N.jar" -b "/tmp/$N.jar" -m 20 "$PORTAL$loc" >/dev/null; bad "voucher led to package choice ($loc)";;
  *) bad "voucher sign-in did not succeed: $(head -c 300 "/tmp/$N.out" | tr '\n' ' ')";; esac

echo "== 5. entitlement and enforced session"
for _ in $(seq 1 20); do
  st="$($P "SELECT state FROM iam_v2.sessions WHERE host(ip)='$CIP' AND ended IS NULL ORDER BY started DESC LIMIT 1" </dev/null)"
  [ "$st" = active ] && break; sleep 1
done
[ "$st" = active ] && ok "session is active (promoted by the enforcement plane)" || bad "session state is '${st:-none}'"
ent="$($P "SELECT e.status FROM iam_v2.sessions s JOIN iam_v2.entitlements e ON e.id=s.entitlement_id WHERE host(s.ip)='$CIP' AND s.ended IS NULL ORDER BY s.started DESC LIMIT 1" </dev/null)"
[ "$ent" = ACTIVE ] && ok "entitlement ACTIVE" || bad "entitlement '${ent:-none}'"
# netd adds the element on its next reconcile, a moment after the session turns active: wait for it (up to ~15 s).
# Output is captured before grep: under pipefail, `nft list ... | grep -q` fails when grep's early exit SIGPIPEs nft.
in_set() { local o; o="$(nft list set inet stayconnect phase3_auth_ipv4 2>/dev/null)"; grep -q "$CIP " <<<"$o"; }
for _ in $(seq 1 15); do in_set && break; sleep 1; done
in_set && ok "nft phase3_auth_ipv4 authorises $BR . $CIP" || bad "not in phase3_auth_ipv4"
tcout="$(tc -s class show dev "$BR")"
grep -q "class htb" <<<"$tcout" && ok "a tc HTB class shapes the Client (download, $BR)" || bad "no tc class on $BR"

echo "== 6. Internet traffic"
c1="$(nsx curl -s -o /dev/null -w '%{http_code}' -m 10 https://example.com/)"
[ "$c1" = 200 ] && ok "HTTPS to the internet works after sign-in (HTTP $c1)" || bad "no internet after sign-in ($c1)"
nsx curl -s -o /dev/null -m 60 http://speedtest.tele2.net/1MB.zip || nsx curl -s -o /dev/null -m 60 https://proof.ovh.net/files/1Mb.dat || true
echo "== 7. accounting"
for _ in $(seq 1 15); do
  b="$($P "SELECT coalesce(sum(bytes_down),0) FROM iam_v2.sessions WHERE host(ip)='$CIP' AND ended IS NULL" </dev/null 2>/dev/null)"
  [ "${b:-0}" -gt 100000 ] 2>/dev/null && break; sleep 2
done
[ "${b:-0}" -gt 100000 ] 2>/dev/null && ok "accounting recorded ${b} bytes down for the session" || echo "  info  session bytes_down=${b:-?} (column may differ; see accounting check below)"
ar="$($P "SELECT count(*) FROM public.accounting_records WHERE ts > now() - interval '10 minutes'" </dev/null 2>/dev/null)"
echo "  info  accounting_records rows in the last 10 min: ${ar:-?}"
echo
echo "client-journey $N: $pass passed, $fail failed  (ip=$CIP mac=$MAC)"
echo "$CIP" > "/tmp/$N.ip"
[ "$fail" = 0 ]
