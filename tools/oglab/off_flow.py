#!/usr/bin/env python3
"""Runs ON the lab Central. offline-flow <request.json> <activation_code> [customer_id] -> imports the appliance's offline request,
activates the appliance, mints the activation package into /root/offpkg.json. Prints ids and states only (no secrets)."""
import json, sys
sys.argv_saved = list(sys.argv)
sys.argv = ["central_api.py", "list"]          # reuse the helper's login without running a command
import io, contextlib
buf = io.StringIO()
with contextlib.redirect_stdout(buf):
    import runpy
    g = runpy.run_path("/root/central_api.py")
call, reauth, cred = g["call"], g["reauth"], g["cred"]
args = [a for a in sys.argv_saved[1:] if not a.startswith("--")]
req = json.load(open(args[0]))
code = args[1]
customer = args[2] if len(args) > 2 else None

reauth()
st, out = call("POST", "/cloud/v1/offline-activation/requests?activation_code=" + code, req)
print("import request ->", st, json.dumps(out)[:200])
if st != 200:
    sys.exit(1)
app_id = out.get("id") or out.get("appliance_id")
print("appliance id:", app_id, "| activation:", out.get("activation"), "| offline_request_pending:", out.get("offline_request_pending"))

body = {"new_site": {"name": "Offline Site", "code": "OFF01", "timezone": "Africa/Cairo", "country": "EG", "site_type": "OTHER"},
        "license": {"max_concurrent_online_guests": 50, "valid_days": 30, "grace_period_days": 7, "modules": []},
        "reason": "isolated lab: first-time offline activation acceptance", "activation_code": code}
if customer:
    body["customer_id"] = customer
else:
    body["new_customer"] = {"name": "OGLab Offline", "slug": "oglab-offline"}
reauth()
st, out = call("POST", f"/cloud/v1/appliances/{app_id}/activate", body)
print("activate ->", st, (json.dumps({k: out.get(k) for k in ("activation", "customer_name", "site_name")}) if st == 200 else json.dumps(out)[:300]))
if st != 200 and "--package-only" not in sys.argv_saved:
    sys.exit(1)

reauth()
import urllib.request, urllib.error
rq = urllib.request.Request(g["BASE"] + f"/cloud/v1/appliances/{app_id}/offline-activation-package", data=json.dumps({"valid_hours": 24}).encode(), method="POST",
                            headers={"Host": g["HOST"], "Content-Type": "application/json", "Origin": "https://" + g["HOST"]})
try:
    with g["op"].open(rq, timeout=60) as r:
        st, rawbody = r.status, r.read()
except urllib.error.HTTPError as e:
    st, rawbody = e.code, e.read()
print("package ->", st, len(rawbody), "bytes")
if st not in (200, 201):
    print(rawbody[:300]); sys.exit(1)
open("/root/offpkg.json", "wb").write(rawbody)   # byte for byte, exactly what the console downloads
print("package saved to /root/offpkg.json (raw)")
