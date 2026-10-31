import json, sys
sys.path.insert(0, "/root/oglab")
import labapi
for ip, pl in (("10.70.0.1", 18), ("10.70.0.1", 19), ("172.30.0.1", 19)):
    st, out = labapi.call("POST", "/network/system/validate", {"lan": {"physical_interface": "eth1", "ip": ip, "prefix_len": pl, "dhcp_enabled": False}})
    print(ip, pl, st, json.dumps([(i.get("code"), i.get("field"), i.get("message","")[:90]) for i in out.get("validation",{}).get("issues",[])]))
