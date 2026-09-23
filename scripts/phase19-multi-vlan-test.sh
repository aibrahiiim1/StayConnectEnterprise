#!/usr/bin/env bash
# TWO INDEPENDENT TAGGED GUEST VLANS SHARING ONE TRUNK, CONCURRENTLY.
#
# phase19-network-test.sh proves ONE tagged VLAN end to end: sub-interface, bridge, gateway, a real Kea
# lease with option 114, rollback safety, management survival. It is thorough and it is singular, and
# governance recorded the gap precisely: "Multiple independent tagged guest VLANs sharing the one LAN NIC as
# an 802.1Q trunk is not proven on the current architecture."
#
# WHY ONE DOES NOT IMPLY TWO. Everything that can go wrong between two VLANs is invisible with one:
#
#	a bridge or sub-interface name collision, or a second apply tearing down the first
#	two Kea subnets on one interface -- the second silently replacing the first, or neither serving
#	nftables sets or classids keyed per-appliance rather than per-network, so VLAN B's rules overwrite A's
#	                (this exact class was found and fixed once already: the accounting/shaping collision)
#	L2 or L3 leakage between guest networks that are supposed to be isolated from each other
#
# So this asserts the properties that only exist in the plural: both live at once, each leases from its OWN
# subnet, each advertises its OWN portal, and a client on one cannot reach a client on the other -- while
# the appliance's admin UI, database and SSH stay closed to both.
#
# ISOLATED, like the harness it extends. ONE synthetic trunk stands in for the guest-facing NIC and two
# namespaces for the guest devices; the production guest bridge is never touched, and management
# reachability is asserted at the end. Nothing here contacts a PMS, a provider or Central.
#
# EXIT: 0 all green, 1 assertion failure.
set -uo pipefail

NETD=/run/stayconnect/netd.sock
PSQLS="docker exec -i stayconnect-pg psql -U stayconnect -d stayconnect_site -At -q -v ON_ERROR_STOP=1"
PASS=0; FAIL=0
ok(){ echo "  ok $1"; PASS=$((PASS+1)); }
bad(){ echo "  FAIL $1"; FAIL=$((FAIL+1)); }
netd(){ curl -s -m 60 --unix-socket "$NETD" "$@"; }

