#!/usr/bin/env python3
"""Host-side orchestrator for the outage-continuity scenarios (OUTAGE_CONTINUITY_LAB_PLAN.md). Real wall clock only.
usage: orch.py <name> <mode> <window_min> <outage_min> <events> [run_idx]
  mode   acctd_stop | acctd_hang | pg_down
  events comma list of <offset_min>:<kind>; kinds: traffic20 disc5 entend5 quota netd3 netd9 netdstop acct20 signin5 discdb
"""
import json, os, subprocess, sys, time

os.environ["MSYS_NO_PATHCONV"] = "1"
name, mode, W, OUT, evs = sys.argv[1], sys.argv[2], int(sys.argv[3]), int(sys.argv[4]), sys.argv[5]
RUN = int(sys.argv[6]) if len(sys.argv) > 6 else 0
EV = sorted([(float(a.split(":")[0]), a.split(":")[1]) for a in evs.split(",") if a])
EVDIR = "D:/tmp/fm/ev"
os.makedirs(EVDIR, exist_ok=True)
LOG = open("%s/%s.orch.log" % (EVDIR, name), "a", buffering=1)
clients = {c["idx"]: c for c in json.load(open("D:/tmp/fm/lab/vch/clients.json"))}
SINK = "http://172.30.10.50:8080"


def log(msg, **kw):
    LOG.write(json.dumps({"t": round(time.time(), 2), "msg": msg, **kw}) + "\n")


def dx(c, cmd, timeout=900):
    r = subprocess.run(["docker", "exec", c, "sh", "-c", cmd], capture_output=True, text=True, timeout=timeout)
    return (r.stdout + r.stderr).strip()


def dxd(c, cmd):
    subprocess.run(["docker", "exec", "-d", c, "sh", "-c", cmd])


def gen(cmd, timeout=900):
    return dx("fm-gen", "ulimit -n 1048576; cd /root; " + cmd, timeout)


def gend(cmd):
    dxd("fm-gen", "ulimit -n 1048576; cd /root; " + cmd)


def app(cmd, timeout=900):
    return dx("fm-appl", ". /root/cont-lib.sh; " + cmd, timeout)


def ips(a, b):
    return " ".join(clients[i]["ip"] for i in range(a, b))


def wait_until(t):
    while time.time() < t:
        time.sleep(min(2.0, max(0.0, t - time.time())))


def reset_flags(a, b):
    gen("python3 -c \"import json;p='/var/lib/ogload/clients.json';c=json.load(open(p));[x.__setitem__('signed_in',False) for x in c if %d<=x['idx']<%d];json.dump(c,open(p,'w'))\"" % (a, b))


def signin(a, b, vfile, off, rate=120):
    reset_flags(a, b)
    out = gen("./ogload signin -from %d -to %d -vouchers /root/%s -voucher-offset %d -rate %d -log /root/%s.si-%d-%d.log | tail -n 1" % (a, b, vfile, off, rate, name, a, b))
    log("signin", range=[a, b], result=out[-300:])



def ensure_population():
    """Re-sign any of clients 0..1699 that is not in the kernel set (fresh voucher codes from v-long2.txt, offset kept on the host)."""
    present = set(app("nft list set inet stayconnect phase3_auth_ipv4 | grep -o '10\.6[01]\.[0-9]*\.[0-9]*'").split())
    missing = [i for i in range(0, 1700) if clients[i]["ip"] not in present]
    log("ensure_population", missing=len(missing), sample=missing[:10])
    if not missing:
        return
    offf = "D:/tmp/fm/lab/v2offset.txt"
    off = int(open(offf).read()) if os.path.exists(offf) else 0
    lo, hi = min(missing), max(missing) + 1
    ml = ",".join(str(i) for i in missing)
    gen("python3 -c \"import json;p='/var/lib/ogload/clients.json';m=set([%s]);c=json.load(open(p));[x.__setitem__('signed_in',(x['idx'] not in m) if %d<=x['idx']<%d else x['signed_in']) for x in c];json.dump(c,open(p,'w'))\"" % (ml, lo, hi))
    out = gen("./ogload signin -from %d -to %d -vouchers /root/v-long2.txt -voucher-offset %d -rate 240 -log /root/%s.restore.log | tail -n 1" % (lo, hi, off, name))
    open(offf, "w").write(str(off + len(missing)))
    log("ensure_population_done", result=out[-300:])
    time.sleep(30)


