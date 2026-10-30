import sys, re
for i in (1, 2, 3):
    try: t = open("%s-pool-%d.txt" % (sys.argv[1], i)).read()
    except Exception: continue
    vals = {}
    for ln in t.split("\n"):
        m = re.match(r"scd_db_pool_(\w+)\{.*\} (\S+)", ln)
        if m: vals[m.group(1)] = float(m.group(2))
    print(i, {k: round(v, 1) for k, v in vals.items()})
