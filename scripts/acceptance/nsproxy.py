#!/usr/bin/env python3
"""A one-file HTTP proxy (CONNECT + absolute-URI GET/POST) that runs INSIDE a guest-network namespace, so every
connection the workstation browser makes through it really originates from that guest device's address."""
import socket, sys, threading, select
HOST, PORT = sys.argv[1], int(sys.argv[2])
def pipe(a, b):
    socks = [a, b]
    try:
        while True:
            r, _, x = select.select(socks, [], socks, 300)
            if x or not r: break
            for s in r:
                d = s.recv(65536)
                if not d: return
                (b if s is a else a).sendall(d)
    except OSError: pass
    finally:
        for s in socks:
            try: s.close()
            except OSError: pass
def handle(c):
    try:
        buf = b''
        while b'\r\n\r\n' not in buf:
            d = c.recv(65536)
            if not d: c.close(); return
            buf += d
        head, rest = buf.split(b'\r\n\r\n', 1)
        lines = head.split(b'\r\n'); method, target, ver = lines[0].split(b' ', 2)
        if method == b'CONNECT':
            h, p = target.rsplit(b':', 1)
            u = socket.create_connection((h.decode(), int(p)), timeout=20); u.settimeout(None)
            c.sendall(b'HTTP/1.1 200 Connection established\r\n\r\n')
            if rest: u.sendall(rest)
            pipe(c, u); return
        # absolute-URI request: http://host[:port]/path
        t = target.decode(); assert t.startswith('http://')
        hp, _, path = t[7:].partition('/'); h, _, p = hp.partition(':')
        u = socket.create_connection((h, int(p or 80)), timeout=20); u.settimeout(None)
        out = [method + b' /' + path.encode() + b' ' + ver] + [l for l in lines[1:] if not l.lower().startswith(b'proxy-')]
        u.sendall(b'\r\n'.join(out) + b'\r\n\r\n' + rest)
        pipe(c, u)
    except Exception:
        try: c.sendall(b'HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n')
        except OSError: pass
        c.close()
s = socket.socket(); s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1); s.bind((HOST, PORT)); s.listen(64)
while True:
    c, _ = s.accept(); threading.Thread(target=handle, args=(c,), daemon=True).start()
