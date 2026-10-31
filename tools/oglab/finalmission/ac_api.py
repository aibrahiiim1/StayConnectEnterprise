import json, sys
sys.path.insert(0, "/root/oglab")
import labapi
call = labapi.call
PW = labapi.PW


def show(tag, st, out, keys=None):
    if keys and isinstance(out, dict):
        out = {k: out.get(k) for k in keys}
    print(json.dumps({"check": tag, "http": st, "body": out})[:700])


st, g = call("GET", "/after-checkout-access")
show("GET initial", st, g, ["enabled", "configured", "config_version", "policy", "effective"])
v = g.get("config_version") or 0
pol = {"duration_hours": 8, "down_kbps": 5000, "up_kbps": 2000, "data_quota_bytes": 1000000000, "device_limit": 5, "device_policy": "REJECT_NEW_DEVICE"}
st, out = call("PUT", "/after-checkout-access", dict(pol, expected_config_version=v, reason_code="LAB_WORKED_EXAMPLE", password=PW))
show("PUT worked-example policy (8 h, 5/2 Mbps, 1 GB shared, 5 devices)", st, out)
st, g2 = call("GET", "/after-checkout-access")
show("GET after", st, g2, ["enabled", "configured", "config_version", "policy", "effective"])
st, out = call("PUT", "/after-checkout-access", dict(pol, expected_config_version=v, reason_code="LAB_STALE", password=PW))
show("PUT with the stale version (expect 409 version_conflict)", st, out)
bad = dict(pol, duration_hours=100)
st, out = call("PUT", "/after-checkout-access", dict(bad, expected_config_version=g2["config_version"], reason_code="LAB_BOUNDS", password=PW))
show("PUT duration 100 h (expect 400 validation)", st, out)
bad = dict(pol, data_quota_bytes=1000)
st, out = call("PUT", "/after-checkout-access", dict(bad, expected_config_version=g2["config_version"], reason_code="LAB_BOUNDS", password=PW))
show("PUT quota 1000 bytes (expect 400 validation)", st, out)
st, out = call("PUT", "/after-checkout-access", dict(pol, expected_config_version=g2["config_version"], reason_code="lab bad", password=PW))
show("PUT invalid reason code (expect 400 reason_invalid)", st, out)
st, out = call("PUT", "/after-checkout-access", dict(pol, expected_config_version=g2["config_version"], reason_code="LAB_NOPW", password="wrong-password"))
show("PUT wrong step-up password (expect 401/403)", st, out)
st, out = call("PUT", "/after-checkout-access", {"expected_config_version": g2["config_version"], "reason_code": "LAB_NOTHING", "password": PW})
show("PUT with nothing to change (expect 400 nothing_to_change)", st, out)
st, out = call("PUT", "/after-checkout-access", {"enabled": False, "expected_config_version": g2["config_version"], "reason_code": "LAB_SWITCH_OFF", "password": PW})
show("PUT switch off", st, out)
st, g3 = call("GET", "/after-checkout-access")
show("GET switch off (effective source DISABLED expected)", st, g3, ["enabled", "config_version", "effective"])
st, out = call("PUT", "/after-checkout-access", {"enabled": True, "expected_config_version": g3["config_version"], "reason_code": "LAB_SWITCH_ON", "password": PW})
show("PUT switch on", st, out)
st, h = call("GET", "/after-checkout-access/history")
print(json.dumps({"check": "history (newest first)", "http": st, "rows": [(r.get("config_version"), r.get("reason_code"), (r.get("policy") or {}).get("change") or (r.get("policy") or {}).get("duration_hours")) for r in (h.get("data") or [])[:6]]}))
