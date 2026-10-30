import sys, json, collections
# usage: routes.py <journal-json-lines file>   (reads "http request" records: route, ms, status)
d = collections.defaultdict(list)
st = collections.defaultdict(collections.Counter)
for ln in open(sys.argv[1], errors="replace"):
    i = ln.find("{")
    if i < 0: continue
    try: r = json.loads(ln[i:])
    except Exception: continue
    if r.get("msg") != "http request": continue
    k = r.get("method", "") + " " + r.get("route", "")
    d[k].append(r.get("ms", 0)); st[k][r.get("status")] += 1
def pc(a, q): a = sorted(a); return a[int(q * (len(a) - 1))]
print("%-62s %6s %7s %7s %7s %7s  %s" % ("route", "n", "p50", "p95", "p99", "max", "status"))
for k, a in sorted(d.items(), key=lambda kv: -sum(kv[1])):
    if len(a) < 5: continue
    print("%-62s %6d %7d %7d %7d %7d  %s" % (k[:62], len(a), pc(a, .5), pc(a, .95), pc(a, .99), max(a), dict(st[k])))
