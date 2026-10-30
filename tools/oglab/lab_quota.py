#!/usr/bin/env python3
"""Lab appliance: a plan with a DATA allowance (bytes) and N single-use vouchers for it (codes -> file, never printed).
usage: lab_quota.py <count> <out-file> <quota-bytes> [code=LAB-Q]"""
import json, os, sys
import labapi
call = labapi.call
N, OUT, QUOTA = int(sys.argv[1]), sys.argv[2], int(sys.argv[3])
PKG = sys.argv[4] if len(sys.argv) > 4 else "LAB-Q"

code = PKG + "-PLAN"
st, plans = call("GET", "/commercial-packages/plans")
if not any(p["code"] == code for p in plans.get("data", [])):
    st, out = call("POST", "/commercial-packages/plans", {
        "code": code, "name": "Lab data-quota plan", "down_kbps": 20000, "up_kbps": 10000, "max_concurrent_devices": 1,
        "device_limit_policy": "REJECT_NEW_DEVICE", "time_accounting_mode": "VALIDITY_WINDOW", "speed_allocation": "PER_DEVICE",
        "data_quota_bytes": QUOTA, "create_only": True})
    print("plan ->", st, json.dumps(out)[:200])
st, plans = call("GET", "/commercial-packages/plans")
plan = next(p for p in plans["data"] if p["code"] == code)
st, pk = call("GET", "/commercial-packages")
if not any(p["code"] == PKG for p in pk["data"]):
    st, out = call("POST", "/commercial-packages", {
        "code": PKG, "service_plan_revision_id": plan["current_revision_id"], "display": {"name": "Lab data quota"},
        "duration_policy": {"end_mode": "VALIDITY_WINDOW", "duration_seconds": 7200}, "grant_tiers": [{"order": 10, "grant": {}}],
        "create_only": True, "price_minor": 0, "currency": "EGP", "currency_exponent": 2, "acquisition_methods": ["PREPAID"]})
    print("package ->", st, json.dumps(out)[:300])
st, pk = call("GET", "/commercial-packages")
rev = next(p for p in pk["data"] if p["code"] == PKG)["current_revision_id"]
st, out = call("POST", "/vouchers/issue", {"package_revision_id": rev, "count": N, "note": "lab quota run", "password": labapi.PW})
if st not in (200, 201):
    print("issue failed", st, json.dumps(out)[:300]); sys.exit(1)
codes = [c if isinstance(c, str) else (c.get("code") or c.get("plaintext") or list(c.values())[0]) for c in out["codes"]]
open(OUT, "w").write("\n".join(codes) + "\n")
os.chmod(OUT, 0o600)
print("issued", len(codes), "quota vouchers ->", OUT, "(codes not shown); quota", QUOTA, "bytes; revision", rev)
