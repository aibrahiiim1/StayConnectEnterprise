import json, sys, time
sys.path.insert(0, "/root/oglab")
import labapi
call = labapi.call
PW = labapi.PW
st, nets = call("GET", "/network/guest-networks")
rows = nets if isinstance(nets, list) else nets.get("data") or []
b19 = next((n for n in rows if n.get("name") == "Lab /19 second"), None)
if b19:
    print("disable b19", call("POST", "/network/guest-networks/%s/disable" % b19["id"], {"your_password": PW, "password": PW})[0], "delete", call("DELETE", "/network/guest-networks/%s" % b19["id"])[0])
import subprocess


def run(*a):
    print(subprocess.run(["python3", "/root/s19_apply.py"] + list(a), capture_output=True, text=True).stdout.strip()[:420])


run("create", "f20")
out = subprocess.run(["python3", "/root/s19_apply.py", "apply", "f20 /20 for the replacement test"], capture_output=True, text=True).stdout
print(out.strip()[:300])
rid = json.loads(out.split("took", 1)[1].split("s ", 1)[1])["revision_id"]
run("confirm", rid)
time.sleep(3)
st, nets = call("GET", "/network/guest-networks")
rows = nets if isinstance(nets, list) else nets.get("data") or []
f20 = next(n for n in rows if n.get("name") == "Lab /20 to replace")
print("old /20 before:", f20["subnet_cidr"], "enabled", f20["enabled"], "bridge", f20.get("bridge_name"))
rep = {"reason": "widen to a /19 by replacement", "network_type": "vlan", "parent_interface": "eth1", "vlan_id": 121, "subnet_cidr": "10.68.0.0/19", "gateway_ip": "10.68.0.1",
       "pools": [{"start_ip": "10.68.0.10", "end_ip": "10.68.31.250"}], "name": "Lab /19 (replaced the /20)"}
st, out = call("POST", "/network/guest-networks/%s/replace" % f20["id"], rep)
print("replace", st, json.dumps(out)[:400])
out = subprocess.run(["python3", "/root/s19_apply.py", "apply", "replace the /20 by a /19"], capture_output=True, text=True).stdout
print(out.strip()[:420])
rid = json.loads(out.split("took", 1)[1].split("s ", 1)[1])["revision_id"]
run("confirm", rid)
time.sleep(3)
st, nets = call("GET", "/network/guest-networks")
rows = nets if isinstance(nets, list) else nets.get("data") or []
for n in rows:
    print(json.dumps({"name": n.get("name"), "cidr": n.get("subnet_cidr"), "enabled": n.get("enabled"), "archived": n.get("archived"), "bridge": n.get("bridge_name")}))
