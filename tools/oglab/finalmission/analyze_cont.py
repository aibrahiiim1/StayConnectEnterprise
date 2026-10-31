#!/usr/bin/env python3
"""Evaluate one continuity run against the plan criteria. usage: analyze_cont.py <name> <window_min> [outage_min]"""
import json, os, subprocess, sys, statistics, gzip, re

os.environ["MSYS_NO_PATHCONV"] = "1"
EV = "D:/tmp/fm/ev"
name, W = sys.argv[1], int(sys.argv[2])
clients = {c["idx"]: c["ip"] for c in json.load(open("D:/tmp/fm/lab/vch/clients.json"))}
ip2idx = {v: k for k, v in clients.items()}
T0 = float(open("%s/%s.t0" % (EV, name)).read())
Tr = float(open("%s/%s.tr" % (EV, name)).read()) if os.path.exists("%s/%s.tr" % (EV, name)) else None
iv = json.load(open("%s/%s.intervals.json" % (EV, name)))
rows = [l.strip().split(",") for l in open("%s/%s.csv" % (EV, name)) if l.strip()]
series = [(int(r[0]), int(r[1]), r[2], r[3], r[4], r[5], r[6], r[7]) for r in rows]
orch = [json.loads(l) for l in open("%s/%s.orch.log" % (EV, name))]
CAP = Tr
out = {"name": name, "window_min": W, "T0": T0, "Tr": Tr}


def psql(sql):
    r = subprocess.run(["docker", "exec", "fm-appl", "sh", "-c", "docker exec -i stayconnect-pg psql -X -At -F '|' -U stayconnect -d stayconnect_site -c \"%s\"" % sql], capture_output=True, text=True, timeout=120)
    return r.stdout.strip()


def present_at(ip, t):
    for a, b in iv.get(ip, []):
        if a - 2 <= t <= b + 2:
            return True
    return False


def last_seen_after_T0(ip):
    best = None
    for a, b in iv.get(ip, []):
        if b >= T0 and a < (CAP or 1e18):
            b = min(b, CAP) if CAP else b
            best = b if best is None or b > best else best
    return best


def first_seen_after(ip, t):
    for a, b in iv.get(ip, []):
        if a > t:
            return a
    return None


def events(kind):
    return [o for o in orch if o.get("msg") == kind]


extra_ex = set()
for _k in ("disc", "entend"):
    _f = "%s/%s.manual_%s.out" % (EV, name, _k)
    if os.path.exists(_f):
        for _ln in open(_f):
            try:
                extra_ex.add(ip2idx.get(json.loads(_ln)["ip"]))
            except Exception:
                pass
