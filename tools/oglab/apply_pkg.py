#!/usr/bin/env python3
"""On the lab appliance: apply an offline activation package file BYTE FOR BYTE (as the console uploads it).
apply_pkg.py <file> [mode]   mode = none | sig | serial | appliance | expiry | truncate  (tampering is done on the text, other bytes untouched)"""
import json, re, sys, urllib.request
import labapi

path = sys.argv[1]
mode = sys.argv[2] if len(sys.argv) > 2 else "none"
raw = open(path, "rb").read().decode("utf-8")
# Central's console saves the inner package object (the wrapper {"package_id","package"} is the API envelope); unwrap it the same way.
_doc = json.loads(raw)
if isinstance(_doc, dict) and "package" in _doc and "request_id" not in _doc:
    raw = json.dumps(_doc["package"])
if mode == "sig":
    m = re.search(r'"signature":\s*"([^"]+)"', raw)
    s = m.group(1)
    raw = raw[:m.start(1)] + ("A" if s[0] != "A" else "B") + s[1:] + raw[m.end(1):]
elif mode == "serial":
    raw = re.sub(r'("serial":\s*")[^"]+(")', r'\1SC-FAKE-FAKE-FAKE\2', raw, count=1)
elif mode == "appliance":
    raw = re.sub(r'("appliance_id":\s*")[^"]+(")', r'\g<1>00000000-0000-4000-8000-000000000000\2', raw, count=1)
elif mode == "expiry":
    m = re.search(r'"expires_at":\s*(\d+)', raw)
    raw = raw[:m.start(1)] + str(int(m.group(1)) + 10 ** 9) + raw[m.end(1):]
elif mode == "truncate":
    raw = raw[: len(raw) // 2]
req = urllib.request.Request(labapi.BASE + "/central/offline-package", data=raw.encode("utf-8"), method="POST",
                             headers={"Host": labapi.HOST, "Content-Type": "application/json", "Origin": "https://" + labapi.HOST})
try:
    with labapi.op.open(req, timeout=120) as r:
        st, body = r.status, r.read().decode(errors="replace")
except urllib.error.HTTPError as e:
    st, body = e.code, e.read().decode(errors="replace")
print(mode, "->", st, body.replace("\n", " ")[:380])
