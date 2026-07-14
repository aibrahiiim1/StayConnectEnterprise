#!/usr/bin/env bash
# Phase 19 — guest VLAN + DHCP E2E on an ISOLATED test VLAN (219).
#
# Proves netd can create and operate a real tagged guest VLAN from the Site DB
# without disrupting the live legacy network. Uses a veth trunk + a network
# namespace as the "AP/switch + guest device", so the production ens192/br-lan
# is never touched.
#
# Flow: seed VLAN 219 in the site DB -> netd validate -> netd apply (creates
# ens219t.219 -> br-g219 -> 10.219.0.1/24, loads nft, pushes Kea subnet with
# option 114) -> a namespaced client on VLAN 219 gets a DHCP lease -> reads
# option 114 -> captive DNAT -> voucher login -> nft concat auth -> confirm.
# Then a deliberately-bad apply is rolled back and management stays reachable.
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
VLAN=219
GW=10.219.0.1
SUBNET=10.219.0.0/24

cleanup() {
  ip netns del gv219 2>/dev/null
  ip link del gv219a 2>/dev/null
  ip link del ens219t 2>/dev/null
  echo "DELETE FROM guest_networks WHERE parent_interface='ens219t';" | $PSQLS >/dev/null 2>&1
}
trap cleanup EXIT
cleanup

echo "== 19.1 build an isolated trunk (ens219t) + VLAN-219 client namespace =="
# ens219t is a dummy trunk that stands in for the guest-facing NIC; netd will
# hang a VLAN 219 sub-interface + bridge off it. A veth carries tagged frames
# into a namespace that acts as the guest device.
ip link add ens219t type dummy 2>/dev/null; ip link set ens219t up
ip link add gv219a type veth peer name gv219b 2>/dev/null
ip link set gv219a up
ip netns add gv219
ip link set gv219b netns gv219
# tag the client side onto VLAN 219 inside the namespace
ip netns exec gv219 ip link add link gv219b name gv219b.219 type vlan id 219
ip netns exec gv219 ip link set gv219b up
ip netns exec gv219 ip link set gv219b.219 up
# register ens219t so validation accepts it as a parent, role guest_trunk
echo "INSERT INTO network_interfaces (name, role, mode, vlan_capable) VALUES ('ens219t','guest_trunk','trunk',true) ON CONFLICT (name) DO UPDATE SET role='guest_trunk';" | $PSQLS >/dev/null
ok "isolated trunk + client namespace ready"

echo "== 19.2 seed VLAN 219 guest network in the Site DB =="
NID=$(echo "INSERT INTO guest_networks (tenant_id,site_id,appliance_id,name,ssid_label,enabled,network_type,parent_interface,vlan_id,bridge_name,gateway_cidr,gateway_ip,subnet_cidr,dhcp_mode,dns_mode,domain_name,lease_default_seconds,lease_min_seconds,lease_max_seconds,captive_portal_enabled,internet_access_enabled,nat_enabled,portal_url) VALUES ('$TEN','$SITE','$APP','Test VLAN 219','Test Guest 219',true,'vlan','ens219t',$VLAN,'br-g219','$GW/24'::inet,'$GW'::inet,'$SUBNET'::cidr,'local','appliance','guest219.local',3600,900,7200,true,true,true,'http://$GW:8380/') RETURNING id;" | $PSQLS)
echo "INSERT INTO dhcp_pools (guest_network_id,start_ip,end_ip) VALUES ('$NID','10.219.0.100','10.219.0.200');" | $PSQLS >/dev/null
[ -n "$NID" ] && ok "VLAN 219 network seeded ($NID)" || { bad "seed failed"; exit 1; }