groups = {
    "long_undisturbed": [i for i in range(0, 1700) if not (100 <= i < 105 or 110 <= i < 111 or 200 <= i < 205 or i in extra_ex)],
    "disc5": list(range(100, 105)), "kill9_disc": [110], "entend5": list(range(200, 205)),
    "q200": list(range(1700, 1720)), "q50": list(range(1720, 1730)), "w5": list(range(1730, 1780)),
}
dead = T0 + W * 60
CAP = Tr
res = {}
# deadline / emptiness
lu = [i for i in groups["long_undisturbed"] if clients[i] in iv and present_at(clients[i], T0)]
res["long_present_at_T0"] = len(lu)
ls = [last_seen_after_T0(clients[i]) for i in lu]
rel = sorted(x - dead for x in ls if x is not None)
if rel:
    res["long_last_seen_rel_deadline_s"] = {"min": rel[0], "p50": rel[len(rel) // 2], "max": rel[-1]}
    res["long_left_before_deadline_minus_5s"] = sum(1 for x in rel if x < -5)
    res["long_left_after_deadline_plus_15s"] = sum(1 for x in rel if x > 15)
# set size trajectory
pre = [s for s in series if s[0] < T0]
during = [s for s in series if T0 + 5 <= s[0] <= dead - 10]
if during:
    res["authset_min_during_window"] = min(s[1] for s in during)
    res["authset_max_during_window"] = max(s[1] for s in during)
    res["authset_at_T0"] = pre[-1][1] if pre else None
first_empty = next((s[0] for s in series if s[0] > T0 + 120 and s[1] <= 5), None)
res["set_first_<=5_elements_rel_deadline_s"] = (first_empty - dead) if first_empty else None
# new onlines (never in the set before T0, appear during the outage window)
new_on = [ip for ip, ints in iv.items() if ints and all(a > T0 + 5 for a, b in ints) and ints[0][0] < (Tr or dead + 600)]
res["clients_that_became_online_during_outage"] = len(new_on)
res["new_online_idx_sample"] = sorted(ip2idx.get(i, -1) for i in new_on)[:20]
# staff disconnects
for kind, grp in (("disc5", "disc5"), ("entend5", "entend5")):
    ev = events(kind)
    mf = "%s/%s.manual_%s.out" % (EV, name, "disc" if kind == "disc5" else "entend")
    if os.path.exists(mf):
        ev = [{"out": open(mf).read()}]
    if ev:
        calls = []
        for ln in ev[0]["out"].splitlines():
            try:
                calls.append(json.loads(ln))
            except Exception:
                pass
        d = {}
        for c in calls:
            ip = c.get("ip")
            ls_ = last_seen_after_T0(ip)
            d[ip2idx.get(ip)] = {"call_t": round(c.get("t", 0) - T0, 1), "done_t": round(c.get("t_done", 0) - T0, 1), "status": c.get("status", c.get("result")),
                                 "last_seen_rel_call_s": None if ls_ is None else round(ls_ - c.get("t", 0), 1)}
        res[kind] = d
# quota
for grpname, grp in (("q200", groups["q200"]), ("q50", groups["q50"])):
    pc = {}
    f = "%s/%s.quota.json" % (EV, name)
    if os.path.exists(f):
        pc = json.load(open(f))
    d = {}
    start = [o["t"] for o in orch if o.get("msg") == "quota_drive_started"]
    for i in grp:
        ls_ = last_seen_after_T0(clients[i])
        d[i] = {"last_seen_rel_T0_s": None if ls_ is None else round(ls_ - T0, 1), "bytes_rx": pc.get(str(i), {}).get("ok_bytes")}
    res[grpname] = d
# 5 minute window sessions
w5 = {}
for i in groups["w5"]:
    r = psql("select '', extract(epoch from e.window_ends_at), coalesce(se.end_reason,''), coalesce(extract(epoch from se.ended)::text,'') from iam_v2.sessions se join iam_v2.entitlements e on e.id=se.entitlement_id where host(se.ip)='%s' order by se.started desc limit 1" % clients[i])
    ls_ = last_seen_after_T0(clients[i])
    try:
        exp = float(r.split("|")[1])
    except Exception:
        exp = None
    w5[i] = {"expires_epoch_rel_T0": None if exp is None else round(exp - T0, 1), "last_seen_rel_T0": None if ls_ is None else round(ls_ - T0, 1), "end_reason": r.split("|")[2] if r else ""}
diffs = [v["last_seen_rel_T0"] - v["expires_epoch_rel_T0"] for v in w5.values() if v["last_seen_rel_T0"] is not None and v["expires_epoch_rel_T0"] is not None]
res["w5_last_seen_minus_expiry_s"] = {"n": len(diffs), "min": min(diffs) if diffs else None, "max": max(diffs) if diffs else None}
# journalled revocations (max over series) and renewals
res["journalled_revocations_max"] = max((int(s[4]) for s in series if s[4].isdigit()), default=None)
res["renewals_max"] = max((int(s[7]) for s in series if s[7].isdigit()), default=None)
res["governor_active_first_t_rel_T0"] = next((s[0] - T0 for s in series if s[2] == "1"), None)
probe = [s for s in series if T0 + 60 < s[0] < dead and s[5] != ""]
res["probe_ok_fraction_in_outage"] = None if not probe else round(sum(1 for s in probe if s[5] == "1") / len(probe), 3)
res["probe_failures_max"] = max((int(s[6]) for s in series if s[6].isdigit()), default=None)
# dropped reasons: last cont snapshot with dropped before recovery
reasons = {}
try:
    snaps = [json.loads(l) for l in open("%s/%s.cont.jsonl" % (EV, name))]
    best = None
    for sn in snaps:
        if sn["t"] <= (Tr or sn["t"]) and sn.get("cont", {}).get("dropped"):
            best = sn
    if best:
        for sid, why in best["cont"]["dropped"].items():
            reasons[why] = reasons.get(why, 0) + 1
        res["dropped_reasons_last_snapshot"] = {"t_rel_T0": best["t"] - T0, "reasons": reasons}
        res["budget_used_limit_last_snapshot"] = {"used": len(best["cont"].get("budget_used_bytes", {})), "sample": list(best["cont"].get("budget_used_bytes", {}).items())[:3]}
except Exception as e:
    res["cont_snapshot_error"] = str(e)
# recovery
if Tr:
    after = [(s[0] - Tr, s[1]) for s in series if s[0] >= Tr]
    for target in (0.5, 0.9, 0.99):
        base = res.get("authset_at_T0") or 1700
        hit = next((a for a, n in after if n >= base * target), None)
        res["recovery_to_%d_percent_of_pre_s" % int(target * 100)] = hit
    res["authset_final"] = after[-1][1] if after else None
# bg traffic
f = "%s/%s.bg.out" % (EV, name)
if os.path.exists(f):
    txt = open(f).read().strip().splitlines()
    for l in reversed(txt):
        if l.startswith("{") and '"phase":"traffic"' in l.replace(" ", ""):
            j = json.loads(l)
            res["bg_traffic"] = {"results": j.get("results"), "n": j.get("n"), "p50_ms": j.get("p50_ms"), "p99_ms": j.get("p99_ms"), "bytes": j.get("bytes")}
            break
# record rewrite cadence
mt = []
for r in rows:
    if len(r) > 9 and r[9]:
        mt.append((int(r[0]), float(r[9])))
chg = [m for i, (t, m) in enumerate(mt) if i == 0 or m != mt[i - 1][1]]
if len(chg) > 3:
    gaps = [b - a for a, b in zip(chg, chg[1:])]
    res["record_rewrite_gap_s"] = {"n": len(chg), "min": round(min(gaps), 1), "p50": round(statistics.median(gaps), 1), "max": round(max(gaps), 1)}
# cpu from sampler
sf = "%s/%s.sampler.csv" % (EV, name)
if os.path.exists(sf):
    ls = [l.strip().split(",") for l in open(sf)][1:]
    pts = [(int(l[0]), int(l[1]), int(l[7]) if l[7].isdigit() else 0, int(l[2])) for l in ls if len(l) > 7 and l[0].isdigit() and l[1].isdigit()]
    def cpu(a, b):
        sel = [p for p in pts if a <= p[0] <= b]
        if len(sel) < 2:
            return None
        return round(100 * (sel[-1][1] - sel[0][1]) / 1e6 / (sel[-1][0] - sel[0][0]) / 2, 1)
    res["cgroup_cpu_percent_of_2vcpu"] = {"healthy_before_T0": cpu(T0 - 120, T0 - 5), "during_window": cpu(T0 + 30, dead - 30), "mem_cur_max_mb": round(max(p[3] for p in pts) / 1e6)}
    res["netd_rss_kb"] = {"min": min(p[2] for p in pts), "max": max(p[2] for p in pts)}
out["results"] = res
print(json.dumps(out, indent=1, default=str))
