import json, sys
sys.path.insert(0, "/root/oglab")
import labapi
call = labapi.call
st, nets = call("GET", "/network/guest-networks")
rows = nets if isinstance(nets, list) else nets.get("data") or []
lab24 = next((n for n in rows if n.get("name") == "lab24"), None)
if lab24:
    print("disable", call("POST", "/network/guest-networks/%s/disable" % lab24["id"], {"your_password": labapi.PW, "password": labapi.PW})[0])
    print("delete lab24", call("DELETE", "/network/guest-networks/%s" % lab24["id"])[0])
a = next(n for n in rows if n.get("subnet_cidr") == "10.60.0.0/20")
keys = ("name", "network_type", "parent_interface", "gateway_ip", "dhcp_mode", "pools", "lease_default_seconds", "lease_min_seconds", "lease_max_seconds",
        "captive_portal_enabled", "internet_access_enabled", "nat_enabled", "domain_name", "dns_mode")
body = {k: a[k] for k in keys if k in a}
body["subnet_cidr"] = "10.60.0.0/19"
st, out = call("PUT", "/network/guest-networks/%s" % a["id"], body)
print(json.dumps({"check": "1.6 widen enabled /20 in place", "http": st, "code": out.get("error"), "message": (out.get("message") or "")[:220], "expected": "409 subnet_widen_refused"}))
st, after = call("GET", "/network/guest-networks/%s" % a["id"])
print(json.dumps({"check": "1.6 the /20 is untouched", "subnet_cidr": after.get("subnet_cidr"), "enabled": after.get("enabled")}))
for ip, pl, label in (("10.70.0.1", 18, "1.7 LAN prefix 18"), ("10.70.0.1", 19, "1.7 LAN prefix 19 beside the WAN"), ("172.30.0.1", 19, "1.7 LAN /19 containing the WAN subnet")):
    prop = {"lan": {"physical_interface": "eth1", "ip": ip, "prefix_len": pl, "dhcp_enabled": False}}
    st, out = call("POST", "/network/system/validate", prop)
    print(json.dumps({"check": label, "http": st, "ok": out.get("ok"), "issues": [(i.get("code"), i.get("field")) for i in out.get("issues", [])] if isinstance(out, dict) else None}))
