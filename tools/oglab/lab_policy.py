import json, sys
import labapi
st, cur = labapi.call("GET", "/network/enforcement-policy")
print("get", st, json.dumps(cur)[:300])
v = cur.get("config_version") or cur.get("version") or (cur.get("data") or {}).get("config_version")
body = {"private_allow_cidrs": ["172.30.10.50/32"], "reason": "lab sink exception for the capacity run", "password": labapi.PW}
if v is not None:
    body["expected_version"] = v
st, out = labapi.call("PATCH", "/network/enforcement-policy", body)
print("patch", st, json.dumps(out)[:300])
