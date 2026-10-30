#!/usr/bin/env python3
"""Minimal DHCPv4 test client for the /19 Kea proof. Usage:
  dhcpclient.py <iface> <count> <mac-prefix-int-start> [reboot-ip-file]
Runs DISCOVER/OFFER/REQUEST/ACK for <count> distinct MACs (broadcast flag) on <iface>, prints one line per client
"MAC IP lease router opt114" or "MAC NONE", and a summary. Reads/writes /tmp/dhcp-results.txt."""
import os, random, socket, struct, sys, time

IFACE = sys.argv[1]
COUNT = int(sys.argv[2])
START = int(sys.argv[3])
MODE = sys.argv[4] if len(sys.argv) > 4 else "dora"
RESULTS = "/tmp/dhcp-results.txt"


def mac_of(n):
    return bytes([0x02, 0x00]) + struct.pack(">I", n)


def build(msg_type, mac, xid, requested=None, server=None, ciaddr="0.0.0.0"):
    b = struct.pack(">BBBBIHH4s4s4s4s16s64s128s", 1, 1, 6, 0, xid, 0, 0x8000,
                    socket.inet_aton(ciaddr), b"\0" * 4, b"\0" * 4, b"\0" * 4, mac + b"\0" * 10, b"", b"")
    o = b"\x63\x82\x53\x63" + bytes([53, 1, msg_type]) + bytes([61, 7, 1]) + mac
    if requested:
        o += bytes([50, 4]) + socket.inet_aton(requested)
    if server:
        o += bytes([54, 4]) + socket.inet_aton(server)
    o += bytes([55, 6, 1, 3, 6, 15, 51, 114]) + bytes([255])
    return b + o


def parse(data):
    if len(data) < 240:
        return None
    xid = struct.unpack(">I", data[4:8])[0]
    yi = socket.inet_ntoa(data[16:20])
    mac = data[28:34]
    opts = {}
    i = 240
    while i < len(data) and data[i] != 255:
        if data[i] == 0:
            i += 1
            continue
        t, l = data[i], data[i + 1]
        opts[t] = data[i + 2:i + 2 + l]
        i += 2 + l
    return xid, yi, mac, opts


s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
s.setsockopt(socket.SOL_SOCKET, socket.SO_BROADCAST, 1)
s.setsockopt(socket.SOL_SOCKET, socket.SO_BINDTODEVICE, IFACE.encode())
s.bind(("0.0.0.0", 68))
s.settimeout(0.4)


def wait(xid, want_type):
    end = time.time() + 0.6
    while time.time() < end:
        try:
            d, _ = s.recvfrom(2048)
        except socket.timeout:
            return None
        p = parse(d)
        if p and p[0] == xid and p[3].get(53, b"\0")[0] in want_type:
            return p
    return None


got = {}
none = 0
t0 = time.time()
reboot = {}
if MODE == "reboot":
    for line in open(RESULTS):
        f = line.split()
        if len(f) >= 2 and f[1] != "NONE":
            reboot[f[0]] = f[1]
for n in range(START, START + COUNT):
    mac = mac_of(n)
    macs = ":".join("%02x" % c for c in mac)
    if MODE == "reboot":
        ip = reboot.get(macs)
        if not ip:
            continue
        xid = random.getrandbits(32)
        s.sendto(build(3, mac, xid, requested=ip), ("255.255.255.255", 67))  # INIT-REBOOT: REQUEST without server id
        a = wait(xid, (5, 6))
        print(macs, ip, "ACK" if a and a[3][53][0] == 5 and a[1] == ip else "NO-ACK")
        continue
    xid = random.getrandbits(32)
    ok = None
    for attempt in range(2):
        s.sendto(build(1, mac, xid), ("255.255.255.255", 67))
        off = wait(xid, (2,))
        if off:
            srv = off[3].get(54)
            s.sendto(build(3, mac, xid, requested=off[1], server=socket.inet_ntoa(srv)), ("255.255.255.255", 67))
            ack = wait(xid, (5, 6))
            if ack and ack[3][53][0] == 5:
                ok = ack
                break
        else:
            break  # no OFFER: pool exhausted (or server down); do not retry
    if ok:
        lease = struct.unpack(">I", ok[3].get(51, b"\0\0\0\0"))[0]
        router = socket.inet_ntoa(ok[3][3][:4]) if 3 in ok[3] else "-"
        o114 = ok[3].get(114, b"").decode() or "-"
        got[macs] = (ok[1], lease, router, o114)
    else:
        got[macs] = None
        none += 1
if MODE != "reboot":
    with open(RESULTS, "w") as f:
        for m, v in got.items():
            f.write("%s %s\n" % (m, "NONE" if v is None else " ".join(map(str, v))))
    ips = [v[0] for v in got.values() if v]
    print("clients=%d leased=%d none=%d unique=%d elapsed=%.1fs rate=%.0f/s" %
          (COUNT, len(ips), none, len(set(ips)), time.time() - t0, COUNT / max(time.time() - t0, 0.001)))
    if ips:
        key = lambda x: struct.unpack(">I", socket.inet_aton(x))[0]
        print("lowest=%s highest=%s" % (min(ips, key=key), max(ips, key=key)))
        samp = next(v for v in got.values() if v)
        print("sample lease=%s router=%s option114=%s" % (samp[1], samp[2], samp[3]))
