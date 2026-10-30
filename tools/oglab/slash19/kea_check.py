#!/usr/bin/env python3
"""Checks for the /19 Kea proof. Usage: kea_check.py <results-file> <pool-first-index> <pool-last-index> <base-ip>
Reads dhcpclient.py's results and Kea's own view (lease4-get-all over the control socket, the memfile CSV) and prints PASS/FAIL lines."""
import csv, json, socket, struct, sys

results, lo, hi, base, reserved = sys.argv[1], int(sys.argv[2]), int(sys.argv[3]), sys.argv[4], int(sys.argv[5])
b = struct.unpack(">I", socket.inet_aton(base))[0]
fails = 0


def check(name, ok, detail=""):
    global fails
    print(("PASS " if ok else "FAIL ") + name + ((" - " + detail) if detail else ""))
    if not ok:
        fails += 1


rows = [l.split() for l in open(results) if l.strip()]
leased = {r[0]: r[1] for r in rows if r[1] != "NONE"}
none = [r[0] for r in rows if r[1] == "NONE"]
idx = sorted(struct.unpack(">I", socket.inet_aton(ip))[0] - b for ip in leased.values())
residx = [i for i in idx if not (lo <= i <= hi)]
idx = [i for i in idx if lo <= i <= hi]
pool = hi - lo + 1 + reserved  # the pool plus the reservations held outside it
check("every pool address leased exactly once", len(leased) == min(len(rows), pool) and len(set(leased.values())) == len(leased),
      "%d leased of %d clients, pool %d" % (len(leased), len(rows), pool))
check("every lease is inside the pool or is the reservation", len(residx) == reserved, "pool index %d..%d, %d outside the pool" % (idx[0], idx[-1], len(residx)))
check("the TOP of the /19 is reached", bool(idx) and idx[-1] == hi, "highest index %s, pool top %d" % (idx[-1] if idx else None, hi))
check("clients beyond the pool get nothing (exhaustion, no wrong address)", len(none) == max(0, len(rows) - pool), "%d without an offer" % len(none))

# Kea's own view
s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
s.connect("/run/kea/kea4-ctrl.sock")
s.sendall(json.dumps({"command": "lease4-get-all"}).encode())
data = b""
while True:
    c = s.recv(1 << 20)
    if not c:
        break
    data += c
    try:
        json.loads(data)
        break
    except ValueError:
        pass
resp = json.loads(data)
if isinstance(resp, list):
    resp = resp[0]
kea_leases = resp.get("arguments", {}).get("leases", [])
check("Kea reports every lease through lease_cmds", len(kea_leases) == len(leased), "%d in Kea, %d handed out" % (len(kea_leases), len(leased)))

rowsCSV = list(csv.DictReader(open("/var/lib/kea/kea-leases4.csv")))
live = {}
for r in rowsCSV:
    live[r["address"]] = r  # the memfile is append-only: the last row per address wins
active = [a for a, r in live.items() if r["state"] == "0"]
check("lease file holds every lease", len(active) == len(leased), "%d active addresses in %d appended rows" % (len(active), len(rowsCSV)))
print("INFO lease file: %d rows" % len(rowsCSV))
sys.exit(1 if fails else 0)
