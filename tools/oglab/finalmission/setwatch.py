#!/usr/bin/env python3
"""Appliance-side watcher for the outage-continuity runs. Every INTERVAL seconds:
  * reads the real kernel set inet stayconnect phase3_auth_ipv4 (nft -j),
  * reads netd's continuity governor state (unix socket),
  * appends one CSV line: epoch,authset_size,active,sessions,journalled,probe_ok,probe_failures,renewals,refused
  * keeps, per element address, the list of [first_seen, last_seen] presence intervals (epoch seconds, as sampled)
    and writes them to <prefix>.intervals.json every 30 s and at exit.
usage: setwatch.py <prefix> [interval=2] [duration_s=0 (forever)]"""
import json, subprocess, sys, time, http.client, socket, signal

prefix = sys.argv[1]
interval = float(sys.argv[2]) if len(sys.argv) > 2 else 2.0
duration = float(sys.argv[3]) if len(sys.argv) > 3 else 0.0


class UnixConn(http.client.HTTPConnection):
    def __init__(self, path):
        super().__init__("netd", timeout=4)
        self.path = path

    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.sock.settimeout(4)
        self.sock.connect(self.path)


def contstat():
    try:
        c = UnixConn("/run/stayconnect/netd.sock")
        c.request("GET", "/v1/phase3/continuity")
        return json.loads(c.getresponse().read())
    except Exception:
        return {}


def authset():
    try:
        out = subprocess.run(["nft", "-j", "list", "set", "inet", "stayconnect", "phase3_auth_ipv4"], capture_output=True, timeout=10).stdout
        j = json.loads(out)
        ips = set()
        for o in j.get("nftables", []):
            st = o.get("set")
            if not st:
                continue
            for e in st.get("elem", []) or []:
                v = e.get("elem", e)
                val = v.get("val", v)
                if isinstance(val, dict) and "concat" in val:
                    ips.add(val["concat"][-1])
        return ips
    except Exception:
        return None


intervals = {}   # ip -> [[first,last],...]
last_present = set()
csv = open(prefix + ".csv", "a", buffering=1)
cj = open(prefix + ".cont.jsonl", "a", buffering=1)
last_cj = 0
stop = False


def rec_mtime():
    try:
        import os
        return "%.1f" % os.stat("/var/lib/stayconnect/netd-phase3-continuity.json").st_mtime
    except Exception:
        return ""


def dump():
    json.dump(intervals, open(prefix + ".intervals.json", "w"))


def onsig(*a):
    global stop
    stop = True


signal.signal(signal.SIGTERM, onsig)
signal.signal(signal.SIGINT, onsig)
t_end = time.time() + duration if duration else None
last_dump = time.time()
while not stop and (t_end is None or time.time() < t_end):
    t0 = time.time()
    ips = authset()
    cs = contstat()
    now = int(time.time())
    if ips is not None:
        for ip in ips:
            if ip in last_present:
                intervals[ip][-1][1] = now
            else:
                intervals.setdefault(ip, []).append([now, now])
        last_present = ips
        n = len(ips)
    else:
        n = -1
    refused = (cs.get("refused") or "")
    csv.write("%d,%d,%s,%s,%s,%s,%s,%s,%s,%s\n" % (now, n, int(bool(cs.get("active"))), cs.get("sessions", ""), cs.get("journalled_revocations", ""),
              int(bool(cs.get("database_probe_ok"))) if cs else "", cs.get("database_probe_failures", ""), cs.get("renewals", ""), json.dumps(refused)[:60].replace(",", ";"), rec_mtime()))
    if time.time() - last_cj >= 15:
        cj.write(json.dumps({"t": now, "cont": cs}) + "\n")
        last_cj = time.time()
    if time.time() - last_dump > 30:
        dump()
        last_dump = time.time()
    d = interval - (time.time() - t0)
    if d > 0:
        time.sleep(d)
dump()