def snap(tag):
    """Accounting + client-side counters for clients 400..419 (accounting test) and 1700..1719 (Q200)."""
    out = {"t": time.time(), "tag": tag, "acct": {}, "ifc": {}}
    for line in app("python3 /root/cont_actions.py entusage %s" % (ips(400, 420) + " " + ips(1700, 1720)), timeout=300).splitlines():
        try:
            d = json.loads(line)
            out["acct"][d["ip"]] = d.get("info")
        except Exception:
            pass
    txt = gen("for i in $(seq 400 419); do n=$(printf %04d $i); echo $i $(ip netns exec cl$n cat /sys/class/net/mv$n/statistics/rx_bytes /sys/class/net/mv$n/statistics/tx_bytes); done")
    for line in txt.splitlines():
        a = line.split()
        if len(a) == 3:
            out["ifc"][a[0]] = [int(a[1]), int(a[2])]
    json.dump(out, open("%s/%s.snap-%s.json" % (EVDIR, name, tag), "w"))
    log("snap", tag=tag, n_acct=len(out["acct"]), n_ifc=len(out["ifc"]))


# ---------------------------------------------------------------- population
ensure_population()
log("start", name=name, mode=mode, window_min=W, outage_min=OUT, events=evs, run=RUN)
app("setwin %d '%s'" % (W, name))
time.sleep(100)
cs = app("contstat")
log("window_set", contstat=cs, authset=app("authset"))
# extra populations: Q200 (20), Q50 (10), then 60 s before T0 the 5-minute-window sessions
signin(1700, 1720, "v-q200.txt", RUN * 20)
signin(1720, 1730, "v-q50a.txt", RUN * 10)
time.sleep(240)  # the first plans after these sign-ins carry their budgets/baselines
signin(1730, 1780, "v-w5.txt", RUN * 50, rate=300)
t_w5 = time.time()
pre = {"authset": app("authset"), "contstat": app("contstat"), "psql_active": app("psql_c \"select count(*) from iam_v2.sessions where state='active' and ended is null\"")}
log("pre_state", **pre)
app("mkdir -p /root/ev; rm -f /root/ev/%s.*" % name)
total_s = OUT * 60 + 600
dxd("fm-appl", "python3 /root/setwatch.py /root/ev/%s 2 %d" % (name, total_s + 120))
dxd("fm-appl", "/root/sampler.sh /root/ev/%s.sampler.csv 5 2500" % name)
if mode == "acctd_hang":
    app("mkdir -p /etc/systemd/system/stayconnect-acctd.service.d; printf '[Service]\\nWatchdogSec=0\\n' > /etc/systemd/system/stayconnect-acctd.service.d/lab-nowatchdog.conf; systemctl daemon-reload; systemctl restart stayconnect-acctd")
    time.sleep(70)
    log("acctd_hang_prepared", watchdog=app("systemctl show -p WatchdogUSec stayconnect-acctd"))
wait_until(t_w5 + 130)  # W5 sessions are then >= 2 minutes old and end ~170 s into the outage
snap("before")

# ---------------------------------------------------------------- T0
T0 = time.time()
open("%s/%s.t0" % (EVDIR, name), "w").write(str(T0))
log("T0", T0=T0)
if mode == "acctd_stop":
    app("systemctl stop stayconnect-acctd")
elif mode == "acctd_hang":
    app("kill -STOP $(systemctl show -p MainPID --value stayconnect-acctd)")
elif mode == "pg_down":
    app("docker compose -f /opt/stayconnect/deploy/compose/infra.yml stop postgres")
log("outage_started", took_s=round(time.time() - T0, 2))
# background movers (10 percent of clients 300..1699), measured only while the window is open
bgdur = max(60, W * 60 - 90)
gend("./ogload traffic -from 300 -to 1700 -sink %s -pct 10 -bytes 262144 -every 5s -spread 4s -duration %ds -perclient /root/%s.bg.json > /root/%s.bg.out 2>&1" % (SINK, bgdur, name, name))
log("bg_traffic_started", duration_s=bgdur)


def rec_file():
    return app("jq -c '{b:.last_plan_boot_ms,n:(.sessions|length)}' /var/lib/stayconnect/netd-phase3-continuity.json 2>&1 | head -c 200")


