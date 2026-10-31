#!/usr/bin/env python3
"""Appliance-side: create the lab /19 (VLAN 119) and run the staged apply. usage: s19_apply.py create|apply|confirm|rollback|status [revision]"""
import json, sys, time
sys.path.insert(0, "/root/oglab")
import labapi

call = labapi.call
PW = labapi.PW
step = sys.argv[1]
NAME = "Lab /19"


def body(name, vlan, cidr, gw, p1, p2):
    return {"name": name, "network_type": "vlan", "parent_interface": "eth1", "vlan_id": vlan, "gateway_ip": gw, "subnet_cidr": cidr, "dhcp_mode": "local",
            "pools": [{"start_ip": p1, "end_ip": p2}], "lease_default_seconds": 3600, "lease_min_seconds": 900, "lease_max_seconds": 7200,
            "captive_portal_enabled": True, "internet_access_enabled": True, "nat_enabled": True}


def find(name):
    st, nets = call("GET", "/network/guest-networks")
    rows = nets if isinstance(nets, list) else nets.get("data") or []
    return next((n for n in rows if n.get("name") == name), None)


if step == "create":
    spec = sys.argv[2]   # a19 | b19 | f20
    b = {"a19": body(NAME, 119, "10.66.0.0/19", "10.66.0.1", "10.66.0.10", "10.66.31.250"),
         "b19": body("Lab /19 second", 120, "10.67.0.0/19", "10.67.0.1", "10.67.0.10", "10.67.31.250"),
         "f20": body("Lab /20 to replace", 121, "10.68.0.0/20", "10.68.0.1", "10.68.0.10", "10.68.15.250")}[spec]
    st, out = call("POST", "/network/guest-networks", b)
    print("create", spec, st, json.dumps(out)[:200])
elif step == "validate":
    st, out = call("POST", "/network/validate", {})
    print("validate", st, json.dumps(out)[:300])
elif step == "apply":
    t = time.time()
    st, out = call("POST", "/network/apply", {"reason": sys.argv[2] if len(sys.argv) > 2 else "/19 lab", "your_password": PW, "password": PW})
    print("apply", st, "took %.1fs" % (time.time() - t), json.dumps({"state": out.get("state"), "revision_id": out.get("revision_id"), "message": out.get("message"),
                                                                    "health": [(h.get("name"), h.get("ok"), h.get("detail")) for h in (out.get("health") or [])], "error": out.get("error")}))
elif step == "confirm":
    st, out = call("POST", "/network/revisions/%s/confirm" % sys.argv[2], {"your_password": PW, "password": PW})
    print("confirm", st, json.dumps(out)[:200])
elif step == "rollback":
    st, out = call("POST", "/network/revisions/%s/rollback" % sys.argv[2], {"your_password": PW, "password": PW})
    print("rollback", st, json.dumps(out)[:300])
elif step == "status":
    st, out = call("GET", "/network/revisions")
    for r in (out.get("data") or [])[:6]:
        print(json.dumps({k: r.get(k) for k in ("id", "seq", "state", "reason", "created_at")}))
