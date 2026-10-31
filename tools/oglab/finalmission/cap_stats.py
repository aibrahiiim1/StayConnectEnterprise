import csv, json, statistics, sys
d = "D:/tmp/fm/ev/cap"
T0 = int(open(d + "/cap.T0").read())
rows = list(csv.DictReader(open(d + "/cap.sampler.csv")))
ts = [int(r["ts"]) for r in rows]
sel = [r for r in rows if T0 <= int(r["ts"]) <= T0 + 3600]
print("samples", len(sel))
cpu = [(int(b["cpu_usec"]) - int(a["cpu_usec"])) / 1e6 / (int(b["ts"]) - int(a["ts"])) / 2 * 100 for a, b in zip(sel, sel[1:])]
cpu.sort()
q = lambda v, p: v[int(p * (len(v) - 1))]
print("cpu %% of 2 vCPU mean %.1f p50 %.1f p95 %.1f p99 %.1f max %.1f; windows>=95%%: %d of %d; >=100%%: %d" % (statistics.mean(cpu), q(cpu, .5), q(cpu, .95), q(cpu, .99), cpu[-1], sum(1 for c in cpu if c >= 95), len(cpu), sum(1 for c in cpu if c >= 100)))
l1 = [float(r["load1"]) for r in sel]
print("load1 mean %.2f max %.2f" % (statistics.mean(l1), max(l1)))
for k in ("mem_cur", "mem_peak"):
    v = [int(r[k]) for r in sel]; print(k, "max MB", round(max(v) / 1e6), "mean", round(statistics.mean(v) / 1e6))
for k in ("rss_scd", "rss_edged", "rss_netd", "rss_portald", "rss_acctd", "rss_pg_all"):
    v = [int(r[k]) for r in sel if r[k].isdigit()]; print(k, "KB min", min(v), "max", max(v), "mean", round(statistics.mean(v)))
for k in ("pg_total", "pg_scd", "pg_acctd", "pg_edged", "pg_netd", "pg_idle_in_tx", "pg_waiting_lock", "pg_waiting_other", "locks_notgranted"):
    v = [int(r[k]) for r in sel if r[k].lstrip("-").isdigit()]; print(k, "max", max(v), "mean %.2f" % statistics.mean(v), "samples>0:", sum(1 for x in v if x > 0))
print("columns:", list(rows[0].keys())[28:])
for k in rows[0].keys():
    if k in ("sess_dup", "dup_devices", "dup"):
        v = [int(r[k]) for r in sel if r[k].isdigit()]; print(k, "max", max(v))
# authorisation set / online / licence
for k in list(rows[0].keys())[28:]:
    v = [int(r[k]) for r in sel if r[k].lstrip("-").isdigit()]
    if v: print(k, "min", min(v), "max", max(v), "mean %.0f" % statistics.mean(v))
