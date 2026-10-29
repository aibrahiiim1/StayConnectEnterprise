import json, sys, time
import labapi
call = labapi.call
# fresh-install check of the baseline seed fix: the policy page must answer without a manual row
st, pol = call("GET", "/network/enforcement-policy")
print("enforcement-policy on a fresh install ->", st, ("OK, history rows: %d" % len(pol.get("history", []))) if st == 200 else json.dumps(pol)[:200])

nets = [
    {"name": "Lab clients A", "network_type": "untagged", "parent_interface": "eth1", "gateway_ip": "10.60.0.1", "subnet_cidr": "10.60.0.0/20",
     "pools": [{"start_ip": "10.60.0.10", "end_ip": "10.60.15.250"}]},
    {"name": "Lab clients B", "network_type": "vlan", "parent_interface": "eth1", "vlan_id": 102, "gateway_ip": "10.61.0.1", "subnet_cidr": "10.61.0.0/20",
     "pools": [{"start_ip": "10.61.0.10", "end_ip": "10.61.15.250"}]},
]
for n in nets:
    n.update({"lease_default_seconds": 86400, "lease_min_seconds": 3600, "lease_max_seconds": 172800,
              "captive_portal_enabled": True, "internet_access_enabled": True, "nat_enabled": True})
    st, out = call("POST", "/network/guest-networks", n)
    print("network", n["name"], "->", st, json.dumps(out)[:160])

st, out = call("POST", "/network/apply", {"reason": "lab capacity networks", "your_password": labapi.PW, "password": labapi.PW})
print("apply ->", st, out.get("state"), out.get("message"), [c.get("name") + ":" + str(c.get("ok")) for c in (out.get("health") or [])])
rid = out.get("revision_id")
if st == 200 and out.get("state") == "pending_confirmation":
    st, o2 = call("POST", f"/network/revisions/{rid}/confirm", {"your_password": labapi.PW, "password": labapi.PW})
    print("confirm ->", st, o2)
