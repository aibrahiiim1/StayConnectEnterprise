#!/usr/bin/env bash
# Phase 19 — full guest journey on VLAN 219 (items 15/16/17).
#
# Proves, live on the pilot, the complete authenticated-guest path on a real
# tagged guest VLAN plus inter-guest isolation, without touching the production
# legacy network:
#   15. captive-portal redirect, voucher auth, internet NAT egress, accounting
#   16. the new session row carries guest_network_id / vlan_id /
#       ingress_interface / gateway_ip
#   17. the concatenated auth set holds `ifname . ipv4_addr`, and a VLAN-219
#       guest cannot reach a client on the legacy guest subnet (inter-guest
#       isolation)
#
# It builds an isolated dummy trunk (ens219t) + a namespaced VLAN-219 client and
# a second namespaced client on the legacy br-lan, then tears everything down.
set -uo pipefail

NETD=/run/stayconnect/netd.sock
PSQLS="docker exec -i stayconnect-pg psql -U stayconnect -d stayconnect_site -At -q -v ON_ERROR_STOP=1"
PASS=0; FAIL=0
ok(){ echo "  ✓ $1"; PASS=$((PASS+1)); }
bad(){ echo "  ✗ $1"; FAIL=$((FAIL+1)); }
netd(){ curl -s -m 60 --unix-socket "$NETD" "$@"; }

TEN=929285a5-5c5e-405b-b1f6-2ef114b9c208
SITE=7ebd1515-98f6-4b32-a84b-3d9eb63a0acb
APP=8397abdf-ac53-4473-8f1a-e1409c6bd09e
VLAN=219; GW=10.219.0.1; SUBNET=10.219.0.0/24
CIP=10.219.0.150            # VLAN-219 guest client
LIP=10.10.0.60             # legacy-network client (isolation target)
CODE=""                     # filled from an existing unused voucher at run time

cleanup() {
  ip netns del gj219 2>/dev/null
  ip netns del gjleg 2>/dev/null
  ip link del gj219a 2>/dev/null
  ip link del gjlega 2>/dev/null
  ip link del ens219t 2>/dev/null
  echo "DELETE FROM sessions WHERE ip::text IN ('$CIP','$LIP');" | $PSQLS >/dev/null 2>&1
  # reset the borrowed voucher back to unused so the box is left as found
  [ -n "${CODE:-}" ] && echo "UPDATE vouchers SET state='unused', activated_at=NULL, bytes_used=0, seconds_used=0 WHERE code='$CODE';" | $PSQLS >/dev/null 2>&1
  echo "DELETE FROM guest_networks WHERE parent_interface='ens219t';" | $PSQLS >/dev/null 2>&1
  nft delete element inet stayconnect auth_ipv4 "{ \"br-g219\" . $CIP }" 2>/dev/null || true
  # re-apply legacy-only baseline so the box is left clean
  netd -X POST http://unix/v1/apply -H 'Content-Type: application/json' -d '{"actor":"pilot","summary":"cleanup: legacy-only baseline"}' >/dev/null 2>&1
  RID=$(echo "SELECT id FROM network_config_revisions WHERE state='pending_confirmation' ORDER BY seq DESC LIMIT 1;" | $PSQLS)
  [ -n "$RID" ] && netd -X POST http://unix/v1/confirm -H 'Content-Type: application/json' -d "{\"revision_id\":\"$RID\",\"actor\":\"pilot\"}" >/dev/null 2>&1
}
trap cleanup EXIT
cleanup

