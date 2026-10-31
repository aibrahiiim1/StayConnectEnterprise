#!/usr/bin/env python3
"""Appliance-side: section 1 of NETWORK_19_LAB_PLAN (refusals). Prints one JSON line per check. Uses /root/oglab/labapi.py."""
import json, sys
sys.path.insert(0, "/root/oglab")
import labapi

call = labapi.call


def net(name, cidr, gw, vlan, pool, extra=None):
    d = {"name": name, "network_type": "vlan", "parent_interface": "eth1", "vlan_id": vlan, "gateway_ip": gw, "subnet_cidr": cidr,
         "dhcp_mode": "local", "pools": [{"start_ip": pool[0], "end_ip": pool[1]}], "lease_default_seconds": 3600, "lease_min_seconds": 900,
         "lease_max_seconds": 7200, "captive_portal_enabled": True, "internet_access_enabled": True, "nat_enabled": True}
    if extra:
        d.update(extra)
    return d


def show(tag, st, out, expect):
    code = out.get("error") or out.get("code") if isinstance(out, dict) else None
    print(json.dumps({"check": tag, "http": st, "code": code, "message": (out.get("message") or "")[:160] if isinstance(out, dict) else "", "expected": expect}))


st, out = call("POST", "/network/guest-networks", net("t18", "10.66.0.0/18", "10.66.0.1", 118, ("10.66.0.10", "10.66.63.250")))
show("1.1 /18", st, out, "400 subnet_too_large")
for c, gw, p in (("10.64.0.0/17", "10.64.0.1", ("10.64.0.10", "10.64.127.250")), ("10.0.0.0/8", "10.0.0.1", ("10.0.0.10", "10.255.255.250"))):
    st, out = call("POST", "/network/guest-networks", net("t" + c, c, gw, 117, p))
    show("1.2 " + c, st, out, "400 subnet_too_large")
st, out = call("POST", "/network/guest-networks", net("t31", "10.66.200.0/31", "10.66.200.1", 131, ("10.66.200.0", "10.66.200.1")))
show("1.3 /31", st, out, "400 subnet_too_small")
# 1.4: a /24 first (kept until the check is done), then a /19 that contains it
st, out24 = call("POST", "/network/guest-networks", net("lab24", "10.66.16.0/24", "10.66.16.1", 116, ("10.66.16.10", "10.66.16.250")))
show("1.4 setup /24", st, out24, "201")
st, out = call("POST", "/network/guest-networks", net("lab19x", "10.66.0.0/19", "10.66.0.1", 119, ("10.66.0.10", "10.66.15.250")))
show("1.4 /19 over a /24", st, out, "409 subnet_overlap")
id24 = out24.get("id")
if id24:
    st, out = call("DELETE", "/network/guest-networks/%s" % id24)
    print(json.dumps({"check": "1.4 cleanup /24", "http": st}))
# 1.5: the WAN subnet (eth0 172.30.10.20/24) inside a /19
st, out = call("POST", "/network/guest-networks", net("lab19wan", "172.30.0.0/19", "172.30.0.1", 130, ("172.30.0.10", "172.30.31.250")))
show("1.5 /19 containing the WAN address", st, out, "400 subnet_overlaps_host")
if st == 201 and out.get("id"):
    call("DELETE", "/network/guest-networks/%s" % out["id"])
# 1.6: widen an enabled /20 to the enclosing /19 in place
st, nets = call("GET", "/network/guest-networks")
rows = nets if isinstance(nets, list) else nets.get("data") or nets.get("items") or []
a = next((n for n in rows if n.get("subnet_cidr") == "10.60.0.0/20"), None)
if a:
    body = dict(a)
    body["subnet_cidr"] = "10.60.0.0/19"
    st, out = call("PUT", "/network/guest-networks/%s" % a["id"], body)
    show("1.6 widen enabled /20 in place", st, out, "409 subnet_widen_refused")
    st, after = call("GET", "/network/guest-networks/%s" % a["id"])
    print(json.dumps({"check": "1.6 the /20 is untouched", "subnet_cidr": after.get("subnet_cidr"), "enabled": after.get("enabled")}))
# 1.7: LAN prefix on Network -> System
st, sysn = call("GET", "/network/system")
print(json.dumps({"check": "1.7 system read", "http": st, "keys": list(sysn.keys())[:12] if isinstance(sysn, dict) else None}))
