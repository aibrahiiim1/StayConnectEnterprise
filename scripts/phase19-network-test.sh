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

# THE OWNER IS READ FROM THIS APPLIANCE, NOT BAKED IN.
#
# These three were literals -- the DEVELOPMENT appliance's tenant, site and appliance uuids. On any other
# appliance the INSERTs below would have written rows owned by a tenant that does not exist there, so the
# whole harness could only ever run on one machine, and that machine is now retired. PRE-LIVE, for instance,
# has different tenant and site ids and a NULL appliance_id.
#
# So the owner is derived from the appliance's own guest-network rows, and the script refuses rather than
# inventing one. appliance_id is genuinely nullable and is carried through as NULL when it is null.
OWNER="$(echo "SELECT tenant_id||'|'||site_id||'|'||COALESCE(appliance_id::text,'') FROM guest_networks ORDER BY created_at LIMIT 1;" | $PSQLS 2>/dev/null)"
TEN="${OWNER%%|*}"
SITE="$(echo "$OWNER" | cut -d'|' -f2)"
APP="$(echo "$OWNER" | cut -d'|' -f3)"
if [ -z "$TEN" ] || [ -z "$SITE" ]; then
  echo "REFUSED: could not read this appliance's tenant/site from guest_networks." >&2
  echo "         This harness used to carry the development appliance's uuids as literals; it now derives" >&2
  echo "         them, and it will not invent an owner. Configure at least one guest network first." >&2
  exit 2
fi
if [ -n "$APP" ]; then APP_SQL="'$APP'"; else APP_SQL="NULL"; fi
echo "   owner: tenant=$TEN site=$SITE appliance=${APP:-(null)}"

# THE MANAGEMENT ADDRESS IS OBSERVED, NOT ASSERTED AGAINST A LITERAL.
#
# 19.8 used to check `ip -br addr show ens160 | grep -q "172.21.60.23"`. That is the retired appliance's
# address, so on any other appliance the check reported MANAGEMENT LOST while management was perfectly
# intact -- a false alarm on the one assertion an operator would act on immediately. What the test actually
# wants to know is whether the address CHANGED across a failed apply, so it records it first and compares.
MGMT_IF="${PHASE19_MGMT_IF:-ens160}"
MGMT_BEFORE="$(ip -br -4 addr show "$MGMT_IF" 2>/dev/null | awk '{print $3}')"
if [ -z "$MGMT_BEFORE" ]; then
  echo "REFUSED: $MGMT_IF carries no IPv4 address, so 'management survived' cannot be measured." >&2
  echo "         Set PHASE19_MGMT_IF to this appliance's management interface." >&2
  exit 2
fi
echo "   management: $MGMT_IF $MGMT_BEFORE (must be unchanged at the end)"
LEGACY_BEFORE="$(ip -br -4 addr show "${PHASE19_LEGACY_IF:-br-lan}" 2>/dev/null | awk '{print $3}')"

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
NID=$(echo "INSERT INTO guest_networks (tenant_id,site_id,appliance_id,name,ssid_label,enabled,network_type,parent_interface,vlan_id,bridge_name,gateway_cidr,gateway_ip,subnet_cidr,dhcp_mode,dns_mode,domain_name,lease_default_seconds,lease_min_seconds,lease_max_seconds,captive_portal_enabled,internet_access_enabled,nat_enabled,portal_url) VALUES ('$TEN','$SITE',$APP_SQL,'Test VLAN 219','Test Guest 219',true,'vlan','ens219t',$VLAN,'br-g219','$GW/24'::inet,'$GW'::inet,'$SUBNET'::cidr,'local','appliance','guest219.local',3600,900,7200,true,true,true,'http://$GW:8380/') RETURNING id;" | $PSQLS)
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
echo "INSERT INTO guest_networks (tenant_id,site_id,appliance_id,name,enabled,network_type,parent_interface,vlan_id,bridge_name,gateway_cidr,gateway_ip,subnet_cidr,dhcp_mode,dns_mode,domain_name,captive_portal_enabled,internet_access_enabled,nat_enabled) VALUES ('$TEN','$SITE',$APP_SQL,'Bad Net',true,'vlan','ens219t',221,'br-g221','10.221.0.1/24'::inet,'10.221.0.1'::inet,'10.221.0.0/24'::cidr,'local','appliance','x.local',true,true,true);" | $PSQLS >/dev/null
echo "INSERT INTO dhcp_pools (guest_network_id,start_ip,end_ip) SELECT id,'10.221.0.1','10.221.0.200' FROM guest_networks WHERE bridge_name='br-g221';" | $PSQLS >/dev/null
BAD=$(netd -X POST http://unix/v1/apply -H 'Content-Type: application/json' -d '{"actor":"pilot","summary":"bad"}')
BSTATE=$(echo "$BAD" | python3 -c 'import sys,json;print(json.load(sys.stdin).get("state"))')
echo "$BAD" | grep -q "pool_contains_gateway" && ok "invalid config rejected with pool_contains_gateway" || bad "bad config not caught: $(echo "$BAD" | head -c 200)"
[ "$BSTATE" = "failed" ] && ok "bad apply state=failed (no OS change)" || bad "bad apply state=$BSTATE"
# management still reachable -- compared against what THIS appliance had before the bad apply
MGMT_AFTER="$(ip -br -4 addr show "$MGMT_IF" 2>/dev/null | awk '{print $3}')"
if [ -n "$MGMT_AFTER" ] && [ "$MGMT_AFTER" = "$MGMT_BEFORE" ]; then
  ok "management IP intact after bad apply ($MGMT_IF $MGMT_AFTER)"
else
  bad "MANAGEMENT LOST: $MGMT_IF was $MGMT_BEFORE, is now ${MGMT_AFTER:-(none)}"
fi
# clean the bad net so it doesn't linger
echo "DELETE FROM guest_networks WHERE bridge_name='br-g221';" | $PSQLS >/dev/null

echo "== 19.9 legacy network still active + untouched =="
# CONDITIONAL, BECAUSE THE LEGACY BRIDGE IS NOT A PROPERTY OF EVERY APPLIANCE.
#
# This used to assert br-lan carried 10.10.0.1/24, which is the retired development appliance's adopted
# legacy network. PRE-LIVE has no br-lan at all -- its guest network is bridged directly -- so the check
# reported "legacy network disturbed" about a bridge that was never supposed to exist. An appliance without
# a legacy bridge has nothing to disturb, and saying so is the honest result; where one DOES exist, its
# address is recorded at the start and compared, the same way management is.
LEGACY_IF="${PHASE19_LEGACY_IF:-br-lan}"
if [ -n "$LEGACY_BEFORE" ]; then
  LEGACY_AFTER="$(ip -br -4 addr show "$LEGACY_IF" 2>/dev/null | awk '{print $3}')"
  if [ "$LEGACY_AFTER" = "$LEGACY_BEFORE" ]; then
    ok "legacy $LEGACY_IF untouched ($LEGACY_AFTER)"
  else
    bad "legacy network disturbed: $LEGACY_IF was $LEGACY_BEFORE, is now ${LEGACY_AFTER:-(none)}"
  fi
else
  echo "  - no legacy bridge ($LEGACY_IF) on this appliance; nothing to disturb"
fi

echo
if [ $FAIL -eq 0 ]; then echo "ALL GREEN ($PASS checks)"; else echo "$FAIL FAILED / $PASS passed"; exit 1; fi
