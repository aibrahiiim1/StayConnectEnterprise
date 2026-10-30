import json, os
import labapi
st, out = labapi.call("POST", "/backup-keys/holders", {"label": "Lab backup holder", "acknowledge_loss": True, "password": labapi.PW})
print("add holder ->", st, "keys:", sorted(out.keys()) if isinstance(out, dict) else type(out))
if st not in (200, 201):
    print(json.dumps(out)[:300]); raise SystemExit(1)
open("/root/backup-holder.json", "w").write(json.dumps(out)); os.chmod("/root/backup-holder.json", 0o600)   # holds the secret key file; never printed
def find(d, *names):
    if isinstance(d, dict):
        for k, v in d.items():
            if k in names and isinstance(v, str):
                return v
            r = find(v, *names)
            if r: return r
    return None
hid = find(out, "id", "holder_id")
chk = find(out, "check_code")
print("holder id:", hid, "| check code present:", bool(chk))
st, o2 = labapi.call("POST", f"/backup-keys/holders/{hid}/confirm", {"check_code": chk})
print("confirm ->", st, json.dumps(o2)[:200])
