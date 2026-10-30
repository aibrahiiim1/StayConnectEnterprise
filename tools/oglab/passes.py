import json, sys
rows = []
for ln in open(sys.argv[1], errors="replace"):
    i = ln.find("{")
    if i < 0 or "phase3: pass" not in ln: continue
    try: rows.append(json.loads(ln[i:]))
    except Exception: pass
print("passes", len(rows))
keys = ("time", "sessions", "lock_wait_ms", "pass_ms", "n_admit", "admit", "renew", "precheck", "individual", "db_activate", "db_alloc", "db_origin", "failed")
n = int(sys.argv[2]) if len(sys.argv) > 2 else 15
for r in rows[-n:]:
    print({k: (r[k][11:19] if k == "time" else r[k]) for k in keys if k in r})
pm = sorted(r.get("pass_ms", 0) for r in rows)
if pm:
    print("pass_ms p50 %d p95 %d max %d" % (pm[len(pm) // 2], pm[int(len(pm) * .95)], pm[-1]))