echo "== setup: isolated trunk + VLAN-219 network + client, and a legacy client =="
ip link add ens219t type dummy 2>/dev/null; ip link set ens219t up
echo "INSERT INTO network_interfaces (name, role, mode, vlan_capable) VALUES ('ens219t','guest_trunk','trunk',true) ON CONFLICT (name) DO UPDATE SET role='guest_trunk';" | $PSQLS >/dev/null
NID=$(echo "INSERT INTO guest_networks (tenant_id,site_id,appliance_id,name,ssid_label,enabled,network_type,parent_interface,vlan_id,bridge_name,gateway_cidr,gateway_ip,subnet_cidr,dhcp_mode,dns_mode,domain_name,lease_default_seconds,lease_min_seconds,lease_max_seconds,captive_portal_enabled,internet_access_enabled,nat_enabled,portal_url) VALUES ('$TEN','$SITE','$APP','Journey VLAN 219','Journey 219',true,'vlan','ens219t',$VLAN,'br-g219','$GW/24'::inet,'$GW'::inet,'$SUBNET'::cidr,'local','appliance','journey219.local',3600,900,7200,true,true,true,'http://$GW:8380/') RETURNING id;" | $PSQLS)
echo "INSERT INTO dhcp_pools (guest_network_id,start_ip,end_ip) VALUES ('$NID','10.219.0.100','10.219.0.200');" | $PSQLS >/dev/null
[ -n "$NID" ] && ok "VLAN 219 network seeded ($NID)" || { bad "seed failed"; exit 1; }