def do(kind, off):
    if kind == "traffic20":
        gend("./ogload traffic -from 0 -to 20 -sink %s -pct 100 -bytes 1048576 -every 5s -duration 60s -perclient /root/%s.t20.json > /root/%s.t20.out 2>&1" % (SINK, name, name))
        log("traffic20", clients=[0, 20])
    elif kind == "disc5":
        out = app("python3 /root/cont_actions.py disconnect %s" % ips(100, 105))
        log("disc5", clients=[100, 105], out=out)
    elif kind == "entend5":
        out = app("python3 /root/cont_actions.py entend %s" % ips(200, 205))
        log("entend5", clients=[200, 205], out=out)
    elif kind == "quota":
        gend("./ogload traffic -from 1700 -to 1730 -sink %s -pct 100 -bytes 2097152 -every 2s -duration 900s -perclient /root/%s.quota.json > /root/%s.quota.out 2>&1" % (SINK, name, name))
        log("quota_drive_started", clients=[1700, 1730])
    elif kind == "netd3":   # 7a: three restarts, 30 s apart
        for i in range(3):
            before = app("authset")
            r0 = rec_file()
            t = time.time()
            app("systemctl stop stayconnect-netd; systemctl reset-failed stayconnect-netd; systemctl start --no-block stayconnect-netd")
            time.sleep(25)
            log("netd_restart", n=i + 1, t=t, authset_before=before, authset_after=app("authset"), record_before=r0, record_after=rec_file(), contstat=app("contstat")[:300])
            time.sleep(5)
    elif kind == "netd9":   # 7b: staff disconnect, then kill -9
        out = app("python3 /root/cont_actions.py disconnect %s" % ips(110, 111))
        log("disc_before_kill9", out=out)
        t = time.time()
        app("kill -9 $(pidof netd)")
        time.sleep(30)
        log("netd_kill9", t=t, authset=app("authset"), contstat=app("contstat")[:400])
    elif kind == "netdstop":  # 7c: SIGSTOP netd for 3 minutes
        t = time.time()
        app("kill -STOP $(pidof netd)")
        log("netd_stop", t=t)
        time.sleep(180)
        app("kill -CONT $(pidof netd)")
        log("netd_cont", t=time.time(), contstat=app("contstat")[:300])
    elif kind == "signin5":
        signin(1780, 1785, "v-long.txt", 1800 + RUN * 5, rate=60)
        log("signin5_done")
    elif kind == "discdb":
        out = app("python3 /root/cont_actions.py disconnect %s" % ips(100, 105), timeout=300)
        log("disc_with_db_down", out=out[-600:])
    elif kind == "acct20":   # section 3 traffic: 20 clients, known bytes each way, 10 minutes
        gend("./ogload traffic -from 400 -to 420 -sink %s -pct 100 -bytes 4194304 -up 1048576 -every 10s -duration 600s -perclient /root/%s.acct.json > /root/%s.acct.out 2>&1" % (SINK, name, name))
        log("acct20_started", clients=[400, 420])
    elif kind == "quota2":   # Q200 only
        gend("./ogload traffic -from 1700 -to 1720 -sink %s -pct 100 -bytes 2097152 -every 2s -duration 900s -perclient /root/%s.quota.json > /root/%s.quota.out 2>&1" % (SINK, name, name))
        log("quota2_drive_started", clients=[1700, 1720])


pending = list(EV)
restore_at = T0 + OUT * 60
while pending and T0 + pending[0][0] * 60 < restore_at:
    off, kind = pending.pop(0)
    wait_until(T0 + off * 60)
    log("event", kind=kind, due=off, late_s=round(time.time() - (T0 + off * 60), 2))
    do(kind, off)

wait_until(restore_at)
log("restore", mode=mode, contstat=app("contstat"), authset=app("authset"))
Tr = time.time()
if mode == "acctd_stop":
    app("systemctl start stayconnect-acctd")
elif mode == "acctd_hang":
    app("kill -CONT $(systemctl show -p MainPID --value stayconnect-acctd)")
elif mode == "pg_down":
    app("docker compose -f /opt/stayconnect/deploy/compose/infra.yml start postgres")
log("restore_done", took_s=round(time.time() - Tr, 2), Tr=Tr)
open("%s/%s.tr" % (EVDIR, name), "w").write(str(Tr))
time.sleep(150)
snap("after")
time.sleep(90)
log("after_restore", contstat=app("contstat"), authset=app("authset"))
if mode == "acctd_hang":
    app("rm -f /etc/systemd/system/stayconnect-acctd.service.d/lab-nowatchdog.conf; rmdir /etc/systemd/system/stayconnect-acctd.service.d; systemctl daemon-reload; systemctl restart stayconnect-acctd")
    log("dropin_removed", watchdog=app("systemctl show -p WatchdogUSec stayconnect-acctd"))
# wait for the watcher to end, then collect
wait_until(T0 + total_s + 125)
app("pkill -f sampler.sh")
app("journalctl -u stayconnect-netd -u stayconnect-acctd --since @%d --no-pager | gzip > /root/ev/%s.journal.gz" % (int(T0) - 400, name))
app("ls -l --time-style=full-iso /var/lib/stayconnect/netd-phase3-continuity*.json > /root/ev/%s.recordfiles.txt 2>&1" % name)
for suffix in ["csv", "intervals.json", "cont.jsonl", "sampler.csv", "journal.gz", "recordfiles.txt"]:
    subprocess.run(["docker", "cp", "fm-appl:/root/ev/%s.%s" % (name, suffix), "%s/%s.%s" % (EVDIR, name, suffix)], capture_output=True)
for suffix in ["bg.json", "bg.out", "t20.json", "t20.out", "quota.json", "quota.out", "acct.json", "acct.out"]:
    subprocess.run(["docker", "cp", "fm-gen:/root/%s.%s" % (name, suffix), "%s/%s.%s" % (EVDIR, name, suffix)], capture_output=True)
for f in []:
    pass
log("done")
open("%s/%s.done" % (EVDIR, name), "w").write("done")
