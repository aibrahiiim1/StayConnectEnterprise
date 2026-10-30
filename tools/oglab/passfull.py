import json, sys
rows = []
for ln in open(sys.argv[1], errors="replace"):
    i = ln.find("{")
    if "phase3: pass" not in ln: continue
    try: rows.append(json.loads(ln[i:]))
    except Exception: pass
n = int(sys.argv[2]) if len(sys.argv) > 2 else 8
for r in rows[-n:]:
    print({k: v for k, v in r.items() if k not in ("time", "msg", "level", "gen", "budget_at_lock_ms", "budget_left_ms", "strays", "legacy", "save", "gate_list", "setup", "pre_gate")})
