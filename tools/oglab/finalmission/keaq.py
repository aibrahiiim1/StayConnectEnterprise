import json, socket, sys
s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
s.connect("/run/kea/kea4-ctrl-socket")
s.sendall(json.dumps({"command": "subnet4-list"}).encode() if False else json.dumps({"command": "config-get"}).encode())
buf = b""
while True:
    d = s.recv(1 << 20)
    if not d:
        break
    buf += d
    try:
        j = json.loads(buf); break
    except Exception:
        continue
subs = j["arguments"]["Dhcp4"]["subnet4"]
print("kea version/pid ok; subnets:", [(x["subnet"], [p["pool"] for p in x.get("pools", [])]) for x in subs])
