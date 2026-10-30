import csv, json, sys, statistics as st

mon = sys.argv[1]
log = sys.argv[2]
CPUS = 2.0

rows = []
with open(mon) as f:
    r = csv.DictReader(f)
    for x in r:
        try:
            rows.append({k: (float(v) if v not in ("", None) else 0.0) for k, v in x.items()})
        except ValueError:
            pass
for i in range(1, len(rows)):
    dt = rows[i]["ts"] - rows[i - 1]["ts"]
    rows[i]["cpu_pct"] = 100.0 * (rows[i]["cpu_usec"] - rows[i - 1]["cpu_usec"]) / (dt * 1e6 * CPUS) if dt > 0 else 0
rows[0]["cpu_pct"] = 0

ev = []
for ln in open(log):
    ln = ln.strip()
    if ln.startswith("{"):
        try:
            d = json.loads(ln)
        except Exception:
            continue
        if "event" in d:
            ev.append(d)
marks = {e["event"]: e["t"] for e in ev}
phases = []
for k in list(marks):
    if k.startswith("start:"):
        name = k[6:]
        if "end:" + name in marks:
            phases.append((name, marks[k], marks["end:" + name]))
phases.insert(0, ("idle-before", rows[0]["ts"], marks.get("scenario-start", rows[0]["ts"])))
if "scenario-end" in marks:
    phases.append(("cooldown", marks["scenario-end"], rows[-1]["ts"]))

MB = 1048576.0
print("| phase | secs | CPU avg/p95/max % (of 2 vCPU) | mem cur max MB | mem peak MB | load1 max | pg conns max (scd/acctd/edged/netd) | active sessions max | raw rows (+) | ct max | tc classes max |")
print("|---|---|---|---|---|---|---|---|---|---|---|")
for name, a, b in phases:
    s = [r for r in rows if a <= r["ts"] <= b and "cpu_pct" in r]
    if not s:
        continue
    cp = sorted(r["cpu_pct"] for r in s)
    p95 = cp[int(0.95 * (len(cp) - 1))]
    print("| %s | %d | %.0f / %.0f / %.0f | %.0f | %.0f | %.1f | %d (%d/%d/%d/%d) | %d | +%d | %d | %d |" % (
        name, b - a, st.mean(cp), p95, max(cp), max(r["mem_cur"] for r in s) / MB, max(r["mem_peak"] for r in s) / MB,
        max(r["load1"] for r in s), max(r["pg_conns"] for r in s), max(r["pg_conns_scd"] for r in s), max(r["pg_conns_acctd"] for r in s),
        max(r["pg_conns_edged"] for r in s), max(r["pg_conns_netd"] for r in s), max(r["sess_active"] for r in s),
        s[-1]["acct_raw"] - s[0]["acct_raw"], max(r["ct_count"] for r in s), max(r["tc_classes"] for r in s)))
print()
print("RSS max per process over the whole run (MB):", {k[4:]: round(max(r[k] for r in rows) / 1024.0) for k in rows[0] if k.startswith("rss_")})
