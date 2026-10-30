import sys, re
v = []
for ln in open(sys.argv[1]):
    m = re.search(r"Time: ([0-9.]+) ms", ln)
    if m: v.append(float(m.group(1)))
v.sort()
if v:
    print("probe n=%d p50 %.1f p90 %.1f p99 %.1f max %.1f ms" % (len(v), v[len(v)//2], v[int(len(v)*.9)], v[int(len(v)*.99)], v[-1]))
