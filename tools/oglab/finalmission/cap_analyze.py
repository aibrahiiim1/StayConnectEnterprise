#!/usr/bin/env python3
"""Capacity run analysis: OFFERED rate (generator schedule) vs ENFORCED rate (appliance activation timestamps).
usage: cap_analyze.py <dir with cap.signin.log, cap.activations.csv, cap.sampler.csv>"""
import csv, json, math, sys, statistics, collections

d = sys.argv[1]
clients = {c["idx"]: c["ip"] for c in json.load(open("D:/tmp/fm/lab/vch/clients.json"))}
churn_ips = {clients[i] for i in range(2000, 2400)}
att = []
for l in open(d + "/cap.signin.log"):
    a = l.strip().split(",")
    if len(a) < 6:
        continue
    att.append({"end": int(a[0]), "idx": int(a[1]), "res": a[2], "lat": int(a[3]), "sched": int(a[4]), "start": int(a[5])})
att.sort(key=lambda x: x["sched"])
s0 = att[0]["sched"]
out = {"attempts": len(att)}
# OFFERED per minute from the generator's own schedule
off = collections.Counter((x["sched"] - s0) // 60000 for x in att)
full = [off[m] for m in range(0, max(off) ) ]   # drop the last, partial minute
out["offered"] = {"total": len(att), "span_s": round((att[-1]["sched"] - s0) / 1000, 1), "mean_per_min": round(len(att) / ((att[-1]["sched"] - s0) / 60000), 2),
                  "per_full_minute_min": min(full), "median": statistics.median(full), "max": max(full), "minutes": len(full)}
# dispatch lag (actual start - scheduled)
lag = sorted(x["start"] - x["sched"] for x in att)
out["dispatch_lag_ms"] = {"p50": lag[len(lag) // 2], "p99": lag[int(len(lag) * .99)], "max": lag[-1]}
# client-observed result
res = collections.Counter(x["res"] for x in att)
out["results"] = dict(res)
ok = sorted(x["lat"] for x in att if x["res"] == "ok")
pc = lambda q: ok[min(len(ok) - 1, int(q * (len(ok) - 1)))] / 1000.0
out["latency_s_ok"] = {"n": len(ok), "p50": pc(.5), "p95": pc(.95), "p99": pc(.99), "max": ok[-1] / 1000.0, "inside_8s": sum(1 for v in ok if v <= 8000)}
# ENFORCED per minute from the appliance activation timestamps (churn clients only), on the same minute grid
act = []
for r in csv.reader(open(d + "/cap.activations.csv")):
    if len(r) < 3:
        continue
    try:
        t = float(r[0])
    except ValueError:
        continue
    if r[2] in churn_ips:
        act.append(t)
act.sort()
enf = collections.Counter(int((t * 1000 - s0) // 60000) for t in act if t * 1000 >= s0)
fullm = [enf[m] for m in range(0, max(off))]
out["enforced"] = {"total_in_window": sum(enf[m] for m in range(0, max(off) + 3)), "per_full_minute_min": min(fullm), "median": statistics.median(fullm), "max": max(fullm),
                   "per_10_minutes": [sum(enf[m] for m in range(i, min(i + 10, max(off)))) for i in range(0, max(off), 10)]}
out["offered_per_10_minutes"] = [sum(off[m] for m in range(i, min(i + 10, max(off)))) for i in range(0, max(off), 10)]
# offered -> enforced matching: an attempt is enforced if the client got /success AND an activation row exists for its address after the offer
print(json.dumps(out, indent=1))
