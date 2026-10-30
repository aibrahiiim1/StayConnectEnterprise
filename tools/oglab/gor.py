import sys, re, collections
lines = open(sys.argv[1], errors="replace").read().split("\n")
blocks = []; cur = None
for l in lines:
    if l.startswith("goroutine "):
        cur = [l]; blocks.append(cur)
    elif cur is not None and not l.startswith("{"):
        cur.append(l)
c = collections.Counter()
for b in blocks:
    hdr = re.sub(r"goroutine \d+ gp=\S+ m=\S+ ", "", b[0])
    hdr = re.sub(r"\d+ minutes", "N minutes", hdr)
    frames = [l.strip() for l in b[1:] if l and not l.startswith("\t")]
    app = [f for f in frames if "stayconnect" in f or "jackc" in f or "main." in f]
    sel = app[:int(sys.argv[3]) if len(sys.argv) > 3 else 6] if app else frames[:4]
    def nm(f):
        f = f.replace("github.com/stayconnect/enterprise/data-plane/", "")
        if f.endswith(")") and f.rfind("(") > 0: f = f[:f.rfind("(")]
        return f[:70]
    key = hdr + " || " + " <- ".join(nm(f) for f in sel)
    c[key] += 1
for k, v in c.most_common(int(sys.argv[2]) if len(sys.argv) > 2 else 25):
    print(v, k)
