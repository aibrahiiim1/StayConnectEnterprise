#!/usr/bin/env python3
"""OneGate appliance monitor: passive, read-only health checks, client activity and per-device diagnostics.

quick (every 5 min): services, resources, database, enforcement, DHCP, network, portal, console, PMS, posting, licence,
certificates, sign-in health and session integrity. deep (every 30 min): quick plus client analytics, device diagnostics,
install parity and trends, written to the state directory. See deploy/monitor/README.md.
"""
import argparse
import datetime as dt
import fcntl
import glob
import json
import os
import re
import resource
import shlex
import socket
import subprocess
import sys
import time
import urllib.error
import urllib.request

PASS, WARN, CRIT, NV = "PASS", "WARN", "CRITICAL", "NOT_VERIFIED"
RANK = {PASS: 0, NV: 1, WARN: 2, CRIT: 3}

REJOINABLE = ("ADDRESS_NO_LONGER_OWNED", "ADDRESS_CHANGED", "SUPERSEDED_ON_DEVICE_MOVE", "SUPERSEDED_ON_ADDRESS")
SYSTEM_ENDS = REJOINABLE + ("ENFORCEMENT_TORN_DOWN", "NETWORK_REPLACED", "NETWORK_REMOVED")
GUEST_ERRORS = ("CREDENTIAL_MISMATCH", "ROOM_NOT_IN_MIRROR", "RATE_LIMITED", "AMBIGUOUS_ROOM_CANDIDATES",
                "MALFORMED_SUBMISSION", "SPENT_REQUEST_ID")
POLICY_REFUSALS = ("DEVICE_LIMIT_REACHED", "STAY_NOT_ELIGIBLE", "VERIFIED_NO_ELIGIBLE_PACKAGE",
                   "AFTER_CHECKOUT_NEW_DEVICE_REJECTED")
# Result keys that name one entity of a list the check reads in full; an entity missing from a complete read no longer exists.
ENTITY_LISTS = ("svc:", "svc-restart:", "container:", "res:disk:", "net:bridge:", "pms:link:", "pms:sync:", "cert:", "errors:")
# scd's rejoin refusals that are server failures, not ineligibility (data-plane/cmd/scd/phase3_rejoin.go).
REJOIN_FAILURES = ("device:", "begin", "candidate:", "capacity:", "session:", "commit", "enforcement_not_confirmed")
# The stay arm of scd's candidate query, with authctx.AfterCheckoutLive inlined.
STAY_REJOINABLE = """(st.status IN ('IN_HOUSE','POST_STAY_ACTIVE') OR (st.status = 'CHECKED_OUT' AND EXISTS (
    SELECT 1 FROM iam_v2.entitlements g WHERE g.tenant_id = st.tenant_id AND g.site_id = st.site_id AND g.stay_id = st.id
      AND g.status = 'ACTIVE' AND g.end_mode = 'GRACE_AFTER_CHECKOUT' AND (g.window_ends_at IS NULL OR g.window_ends_at > now()))))"""
UNITS = ["stayconnect-edged", "stayconnect-scd", "stayconnect-portald", "stayconnect-netd", "stayconnect-acctd",
         "stayconnect-pmsd", "stayconnect-caddy", "stayconnect-hotel-admin", "kea-dhcp4-server", "unbound", "docker"]


def env(k, d):
    return os.environ.get(k, d)


CFG = {
    "psql": env("OG_PSQL", "docker exec -i stayconnect-pg psql -X -q -v ON_ERROR_STOP=1 -U stayconnect -d stayconnect_site"),
    "log": env("OG_LOG", "/var/log/ogcheck.log"),
    "state": env("OG_STATE_DIR", "/var/lib/onegate-monitor"),
    "kea_conf": env("OG_KEA_CONF", "/etc/kea/kea-dhcp4.conf"),
    "kea_leases": env("OG_KEA_LEASES", "/var/lib/kea/kea-leases4.csv"),
    "kea_log": env("OG_KEA_LOG", "/var/log/kea/kea-dhcp4.log"),
    "kea_socket": env("OG_KEA_SOCKET", "/run/kea/kea4-ctrl-socket"),
    "license_dir": env("OG_LICENSE_DIR", "/etc/stayconnect/license"),
    "opt": env("OG_OPT", "/opt/stayconnect"),
    "backups": env("OG_BACKUPS", "/opt/stayconnect/backups/db"),
    "edged_health": env("OG_EDGED_HEALTH", "http://127.0.0.1:3100/api/edge/v1/health"),
    "console": env("OG_CONSOLE_URL", "http://127.0.0.1:3100/"),
    "portal": env("OG_PORTAL_URL", "http://127.0.0.1:8380/"),
    "certs": {"portal certificate": "/etc/stayconnect/portal-tls/*.crt",
              "Admin Console certificate": "/etc/caddy/hotel-admin/hotel-admin.crt",
              "Central client certificate": "/etc/stayconnect/certs/client.crt"},
}


class Unavailable(Exception):
    """The evidence source could not be read; the check reports NOT_VERIFIED, never PASS."""


class Sys:
    """Every read the monitor makes of the appliance; tests replace it."""

    def __init__(self, cfg):
        self.cfg = cfg

    def now(self):
        return dt.datetime.now(dt.timezone.utc)

    def sh(self, args, timeout=20, stderr=False):
        """stderr=True returns both streams: a container's log (PostgreSQL logs to stderr) is split across them by docker."""
        try:
            p = subprocess.run(args, stdout=subprocess.PIPE, stderr=subprocess.STDOUT if stderr else subprocess.PIPE,
                               text=True, timeout=timeout)
        except (OSError, subprocess.TimeoutExpired) as e:
            raise Unavailable(f"{args[0]}: {e.__class__.__name__}")
        return p.returncode, p.stdout

    def sql(self, query, timeout=30):
        args = shlex.split(self.cfg["psql"]) + ["-AtF", "\t", "-c", query]
        try:
            p = subprocess.run(args, capture_output=True, text=True, timeout=timeout)
        except (OSError, subprocess.TimeoutExpired) as e:
            raise Unavailable(f"database: {e.__class__.__name__}")
        if p.returncode != 0:
            raise Unavailable("database query failed: " + (p.stderr.strip().splitlines() or ["?"])[-1][:160])
        return [line.split("\t") for line in p.stdout.splitlines() if line != ""]

    def read(self, path, tail=None):
        try:
            with open(path, "rb") as f:
                if tail:
                    f.seek(0, os.SEEK_END)
                    f.seek(max(0, f.tell() - tail))
                return f.read().decode("utf-8", "replace")
        except OSError as e:
            raise Unavailable(f"{path}: {e.strerror}")

    def glob(self, pattern):
        return sorted(glob.glob(pattern))

    def http(self, url, timeout=5):
        try:
            with urllib.request.urlopen(urllib.request.Request(url, method="GET"), timeout=timeout) as r:
                return r.status, r.read(4096).decode("utf-8", "replace")
        except urllib.error.HTTPError as e:
            return e.code, ""
        except (urllib.error.URLError, OSError) as e:
            return 0, str(getattr(e, "reason", e))

    def unix_json(self, path, payload, timeout=3):
        try:
            s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
            s.settimeout(timeout)
            s.connect(path)
            s.sendall(json.dumps(payload).encode())
            buf = b""
            while True:
                chunk = s.recv(65536)
                if not chunk:
                    break
                buf += chunk
                try:
                    return json.loads(buf)
                except ValueError:
                    continue
            s.close()
            return json.loads(buf)
        except (OSError, ValueError) as e:
            raise Unavailable(f"Kea control socket: {e.__class__.__name__}")

    def statvfs(self, path):
        try:
            return os.statvfs(path)
        except OSError as e:
            raise Unavailable(f"{path}: {e.strerror}")


class Result:
    def __init__(self, key, area, status, text, persist=1):
        self.key, self.area, self.status, self.text, self.persist = key, area, status, text, persist

    def __repr__(self):
        return f"{self.status} {self.key}: {self.text}"


def mask_mac(mac):
    """Last two octets only: enough to find the device in the console, not to identify it elsewhere."""
    if not mac or len(mac) < 5:
        return "unknown"
    return "…:" + mac.lower()[-5:]


def short(uid):
    return (uid or "")[:8] or "unknown"


def n(v, default=0):
    try:
        return int(float(v))
    except (TypeError, ValueError):
        return default


def ts(s):
    if not s:
        return None
    s = re.sub(r"([+-]\d\d)$", r"\1:00", s.strip().replace(" ", "T", 1))
    s = re.sub(r"\.(\d{1,6})\d*", lambda m: "." + m.group(1).ljust(6, "0"), s, count=1)
    s = s.replace("Z", "+00:00")
    d = dt.datetime.fromisoformat(s)
    return d if d.tzinfo else d.replace(tzinfo=dt.timezone.utc)


def hm(d):
    return d.strftime("%H:%MZ") if d else "?"


