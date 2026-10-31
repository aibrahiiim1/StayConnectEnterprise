#!/usr/bin/env python3
"""Re-sign any of clients 0..1699 that is not in the kernel set (fresh codes from v-long2.txt). usage: ensure_pop.py <tag>"""
import json, os, subprocess, sys, time
os.environ["MSYS_NO_PATHCONV"] = "1"
name = sys.argv[1]
clients = {c["idx"]: c for c in json.load(open("D:/tmp/fm/lab/vch/clients.json"))}


def dx(c, cmd, timeout=900):
    r = subprocess.run(["docker", "exec", c, "sh", "-c", cmd], capture_output=True, text=True, timeout=timeout)
    return (r.stdout + r.stderr).strip()


present = set(dx("fm-appl", "nft list set inet stayconnect phase3_auth_ipv4 | grep -o '10\\.6[01]\\.[0-9]*\\.[0-9]*'").split())
missing = [i for i in range(0, 1700) if clients[i]["ip"] not in present]
print("missing", len(missing))
if missing:
    offf = "D:/tmp/fm/lab/v2offset.txt"
    off = int(open(offf).read()) if os.path.exists(offf) else 0
    lo, hi = min(missing), max(missing) + 1
    ml = ",".join(str(i) for i in missing)
    dx("fm-gen", "cd /root; python3 -c \"import json;p='/var/lib/ogload/clients.json';m=set([%s]);c=json.load(open(p));[x.__setitem__('signed_in',(x['idx'] not in m) if %d<=x['idx']<%d else x['signed_in']) for x in c];json.dump(c,open(p,'w'))\"" % (ml, lo, hi))
    out = dx("fm-gen", "ulimit -n 1048576; cd /root; ./ogload signin -from %d -to %d -vouchers /root/v-long2.txt -voucher-offset %d -rate 240 -log /root/%s.restore.log | tail -n 1" % (lo, hi, off, name))
    open(offf, "w").write(str(off + len(missing)))
    print(out[-300:])
    time.sleep(30)