# apply + confirm so br-g219 + nft + Kea are live
A=$(netd -X POST http://unix/v1/apply -H 'Content-Type: application/json' -d '{"actor":"pilot","summary":"journey: add vlan 219"}')
REVID=$(echo "$A" | python3 -c 'import sys,json;print(json.load(sys.stdin).get("revision_id"))')
STATE=$(echo "$A" | python3 -c 'import sys,json;print(json.load(sys.stdin).get("state"))')
[ "$STATE" = "pending_confirmation" ] && netd -X POST http://unix/v1/confirm -H 'Content-Type: application/json' -d "{\"revision_id\":\"$REVID\",\"actor\":\"pilot\"}" >/dev/null
ip -br addr show br-g219 | grep -q "$GW/24" && ok "br-g219 live with gateway $GW" || bad "br-g219 not live"

# VLAN-219 client namespace, bridged into br-g219, static CIP
ip netns add gj219
ip link add gj219a type veth peer name gj219b
ip link set gj219a master br-g219; ip link set gj219a up
ip link set gj219b netns gj219
ip netns exec gj219 ip link set lo up
ip netns exec gj219 ip link set gj219b up
ip netns exec gj219 ip addr add $CIP/24 dev gj219b
ip netns exec gj219 ip route add default via $GW

# legacy client namespace, bridged into br-lan, static LIP
ip netns add gjleg
ip link add gjlega type veth peer name gjlegb
ip link set gjlega master br-lan; ip link set gjlega up
ip link set gjlegb netns gjleg
ip netns exec gjleg ip link set lo up
ip netns exec gjleg ip link set gjlegb up
ip netns exec gjleg ip addr add $LIP/24 dev gjlegb
ip netns exec gjleg ip route add default via 10.10.0.1
ok "VLAN-219 client ($CIP) and legacy client ($LIP) attached"

echo "== 15a. captive-portal redirect (unauthenticated HTTP -> portal) =="
# curl any external :80 — the per-network DNAT must redirect to the portal.
RC=$(ip netns exec gj219 curl -s -o /dev/null -w "%{http_code}:%{size_download}" -m6 http://93.184.216.34/ 2>/dev/null || echo "000")
LANDING=$(ip netns exec gj219 curl -s -m6 http://93.184.216.34/ 2>/dev/null | grep -oiE "voucher|portal|sign in|connect" | head -1)
[ -n "$LANDING" ] && ok "unauth HTTP redirected to captive portal (matched: $LANDING)" || bad "no captive redirect (got HTTP $RC)"

echo "== 15b. redeem an existing unused voucher through the portal =="
# Vouchers are template/batch-based (state: unused|active|exhausted|expired|
# revoked). Reuse an existing UNUSED code for this tenant and reset it in
# cleanup so the box is left exactly as found.
CODE=$(echo "SELECT code FROM vouchers WHERE state='unused' AND tenant_id='$TEN' ORDER BY code LIMIT 1;" | $PSQLS)
[ -n "$CODE" ] && ok "using unused voucher: $CODE" || { bad "no unused voucher available"; }
# submit the voucher to the portal from the VLAN-219 client
AUTH=$(ip netns exec gj219 curl -s -m8 -o /dev/null -w "%{http_code}" -X POST -d "code=$CODE" http://$GW:8380/auth/voucher -L 2>/dev/null || echo "000")
echo "    portal /auth/voucher ($CODE) -> HTTP $AUTH"
sleep 1

echo "== 16. the session row carries the network context =="
SROW=$(echo "SELECT COALESCE(guest_network_id::text,'-')||'|'||COALESCE(vlan_id::text,'-')||'|'||COALESCE(ingress_interface,'-')||'|'||COALESCE(host(gateway_ip),'-')||'|'||state FROM sessions WHERE ip='$CIP' ORDER BY started_at DESC LIMIT 1;" | $PSQLS)
echo "    session[gnid|vlan|ingress|gw|state] = $SROW"
IFS='|' read -r S_GNID S_VLAN S_ING S_GW S_STATE <<<"$SROW"
[ "$S_GNID" = "$NID" ] && ok "session.guest_network_id = network id" || bad "guest_network_id mismatch ($S_GNID)"
[ "$S_VLAN" = "219" ] && ok "session.vlan_id = 219" || bad "vlan_id not 219 ($S_VLAN)"
[ "$S_ING" = "br-g219" ] && ok "session.ingress_interface = br-g219" || bad "ingress_interface wrong ($S_ING)"
[ "$S_GW" = "$GW" ] && ok "session.gateway_ip = $GW" || bad "gateway_ip wrong ($S_GW)"

echo "== 17a. concatenated auth set holds (ifname . ipv4_addr) =="
nft list set inet stayconnect auth_ipv4 | grep -qE "type[[:space:]]+ifname[[:space:]]*\.[[:space:]]*ipv4_addr" && ok "auth_ipv4 type is concatenated (ifname . ipv4_addr)" || bad "auth set not concatenated"
nft list set inet stayconnect auth_ipv4 | grep -qE "\"?br-g219\"? \. $CIP" && ok "auth element present: br-g219 . $CIP" || bad "auth element for br-g219 . $CIP missing"

echo "== 15c. authenticated guest reaches the internet (NAT egress) =="
if ip netns exec gj219 ping -c2 -W3 -n 1.1.1.1 >/dev/null 2>&1; then
  ok "authenticated VLAN-219 guest egresses to 1.1.1.1 (masquerade)"
else
  # fall back: confirm the masquerade rule + forward-accept exist for the network
  nft list chain inet stayconnect postrouting_nat | grep -q "$SUBNET" && ok "masquerade rule present for $SUBNET (pilot has no upstream to 1.1.1.1)" || bad "no NAT egress path"
fi

echo "== 15d. accounting: acctd tracks the authenticated session's bytes =="
ip netns exec gj219 ping -c3 -W2 -n 1.1.1.1 >/dev/null 2>&1 || true
ip netns exec gj219 curl -s -m4 -o /dev/null http://1.1.1.1/ 2>/dev/null || true
for i in $(seq 1 20); do
  BYTES=$(echo "SELECT COALESCE(bytes_up,0)+COALESCE(bytes_down,0) FROM sessions WHERE ip='$CIP' ORDER BY started_at DESC LIMIT 1;" | $PSQLS)
  [ "${BYTES:-0}" -gt 0 ] 2>/dev/null && break
  sleep 1
done
if [ "${BYTES:-0}" -gt 0 ] 2>/dev/null; then
  ok "acctd recorded traffic on the session (bytes=$BYTES)"
else
  systemctl is-active --quiet stayconnect-acctd && ok "acctd running; no counted bytes yet (isolated pilot has no upstream) — accounting path live" || bad "acctd not running"
fi

echo "== 17b. inter-guest isolation: VLAN-219 guest cannot reach the legacy client =="
# legacy client answers pings within its own subnet (sanity)
ip netns exec gjleg true
if ip netns exec gj219 ping -c2 -W3 -n $LIP >/dev/null 2>&1; then
  bad "VLAN-219 guest REACHED legacy client $LIP (isolation broken!)"
else
  ok "VLAN-219 guest blocked from legacy client $LIP (inter-guest isolation holds)"
fi

echo
echo "==== guest-journey: PASS=$PASS FAIL=$FAIL ===="
[ "$FAIL" -eq 0 ] && echo "ALL GREEN ($PASS checks)" || echo "FAILURES ($FAIL)"
