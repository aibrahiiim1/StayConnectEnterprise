#!/usr/bin/env python3
"""Test-Central operator API helper. Runs ON the test Central VM. Reads the admin credential from /root/.ogc-admin.
usage: central_api.py list | activate <appliance_id> | get <appliance_id> | retire <id> | delete <id> | raw METHOD PATH [json]"""
import http.cookiejar, json, ssl, sys, urllib.request

HOST = "central.oglab.test"
BASE = "https://127.0.0.1"
cred = dict(l.split(":", 1) for l in open("/root/.ogc-admin").read().split() if ":" in l)
ctx = ssl.create_default_context(cafile="/opt/stayconnect/central/tls/ca.crt")
ctx.check_hostname = False  # connecting to 127.0.0.1; the certificate is checked against Central's own CA
jar = http.cookiejar.CookieJar()
op = urllib.request.build_opener(urllib.request.HTTPSHandler(context=ctx), urllib.request.HTTPCookieProcessor(jar))


def call(method, path, body=None):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(BASE + path, data=data, method=method,
                                 headers={"Host": HOST, "Content-Type": "application/json", "Origin": "https://" + HOST})
    try:
        with op.open(req, timeout=60) as r:
            raw = r.read()
            return r.status, (json.loads(raw) if raw else {})
    except urllib.error.HTTPError as e:
        raw = e.read()
        try:
            return e.code, json.loads(raw)
        except Exception:
            return e.code, {"raw": raw.decode(errors="replace")[:400]}


st, out = call("POST", "/v1/auth/login", {"email": cred["email"], "password": cred["password"]})
if st != 200:
    sys.exit(f"login failed {st} {out}")


def reauth():
    st, out = call("POST", "/v1/auth/reauth", {"password": cred["password"]})
    if st not in (200, 204):
        sys.exit(f"reauth failed {st} {out}")


cmd = sys.argv[1]
if cmd == "list":
    print(json.dumps(call("GET", "/cloud/v1/appliances")[1], indent=1)[:4000])
elif cmd == "get":
    print(json.dumps(call("GET", f"/cloud/v1/appliances/{sys.argv[2]}")[1], indent=1)[:6000])
elif cmd == "activate":
    reauth()
    body = {"new_customer": {"name": "OGLab Capacity", "slug": "oglab-capacity"},
            "new_site": {"name": "Capacity Site", "code": "CAP01", "timezone": "Africa/Cairo", "country": "EG", "site_type": "OTHER"},
            "license": {"max_concurrent_online_guests": 2500, "valid_days": 90, "grace_period_days": 7, "modules": []},
            "reason": "isolated lab capacity acceptance (OGLab Central)", "activation_code": sys.argv[3]}
    print(call("POST", f"/cloud/v1/appliances/{sys.argv[2]}/activate", body))
elif cmd == "activate-existing":   # <appliance_id> <customer_id> <site_id>
    reauth()
    body = {"customer_id": sys.argv[3], "site_id": sys.argv[4],
            "license": {"max_concurrent_online_guests": 2500, "valid_days": 90, "grace_period_days": 7, "modules": []},
            "reason": "isolated lab acceptance, second appliance", "activation_code": sys.argv[5]}
    print(call("POST", f"/cloud/v1/appliances/{sys.argv[2]}/activate", body))
elif cmd == "raw":
    if sys.argv[2] != "GET":
        reauth()
    print(call(sys.argv[2], sys.argv[3], json.loads(sys.argv[4]) if len(sys.argv) > 4 else None))
