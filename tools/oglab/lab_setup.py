#!/usr/bin/env python3
"""Lab appliance: one Client network on eth1 (/20), a validity-window plan + free voucher package, and N vouchers (codes -> file, never printed)."""
import json, os, sys
import labapi
call = labapi.call
N = int(sys.argv[1]) if len(sys.argv) > 1 else 10
OUT = sys.argv[2] if len(sys.argv) > 2 else "/root/vouchers.txt"
PKG = sys.argv[3] if len(sys.argv) > 3 else "LAB-CAP"
DUR = int(sys.argv[4]) if len(sys.argv) > 4 else 86400
APPEND = len(sys.argv) > 5 and sys.argv[5] == "append"

st, nets = call("GET", "/network/guest-networks")
rows = nets if isinstance(nets, list) else nets.get("data") or nets.get("items") or []
if not rows:
    st, out = call("POST", "/network/guest-networks", {
        "name": "Lab clients", "network_type": "untagged", "parent_interface": "eth1",
        "gateway_ip": "10.60.0.1", "subnet_cidr": "10.60.0.0/20",
        "pools": [{"start_ip": "10.60.0.10", "end_ip": "10.60.15.250"}],
        "lease_default_seconds": 86400, "lease_min_seconds": 3600, "lease_max_seconds": 172800,
        "captive_portal_enabled": True, "internet_access_enabled": True, "nat_enabled": True})
    print("network ->", st, json.dumps(out)[:300])
else:
    print("network exists", len(rows))

st, plans = call("GET", "/commercial-packages/plans")
code = PKG + "-PLAN"
if not any(p["code"] == code for p in plans.get("data", [])):
    st, out = call("POST", "/commercial-packages/plans", {
        "code": code, "name": "Lab capacity plan", "down_kbps": 20000, "up_kbps": 10000, "max_concurrent_devices": 1,
        "device_limit_policy": "REJECT_NEW_DEVICE", "time_accounting_mode": "VALIDITY_WINDOW",
        "speed_allocation": "PER_DEVICE", "create_only": True})
    print("plan ->", st, json.dumps(out)[:200])
st, plans = call("GET", "/commercial-packages/plans")
plan = next(p for p in plans["data"] if p["code"] == code)
st, pk = call("GET", "/commercial-packages")
if not any(p["code"] == PKG for p in pk["data"]):
    st, out = call("POST", "/commercial-packages", {
        "code": PKG, "service_plan_revision_id": plan["current_revision_id"], "display": {"name": "Lab capacity"},
        "duration_policy": {"end_mode": "VALIDITY_WINDOW", "duration_seconds": DUR}, "grant_tiers": [{"order": 10, "grant": {}}],
        "create_only": True, "price_minor": 0, "currency": "EGP", "currency_exponent": 2, "acquisition_methods": ["PREPAID"]})
    print("package ->", st, json.dumps(out)[:300])
st, pk = call("GET", "/commercial-packages")
rev = next(p for p in pk["data"] if p["code"] == PKG)["current_revision_id"]
codes = []
left = N
while left > 0:
    n = min(left, 500)
    st, out = call("POST", "/vouchers/issue", {"package_revision_id": rev, "count": n, "note": "lab capacity run", "password": labapi.PW})
    if st != 200 and st != 201:
        print("issue failed", st, json.dumps(out)[:300]); sys.exit(1)
    for c in out["codes"]:
        codes.append(c if isinstance(c, str) else (c.get("code") or c.get("plaintext") or list(c.values())[0]))
    left -= n
open(OUT, "a" if APPEND else "w").write("\n".join(codes) + "\n")
os.chmod(OUT, 0o600)
print("issued", len(codes), "vouchers ->", OUT, "(codes not shown); package revision", rev)
