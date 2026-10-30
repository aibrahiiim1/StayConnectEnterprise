import sys, csv
rows = list(csv.DictReader(open(sys.argv[1])))
t0 = int(sys.argv[2]) if len(sys.argv) > 2 else 0
prev = None
out = []
for r in rows:
    try:
        ts = int(r["ts"]); cpu = int(r["cpu_usec"])
    except Exception:
        continue
    if ts < t0: prev = (ts, cpu); continue
    if prev and ts > prev[0]:
        out.append((ts, (cpu - prev[1]) / 1e6 / (ts - prev[0]) / 2 * 100, r))
    prev = (ts, cpu)
step = int(sys.argv[3]) if len(sys.argv) > 3 else 6
for i in range(0, len(out), step):
    chunk = out[i:i + step]
    ts = chunk[-1][0]; cpu = sum(c[1] for c in chunk) / len(chunk); r = chunk[-1][2]
    print("t+%4ds cpu%%=%5.1f load=%s mem=%dMB scd/acctd/netd rss=%d/%d/%d MB conns=%s(scd %s acct %s) idletx=%s waitlock=%s act=%s pend=%s ended=%s dup=%s auth=%s" % (
        ts - out[0][0], cpu, r["load1"], int(r["mem_cur"] or 0) // 1048576, int(r["rss_scd"]) // 1024, int(r["rss_acctd"]) // 1024, int(r["rss_netd"]) // 1024,
        r["pg_total"], r["pg_scd"], r["pg_acctd"], r["pg_idle_in_tx"], r["pg_waiting_lock"], r["sess_active"], r["sess_pending"], r["sess_ended"], r["dup_live_dev"], r["auth_set"]))
