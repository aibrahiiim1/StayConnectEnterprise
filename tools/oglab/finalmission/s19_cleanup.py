import json, subprocess, sys, time
sys.path.insert(0, "/root/oglab")
import labapi
call = labapi.call
PW = labapi.PW
st, nets = call("GET", "/network/guest-networks")
rows = nets if isinstance(nets, list) else nets.get("data") or []
for n in rows:
    if n.get("name") in ("Lab /19", "Lab /19 (replaced the /20)", "Lab /20 to replace"):
        a = call("POST", "/network/guest-networks/%s/disable" % n["id"], {"your_password": PW, "password": PW})[0]
        print("disable", n["name"], a)
out = subprocess.run(["python3", "/root/s19_apply.py", "apply", "remove the /19 lab networks"], capture_output=True, text=True).stdout
print(out.strip()[:300])
rid = json.loads(out.split("took", 1)[1].split("s ", 1)[1])["revision_id"]
print(subprocess.run(["python3", "/root/s19_apply.py", "confirm", rid], capture_output=True, text=True).stdout.strip())
time.sleep(5)
st, nets = call("GET", "/network/guest-networks")
rows = nets if isinstance(nets, list) else nets.get("data") or []
for n in rows:
    if n.get("name") in ("Lab /19", "Lab /19 (replaced the /20)", "Lab /20 to replace"):
        print("delete", n["name"], call("DELETE", "/network/guest-networks/%s" % n["id"])[0])
st, nets = call("GET", "/network/guest-networks")
rows = nets if isinstance(nets, list) else nets.get("data") or []
print([(n["name"], n["subnet_cidr"], n["enabled"]) for n in rows])