class Monitor:
    def __init__(self, system, mode, cfg=CFG):
        self.s, self.mode, self.cfg = system, mode, cfg
        self.results, self.m, self.report = [], {}, {}
        self.now = system.now()
        self._journal = {}
        self.complete = set()

    # ---- helpers -------------------------------------------------------------------------------------------------
    def add(self, key, area, status, text, persist=1):
        self.results.append(Result(key, area, status, text, persist))

    def journal(self, unit, minutes):
        k = (unit, minutes)
        if k not in self._journal:
            rc, out = self.s.sh(["journalctl", "-u", unit, "--since", f"-{minutes} min", "--no-pager", "-o", "cat"], timeout=30)
            if rc != 0:
                raise Unavailable(f"journal of {unit} unreadable")
            self._journal[k] = out.splitlines()
        return self._journal[k]

    def leases(self):
        if self.m.get("leases") is Unavailable:
            raise Unavailable("Kea lease files unreadable")
        if "leases" not in self.m:
            live = {}
            now = self.now.timestamp()
            current = False
            for f in (self.cfg["kea_leases"] + ".2", self.cfg["kea_leases"] + ".1", self.cfg["kea_leases"]):
                try:
                    text = self.s.read(f)
                    current = current or f == self.cfg["kea_leases"]
                except Unavailable:
                    continue
                for line in text.splitlines():
                    r = line.split(",")
                    if len(r) > 9 and r[0][:1].isdigit():
                        live[r[0]] = (r[1].lower(), n(r[4]), n(r[3]), r[9] == "0")
            if not current:
                self.m["leases"] = Unavailable
                raise Unavailable("Kea's current lease file is unreadable")
            self.m["leases"] = {ip: (mac, exp, exp - valid) for ip, (mac, exp, valid, ok) in live.items() if ok and exp > now}
        return self.m["leases"]

    # ---- quick checks ------------------------------------------------------------------------------------------------
    def check_services(self, state):
        rc, out = self.s.sh(["systemctl", "show", "-p", "Id,ActiveState,SubState,NRestarts", *[u + ".service" for u in UNITS]])
        if rc != 0 and not out:
            raise Unavailable("systemctl unavailable")
        prev = state.get("restarts", {})
        cur = {}
        for block in out.strip().split("\n\n"):
            kv = dict(l.split("=", 1) for l in block.splitlines() if "=" in l)
            unit = kv.get("Id", "?").removesuffix(".service")
            cur[unit] = n(kv.get("NRestarts"))
            if kv.get("ActiveState") != "active":
                self.add(f"svc:{unit}", "services", CRIT, f"{unit} is {kv.get('ActiveState', 'unknown')}/{kv.get('SubState', '?')}")
            else:
                self.add(f"svc:{unit}", "services", PASS, f"{unit} active")
            if unit in prev and cur[unit] > prev[unit]:
                self.add(f"svc-restart:{unit}", "services", WARN, f"{unit} crashed and was restarted by systemd {cur[unit] - prev[unit]} time(s) since the last run")
            else:
                self.add(f"svc-restart:{unit}", "services", PASS, "no crash restart")
        state["restarts"] = cur
        rc, out = self.s.sh(["systemctl", "--failed", "--no-legend", "--plain"])
        failed = [l.split()[0] for l in out.splitlines() if l.strip()]
        self.add("svc:failed-units", "services", WARN if failed else PASS, "failed units: " + " ".join(failed) if failed else "no failed units")
        rc, out = self.s.sh(["docker", "inspect", "-f", "{{.Name}} {{.State.Running}} {{.RestartCount}}", "stayconnect-pg", "stayconnect-redis"])
        seen = {}
        for l in out.splitlines():
            p = l.split()
            if len(p) >= 2:
                seen[p[0].lstrip("/")] = p[1] == "true"
        for c in ("stayconnect-pg", "stayconnect-redis"):
            ok = seen.get(c)
            self.add(f"container:{c}", "services", PASS if ok else CRIT, f"container {c} " + ("running" if ok else "not running"))

    def check_resources(self, state):
        def cpu_times():
            f = self.s.read("/proc/stat").splitlines()[0].split()[1:]
            v = [int(x) for x in f]
            return sum(v), v[3] + (v[4] if len(v) > 4 else 0)
        t1, i1 = cpu_times()
        time.sleep(2 if not os.environ.get("OG_FAST") else 0)
        t2, i2 = cpu_times()
        busy = 0 if t2 == t1 else round(100 * (1 - (i2 - i1) / (t2 - t1)))
        self.m["cpu"] = busy
        # Each severity keeps its own streak, so warning-level samples never satisfy the critical persistence.
        self.add("res:cpu", "resources", WARN if busy > 85 else PASS, f"CPU {busy}% busy (2 s sample)", persist=2)
        self.add("res:cpu-critical", "resources", CRIT if busy > 95 else PASS, f"CPU {busy}% busy, above 95% (2 s sample)", persist=3)
        mem = {l.split(":")[0]: n(l.split()[1]) for l in self.s.read("/proc/meminfo").splitlines() if ":" in l}
        avail = mem.get("MemAvailable", 0) // 1024
        self.m["mem"] = avail
        self.add("res:memory", "resources", WARN if avail < 500 else PASS, f"{avail} MB memory available", persist=2)
        self.add("res:memory-critical", "resources", CRIT if avail < 200 else PASS, f"{avail} MB memory available, under 200 MB", persist=2)
        worst = None
        for path in ("/", "/var/lib/docker"):
            try:
                v = self.s.statvfs(path)
            except Unavailable as e:
                self.add(f"res:disk:{path}", "resources", NV, f"disk {path} unreadable: {e}")
                continue
            pct = round(100 * (1 - v.f_bavail / v.f_blocks)) if v.f_blocks else 0
            worst = pct if worst is None else max(worst, pct)
            self.add(f"res:disk:{path}", "resources", CRIT if pct > 95 else WARN if pct > 85 else PASS, f"disk {path} {pct}% used")
        self.m["disk"] = "?" if worst is None else worst
        self.m["load"] = self.s.read("/proc/loadavg").split()[0]

    def check_database(self, state):
        t0 = time.monotonic()
        r = self.s.sql("""SELECT (SELECT count(*) FROM pg_stat_activity),
            current_setting('max_connections')::int,
            (SELECT count(*) FROM pg_stat_activity WHERE backend_type = 'client backend' AND state <> 'idle' AND xact_start < now() - interval '5 minutes'),
            (SELECT count(*) FROM pg_locks WHERE NOT granted),
            (SELECT deadlocks FROM pg_stat_database WHERE datname = current_database())""")[0]
        ms = round((time.monotonic() - t0) * 1000)
        conns, maxc, longtx, waits, deadlocks = (n(x) for x in r)
        self.m["db_ms"] = ms
        self.add("db:latency", "database", WARN if ms > 3000 else PASS, f"database answered in {ms} ms", persist=2)
        self.add("db:connections", "database", WARN if conns > 0.8 * maxc else PASS, f"{conns} of {maxc} connections in use")
        self.add("db:long-transactions", "database", WARN if longtx else PASS, f"{longtx} transaction(s) open longer than 5 min", persist=2)
        self.add("db:lock-waits", "database", WARN if waits >= 5 else PASS, f"{waits} lock wait(s)", persist=2)
        hist = [h for h in state.get("deadlocks", []) if ts(h[0]) > self.now - dt.timedelta(days=8)]
        hist.append([self.now.isoformat(), deadlocks])
        state["deadlocks"] = hist
        hour = [h for h in hist if ts(h[0]) >= self.now - dt.timedelta(hours=1)]
        last_hour = max(0, deadlocks - hour[0][1]) if len(hour) > 1 else 0
        self.m["deadlocks_1h"] = last_hour
        self.add("db:deadlocks", "database", WARN if last_hour > 5 else PASS,
                 f"{last_hour} deadlock(s) in the last hour ({deadlocks} since the database started)")
        s = dict(self.s.sql("""SELECT name, setting FROM pg_settings
                                WHERE name IN ('log_lock_waits', 'log_error_verbosity', 'log_min_error_statement')"""))
        self.add("db:lock-wait-logging", "database", PASS if s.get("log_lock_waits") == "on" else WARN,
                 "lock waits over deadlock_timeout are logged" if s.get("log_lock_waits") == "on" else
                 "log_lock_waits is off: a deadlock's tables cannot be traced (the installer sets it)")
        private = s.get("log_error_verbosity") == "terse" and s.get("log_min_error_statement") == "panic"
        self.add("db:log-privacy", "database", PASS if private else WARN,
                 "the database log keeps no statements or detail lines (GDPR audit C5)" if private else
                 f"the database log may carry guest data: log_error_verbosity={s.get('log_error_verbosity')}, "
                 f"log_min_error_statement={s.get('log_min_error_statement')} (C5 requires terse and panic)")
        self.lock_waits(state)

    def lock_waits(self, state):
        """Lock waits the database logged since the previous run, by the table they waited on (relation ids resolved to names).
        Each run reads only what is new, so the daily totals count every wait once."""
        since = state.get("lock_waits_until") or (self.now - dt.timedelta(minutes=15)).strftime("%Y-%m-%dT%H:%M:%SZ")
        until = self.now.strftime("%Y-%m-%dT%H:%M:%SZ")
        try:
            rc, out = self.s.sh(["docker", "logs", "--since", since, "--until", until, "stayconnect-pg"], timeout=30, stderr=True)
        except Unavailable:
            rc, out = 1, ""
        if rc != 0:
            self.add("db:lock-waits-logged", "database", NV, "database log unreadable")
            return
        state["lock_waits_until"] = until
        waits = [l for l in out.splitlines() if "still waiting for" in l]
        rels = {}
        for l in waits:
            m = re.search(r"of relation (\d+)", l)
            k = m.group(1) if m else ("transaction" if "on transaction" in l else "other")
            rels[k] = rels.get(k, 0) + 1
        oids = [k for k in rels if k.isdigit()]
        names = dict(self.s.sql(f"SELECT oid::text, relname FROM pg_class WHERE oid IN ({','.join(oids)})")) if oids else {}
        named = {names.get(k, k): v for k, v in rels.items()}
        day = self.now.strftime("%Y-%m-%d")
        hist = state.setdefault("lock_waits", {})
        hist.setdefault(day, {})
        for k, v in named.items():
            hist[day][k] = hist[day].get(k, 0) + v
        for d in sorted(hist)[:-8]:
            del hist[d]
        top = ", ".join(f"{k}={v}" for k, v in sorted(named.items(), key=lambda x: -x[1])[:4]) or "none"
        self.add("db:lock-waits-logged", "database", WARN if len(waits) > 30 else PASS,
                 f"{len(waits)} lock wait(s) over 1 s since {since[11:16]}Z (by what they waited on: {top})")

    def check_enforcement(self, state):
        try:
            lines = self.journal("stayconnect-netd", 15)
        except Unavailable as e:
            self.add("enf:pass", "enforcement", NV, str(e))
            return
        passes = [l for l in lines if '"msg":"phase3: pass"' in l]
        recent = [l for l in passes if self._log_time(l) and self._log_time(l) >= self.now - dt.timedelta(minutes=3)]
        if not recent:
            self.add("enf:pass", "enforcement", CRIT, "netd has not completed an enforcement pass in 3 minutes")
            return
        last = recent[-1]
        sessions, failed, pms = (n(self._json_field(last, k)) for k in ("sessions", "failed", "pass_ms"))
        self.m.update(netd_sessions=sessions, pass_ms=pms)
        self.add("enf:pass", "enforcement", WARN if pms > 15000 else PASS, f"enforcement pass {pms} ms over {sessions} sessions", persist=2)
        problems = [l for l in lines if "shaping applied with problems" in l and "no longer leased" not in l]
        self.add("enf:shaping", "enforcement", WARN if failed and problems else PASS,
                 f"{len(problems)} shaping problem(s) in 15 min" + (f": {self._json_field(problems[-1], 'first')[:120]}" if problems else ""))
        withdrawn = sum(1 for l in lines if "no longer leased" in l)
        self.m["withdrawn_15m"] = withdrawn
        self.add("enf:withdrawn", "enforcement", WARN if withdrawn > 20 else PASS,
                 f"{withdrawn} authorization(s) withdrawn because the address moved to another device in 15 min")
        try:
            db_online = n(self.s.sql("SELECT count(*) FROM iam_v2.sessions WHERE ended IS NULL AND state = 'active'")[0][0])
        except Unavailable as e:
            self.add("enf:consistency", "enforcement", NV, f"online sessions unreadable: {e}")
            return
        self.m["online"] = db_online
        gap = abs(db_online - sessions)
        self.add("enf:consistency", "enforcement", WARN if gap > max(10, db_online // 20) else PASS,
                 f"database shows {db_online} online sessions, netd enforces {sessions}", persist=2)

    def check_acctd(self, state):
        try:
            lines = self.journal("stayconnect-acctd", 15)
        except Unavailable as e:
            self.add("jobs:expiry", "background jobs", NV, str(e))
            return
        fails = [l for l in lines if "expiry enforcement failed" in l]
        dl = sum(1 for l in fails if "40P01" in l)
        self.add("jobs:expiry", "background jobs", WARN if len(fails) >= 3 else PASS,
                 f"package-expiry pass failed {len(fails)} time(s) in 15 min ({dl} by deadlock; the next tick retries)")

    def check_http(self, state):
        code, body = self.s.http(self.cfg["edged_health"])
        try:
            h = json.loads(body) if code == 200 else {}
        except ValueError:
            h = {}
        ok = h.get("status") == "ok" and h.get("db") is True and h.get("scd") is True
        self.add("http:edged", "admin console", PASS if ok else CRIT,
                 "edged healthy (database and scd reachable)" if ok else f"edged health {code}: {body[:120]}")
        code, _ = self.s.http(self.cfg["console"])
        self.add("http:console", "admin console", PASS if 200 <= code < 400 else CRIT, f"Admin Console answers {code}")
        code, _ = self.s.http(self.cfg["portal"])
        self.add("http:portal", "portal", PASS if 200 <= code < 400 else CRIT, f"client portal answers {code}")

    def check_network(self, state):
        rc, out = self.s.sh(["ip", "-j", "link"])
        if rc != 0:
            raise Unavailable("ip link unreadable")
        links = {l["ifname"]: l for l in json.loads(out or "[]")}
        rc, out = self.s.sh(["ip", "-j", "route", "show", "default"])
        routes = json.loads(out or "[]") if rc == 0 else []
        wan = routes[0].get("dev") if routes else None
        self.add("net:default-route", "network", PASS if wan else CRIT, f"default route via {wan}" if wan else "no default route (WAN)")
        if wan:
            st = links.get(wan, {}).get("operstate")
            self.add("net:wan", "network", PASS if st == "UP" else CRIT, f"WAN {wan} {st}")
        else:
            self.add("net:wan", "network", NV, "WAN link unknown without a default route")
        try:
            want = [r[0] for r in self.s.sql("SELECT bridge_name FROM public.guest_networks WHERE enabled ORDER BY 1")]
        except Unavailable as e:
            self.add("net:client-networks", "network", NV, str(e))
            return
        for b in want:
            l = links.get(b)
            st = (l or {}).get("operstate")
            up = l is not None and "UP" in (l.get("flags") or [])
            members = [name for name, x in links.items() if x.get("master") == b and not name.startswith(("veth", "ifb"))]
            down = [m for m in members if links[m].get("operstate") not in ("UP", "UNKNOWN")]
            ok = up and members and not down
            self.add(f"net:bridge:{b}", "network", PASS if ok else CRIT,
                     f"client network {b} up on {' '.join(members)}" if ok else
                     f"client network {b} " + (f"{st or 'missing'}" if not up else "has no uplink" if not members else "uplink down: " + " ".join(down)))
        self.m["bridges"] = want
        for key in [k for k in state.get("incidents", {}) if k.startswith("net:bridge:") and k[len("net:bridge:"):] not in want]:
            self.add(key, "network", PASS, f"client network {key[len('net:bridge:'):]} is no longer configured")
        rc, out = self.s.sh(["unbound-control", "status"])
        self.add("net:dns", "network", PASS if rc == 0 else CRIT, "client DNS (unbound) running" if rc == 0 else "unbound-control status failed")
        try:
            bad = n(self.s.sql(f"SELECT count(*) FROM iam_v2.sessions WHERE ended IS NULL AND state = 'active' AND ingress_interface NOT IN ({','.join(repr(x) for x in links) or repr('')})")[0][0])
            self.add("net:orphans", "network", WARN if bad else PASS, f"{bad} session(s) on a network interface that no longer exists")
        except Unavailable as e:
            self.add("net:orphans", "network", NV, f"sessions unreadable: {e}")

    def check_dhcp(self, state):
        try:
            reply = self.s.unix_json(self.cfg["kea_socket"], {"command": "status-get"})
            ok = isinstance(reply, dict) and reply.get("result") == 0
            self.add("dhcp:kea", "dhcp", PASS if ok else WARN, "Kea control socket answers" if ok else f"Kea status-get: {str(reply)[:120]}")
        except Unavailable as e:
            self.add("dhcp:kea", "dhcp", NV, str(e))
        try:
            conf = json.loads(self.s.read(self.cfg["kea_conf"]))["Dhcp4"]
        except (Unavailable, ValueError, KeyError) as e:
            self.add("dhcp:config", "dhcp", NV, f"Kea configuration unreadable: {e}")
            return
        subnets = conf.get("subnet4", [])
        notauth = [s.get("interface") for s in subnets if s.get("authoritative") is not True]
        self.add("dhcp:authoritative", "dhcp", WARN if notauth else PASS,
                 f"{len(subnets)} client subnet(s) authoritative" if not notauth else "not authoritative: " + " ".join(map(str, notauth)))
        import ipaddress
        try:
            live = self.leases()
        except Unavailable as e:
            self.add("dhcp:pools", "dhcp", NV, f"pool use unknown: {e}")
            live = None
        pools = []
        worst = PASS
        for s in subnets if live is not None else []:
            size = used = 0
            for p in s.get("pools", []):
                a, b = (ipaddress.ip_address(x.strip()) for x in p["pool"].split("-"))
                size += int(b) - int(a) + 1
                used += sum(1 for ip in live if a <= ipaddress.ip_address(ip) <= b)
            pct = 100 * used / size if size else 0
            st = CRIT if pct > 95 else WARN if pct > 85 else PASS
            worst = max(worst, st, key=RANK.get)
            pools.append(f"{str(s.get('interface', '?')).split('/')[0]}={used}/{size}" + ("!" if st != PASS else ""))
        if live is not None:
            self.m["pools"] = " ".join(pools)
            self.add("dhcp:pools", "dhcp", worst, "address pools " + " ".join(pools))
        try:
            text = self.s.read(self.cfg["kea_log"], tail=4 << 20)
        except Unavailable as e:
            self.add("dhcp:log", "dhcp", NV, str(e))
            return
        since = (self.now - dt.timedelta(minutes=15)).strftime("%Y-%m-%d %H:%M:%S")
        recent = [l for l in text.splitlines() if l[:19] >= since]
        self.m["dhcp_alloc"] = sum(1 for l in recent if "DHCP4_LEASE_ALLOC" in l)
        fails = sum(1 for l in recent if "ALLOC_FAIL" in l)
        self.add("dhcp:alloc-fail", "dhcp", WARN if fails else PASS, f"{fails} address request(s) Kea could not serve in 15 min")

    def check_pms(self, state):
        rows = self.s.sql("""SELECT i.display_label, r.transport_status,
              COALESCE(round(extract(epoch FROM now() - r.disconnected_since)), -1),
              COALESCE(round(extract(epoch FROM now() - r.last_complete_sync_at)), -1)
            FROM iam_v2.pms_interface_runtime r JOIN iam_v2.pms_interfaces i ON i.id = r.pms_interface_id
            WHERE i.lifecycle_state = 'ACTIVE' ORDER BY 1""")
        if not rows:
            self.add("pms:none", "pms", PASS, "no active PMS connection")
        links = []
        for label, status, down_s, sync_s in rows:
            down_s, sync_s = n(down_s), n(sync_s)
            links.append(f"{label}={status}")
            if status != "CONNECTED":
                st = CRIT if down_s > 1800 else WARN
                self.add(f"pms:link:{label}", "pms", st, f"PMS {label} {status} for {down_s // 60} min (room sign-in continues from the last good roster)", persist=2)
            else:
                self.add(f"pms:link:{label}", "pms", PASS, f"PMS {label} connected")
            st = CRIT if sync_s > 3600 else WARN if sync_s > 1200 or sync_s < 0 else PASS
            self.add(f"pms:sync:{label}", "pms", st, f"PMS {label} last complete roster {sync_s // 60 if sync_s >= 0 else 'never'} min ago", persist=2)
        self.m["pms"] = " ".join(links)
        q = self.s.sql("""SELECT count(*) FILTER (WHERE state <> 'DONE' AND enqueued_at < now() - interval '30 minutes'),
                                 count(*) FILTER (WHERE parked_reason IS NOT NULL AND state <> 'DONE'),
                                 count(*) FILTER (WHERE state <> 'DONE')
                          FROM iam_v2.posting_outbox""")[0]
        stale, parked, pending = (n(x) for x in q)
        self.add("posting:queue", "posting", WARN if stale or parked else PASS,
                 f"room-charge queue: {pending} pending, {stale} older than 30 min, {parked} parked")

    def check_licence(self, state):
        try:
            cur = json.loads(self.s.read(os.path.join(self.cfg["license_dir"], "current.json")))
            st = json.loads(self.s.read(os.path.join(self.cfg["license_dir"], "state.json")))
        except (Unavailable, ValueError) as e:
            self.add("lic:file", "licensing", NV, f"licence unreadable: {e}")
            return
        p = cur.get("payload")
        if isinstance(p, str):
            import base64
            for dec in (json.loads, lambda x: json.loads(base64.b64decode(x + "=" * (-len(x) % 4))),
                        lambda x: json.loads(base64.urlsafe_b64decode(x + "=" * (-len(x) % 4)))):
                try:
                    p = dec(p)
                    break
                except (ValueError, TypeError):
                    continue
        if not isinstance(p, dict):
            self.add("lic:file", "licensing", NV, "licence payload not readable")
            return
        until = ts(p.get("valid_until"))
        days = (until - self.now).days if until else None
        status = p.get("status")
        rpath = os.path.join(self.cfg["license_dir"], "revoked.json")
        revoked = []
        if self.s.glob(rpath):
            try:
                revoked = json.loads(self.s.read(rpath))
                if not isinstance(revoked, list):
                    raise ValueError("not a list")
            except (Unavailable, ValueError) as e:
                self.add("lic:valid", "licensing", NV, f"licence revocation list unreadable: {e}")
                return
        if p.get("license_id") in revoked:
            self.add("lic:valid", "licensing", CRIT, "the installed licence was revoked by Central; new guests are refused")
        elif status != "active" or days is None or days < 0:
            self.add("lic:valid", "licensing", CRIT, f"licence {status}, valid until {p.get('valid_until')}")
        else:
            self.add("lic:valid", "licensing", CRIT if days < 7 else WARN if days < 30 else PASS, f"licence active, {days} days left")
        last = ts(st.get("last_cloud_validation"))
        grace = n(p.get("offline_grace_days") or p.get("grace_period_days"), 7)
        if not last:
            self.add("lic:central", "central", NV, "no Central validation recorded")
        else:
            age_h = (self.now - last).total_seconds() / 3600
            self.add("lic:central", "central", CRIT if age_h > grace * 24 * 0.8 else WARN if age_h > 24 else PASS,
                     f"last licence validation with Central {age_h:.1f} h ago (offline grace {grace} days)")
# The effective cap as license.Document.EffectiveMaxConcurrentOnlineGuests reads it; zero or less is unlimited.
        cap = n(p.get("max_concurrent_online_guests")) or n((p.get("limits") or {}).get("max_concurrent_guest_sessions"))
        # Counted as scd's liveSessionCount counts them for admission: sessions awaiting enforcement hold a slot too.
        try:
            online = n(self.s.sql("SELECT count(*) FROM iam_v2.sessions WHERE ended IS NULL AND state IN ('active', 'PENDING_ENFORCEMENT')")[0][0])
        except Unavailable:
            online = None
        if cap <= 0:
            self.add("lic:capacity", "licensing", PASS, "licence has no online-guest limit")
        elif online is None:
            self.add("lic:capacity", "licensing", NV, f"online count unknown (licence allows {cap})")
        else:
            pct = 100 * online / cap
            self.add("lic:capacity", "licensing", CRIT if pct >= 100 else WARN if pct > 85 else PASS, f"{online} of {cap} licensed online guests")
        self.m["licence_days"] = days

    def check_certs(self, state):
        for label, pattern in self.cfg["certs"].items():
            files = self.s.glob(pattern)
            if not files:
                self.add(f"cert:{label}", "certificates", NV, f"{label}: no file at {pattern}")
                continue
            for f in files:
                rc, out = self.s.sh(["openssl", "x509", "-noout", "-enddate", "-in", f])
                m = re.search(r"notAfter=(.+)", out or "")
                if rc != 0 or not m:
                    self.add(f"cert:{f}", "certificates", NV, f"{label}: {os.path.basename(f)} unreadable")
                    continue
                end = dt.datetime.strptime(m.group(1).strip().replace("  ", " "), "%b %d %H:%M:%S %Y %Z").replace(tzinfo=dt.timezone.utc)
                days = (end - self.now).days
                self.add(f"cert:{f}", "certificates", CRIT if days < 7 else WARN if days < 21 else PASS,
                         f"{label} {os.path.basename(f)} expires in {days} days")

    def check_signin(self, state):
        r = self.s.sql("""SELECT result, count(*) FROM iam_v2.sign_in_attempts WHERE occurred_at > now() - interval '15 minutes' GROUP BY 1""")
        by = {k: n(v) for k, v in r}
        total = sum(by.values())
        self.m["signin"] = f"{by.get('VERIFIED', 0)}/{by.get('SERVICE_UNAVAILABLE', 0)}/{total}"
        self.m["refused"] = " ".join(f"{k}={v}" for k, v in sorted(by.items(), key=lambda x: -x[1]) if k != "VERIFIED") or "none"
        su = by.get("SERVICE_UNAVAILABLE", 0)
        self.add("auth:service-unavailable", "authentication", CRIT if su >= 10 else WARN if su >= 3 else PASS, f"{su} sign-in(s) refused as service unavailable in 15 min")
        ro = by.get("ROUTING_OR_INTERFACE_FAILURE", 0)
        self.add("auth:routing", "authentication", WARN if ro >= 3 else PASS, f"{ro} sign-in(s) refused for PMS routing in 15 min")
        ac = by.get("ADDRESS_CONFLICT", 0)
        self.add("auth:address-conflict", "authentication", WARN if ac >= 5 else PASS, f"{ac} sign-in(s) from an address leased to another device in 15 min")
        lic = by.get("LICENSE_CAPACITY_REACHED", 0) + by.get("LICENSE_REFUSED", 0)
        self.add("auth:licence-refusals", "licensing", WARN if lic else PASS, f"{lic} sign-in(s) refused by the licence in 15 min")
        stuck = self.s.sql("""SELECT device_mac::text, mode() WITHIN GROUP (ORDER BY result), count(*)
            FROM iam_v2.sign_in_attempts WHERE occurred_at > now() - interval '60 minutes' AND device_mac IS NOT NULL
            GROUP BY 1 HAVING count(*) FILTER (WHERE result <> 'VERIFIED') >= 5 AND bool_and(result <> 'VERIFIED')""")
        system = [s for s in stuck if s[1] not in GUEST_ERRORS + POLICY_REFUSALS]
        self.m["stuck"] = f"{len(stuck)}({len(system)} system)"
        self.add("auth:stuck", "authentication", WARN if system else PASS,
                 f"{len(system)} device(s) refused 5+ times in 60 min for a system reason" if system else
                 f"no device stuck for a system reason ({len(stuck)} stuck on wrong details or policy)")
        try:
            lines = self.journal("stayconnect-scd", 15)
            reasons = [self._json_field(l, "reason") for l in lines if '"msg":"phase3 rejoin: not eligible"' in l]
            fails = [r for r in reasons if r.startswith(REJOIN_FAILURES) and "licensed concurrent-guest capacity reached" not in r]
            done = sum(1 for l in lines if "phase3 rejoin: device reconnected" in l)
            self.m["rejoin_refused"] = len(reasons) - len(fails)
            self.add("auth:rejoin-errors", "automatic reconnection", WARN if len(fails) >= 2 else PASS,
                     f"{len(fails)} automatic reconnection(s) failed on a server error in 15 min"
                     + (f" (latest: {fails[-1][:100]})" if fails else "") + f"; {done} reconnected, {len(reasons) - len(fails)} not eligible")
        except Unavailable as e:
            self.add("auth:rejoin-errors", "authentication", NV, str(e))
        e = self.s.sql("""SELECT COALESCE(end_reason, '-'), count(*) FROM iam_v2.sessions WHERE ended > now() - interval '15 minutes' GROUP BY 1""")
        ends = {k: n(v) for k, v in e}
        self.m["ends"] = " ".join(f"{k}={v}" for k, v in sorted(ends.items(), key=lambda x: -x[1])) or "none"
        gone = ends.get("NETWORK_REMOVED", 0) + ends.get("NETWORK_REPLACED", 0)
        self.add("sess:network-change", "sessions", WARN if gone else PASS, f"{gone} client(s) disconnected by a network removal or replacement in 15 min")
        active_cut = n(self.s.sql("""SELECT count(*) FROM iam_v2.sessions s WHERE s.ended > now() - interval '15 minutes'
            AND s.end_reason IN ('ADDRESS_NO_LONGER_OWNED','ADDRESS_CHANGED','ENFORCEMENT_TORN_DOWN')
            AND EXISTS (SELECT 1 FROM iam_v2.accounting_usage_minute u WHERE u.session_id = s.id
                        AND u.bucket >= s.ended - interval '10 minutes' AND (u.bytes_up + u.bytes_down) > 0)""")[0][0])
        self.add("sess:active-cut-off", "sessions", WARN if active_cut >= 3 else PASS,
                 f"{active_cut} session(s) ended by the system while the device was still passing traffic in 15 min")
        q = self.s.sql("""SELECT
            (SELECT count(*) FROM iam_v2.sessions WHERE credential_method = 'REJOIN' AND started > now() - interval '15 minutes'),
            (SELECT count(*) FROM (SELECT mac FROM iam_v2.sessions WHERE started > now() - interval '6 hours' GROUP BY 1
                HAVING count(*) FILTER (WHERE ended IS NOT NULL AND end_reason IN ('ADDRESS_NO_LONGER_OWNED','ADDRESS_CHANGED','ENFORCEMENT_TORN_DOWN','SUPERSEDED_ON_DEVICE_MOVE','SUPERSEDED_ON_ADDRESS')) >= 2) x),
            (SELECT count(*) FROM (SELECT DISTINCT ON (e.stay_id) e.terminal_reason FROM iam_v2.entitlements e JOIN iam_v2.stays st ON st.id = e.stay_id
                WHERE st.status = 'IN_HOUSE' ORDER BY e.stay_id, e.activated_at DESC NULLS LAST, e.id DESC) x WHERE terminal_reason = 'DATA')""")[0]
        self.m["rejoined"], self.m["recut"], self.m["usedup"] = (n(x) for x in q)

    def check_integrity(self, state):
        q = self.s.sql("""SELECT
          (SELECT count(*) FROM iam_v2.sessions s JOIN iam_v2.entitlements e ON e.id = s.entitlement_id
            WHERE s.ended IS NULL AND s.state = 'active' AND e.status <> 'ACTIVE' AND COALESCE(e.terminated_at, now()) < now() - interval '2 minutes'),
          (SELECT count(*) FROM iam_v2.sessions WHERE ended IS NULL AND state = 'active' AND expires_at < now() - interval '5 minutes'),
          (SELECT count(*) FROM iam_v2.sessions s WHERE s.ended IS NULL AND s.state = 'active' AND NOT EXISTS (
            SELECT 1 FROM iam_v2.entitlement_devices d WHERE d.entitlement_id = s.entitlement_id AND d.device_id = s.device_id AND d.status = 'AUTHORIZED')),
          (SELECT count(*) FROM iam_v2.entitlement_devices d WHERE d.status = 'AUTHORIZED' AND NOT EXISTS (
            SELECT 1 FROM iam_v2.entitlement_device_authorizations a WHERE a.entitlement_id = d.entitlement_id AND a.device_id = d.device_id AND a.deauthorized_at IS NULL)),
          (SELECT count(*) FROM (SELECT e.id FROM iam_v2.entitlements e JOIN iam_v2.sessions s ON s.entitlement_id = e.id AND s.ended IS NULL
            LEFT JOIN iam_v2.service_plan_revisions spr ON spr.id = e.service_plan_revision_id
            WHERE COALESCE(spr.max_concurrent_devices, NULLIF(e.policy_snapshot->>'max_concurrent_devices', '')::int) > 0 GROUP BY e.id
            HAVING count(*) > max(COALESCE(spr.max_concurrent_devices, NULLIF(e.policy_snapshot->>'max_concurrent_devices', '')::int))) x),
          (SELECT count(*) FROM iam_v2.entitlements e LEFT JOIN iam_v2.service_plan_revisions spr ON spr.id = e.service_plan_revision_id
            WHERE e.status = 'ACTIVE' AND e.consumed_data_bytes > COALESCE(e.data_quota_bytes, spr.data_quota_bytes) + 100000000),
          (SELECT count(*) FROM (SELECT device_id FROM iam_v2.sessions WHERE ended IS NULL GROUP BY 1 HAVING count(*) > 1) x),
          (SELECT count(*) FROM iam_v2.sessions s JOIN LATERAL (SELECT x.end_reason FROM iam_v2.sessions x WHERE x.device_id = s.device_id
            AND x.started < s.started ORDER BY x.started DESC LIMIT 1) p ON true
            WHERE s.credential_method = 'REJOIN' AND s.started > now() - interval '1 hour' AND p.end_reason NOT IN ('ADDRESS_NO_LONGER_OWNED','ADDRESS_CHANGED','SUPERSEDED_ON_DEVICE_MOVE','SUPERSEDED_ON_ADDRESS'))""")[0]
        checks = [
            ("ended-package", CRIT, "client(s) still online on an ended package"),
            ("past-expiry", WARN, "client(s) online past their session expiry"),
            ("unauthorized", CRIT, "online session(s) whose device is not authorized on the package"),
            ("auth-records", WARN, "device authorization(s) without an open authorization record"),
            ("device-limit", WARN, "package(s) with more devices online than they allow"),
            ("quota", WARN, "active package(s) more than 100 MB over their data allowance"),
            ("duplicate", WARN, "device(s) with more than one live session"),
            ("rejoin-after-signout", CRIT, "automatic reconnection(s) after a sign-out, staff disconnect or device release"),
        ]
        for (key, sev, text), v in zip(checks, q):
            v = n(v)
            self.add(f"integrity:{key}", "session integrity", sev if v else PASS, f"{v} {text}")

    def check_errors(self, state):
        rc, out = self.s.sh(["journalctl", "-u", "stayconnect-*", "--since", "-15 min", "--no-pager", "-o", "json",
                             "--output-fields=_SYSTEMD_UNIT,MESSAGE"], timeout=30)
        if rc != 0:
            raise Unavailable("journal unreadable")
        per, msgs = {}, {}
        for l in out.splitlines():
            if '"level":"ERROR"' not in l and '\\"level\\":\\"ERROR\\"' not in l:
                continue
            try:
                e = json.loads(l)
            except ValueError:
                continue
            msg = e.get("MESSAGE")
            if not isinstance(msg, str):
                continue
            u = e.get("_SYSTEMD_UNIT", "?").replace(".service", "")
            per[u] = per.get(u, 0) + 1
            m = re.search(r'"msg":"([^"]*)"', msg)
            msgs.setdefault(u, {}).setdefault(m.group(1) if m else msg[:60], 0)
            msgs[u][m.group(1) if m else msg[:60]] += 1
        units = sorted(set(per) | {u for u in UNITS if u.startswith("stayconnect-")} | {k.split(":", 1)[1] for k in state.get("incidents", {}) if k.startswith("errors:")})
        for u in units:
            c = per.get(u, 0)
            top = max(msgs[u].items(), key=lambda x: x[1])[0] if c else ""
            self.add(f"errors:{u}", "errors", WARN if c > 20 else PASS,
                     f"{u}: {c} error line(s) in 15 min" + (f", most often '{top[:80]}'" if c else ""))

    # ---- deep checks ---------------------------------------------------------------------------------------------------
    def check_backup(self, state):
        files = self.s.glob(os.path.join(self.cfg["backups"], "db-*.sql.gz.age"))
        if not files:
            self.add("backup:fresh", "background jobs", NV, "no database backup found")
            return
        stamps = []
        for f in files:
            m = re.search(r"db-(?:safety-)?(\d{8}T\d{6})Z", os.path.basename(f))
            if m:
                stamps.append(dt.datetime.strptime(m.group(1), "%Y%m%dT%H%M%S").replace(tzinfo=dt.timezone.utc))
        when = max(stamps) if stamps else None
        age = (self.now - when).total_seconds() / 3600 if when else None
        self.add("backup:fresh", "background jobs", NV if age is None else CRIT if age > 54 else WARN if age > 30 else PASS,
                 f"newest database backup {age:.0f} h old" if age is not None else "backup time unreadable")

    def check_parity(self, state):
        opt = self.cfg["opt"]
        try:
            want = {}
            for l in self.s.read(os.path.join(opt, "DEPLOYED_BINARIES.sha256")).splitlines():
                p = l.split()
                if len(p) >= 2 and not l.startswith("#") and re.fullmatch(r"[0-9a-f]{64}", p[1]):
                    want[p[0]] = p[1]
            if not want:
                raise Unavailable("DEPLOYED_BINARIES.sha256 lists no binaries")
        except Unavailable as e:
            self.add("parity:binaries", "installation", NV, str(e))
            want = {}
        if want:
            rc, out = self.s.sh(["sha256sum", *[os.path.join(opt, "bin", b) for b in want]])
            have = {os.path.basename(p[1]): p[0] for p in (l.split() for l in out.splitlines()) if len(p) == 2}
            bad = [b for b, h in want.items() if have.get(b) != h]
            self.add("parity:binaries", "installation", CRIT if bad else PASS,
                     f"{len(want)} service binaries match the installed release" if not bad else "binaries differ from the release: " + " ".join(bad))
        units = self.s.glob(os.path.join(opt, "deploy", "systemd", "*.service")) + self.s.glob(os.path.join(opt, "deploy", "systemd", "*.timer"))
        if units:
            pairs = [(u, "/etc/systemd/system/" + os.path.basename(u)) for u in units if "central" not in u and "ctrlapi" not in u and "cloud-admin" not in u]
            pairs += [(os.path.join(opt, "deploy", "caddy", "stayconnect-caddy.edge.service"), "/etc/systemd/system/stayconnect-caddy.service"),
                      (os.path.join(opt, "deploy", "kea", "systemd", "override.conf"), "/etc/systemd/system/kea-dhcp4-server.service.d/override.conf"),
                      (os.path.join(opt, "deploy", "unbound", "systemd", "override.conf"), "/etc/systemd/system/unbound.service.d/override.conf")]
            pairs = [(a, b) for a, b in pairs if self.s.glob(a)]
            def norm(text, cosmetic):
                keep = [l.strip() for l in text.splitlines() if l.strip() and not l.lstrip().startswith(("#", ";"))]
                return [l for l in keep if not (cosmetic and l.startswith(("Description=", "Documentation=")))]
            diff, cosmetic = [], []
            for a, b in pairs:
                try:
                    ta, tb = self.s.read(a), self.s.read(b)
                except Unavailable:
                    diff.append(os.path.basename(b) + " (missing)")
                    continue
                if norm(ta, True) != norm(tb, True):
                    diff.append(os.path.basename(b))
                elif norm(ta, False) != norm(tb, False):
                    cosmetic.append(os.path.basename(b))
            note = f"; description only differs in {' '.join(cosmetic)} (upgrades do not rewrite it)" if cosmetic else ""
            self.add("parity:units", "installation", WARN if diff else PASS,
                     (f"{len(pairs)} service units match the release" if not diff else "units differ from the release: " + " ".join(diff)) + note)
        else:
            self.add("parity:units", "installation", NV, "release unit copies not found")
        try:
            base = self.s.read("/etc/stayconnect/schema-baseline").strip()[:4]
            shipped = sorted(os.path.basename(f)[:-7] for f in self.s.glob(os.path.join(opt, "data-plane", "migrations", "[0-9]*.up.sql")))
            ledger = {r[0] for r in self.s.sql("SELECT version FROM public.schema_migrations")}
            missing = [m for m in shipped if m[:4] > base and m not in ledger]
            self.add("parity:migrations", "installation", CRIT if missing else PASS if shipped else NV,
                     f"all {len([m for m in shipped if m[:4] > base])} migrations after the {base} baseline applied" if not missing else
                     "migrations shipped but not applied: " + " ".join(m[:4] for m in missing))
        except Unavailable as e:
            self.add("parity:migrations", "installation", NV, str(e))

    def analytics(self):
        """Devices, sessions and sign-in attempts are counted separately; none is derived from another."""
        windows = [("15 min", "15 minutes"), ("1 hour", "1 hour"), ("24 hours", "24 hours"), ("7 days", "7 days")]
        out = []
        for label, iv in windows:
            rows = {}
            for name, off in (("now", "0 seconds"), ("previous", iv)):
                t = f"(now() - interval '{off}')"
                f = f"(now() - interval '{off}' - interval '{iv}')"
                r = self.s.sql(f"""WITH w AS (SELECT {f} AS f, {t} AS t)
                  SELECT
                   (SELECT count(DISTINCT s.device_id) FROM iam_v2.sessions s, w WHERE s.started < w.t AND (s.ended IS NULL OR s.ended >= w.f)),
                   (SELECT count(*) FROM (SELECT device_id FROM iam_v2.sessions GROUP BY 1 HAVING min(started) >= (SELECT f FROM w) AND min(started) < (SELECT t FROM w)) x),
                   (SELECT count(DISTINCT s.device_id) FROM iam_v2.sessions s, w WHERE s.started < w.t AND (s.ended IS NULL OR s.ended >= w.f)
                      AND EXISTS (SELECT 1 FROM iam_v2.sessions o WHERE o.device_id = s.device_id AND o.started < w.f)),
                   (SELECT count(*) FROM iam_v2.sessions s, w WHERE s.started >= w.f AND s.started < w.t),
                   (SELECT count(*) FROM iam_v2.sessions s, w WHERE s.started >= w.f AND s.started < w.t AND s.credential_method = 'PMS'),
                   (SELECT count(*) FROM iam_v2.sessions s, w WHERE s.started >= w.f AND s.started < w.t AND s.credential_method = 'REJOIN'),
                   (SELECT count(*) FROM iam_v2.sessions s, w WHERE s.ended >= w.f AND s.ended < w.t),
                   (SELECT count(*) FROM iam_v2.sign_in_attempts a, w WHERE a.occurred_at >= w.f AND a.occurred_at < w.t),
                   (SELECT count(*) FROM iam_v2.sign_in_attempts a, w WHERE a.occurred_at >= w.f AND a.occurred_at < w.t AND a.result = 'VERIFIED'),
                   (SELECT count(*) FROM iam_v2.sign_in_attempts a, w WHERE a.occurred_at >= w.f AND a.occurred_at < w.t AND a.result = 'VERIFIED'
                      AND NOT EXISTS (SELECT 1 FROM iam_v2.sign_in_attempts b WHERE b.tenant_id = a.tenant_id AND b.site_id = a.site_id AND b.device_mac = a.device_mac AND b.result = 'VERIFIED' AND b.occurred_at < a.occurred_at)),
                   (SELECT count(*) FROM iam_v2.sign_in_attempts a, w WHERE a.occurred_at >= w.f AND a.occurred_at < w.t AND a.result = 'VERIFIED'
                      AND EXISTS (SELECT 1 FROM iam_v2.sign_in_attempts b WHERE b.tenant_id = a.tenant_id AND b.site_id = a.site_id AND b.device_mac = a.device_mac AND b.result = 'VERIFIED'
                                  AND b.occurred_at < a.occurred_at AND b.occurred_at >= a.occurred_at - interval '24 hours')),
                   (SELECT count(*) FROM iam_v2.sign_in_attempts a JOIN iam_v2.sessions ns ON ns.id = a.session_id, w
                      WHERE a.occurred_at >= w.f AND a.occurred_at < w.t AND a.result = 'VERIFIED' AND EXISTS (
                        SELECT 1 FROM (SELECT p.* FROM iam_v2.sessions p WHERE p.device_id = ns.device_id AND p.started < ns.started ORDER BY p.started DESC LIMIT 1) p
                        JOIN iam_v2.entitlements e ON e.id = p.entitlement_id
                        WHERE p.end_reason IN ('ADDRESS_NO_LONGER_OWNED','ADDRESS_CHANGED','SUPERSEDED_ON_DEVICE_MOVE','SUPERSEDED_ON_ADDRESS')
                          AND p.ingress_interface = ns.ingress_interface AND e.status = 'ACTIVE' AND (e.window_ends_at IS NULL OR e.window_ends_at > a.occurred_at))),
                   (SELECT count(*) FROM iam_v2.sign_in_attempts a, w WHERE a.occurred_at >= w.f AND a.occurred_at < w.t AND a.result = 'VERIFIED'
                      AND a.session_id IS NULL AND a.occurred_at < now() - interval '15 minutes'),
                   (SELECT count(*) FROM iam_v2.sign_in_attempts a, w WHERE a.occurred_at >= w.f AND a.occurred_at < w.t AND a.result <> 'VERIFIED'),
                   (SELECT count(*) FROM (SELECT a.device_mac FROM iam_v2.sign_in_attempts a, w WHERE a.occurred_at >= w.f AND a.occurred_at < w.t
                      GROUP BY 1 HAVING bool_and(a.result <> 'VERIFIED')) x)""")[0]
                rows[name] = [n(x) for x in r]
            out.append((label, rows))
        names = ["devices connected", "new devices (first session ever)", "returning devices (a session before the window)", "sessions started",
                 "  by manual room sign-in", "  by automatic reconnection", "sessions ended", "room sign-in attempts",
                 "  verified", "  first-time sign-ins (device never verified before)", "  repeat manual sign-ins (verified in prior 24 h)",
                 "    of which unnecessary (package still valid, same network)", "  verified, no package taken", "  refused",
                 "devices refused without getting in"]
        return names, out

    def breakdowns(self):
        res = {}
        res["ends_24h"] = self.s.sql("SELECT COALESCE(end_reason, '-'), count(*) FROM iam_v2.sessions WHERE ended > now() - interval '24 hours' GROUP BY 1 ORDER BY 2 DESC")
        res["methods_24h"] = self.s.sql("SELECT credential_method, count(*) FROM iam_v2.sessions WHERE started > now() - interval '24 hours' GROUP BY 1 ORDER BY 2 DESC")
        res["results_24h"] = self.s.sql("SELECT result, count(*) FROM iam_v2.sign_in_attempts WHERE occurred_at > now() - interval '24 hours' GROUP BY 1 ORDER BY 2 DESC")
        res["networks_24h"] = self.s.sql("""SELECT s.ingress_interface, count(DISTINCT s.device_id), count(*) FILTER (WHERE s.started > now() - interval '24 hours'),
              count(*) FILTER (WHERE s.credential_method = 'PMS' AND s.started > now() - interval '24 hours'),
              count(*) FILTER (WHERE s.credential_method = 'REJOIN' AND s.started > now() - interval '24 hours'),
              count(*) FILTER (WHERE s.end_reason IN ('ADDRESS_NO_LONGER_OWNED','ADDRESS_CHANGED') AND s.ended > now() - interval '24 hours'),
              count(*) FILTER (WHERE s.ended IS NULL)
            FROM iam_v2.sessions s WHERE s.ended IS NULL OR s.ended > now() - interval '24 hours' GROUP BY 1 ORDER BY 2 DESC""")
        res["hourly"] = self.s.sql("""SELECT to_char(h, 'MM-DD HH24":00"'),
              (SELECT count(*) FROM iam_v2.sessions s WHERE s.started < h + interval '1 hour' AND (s.ended IS NULL OR s.ended >= h + interval '1 hour')),
              (SELECT count(*) FROM iam_v2.sessions s WHERE s.started >= h AND s.started < h + interval '1 hour' AND s.credential_method = 'PMS'),
              (SELECT count(*) FROM iam_v2.sessions s WHERE s.started >= h AND s.started < h + interval '1 hour' AND s.credential_method = 'REJOIN'),
              (SELECT count(*) FROM iam_v2.sessions s WHERE s.ended >= h AND s.ended < h + interval '1 hour'),
              (SELECT count(*) FROM iam_v2.sign_in_attempts a WHERE a.occurred_at >= h AND a.occurred_at < h + interval '1 hour' AND a.result <> 'VERIFIED')
            FROM generate_series(date_trunc('hour', now()) - interval '23 hours', date_trunc('hour', now()), interval '1 hour') h ORDER BY h""")
        res["daily"] = self.s.sql("""SELECT to_char(d, 'MM-DD Dy'),
              (SELECT count(DISTINCT s.device_id) FROM iam_v2.sessions s WHERE s.started < d + interval '1 day' AND (s.ended IS NULL OR s.ended >= d)),
              (SELECT count(*) FROM iam_v2.sessions s WHERE s.started >= d AND s.started < d + interval '1 day' AND s.credential_method = 'PMS'),
              (SELECT count(*) FROM iam_v2.sessions s WHERE s.started >= d AND s.started < d + interval '1 day' AND s.credential_method = 'REJOIN'),
              (SELECT count(*) FROM iam_v2.sessions s WHERE s.ended >= d AND s.ended < d + interval '1 day' AND s.end_reason IN ('ADDRESS_NO_LONGER_OWNED','ADDRESS_CHANGED')),
              (SELECT count(*) FROM iam_v2.sign_in_attempts a WHERE a.occurred_at >= d AND a.occurred_at < d + interval '1 day' AND a.result <> 'VERIFIED')
            FROM generate_series(date_trunc('day', now()) - interval '13 days', date_trunc('day', now()), interval '1 day') d ORDER BY d""")
        res["online"] = self.s.sql("SELECT count(*), count(DISTINCT device_id) FROM iam_v2.sessions WHERE ended IS NULL AND state = 'active'")[0]
        return res

    def devices(self, state):
        """Actionable per-device findings from the last 24 h; guest errors and policy refusals are labelled, not escalated."""
        items = []

        def item(dev, mac, ip, net, issue, cause, confidence, sev, first, last, count, status, action):
            items.append(dict(device=short(dev), mac=mask_mac(mac), ip=ip or "", network=net or "", issue=issue, cause=cause,
                              confidence=confidence, severity=sev, first=first, last=last, count=n(count), status=status, action=action))

        try:
            leases_by_mac = {mac: (ip, granted) for ip, (mac, exp, granted) in self.leases().items()}
        except Unavailable as e:
            leases_by_mac = None
            self.add("reconnect:missed", "automatic reconnection", NV, f"returning devices unknown: {e}")
        live = {r[0] for r in self.s.sql("SELECT DISTINCT device_id::text FROM iam_v2.sessions WHERE ended IS NULL")}
        now_or_past = lambda dev: "RESOLVED" if dev in live else "ONGOING"
        for mac, mode, cnt, first, last, ip, net, dev, online in self.s.sql("""SELECT a.device_mac::text,
                  mode() WITHIN GROUP (ORDER BY a.result) FILTER (WHERE a.result <> 'VERIFIED'), count(*) FILTER (WHERE a.result <> 'VERIFIED'),
                  to_char(min(a.occurred_at), 'MM-DD HH24:MI'), to_char(max(a.occurred_at), 'MM-DD HH24:MI'), host(max(a.device_ip)), max(a.guest_network_name),
                  (SELECT d.id::text FROM iam_v2.devices d WHERE d.mac = a.device_mac LIMIT 1),
                  EXISTS (SELECT 1 FROM iam_v2.sessions s JOIN iam_v2.devices d ON d.id = s.device_id WHERE d.mac = a.device_mac AND s.ended IS NULL)
                FROM iam_v2.sign_in_attempts a WHERE a.occurred_at > now() - interval '24 hours' AND a.device_mac IS NOT NULL
                GROUP BY a.device_mac HAVING count(*) FILTER (WHERE a.result <> 'VERIFIED') >= 5
                   AND max(a.occurred_at) FILTER (WHERE a.result = 'VERIFIED') IS DISTINCT FROM max(a.occurred_at)"""):
            system = mode not in GUEST_ERRORS + POLICY_REFUSALS
            item(dev, mac, ip, net, "refused 5+ times", f"mostly {mode}" + ("" if system else (" (guest-entered details)" if mode in GUEST_ERRORS else " (package policy)")),
                 "confirmed", "HIGH" if system else "LOW", first, last, cnt, "RESOLVED" if online == "t" else "ONGOING",
                 "investigate the sign-in service" if system else "front desk: confirm room number and surname" if mode in GUEST_ERRORS else "none: policy working as set")
        for dev, mac, cnt, first, last, ip, net in self.s.sql("""SELECT ns.device_id::text, lower(ns.mac::text), count(*), to_char(min(a.occurred_at), 'MM-DD HH24:MI'),
                  to_char(max(a.occurred_at), 'MM-DD HH24:MI'), host(max(ns.ip)), max(ns.ingress_interface)
                FROM iam_v2.sign_in_attempts a JOIN iam_v2.sessions ns ON ns.id = a.session_id
                JOIN LATERAL (SELECT p.* FROM iam_v2.sessions p WHERE p.device_id = ns.device_id AND p.started < ns.started ORDER BY p.started DESC LIMIT 1) p ON true
                JOIN iam_v2.entitlements e ON e.id = p.entitlement_id
                WHERE a.occurred_at > now() - interval '24 hours' AND a.result = 'VERIFIED'
                  AND p.end_reason IN ('ADDRESS_NO_LONGER_OWNED','ADDRESS_CHANGED','SUPERSEDED_ON_DEVICE_MOVE','SUPERSEDED_ON_ADDRESS')
                  AND p.ingress_interface = ns.ingress_interface
                  AND e.status = 'ACTIVE' AND (e.window_ends_at IS NULL OR e.window_ends_at > a.occurred_at)
                GROUP BY ns.device_id, ns.mac"""):
            item(dev, mac, ip, net, "signed in by hand although it could have reconnected",
                 "previous session on the same network ended on an address change with the package still valid", "confirmed", "MEDIUM",
                 first, last, cnt, now_or_past(dev), "check whether the device's captive probe reached automatic reconnection")
        missed = 0
        import ipaddress
        subnets = {}
        for bridge, cidr in ([] if leases_by_mac is None else self.s.sql("SELECT bridge_name, subnet_cidr::text FROM public.guest_networks WHERE enabled")):
            try:
                subnets[bridge] = ipaddress.ip_network(cidr, strict=False)
            except ValueError:
                pass
        for dev, mac, ip, net, ended, ended_epoch in [] if leases_by_mac is None else self.s.sql(f"""SELECT s.device_id::text, lower(s.mac::text), host(s.ip), s.ingress_interface,
                  to_char(s.ended, 'MM-DD HH24:MI'), extract(epoch FROM s.ended)::bigint
                FROM iam_v2.sessions s JOIN iam_v2.entitlements e ON e.id = s.entitlement_id
                JOIN iam_v2.stays st ON st.id = e.stay_id
                JOIN iam_v2.entitlement_devices d ON d.entitlement_id = e.id AND d.device_id = s.device_id AND d.status = 'AUTHORIZED'
                WHERE s.ended > now() - interval '24 hours' AND s.ended < now() - interval '10 minutes'
                  AND s.end_reason IN ('ADDRESS_NO_LONGER_OWNED','ADDRESS_CHANGED','SUPERSEDED_ON_DEVICE_MOVE','SUPERSEDED_ON_ADDRESS')
                  AND e.status = 'ACTIVE' AND (e.window_ends_at IS NULL OR e.window_ends_at > now()) AND {STAY_REJOINABLE}
                  AND NOT EXISTS (SELECT 1 FROM iam_v2.sessions x WHERE x.device_id = s.device_id AND x.started > s.started)"""):
            lease = leases_by_mac.get(mac)
            same_network = lease is not None and net in subnets and ipaddress.ip_address(lease[0]) in subnets[net]
            if same_network and n(ended_epoch) < lease[1] <= self.now.timestamp() - 600:
                missed += 1
                back = dt.datetime.fromtimestamp(lease[1], dt.timezone.utc).strftime("%m-%d %H:%M")
                item(dev, mac, lease[0], net, "back on the network but not reconnected",
                     f"took a new lease at {back} after its session ended; still eligible (package valid, device authorized)", "confirmed", "MEDIUM",
                     ended, back, 1, "ONGOING", "reconnects when its portal page loads; while the HTTPS portal redirects OS probes first, devices that "
                     "only notify (Android) wait for the guest to tap")
        if leases_by_mac is not None:
            self.add("reconnect:missed", "automatic reconnection", WARN if missed else PASS,
                     f"{missed} eligible device(s) back on the network more than 10 min without being reconnected", persist=2)
        for mac, cnt, first, last, ip, net, dev in self.s.sql("""SELECT a.device_mac::text, count(*), to_char(min(a.occurred_at), 'MM-DD HH24:MI'), to_char(max(a.occurred_at), 'MM-DD HH24:MI'),
                  host(max(a.device_ip)), max(a.guest_network_name), (SELECT d.id::text FROM iam_v2.devices d WHERE d.mac = a.device_mac LIMIT 1)
                FROM iam_v2.sign_in_attempts a WHERE a.occurred_at > now() - interval '24 hours' AND a.result = 'ADDRESS_CONFLICT' GROUP BY a.device_mac"""):
            item(dev, mac, ip, net, "IP conflict", "device used an address DHCP had leased to another device", "confirmed", "MEDIUM", first, last, cnt,
                 now_or_past(dev), "device should renew its address (Wi-Fi off and on); repeated cases: check Kea")
        for dev, mac, ip, net, reason, first, last, cnt in self.s.sql("""SELECT s.device_id::text, lower(s.mac::text), host(max(s.ip)), max(s.ingress_interface), max(s.end_reason),
                  to_char(min(s.ended), 'MM-DD HH24:MI'), to_char(max(s.ended), 'MM-DD HH24:MI'), count(*)
                FROM iam_v2.sessions s WHERE s.ended > now() - interval '24 hours'
                  AND s.end_reason IN ('ADDRESS_NO_LONGER_OWNED','ADDRESS_CHANGED','ENFORCEMENT_TORN_DOWN','SUPERSEDED_ON_DEVICE_MOVE','SUPERSEDED_ON_ADDRESS')
                  AND EXISTS (SELECT 1 FROM iam_v2.accounting_usage_minute u WHERE u.session_id = s.id AND u.bucket >= s.ended - interval '10 minutes'
                              AND (u.bytes_up + u.bytes_down) > 0)
                GROUP BY s.device_id, s.mac"""):
            item(dev, mac, ip, net, "disconnected while active", f"session ended {reason} with traffic in its last 10 min", "confirmed", "HIGH", first, last, cnt,
                 now_or_past(dev), "correlate with Kea and netd logs for this address")
        for dev, mac, ip, net, started in self.s.sql("""SELECT s.device_id::text, lower(s.mac::text), host(s.ip), s.ingress_interface, to_char(s.started, 'MM-DD HH24:MI')
                FROM iam_v2.sessions s WHERE s.ended IS NULL AND s.state = 'active' AND NOT EXISTS (
                  SELECT 1 FROM iam_v2.entitlement_devices d WHERE d.entitlement_id = s.entitlement_id AND d.device_id = s.device_id AND d.status = 'AUTHORIZED')"""):
            item(dev, mac, ip, net, "online without authorization", "live session but the device is not authorized on its package", "confirmed", "HIGH",
                 started, started, 1, "ONGOING", "escalate: authorization integrity")
        for dev, mac, ip, net, cnt, first, last in self.s.sql("""SELECT s.device_id::text, lower(s.mac::text), host(max(s.ip)), max(s.ingress_interface), count(*),
                  to_char(min(s.ended), 'MM-DD HH24:MI'), to_char(max(s.ended), 'MM-DD HH24:MI')
                FROM iam_v2.sessions s WHERE s.ended > now() - interval '6 hours'
                  AND s.end_reason IN ('ADDRESS_NO_LONGER_OWNED','ADDRESS_CHANGED','ENFORCEMENT_TORN_DOWN','SUPERSEDED_ON_DEVICE_MOVE','SUPERSEDED_ON_ADDRESS')
                GROUP BY s.device_id, s.mac HAVING count(*) >= 2"""):
            item(dev, mac, ip, net, "cut off repeatedly", f"{cnt} system-ended sessions in 6 h", "confirmed", "MEDIUM", first, last, cnt, now_or_past(dev),
                 "check the device's DHCP behaviour (randomised address, roaming between networks)")
        prev = state.get("devices", {})
        cur = {}
        for it in items:
            k = f"{it['device']}|{it['issue']}"
            it["seen_since"] = prev.get(k, it["first"])
            cur[k] = it["seen_since"]
        state["devices"] = cur
        order = {"HIGH": 0, "MEDIUM": 1, "LOW": 2}
        items.sort(key=lambda x: x["last"], reverse=True)
        items.sort(key=lambda x: (order.get(x["severity"], 3), x["status"] != "ONGOING"))
        return items

    @staticmethod
    def _json_field(line, key):
        m = re.search(r'"%s":("([^"]*)"|[-0-9.]+)' % re.escape(key), line)
        return (m.group(2) if m.group(2) is not None else m.group(1)) if m else ""

    def _log_time(self, line):
        v = self._json_field(line, "time")
        try:
            return ts(v) if v else None
        except ValueError:
            return None

    # ---- run -----------------------------------------------------------------------------------------------------------
    QUICK = ["check_services", "check_resources", "check_database", "check_enforcement", "check_acctd", "check_http",
             "check_network", "check_dhcp", "check_pms", "check_licence", "check_certs", "check_signin", "check_integrity", "check_errors"]
    DEEP = ["check_backup", "check_parity"]

    def run(self, state):
        t0 = time.monotonic()
        names = self.QUICK + (self.DEEP if self.mode == "deep" else [])
        ran = set()
        self.complete = set()
        for name in names:
            start = len(self.results)
            try:
                getattr(self, name)(state)
                ran.add(name)
                mine = self.results[start:]
                if mine and all(r.status != NV for r in mine):
                    self.complete |= {f for f in ENTITY_LISTS if any(r.key.startswith(f) for r in mine)}
            except Unavailable as e:
                self.add(f"{name}:evidence", name.replace("check_", ""), NV, str(e))
            except Exception as e:
                self.add(f"{name}:evidence", name.replace("check_", ""), NV, f"check failed: {e.__class__.__name__}: {str(e)[:120]}")
        if self.mode == "deep":
            for part, fn in (("analytics", self.analytics), ("breakdowns", self.breakdowns), ("devices", lambda: self.devices(state))):
                try:
                    self.report[part] = fn()
                except Unavailable as e:
                    self.report[part] = None
                    self.add(f"report:{part}", "reporting", NV, f"{part}: {e}")
        self.m["run_ms"] = round((time.monotonic() - t0) * 1000)
        ru = resource.getrusage(resource.RUSAGE_CHILDREN)
        me = resource.getrusage(resource.RUSAGE_SELF)
        self.m["cpu_ms"] = round((ru.ru_utime + ru.ru_stime + me.ru_utime + me.ru_stime) * 1000)
        return ran


class Incidents:
    """Persistence and de-duplication: an issue is raised after `persist` consecutive failing runs, repeated with its
    first-seen time and run count rather than re-announced, and reported once when it clears."""

    def __init__(self, state, now):
        self.st = state.setdefault("incidents", {})
        self.hist = state.setdefault("resolved", [])
        self.now = now

    def apply(self, results, complete=()):
        """complete: entity lists a check read in full this run; an entity it no longer lists is gone (a removed network,
        a decommissioned PMS connection), so its incident is over. Every other incident waits for its own evaluation."""
        active, recovered = [], []
        failing = {r.key: r for r in results if r.status in (WARN, CRIT)}
        evaluated = {r.key for r in results if r.status != NV} | {k for k in self.st if any(k.startswith(f) for f in complete)}
        for key, r in failing.items():
            inc = self.st.get(key) or {"first": self.now.isoformat(), "count": 0, "streak": 0, "raised": False}
            inc["streak"] += 1
            inc["count"] += 1
            inc.update(last=self.now.isoformat(), status=r.status, text=r.text, area=r.area)
            if inc["streak"] >= r.persist:
                inc["raised"] = True
                active.append((r, inc))
            self.st[key] = inc
        for key, inc in self.st.items():
            if key not in failing and key not in evaluated and inc.get("raised"):
                carried = Result(key, inc.get("area", ""), inc.get("status", WARN), inc.get("text", key) + f"; last checked {hm(ts(inc['last']))}")
                active.append((carried, inc))
        for key in list(self.st):
            if key in failing or key not in evaluated:
                continue
            inc = self.st.pop(key)
            if inc.get("raised"):
                recovered.append((key, inc))
                self.hist.append({"key": key, "text": inc.get("text"), "first": inc["first"], "cleared": self.now.isoformat(), "status": inc.get("status")})
        del self.hist[:-50]
        return active, recovered


def overall(active, nv):
    """The headline: the worst active issue, NOT_VERIFIED when evidence is missing and nothing worse is active."""
    worst = max([r.status for r, _ in active] + [PASS], key=RANK.get)
    return NV if worst == PASS and nv else worst


def summary_line(mon, active, nv):
    m = mon.m
    worst = overall(active, nv)
    clients = (f"refused_15m: {m.get('refused', 'none')} | ended_15m: {m.get('ends', 'none')} | rejoined_15m={m.get('rejoined', '?')} "
               f"recut2x_6h={m.get('recut', '?')} inhouse_allowance_used_up={m.get('usedup', '?')} stuck_60m={m.get('stuck', '?')}")
    return (f"{mon.now.strftime('%Y-%m-%dT%H:%M:%SZ')} signin={m.get('signin', '?')} online={m.get('online', '?')} netd_sessions={m.get('netd_sessions', '?')} "
            f"pass_ms={m.get('pass_ms', '?')} cpu={m.get('cpu', '?')}% mem_free={m.get('mem', '?')}MB disk={m.get('disk', '?')}% "
            f"dhcp_alloc_15m={m.get('dhcp_alloc', '?')} pools: {m.get('pools', '?')} pms: {m.get('pms', '?')} deadlocks_1h={m.get('deadlocks_1h', '?')} "
            f"clients: {clients} status={worst} issues={len(active)} not_verified={nv} mode={mon.mode} run_ms={m.get('run_ms')} cpu_ms={m.get('cpu_ms')}")


def issue_lines(active, recovered, now):
    out = []
    for r, inc in sorted(active, key=lambda x: (-RANK[x[0].status], x[0].key)):
        first = ts(inc["first"])
        out.append(f"ISSUE [{r.status}] {r.area}: {r.text} (since {hm(first)}, {inc['count']} run{'s' if inc['count'] != 1 else ''})")
    for key, inc in recovered:
        out.append(f"RECOVERED {inc.get('area', '')}: {inc.get('text', key)} (from {hm(ts(inc['first']))} to {hm(now)})")
    return out


def table(headers, rows):
    rows = [[str(c) for c in r] for r in rows]
    w = [max(len(h), *(len(r[i]) for r in rows)) if rows else len(h) for i, h in enumerate(headers)]
    line = lambda r: "  ".join(c.ljust(w[i]) if i == 0 else c.rjust(w[i]) for i, c in enumerate(r))
    return "\n".join([line(headers), line(["-" * x for x in w])] + [line(r) for r in rows])


def render_report(mon, active, recovered, state):
    now = mon.now.strftime("%Y-%m-%d %H:%M UTC")
    res = mon.results
    counts = {s: sum(1 for r in res if r.status == s) for s in (PASS, WARN, CRIT, NV)}
    worst = overall(active, counts[NV])
    L = [f"OneGate appliance health report - {now} ({mon.mode})", "",
         f"OVERALL: {worst}   checks: {counts[PASS]} PASS, {counts[WARN]} WARN, {counts[CRIT]} CRITICAL, {counts[NV]} NOT VERIFIED",
         f"monitor cost: {mon.m.get('run_ms')} ms elapsed, {mon.m.get('cpu_ms')} ms CPU", ""]
    L.append("ACTIVE ISSUES")
    L += [f"  {l}" for l in issue_lines(active, recovered, mon.now)] or ["  none"]
    L.append("")
    L.append("CHECKS BY AREA")
    by_area = {}
    for r in res:
        by_area.setdefault(r.area, []).append(r)
    for area in sorted(by_area):
        rs = by_area[area]
        worst_a = max((r.status for r in rs), key=RANK.get)
        L.append(f"  [{worst_a}] {area}")
        for r in sorted(rs, key=lambda x: -RANK[x.status]):
            if r.status != PASS or len(rs) <= 4:
                L.append(f"      {r.status:<12} {r.text}")
        passing = [r for r in rs if r.status == PASS]
        if len(rs) > 4 and passing:
            L.append(f"      PASS         {len(passing)} more check(s) passing")
    b = mon.report.get("breakdowns")
    a = mon.report.get("analytics")
    if a:
        names, wins = a
        L += ["", "CLIENT ACTIVITY (devices, sessions and sign-in attempts are counted separately)"]
        if b:
            L.append(f"  online now: {b['online'][0]} sessions on {b['online'][1]} devices")
        headers = ["measure"] + [f"{lab} / previous" for lab, _ in wins]
        rows = []
        for i, nm in enumerate(names):
            rows.append([nm] + [f"{w['now'][i]} / {w['previous'][i]}" for _, w in wins])
        L.append(table(headers, rows))
    if b:
        L += ["", "SESSIONS ENDED, LAST 24 H (by reason)", "  " + ", ".join(f"{k}={v}" for k, v in b["ends_24h"]) or "  none",
              "SESSIONS STARTED, LAST 24 H (by method)", "  " + ", ".join(f"{k}={v}" for k, v in b["methods_24h"]) or "  none",
              "ROOM SIGN-IN RESULTS, LAST 24 H", "  " + ", ".join(f"{k}={v}" for k, v in b["results_24h"]) or "  none",
              "", "PER NETWORK, LAST 24 H",
              table(["network", "devices", "sessions", "manual", "auto-reconnect", "address-loss ends", "online now"], b["networks_24h"]),
              "", "HOURLY, LAST 24 H",
              table(["hour (UTC)", "online at end", "manual sign-ins", "auto-reconnects", "sessions ended", "refused attempts"], b["hourly"]),
              "", "DAILY, LAST 14 DAYS (compare a row with the one 7 lines up for the same weekday)",
              table(["day (UTC)", "devices", "manual sign-ins", "auto-reconnects", "address-loss ends", "refused attempts"], b["daily"])]
    dev = mon.report.get("devices")
    if dev is not None:
        L += ["", f"DEVICE DIAGNOSTICS, LAST 24 H ({len(dev)} finding(s); MAC addresses masked; full list in devices.tsv)"]
        if dev:
            L.append(table(["sev", "device", "mac", "ip", "network", "issue", "cause", "conf", "first", "last", "count", "status"],
                           [[d["severity"], d["device"], d["mac"], d["ip"], d["network"], d["issue"], d["cause"][:60], d["confidence"],
                             d["first"], d["last"], d["count"], d["status"]] for d in dev[:25]]))
        else:
            L.append("  none")
    dl = state.get("deadlocks", [])
    if len(dl) > 1:
        L += ["", "DEADLOCKS (from the database counter)"]
        daily = {}
        for (t1, v1), (t2, v2) in zip(dl, dl[1:]):
            daily[t2[:10]] = daily.get(t2[:10], 0) + max(0, v2 - v1)
        L.append("  " + ", ".join(f"{d}={c}" for d, c in sorted(daily.items())[-8:]))
    lw = state.get("lock_waits", {})
    if lw:
        L += ["", "LOCK WAITS OVER 1 S, BY WHAT THEY WAITED ON (database log, log_lock_waits)"]
        for d in sorted(lw)[-8:]:
            L.append(f"  {d}: " + (", ".join(f"{k}={v}" for k, v in sorted(lw[d].items(), key=lambda x: -x[1])) or "none"))
    if state.get("resolved"):
        L += ["", "RECENTLY RESOLVED"] + [f"  {h['first'][:16]} to {h['cleared'][:16]}  {h.get('text')}" for h in state["resolved"][-10:]]
    return "\n".join(L) + "\n"


def write_private(path, text):
    tmp = path + ".tmp"
    fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, "w") as f:
        f.write(text)
    os.replace(tmp, path)


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("mode", choices=["quick", "deep", "report"])
    ap.add_argument("--stdout", action="store_true", help="print instead of appending to the log")
    args = ap.parse_args(argv)
    sd = CFG["state"]
    os.makedirs(sd, mode=0o700, exist_ok=True)
    if args.mode == "report":
        try:
            sys.stdout.write(open(os.path.join(sd, "report.txt")).read())
        except OSError:
            print("no report yet; the deep run writes it every 30 minutes")
        return 0
    lock = open(os.path.join(sd, ".lock"), "w")
    # A quick run skips while another run holds the lock; a deep run waits up to a minute so its report is not lost.
    deadline = time.monotonic() + (60 if args.mode == "deep" else 0)
    while True:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            break
        except OSError:
            if time.monotonic() >= deadline:
                return 0
            time.sleep(1)
    path = os.path.join(sd, "state.json")
    try:
        state = json.load(open(path))
    except (OSError, ValueError):
        state = {}
    mon = Monitor(Sys(CFG), args.mode)
    mon.run(state)
    active, recovered = Incidents(state, mon.now).apply(mon.results, mon.complete)
    nv = sum(1 for r in mon.results if r.status == NV)
    lines = [summary_line(mon, active, nv)] + issue_lines(active, recovered, mon.now)
    if args.stdout:
        print("\n".join(lines))
    else:
        with open(CFG["log"], "a") as f:
            f.write("\n".join(lines) + "\n")
    if args.mode == "deep":
        write_private(os.path.join(sd, "report.txt"), render_report(mon, active, recovered, state))
        dev = mon.report.get("devices") or []
        cols = ["severity", "status", "device", "mac", "ip", "network", "issue", "cause", "confidence", "first", "last", "count", "seen_since", "action"]
        write_private(os.path.join(sd, "devices.tsv"), "\t".join(cols) + "\n" + "".join("\t".join(str(d.get(c, "")) for c in cols) + "\n" for d in dev))
        write_private(os.path.join(sd, "results.json"), json.dumps({"at": mon.now.isoformat(), "metrics": {k: v for k, v in mon.m.items() if k != "leases"},
                                                                    "results": [vars(r) for r in mon.results]}, indent=1))
    write_private(path, json.dumps(state))
    return 0


if __name__ == "__main__":
    sys.exit(main())
