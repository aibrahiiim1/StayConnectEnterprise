#!/usr/bin/env python3
"""Lab appliance operator API helper (runs ON the lab appliance container). Never prints the password."""
import http.cookiejar, json, ssl, sys, urllib.request

HOST = "hotel.stayconnect.local"
EMAIL = "lab-admin@oglab.test"
PW = open("/root/adminpw.txt").read().strip()
BASE = "https://172.30.10.20/api/edge/v1"
ctx = ssl.create_default_context(cafile="/var/lib/caddy/.local/share/caddy/pki/authorities/local/root.crt")
ctx.check_hostname = False
jar = http.cookiejar.CookieJar()
op = urllib.request.build_opener(urllib.request.HTTPSHandler(context=ctx), urllib.request.HTTPCookieProcessor(jar))


def call(method, path, body=None, timeout=300):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(BASE + path, data=data, method=method,
                                 headers={"Host": HOST, "Content-Type": "application/json", "Origin": "https://" + HOST})
    try:
        with op.open(req, timeout=timeout) as r:
            raw = r.read()
            return r.status, (json.loads(raw) if raw else {})
    except urllib.error.HTTPError as e:
        raw = e.read()
        try:
            return e.code, json.loads(raw)
        except Exception:
            return e.code, {"raw": raw.decode(errors="replace")[:300]}


st, out = call("POST", "/auth/login", {"email": EMAIL, "password": PW})
if st != 200:
    sys.exit(f"login failed {st} {out}")

if __name__ == "__main__":
    body = None
    if len(sys.argv) > 3:
        body = json.loads(sys.argv[3].replace("@PW", json.dumps(PW)[1:-1]))
    st, out = call(sys.argv[1], sys.argv[2], body)
    print(st, json.dumps(out, indent=1)[:int(sys.argv[4]) if len(sys.argv) > 4 else 3000])
