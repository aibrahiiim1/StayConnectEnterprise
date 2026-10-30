import sys, re, collections
a = collections.defaultdict(list)
for ln in open(sys.argv[1]):
    f = ln.rstrip("\n").split("\t")
    if len(f) < 8 or f[2] != "idle in transaction": continue
    q = re.sub(r"\$\d+", "$N", f[7])[:60]
    a[(f[1], q)].append(float(f[5]))
for k, v in sorted(a.items(), key=lambda kv: -len(kv[1]))[:14]:
    v.sort()
    print("%4d samples  age p50 %.2f p90 %.2f max %.2f  %s %s" % (len(v), v[len(v)//2], v[int(len(v)*.9)], v[-1], *k))
