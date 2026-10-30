import sys, re, collections
c = collections.Counter(); b = collections.Counter(); n = 0
def norm(q):
    q = re.sub(r"[0-9a-f]{8}-[0-9a-f-]{27}", "UUID", q); return re.sub(r"\$\d+", "$N", q)
for ln in open(sys.argv[1]):
    f = ln.rstrip("\n").split("\t")
    if len(f) < 8: continue
    t, role, state, wt, we, age, blk, q = f[:8]
    c[(role, state, wt + ":" + we, norm(q)[:80])] += 1
    n += 1
    if blk != "-": b[(role, norm(q)[:60], "<= BLOCKED BY", norm(blk)[:100])] += 1
print("rows", n)
for k, v in c.most_common(int(sys.argv[2]) if len(sys.argv) > 2 else 30):
    print("%5d  %s | %s | %s | %s" % (v, *k))
print("--- blocked pairs")
for k, v in b.most_common(15):
    print("%5d  %s | %s %s %s" % (v, *k))
