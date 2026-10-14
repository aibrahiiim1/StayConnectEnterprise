#!/usr/bin/env bash
# TEMPORARY INTERNET DURING EMAIL-CODE VERIFICATION, FROM A GUEST DEVICE (run as root on the appliance).
#
# A fresh network-namespace device on the guest bridge requests an email code over the HTTPS portal and is then
# checked on what a phone would experience while it waits for the code:
#   * the captive-portal API says NOT captive, with seconds-remaining = the window (so iOS/Android let the mail
#     app use the Wi-Fi; answering "captive" here is what stranded the code on PRE-LIVE, 2026-10-14);
#   * a real mail service is reachable through the window (HTTPS to the mail host's sign-in page);
#   * the window is limited: the device is in verify_ipv4, not in the authenticated sets.
# It never enters a code. The window simply expires. DEST is the mailbox the code goes to (pass it in the
# environment; it is never written to the repository or printed in full).
#
#   DEST=<address> [MAILHOST=outlook.live.com] bash scripts/portal-temporary-access-e2e-test.sh
set -uo pipefail
PASS=0; FAIL=0
ok(){ echo "  ok   $1"; PASS=$((PASS+1)); }
bad(){ echo "  FAIL $1"; FAIL=$((FAIL+1)); }
[ -n "${DEST:-}" ] || { echo "DEST (the mailbox the code is sent to) is required"; exit 2; }
MAILHOST="${MAILHOST:-outlook.live.com}"
PSQL="docker exec stayconnect-pg psql -U stayconnect -d stayconnect_site -tAc"
N="tv$(printf '%04x' $((RANDOM % 65536)))"
cleanup(){ ip netns exec $N dhclient -r -lf /var/lib/dhcp/dhclient-$N.leases -pf /run/dhclient-$N.pid ${N}c >/dev/null 2>&1
  ip netns del $N 2>/dev/null; ip link del ${N}a 2>/dev/null; rm -rf /etc/netns/$N /var/lib/dhcp/dhclient-$N.leases; }
trap cleanup EXIT
read -r ACTIVE HOST <<<"$($PSQL "SELECT https_active, coalesce(canonical_hostname,'-') FROM portal_tls_state" | tr '|' ' ')"
[ "$ACTIVE" = t ] || { echo "HTTPS is not active; nothing to test"; exit 2; }
GB=$($PSQL "SELECT bridge_name FROM guest_networks WHERE enabled ORDER BY created_at LIMIT 1")
GW=$($PSQL "SELECT host(gateway_ip) FROM guest_networks WHERE enabled ORDER BY created_at LIMIT 1")
P="https://$HOST"

ip link add ${N}a type veth peer name ${N}c; ip link set ${N}a master "$GB"; ip link set ${N}a up
ip netns add $N; ip link set ${N}c netns $N; ip netns exec $N ip link set lo up; ip netns exec $N ip link set ${N}c up
mkdir -p /etc/netns/$N; echo "nameserver $GW" > /etc/netns/$N/resolv.conf
sleep 1; ip netns exec $N timeout 25 dhclient -1 -lf /var/lib/dhcp/dhclient-$N.leases -pf /run/dhclient-$N.pid ${N}c >/dev/null 2>&1
CIP=$(ip netns exec $N ip -4 -br addr show ${N}c | awk '{print $3}' | cut -d/ -f1); ip netns exec $N ping -c1 -W2 "$GW" >/dev/null 2>&1
[ -n "$CIP" ] && ok "fresh device leased $CIP" || { bad "no lease"; exit 1; }

echo "== before the code is requested"
A=$(ip netns exec $N curl -s "$P/captive-portal/api"); printf '%s' "$A" | grep -q '"captive":true' && ok "captive API: captive" || bad "captive API before: $A"
C=$(ip netns exec $N curl -s -o /dev/null -w '%{http_code}' -m 8 "https://$MAILHOST/" 2>/dev/null; true)
[ "$C" = 000 ] && ok "the mail host is NOT reachable before a code is requested" || bad "mail host reachable before ($C)"

echo "== request the email code over HTTPS"
R=$(ip netns exec $N curl -s -H 'Content-Type: application/json' -d "{\"channel\":\"email\",\"destination\":\"$DEST\"}" "$P/auth/otp/request")
printf '%s' "$R" | python3 -c 'import sys,json;d=json.load(sys.stdin);t=d.get("temporary_access") or {};sys.exit(0 if d.get("challenge_id") and t.get("seconds",0)>0 else 1)' \
  && ok "code sent; temporary access opened ($(printf '%s' "$R" | python3 -c 'import sys,json;t=json.load(sys.stdin)["temporary_access"];print(t.get("seconds"),"s,",t.get("data_mb"),"MB")'))" \
  || { bad "request: $(printf '%s' "$R" | head -c 300)"; exit 1; }
inv=0; for _ in $(seq 1 10); do nft list set inet stayconnect verify_ipv4 2>/dev/null | grep -q " $CIP " && { inv=1; break; }; sleep 1; done
[ "$inv" = 1 ] && ok "device admitted to the verification set (limited window)" || bad "device not in verify_ipv4"
nft list set inet stayconnect phase3_auth_ipv4 2>/dev/null | grep -q "$CIP" && bad "device in the AUTHENTICATED set" || ok "device is NOT in the authenticated set"

echo "== what the phone sees during the window"
A=$(ip netns exec $N curl -s "$P/captive-portal/api")
python3 - "$A" <<'EOF' && ok "captive API: NOT captive during the window, seconds-remaining set ($A)" || bad "captive API during the window: $A"
import sys, json
d = json.loads(sys.argv[1]); assert d["captive"] is False and 0 < d["seconds-remaining"] <= 3600 and "x-android-use-custom-tabs" not in d
assert 0 < d["bytes-remaining"] <= 1 << 31, "bytes-remaining (RFC 8908) must accompany a data-capped window"
EOF
C=$(ip netns exec $N curl -s -o /dev/null -w '%{http_code}' -m 15 "https://$MAILHOST/")
case "$C" in 2??|3??) ok "the mail service is reachable through the window (https://$MAILHOST/ -> $C)";; *) bad "mail service not reachable ($C)";; esac
PR=$(ip netns exec $N curl -s -o /dev/null -w '%{http_code}' -m 8 http://connectivitycheck.gstatic.com/generate_204)
[ "$PR" = 204 ] && ok "the OS connectivity probe succeeds during the window (204)" || bad "probe during the window: $PR"
echo; echo "PASS=$PASS FAIL=$FAIL"; [ "$FAIL" = 0 ]