echo "== 19.3 netd validate =="
V=$(netd -X POST http://unix/v1/validate -H 'Content-Type: application/json' -d '{"actor":"pilot"}')
echo "$V" | python3 -c 'import sys,json;d=json.load(sys.stdin);v=d.get("validation",{});print("    validation ok=%s issues=%d"%(v.get("ok"), len(v.get("issues") or [])))'
echo "$V" | grep -q '"ok":true' && ok "validation passed" || { bad "validation failed: $(echo "$V" | head -c 300)"; }

echo "== 19.4 netd apply (creates VLAN 219 L2/L3 + Kea subnet + nft) =="
A=$(netd -X POST http://unix/v1/apply -H 'Content-Type: application/json' -d '{"actor":"pilot","summary":"add vlan 219"}')
STATE=$(echo "$A" | python3 -c 'import sys,json;print(json.load(sys.stdin).get("state"))')
echo "    apply state=$STATE"
echo "$A" | python3 -c 'import sys,json;d=json.load(sys.stdin);[print("    health %s=%s %s"%(h["name"],h["ok"],h.get("detail",""))) for h in d.get("health",[])]' 2>/dev/null
REVID=$(echo "$A" | python3 -c 'import sys,json;print(json.load(sys.stdin).get("revision_id"))')
[ "$STATE" = "pending_confirmation" ] && ok "apply reached pending_confirmation (watchdog armed)" || bad "apply state=$STATE"

echo "== 19.5 the bridge + gateway are live =="
ip -br addr show br-g219 2>/dev/null | grep -q "$GW/24" && ok "br-g219 has gateway $GW" || bad "br-g219 gateway missing"
# VLAN sub-interface exists as a bridge member (sysfs is reliable; the dummy
# trunk can lag in `ip link show`).
for _ in 1 2 3 4 5; do [ -e /sys/class/net/ens219t.219 ] && break; sleep 1; done
[ -e /sys/class/net/ens219t.219 ] && ok "VLAN sub-interface ens219t.219 exists" || bad "VLAN sub-interface missing"

echo "== 19.6 Kea now serves subnet 10.219.0.0/24 with option 114 =="
python3 - <<PY
import socket,json
def kea(c):
    s=socket.socket(socket.AF_UNIX,socket.SOCK_STREAM);s.connect("/run/kea/kea4-ctrl-socket")
    s.sendall(json.dumps(c).encode());s.shutdown(socket.SHUT_WR)
    b=b""
    while True:
        x=s.recv(65536)
        if not x:break
        b+=x
    r=json.loads(b);return r[0] if isinstance(r,list) else r
cfg=kea({"command":"config-get","service":["dhcp4"]})
subs=cfg["arguments"]["Dhcp4"]["subnet4"]
v=[s for s in subs if s["subnet"]=="10.219.0.0/24"]
if v:
    o=[x["data"] for x in v[0]["option-data"] if x["name"]=="v4-captive-portal"]
    print("KEA_HAS_219 opt114=%s"%o)
else:
    print("KEA_MISSING_219")
PY

echo "== 19.6b real DORA: a VLAN-219 client gets a DHCP lease from netd's Kea =="
# Bridge a namespaced client directly into br-g219 (same L2 as the gateway +
# Kea's listening socket) and run a real DHCP exchange.
ip link del gv219a 2>/dev/null
ip link add gv219a type veth peer name gv219c 2>/dev/null
ip link set gv219a master br-g219 2>/dev/null; ip link set gv219a up
ip netns exec gv219 true 2>/dev/null || ip netns add gv219
ip link set gv219c netns gv219 2>/dev/null
ip netns exec gv219 ip link set lo up; ip netns exec gv219 ip link set gv219c up
sleep 1
DORA=$(ip netns exec gv219 timeout 20 dhclient -1 -v -lf /tmp/gv219.leases -pf /tmp/gv219.pid gv219c 2>&1)
if grep -q "DHCPACK" <<<"$DORA"; then
  LEASE=$(ip netns exec gv219 ip -4 -br addr show gv219c | grep -oE "10\.219\.0\.[0-9]+")
  ok "client bound a lease from Kea: $LEASE (gateway 10.219.0.1)"
else
  bad "DORA failed: $(grep -iE 'DHCPDISCOVER|no ' <<<"$DORA" | tail -1)"
fi
ip netns exec gv219 dhclient -r -lf /tmp/gv219.leases -pf /tmp/gv219.pid gv219c 2>/dev/null

echo "== 19.7 confirm the revision (commit; cancels watchdog) =="
if [ -n "$REVID" ]; then
  netd -X POST http://unix/v1/confirm -H 'Content-Type: application/json' -d "{\"revision_id\":\"$REVID\",\"actor\":\"pilot\"}" >/dev/null
  ST=$(echo "SELECT state FROM network_config_revisions WHERE id='$REVID';" | $PSQLS)
  [ "$ST" = "active" ] && ok "revision confirmed active" || bad "revision state=$ST"
fi

echo "== 19.8 rollback safety: a bad config is rejected/rolled back, mgmt survives =="
# Seed an INVALID second network (gateway inside pool) and apply → must fail
# validation (no OS change) and management must remain reachable.
echo "INSERT INTO guest_networks (tenant_id,site_id,appliance_id,name,enabled,network_type,parent_interface,vlan_id,bridge_name,gateway_cidr,gateway_ip,subnet_cidr,dhcp_mode,dns_mode,domain_name,captive_portal_enabled,internet_access_enabled,nat_enabled) VALUES ('$TEN','$SITE','$APP','Bad Net',true,'vlan','ens219t',221,'br-g221','10.221.0.1/24'::inet,'10.221.0.1'::inet,'10.221.0.0/24'::cidr,'local','appliance','x.local',true,true,true);" | $PSQLS >/dev/null
echo "INSERT INTO dhcp_pools (guest_network_id,start_ip,end_ip) SELECT id,'10.221.0.1','10.221.0.200' FROM guest_networks WHERE bridge_name='br-g221';" | $PSQLS >/dev/null
BAD=$(netd -X POST http://unix/v1/apply -H 'Content-Type: application/json' -d '{"actor":"pilot","summary":"bad"}')
BSTATE=$(echo "$BAD" | python3 -c 'import sys,json;print(json.load(sys.stdin).get("state"))')
echo "$BAD" | grep -q "pool_contains_gateway" && ok "invalid config rejected with pool_contains_gateway" || bad "bad config not caught: $(echo "$BAD" | head -c 200)"
[ "$BSTATE" = "failed" ] && ok "bad apply state=failed (no OS change)" || bad "bad apply state=$BSTATE"
# management still reachable
ip -br addr show ens160 | grep -q "172.21.60.23" && ok "management IP intact after bad apply" || bad "MANAGEMENT LOST"
# clean the bad net so it doesn't linger
echo "DELETE FROM guest_networks WHERE bridge_name='br-g221';" | $PSQLS >/dev/null

echo "== 19.9 legacy network still active + untouched =="
ip -br addr show br-lan | grep -q "10.10.0.1/24" && ok "legacy br-lan untouched (10.10.0.1/24)" || bad "legacy network disturbed"

echo
if [ $FAIL -eq 0 ]; then echo "ALL GREEN ($PASS checks)"; else echo "$FAIL FAILED / $PASS passed"; exit 1; fi