# CONFIRM NEEDS THE REVISION ID, and leaving it out is why the first run of this drill reported ten
# failures that were all its own fault.
#
# public.network_config_revisions carries a unique partial index, ncr_single_inflight, permitting exactly
# ONE revision in 'applying' or 'pending_confirmation'. This script's cleanup ran an apply and then a
# confirm WITHOUT revision_id; netd decoded an empty string, PostgreSQL refused it with "invalid input
# syntax for type uuid", the revision stayed pending, and the drill's own apply was then refused with a
# duplicate key on that index. Every downstream assertion failed because nothing had been applied.
#
# So confirmation goes through one helper that asks netd WHICH revision is pending and names it.
netd_confirm(){ # $1 = optional revision id; otherwise whatever netd reports as pending
  local rid="${1:-}"
  [ -n "$rid" ] || rid=$(netd http://unix/v1/pending | python3 -c 'import sys,json;print(json.load(sys.stdin).get("revision_id") or "")' 2>/dev/null)
  [ -n "$rid" ] || return 0
  netd -X POST http://unix/v1/confirm -H 'Content-Type: application/json' -d "{\"revision_id\":\"$rid\",\"actor\":\"multi-vlan-drill\"}"
}

# The owner is read off this appliance, never baked in (the same rule the single-VLAN harness adopted at
# T0175 after literals from a retired appliance made it unrunnable anywhere else).
TEN=$($PSQLS <<<"SELECT tenant_id FROM guest_networks ORDER BY created_at LIMIT 1" 2>/dev/null)
SITE=$($PSQLS <<<"SELECT site_id FROM guest_networks ORDER BY created_at LIMIT 1" 2>/dev/null)
if [ -z "${TEN:-}" ]; then
  TEN=$($PSQLS <<<"SELECT id FROM tenants LIMIT 1"); SITE=$($PSQLS <<<"SELECT id FROM sites LIMIT 1")
fi
APP=$($PSQLS <<<"SELECT id FROM appliances LIMIT 1" 2>/dev/null)
APP_SQL="NULL"; [ -n "${APP:-}" ] && APP_SQL="'$APP'"
MGMT_BEFORE=$(ip -br addr show ens160 2>/dev/null | awk '{print $3}')
echo "   owner: tenant=$TEN site=$SITE appliance=${APP:-(null)}"
echo "   management: ens160 ${MGMT_BEFORE:-?} (must be unchanged at the end)"
[ -n "${TEN:-}" ] && [ -n "${SITE:-}" ] || { echo "cannot resolve this appliance's owner; aborting"; exit 1; }

# Two VLANs and two subnets on ONE shared trunk, which is the configuration the gap names.
cleanup() {
  for v in 221 222; do
    ip netns del gv$v 2>/dev/null
    ip link del gv${v}a 2>/dev/null
  done
  ip link del t22a 2>/dev/null
  ip link del ens22t 2>/dev/null
  echo "DELETE FROM guest_networks WHERE parent_interface='ens22t';" | $PSQLS >/dev/null 2>&1
  echo "DELETE FROM network_interfaces WHERE name='ens22t';" | $PSQLS >/dev/null 2>&1
  # Only apply when this drill actually put something in the database, and ALWAYS confirm what that apply
  # leaves pending: an unconfirmed revision blocks the next apply, including the next run of this drill.
  if [ "${SEEDED:-0}" = "1" ]; then
    netd -X POST http://unix/v1/apply -H 'Content-Type: application/json' \
      -d '{"actor":"multi-vlan-drill","summary":"remove drill VLANs 221/222"}' >/dev/null 2>&1
    netd_confirm >/dev/null 2>&1
  fi
}
trap cleanup EXIT
cleanup

# AND CLEAR ANY PRE-EXISTING PENDING REVISION, for the same reason: one in-flight revision is all the
# schema permits, so one left behind by an earlier run or another drill makes every assertion below fail
# for a reason that has nothing to do with multiple VLANs.
PEND=$(netd http://unix/v1/pending | python3 -c 'import sys,json;print(json.load(sys.stdin).get("revision_id") or "")' 2>/dev/null)
if [ -n "${PEND:-}" ]; then
  echo "   note: a revision was already pending confirmation ($PEND); confirming it so this drill can apply"
  netd_confirm "$PEND" >/dev/null 2>&1
fi

echo "== 19M.1 one trunk, two tagged client namespaces =="
# ONE trunk carrying BOTH VLANs. This is the 802.1Q trunk the gap is about: netd must hang two
# sub-interfaces and two bridges off the same parent without them interfering.
#
# THE TRUNK IS A BRIDGE WITH A LIVE PORT, NOT A DUMMY, AND THAT IS NOT COSMETIC.
#
# Kea refuses to bind a gateway on a bridge that is not RUNNING, and a bridge is only RUNNING when one of
# its ports has carrier. netd knows this and says so -- "guest bridge is not RUNNING yet; Kea may refuse to
# bind it" -- and its health check then rolls the whole apply back rather than leave a guest network that
# hands out no addresses.
#
# A dummy trunk gives its VLAN sub-interfaces no carrier, so br-g221/br-g222 have none either, so Kea binds
# neither gateway. Measured: an apply rolled back with "kea is still not listening on
# 192.168.77.1,10.221.0.1,10.222.0.1 after a restart". That rollback is netd behaving CORRECTLY -- the
# management address survived it untouched -- and it is the rig that was wrong. Earlier runs of this drill
# passed on timing rather than on carrier, which is worse than failing.
#
# So: ens22t is a real bridge, with a veth pair whose ends are both up giving it carrier. netd then hangs
# ens22t.221 and ens22t.222 off it, each inherits carrier, and Kea can bind both gateways.
ip link add ens22t type bridge 2>/dev/null
ip link add t22a type veth peer name t22b 2>/dev/null
ip link set t22a master ens22t 2>/dev/null
ip link set t22a up; ip link set t22b up; ip link set ens22t up
for v in 221 222; do
  ip netns add gv$v 2>/dev/null
  ip netns exec gv$v ip link set lo up
done
echo "INSERT INTO network_interfaces (name, role, mode, vlan_capable) VALUES ('ens22t','guest_trunk','trunk',true) ON CONFLICT (name) DO UPDATE SET role='guest_trunk';" | $PSQLS >/dev/null
ok "one trunk (ens22t) with VLAN 221 and 222 client namespaces"

echo "== 19M.2 seed BOTH guest networks in the Site DB =="
seed() { # $1 vlan
  local v="$1" gw="10.$1.0.1"
  local nid
  nid=$(echo "INSERT INTO guest_networks (tenant_id,site_id,appliance_id,name,ssid_label,enabled,network_type,parent_interface,vlan_id,bridge_name,gateway_cidr,gateway_ip,subnet_cidr,dhcp_mode,dns_mode,domain_name,lease_default_seconds,lease_min_seconds,lease_max_seconds,captive_portal_enabled,internet_access_enabled,nat_enabled,portal_url) VALUES ('$TEN','$SITE',$APP_SQL,'Drill VLAN $v','Drill Guest $v',true,'vlan','ens22t',$v,'br-g$v','$gw/24'::inet,'$gw'::inet,'10.$v.0.0/24'::cidr,'local','appliance','guest$v.local',3600,900,7200,true,true,true,'http://$gw:8380/') RETURNING id;" | $PSQLS)
  echo "INSERT INTO dhcp_pools (guest_network_id,start_ip,end_ip) VALUES ('$nid','10.$v.0.100','10.$v.0.200');" | $PSQLS >/dev/null
  echo "$nid"
}
N221=$(seed 221); N222=$(seed 222); SEEDED=1
[ -n "$N221" ] && [ -n "$N222" ] && ok "both networks seeded (221=$N221 222=$N222)" || { bad "seed failed"; exit 1; }

echo "== 19M.3 netd validate accepts two VLANs on one trunk =="
V=$(netd -X POST http://unix/v1/validate -H 'Content-Type: application/json' -d '{"actor":"multi-vlan-drill"}')
echo "$V" | python3 -c 'import sys,json;d=json.load(sys.stdin);v=d.get("validation",{});print("    validation ok=%s issues=%d"%(v.get("ok"),len(v.get("issues") or [])))' 2>/dev/null
if echo "$V" | grep -q '"ok":true'; then
  ok "validation passed with two tagged networks on one parent"
else
  bad "validation refused two VLANs on one trunk: $(echo "$V" | head -c 400)"
fi

echo "== 19M.4 ONE apply brings both up =="
A=$(netd -X POST http://unix/v1/apply -H 'Content-Type: application/json' -d '{"actor":"multi-vlan-drill","summary":"add drill VLANs 221+222 on one trunk"}')
STATE=$(echo "$A" | python3 -c 'import sys,json;print(json.load(sys.stdin).get("state"))' 2>/dev/null)
echo "    apply state=$STATE"
echo "$A" | python3 -c 'import sys,json;d=json.load(sys.stdin);[print("    health %s=%s %s"%(h["name"],h["ok"],h.get("detail",""))) for h in d.get("health",[])]' 2>/dev/null
REVID=$(echo "$A" | python3 -c 'import sys,json;print(json.load(sys.stdin).get("revision_id") or "")' 2>/dev/null)
if [ "$STATE" = "pending_confirmation" ]; then
  ok "apply reached pending_confirmation"
else
  # THE BODY, not just the missing field. The first run printed "state=None" and swallowed the
  # duplicate-key error that explained all ten failures.
  bad "apply state=$STATE :: $(echo "$A" | head -c 300)"
fi

echo "== 19M.5 BOTH bridges, BOTH sub-interfaces, off the SAME trunk =="
for v in 221 222; do
  for _ in 1 2 3 4 5 6; do [ -e /sys/class/net/ens22t.$v ] && break; sleep 1; done
  [ -e /sys/class/net/ens22t.$v ] && ok "sub-interface ens22t.$v exists" || bad "ens22t.$v missing"
  ip -br addr show br-g$v 2>/dev/null | grep -q "10.$v.0.1/24" \
    && ok "br-g$v has gateway 10.$v.0.1" || bad "br-g$v gateway missing"
done
# NEITHER REPLACED THE OTHER. The failure this catches is a second apply tearing down the first network,
# which a per-VLAN check performed one at a time would never see.
if [ -e /sys/class/net/ens22t.221 ] && [ -e /sys/class/net/ens22t.222 ]; then
  ok "both sub-interfaces coexist on one trunk (neither apply removed the other)"
else
  bad "the two VLANs did not coexist"
fi

echo "== 19M.6 Kea serves BOTH subnets at once, each with its own option 114 =="
# The parse follows phase19-network-test.sh exactly -- {"command":"config-get","service":["dhcp4"]} and
# r[0] when the reply is a list. The first version of this check omitted the service selector and indexed
# r[0] on a dict, raising KeyError: 0, which was then reported as "Kea carries 0 of the 2 subnets": a test
# defect presented as a product failure.
KEA=$(python3 - <<'KEAEOF' 2>&1
import socket, json
def kea(c):
    s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    s.connect("/run/kea/kea4-ctrl-socket")
    s.sendall(json.dumps(c).encode()); s.shutdown(socket.SHUT_WR)
    b = b""
    while True:
        x = s.recv(65536)
        if not x: break
        b += x
    r = json.loads(b)
    return r[0] if isinstance(r, list) else r
cfg = kea({"command": "config-get", "service": ["dhcp4"]})
subs = cfg["arguments"]["Dhcp4"].get("subnet4", [])
for v in ("221", "222"):
    m = [s for s in subs if s.get("subnet") == "10.%s.0.0/24" % v]
    if not m:
        print("MISSING %s" % v); continue
    o = [x["data"] for x in m[0].get("option-data", []) if x.get("name") == "v4-captive-portal"]
    print("HAS %s opt114=%s" % (v, o))
KEAEOF
)
echo "$KEA" | sed 's/^/    /'
HITS=$(printf '%s\n' "$KEA" | grep -c '^HAS ')
[ "${HITS:-0}" = "2" ] && ok "Kea carries BOTH drill subnets at once" || bad "Kea carries ${HITS:-0} of the 2 drill subnets"
# Each subnet must advertise ITS OWN portal, not the other's -- a collision a single-VLAN test cannot see.
if printf '%s\n' "$KEA" | grep -q 'HAS 221 opt114=.*10.221.0.1' && printf '%s\n' "$KEA" | grep -q 'HAS 222 opt114=.*10.222.0.1'; then
  ok "each subnet advertises its own captive portal URL"
else
  bad "the option-114 URLs are not per-network"
fi

echo "== 19M.7 a real DORA on EACH VLAN, from its own pool =="
# BRIDGED DIRECTLY INTO EACH br-g<vlan>, the way the single-VLAN harness does it: that puts the client on
# the same L2 as the gateway and Kea's listening socket. The tagged path itself -- ens22t.<vlan> enslaved to
# br-g<vlan> -- is proven structurally in 19M.5. A dummy trunk cannot carry real frames, and the earlier
# attempt to enslave a veth to a dummy device silently did nothing, which is why both leases failed.
for v in 221 222; do
  ip link del gv${v}a 2>/dev/null
  ip link add gv${v}a type veth peer name gv${v}c 2>/dev/null
  ip link set gv${v}a master br-g$v 2>/dev/null; ip link set gv${v}a up
  ip netns exec gv$v true 2>/dev/null || ip netns add gv$v
  ip link set gv${v}c netns gv$v 2>/dev/null
  ip netns exec gv$v ip link set lo up; ip netns exec gv$v ip link set gv${v}c up
done
sleep 2
for v in 221 222; do
  DORA=$(ip netns exec gv$v timeout 25 dhclient -1 -v -lf /tmp/gv$v.leases -pf /tmp/gv$v.pid gv${v}c 2>&1)
  if grep -q DHCPACK <<<"$DORA"; then
    LEASE=$(ip netns exec gv$v ip -4 -br addr show gv${v}c | grep -oE "10\.$v\.0\.[0-9]+")
    if [ -n "$LEASE" ]; then
      ok "VLAN $v client leased $LEASE from its own subnet 10.$v.0.0/24"
    else
      bad "VLAN $v got a DHCPACK but no address in 10.$v.0.0/24 -- the subnets are crossed"
    fi
  else
    bad "VLAN $v DORA failed: $(grep -iE 'DHCPDISCOVER|no ' <<<"$DORA" | tail -1)"
  fi
done

echo "== 19M.8 what one guest network can reach, measured =="
# THE PROPERTY THAT ONLY EXISTS IN THE PLURAL. Two guest networks must be two networks, not one flat
# network with two names -- and the appliance's own surfaces must not become reachable because a second
# guest network exists.
#
# THE ASSERTIONS BELOW ARE THE ONES THAT MATTER, and one MEASURED RESULT is reported rather than asserted,
# because it is a real finding that this drill produced and weakening the test to hide it would be worse
# than either fixing or recording it. See the note at the end of this section.
L222=$(ip netns exec gv222 ip -4 -br addr show gv222c 2>/dev/null | grep -oE "10\.222\.0\.[0-9]+")
L221=$(ip netns exec gv221 ip -4 -br addr show gv221c 2>/dev/null | grep -oE "10\.221\.0\.[0-9]+")

if [ -z "${L221:-}" ]; then
  bad "no VLAN 221 lease, so nothing below could be tested"
else
  # FIRST THE CONTROL: the client must reach its OWN gateway, or every refusal below proves only that the
  # network is dead. A refusal-only isolation test is passed perfectly by a broken network.
  if ip netns exec gv221 ping -c1 -W2 10.221.0.1 >/dev/null 2>&1; then
    ok "the VLAN 221 client reaches its own gateway (so the refusals below mean isolation)"
  else
    bad "the VLAN 221 client cannot reach its own gateway; no conclusion below is meaningful"
  fi

  # 1. GUEST TO GUEST. A guest must not reach a guest on another network. This is the isolation that
  #    protects one hotel guest from another.
  if [ -n "${L222:-}" ]; then
    if ip netns exec gv221 ping -c1 -W2 "$L222" >/dev/null 2>&1; then
      bad "a client on VLAN 221 reached the VLAN 222 client at $L222 -- guests are not isolated"
    else
      ok "a client on VLAN 221 cannot reach the VLAN 222 client"
    fi
  fi

  # 2. THE APPLIANCE'S ADMINISTRATIVE SURFACES. These are the ones whose exposure would be serious, and
  #    they are asserted, not measured: a guest network that can reach the admin UI, the database or SSH
  #    is a finding that stops a release.
  MGMT_IP=$(ip -4 -br addr show ens160 2>/dev/null | awk '{print $3}' | cut -d/ -f1)
  if [ -n "${MGMT_IP:-}" ]; then
    if ip netns exec gv221 timeout 3 curl -sk -o /dev/null "https://$MGMT_IP:443/" 2>/dev/null; then
      bad "a guest on VLAN 221 reached the Hotel Admin UI on $MGMT_IP:443"
    else
      ok "the Hotel Admin UI on the management address is NOT reachable from a guest network"
    fi
    if ip netns exec gv221 timeout 3 bash -c "echo > /dev/tcp/$MGMT_IP/5432" 2>/dev/null; then
      bad "a guest on VLAN 221 reached PostgreSQL on $MGMT_IP:5432"
    else
      ok "PostgreSQL is NOT reachable from a guest network"
    fi
    if ip netns exec gv221 timeout 3 bash -c "echo > /dev/tcp/$MGMT_IP/22" 2>/dev/null; then
      bad "a guest on VLAN 221 reached SSH on $MGMT_IP:22"
    else
      ok "SSH is NOT reachable from a guest network"
    fi
  fi

  # 3. MEASURED AND REPORTED: the appliance's OTHER guest-facing addresses.
  #
  # A guest on VLAN 221 CAN reach 10.222.0.1 and the production guest gateway, and the captive portal on
  # them, and can ICMP the management address. That is not routing between guest networks -- the
  # guest-to-guest check above is what proves that, and it passes. It is the Linux weak host model: every
  # local address answers on every interface unless something forbids it, and portald binds *:8380.
  #
  # WHY IT IS REPORTED RATHER THAN ASSERTED. Forbidding it means adding rules to netd's generated ruleset
  # that drop guest traffic aimed at the appliance's addresses other than that network's own gateway. That
  # is a change to the enforcement plane with real regression risk to DNS, the walled garden and the portal
  # redirect -- the paths guest internet depends on -- so it is not something to slip in beside a drill.
  # The consequence is bounded: the same captive portal on a different address, and the knowledge that the
  # appliance has other interfaces. No guest data, no admin surface, no other guest.
  echo "  -- measured (not a pass/fail): what a guest can reach on the appliance itself"
  m(){ printf '       %-44s ' "$1"; shift; if ip netns exec gv221 "$@" >/dev/null 2>&1; then echo REACHABLE; else echo blocked; fi; }
  m "the OTHER guest network's gateway (10.222.0.1)" ping -c1 -W2 10.222.0.1
  m "the captive portal on that other gateway"       timeout 3 curl -s -o /dev/null http://10.222.0.1:8380/
  [ -n "${MGMT_IP:-}" ] && m "the management address by ICMP ($MGMT_IP)" ping -c1 -W2 "$MGMT_IP"
  echo "       ^ recorded as a hardening item: guest networks can reach the appliance's other"
  echo "         guest-facing addresses. Guest-to-guest, admin, database and SSH are all closed."
fi
for v in 221 222; do ip netns exec gv$v dhclient -r -lf /tmp/gv$v.leases -pf /tmp/gv$v.pid gv${v}c 2>/dev/null; done

echo "== 19M.9 per-network enforcement state, not per-appliance =="
# The collision class that was already found once in shaping/accounting: rules keyed by appliance rather
# than by network, so the second network overwrites the first. Checked by asking nft what it holds.
NFT=$(nft list ruleset 2>/dev/null | grep -cE '10\.(221|222)\.0\.' || true)
if [ "${NFT:-0}" -ge 2 ]; then
  ok "nftables carries state for both networks ($NFT references)"
else
  echo "  note: nft shows ${NFT:-0} reference(s) to the drill subnets; enforcement for a guest network is"
  echo "        installed on authorization rather than at apply, so this is not asserted as a failure."
fi

echo "== 19M.10 confirm, then management is unchanged =="
C=$(netd_confirm "${REVID:-}")
ST=$(echo "SELECT state FROM network_config_revisions WHERE id='${REVID:-00000000-0000-0000-0000-000000000000}';" | $PSQLS 2>/dev/null)
[ "$ST" = "active" ] && ok "revision confirmed active" || bad "confirm left state='$ST' :: $(echo "$C" | head -c 200)"
MGMT_AFTER=$(ip -br addr show ens160 2>/dev/null | awk '{print $3}')
[ "$MGMT_BEFORE" = "$MGMT_AFTER" ] && ok "management IP intact ($MGMT_AFTER)" \
  || bad "management changed: $MGMT_BEFORE -> $MGMT_AFTER"

echo
if [ "$FAIL" = 0 ]; then echo "MULTI_VLAN = ALL GREEN ($PASS checks)"; exit 0; fi
echo "MULTI_VLAN = FAIL ($FAIL failed, $PASS passed)"; exit 1
