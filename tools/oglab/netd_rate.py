import sys, json, datetime, re, collections
ts, kinds = [], []
for l in sys.stdin:
    try:
        d = json.loads(l)
    except Exception:
        continue
    s = re.sub(r"(\.\d{6})\d*", r"\1", d["time"].replace("Z", "+00:00"))
    ts.append(datetime.datetime.fromisoformat(s).timestamp())
    kinds.append(d["msg"][:44])
print(len(ts), "events in", round(ts[-1] - ts[0], 1), "s")
print(collections.Counter(kinds).most_common(5))
gaps = sorted(round(b - a, 3) for a, b in zip(ts, ts[1:]))
print("median gap s:", gaps[len(gaps) // 2], "p95:", gaps[int(len(gaps) * 0.95)], "max:", gaps[-1])
