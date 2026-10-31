#!/usr/bin/env python3
"""Accounting reconciliation of a continuity run: usage python analyze_acct.py <name>"""
import json, sys, os
EV = "D:/tmp/fm/ev"
name = sys.argv[1]
clients = {c["idx"]: c["ip"] for c in json.load(open("D:/tmp/fm/lab/vch/clients.json"))}
b = json.load(open("%s/%s.snap-before.json" % (EV, name)))
a = json.load(open("%s/%s.snap-after.json" % (EV, name)))
gen = json.load(open("%s/%s.acct.json" % (EV, name)))
rows = []
for i in range(400, 420):
    ip = clients[i]
    ib, ia = b["acct"].get(ip, "").split("|"), a["acct"].get(ip, "").split("|")
    # info: status|reason|consumed|usage|acct_sum
    db_delta = int(ia[4]) - int(ib[4])
    cons_delta = int(ia[2]) - int(ib[2])
    use_delta = int(ia[3]) - int(ib[3])
    g = gen.get(str(i), {})
    rx_gen, tx_gen = g.get("ok_bytes", 0), g.get("up_bytes", 0)
    rb, ra = b["ifc"][str(i)], a["ifc"][str(i)]
    rx_if, tx_if = ra[0] - rb[0], ra[1] - rb[1]
    rows.append({"idx": i, "payload_down": rx_gen, "payload_up": tx_gen, "iface_rx": rx_if, "iface_tx": tx_if, "db_delta": db_delta,
                 "entitlement_consumed_delta": cons_delta, "p3_usage_delta": use_delta, "consumed_equals_p3_usage": ia[2] == ia[3],
                 "ratio_db_over_payload": round(db_delta / max(1, rx_gen + tx_gen), 4), "ratio_db_over_iface": round(db_delta / max(1, rx_if + tx_if), 4),
                 "status_after": ia[0]})
tot = {k: sum(r[k] for r in rows) for k in ("payload_down", "payload_up", "iface_rx", "iface_tx", "db_delta", "entitlement_consumed_delta", "p3_usage_delta")}
tot["ratio_db_over_payload"] = round(tot["db_delta"] / (tot["payload_down"] + tot["payload_up"]), 4)
tot["ratio_db_over_iface"] = round(tot["db_delta"] / (tot["iface_rx"] + tot["iface_tx"]), 4)
rr = [r["ratio_db_over_payload"] for r in rows]
ri = [r["ratio_db_over_iface"] for r in rows]
print(json.dumps({"clients": len(rows), "totals": tot, "per_client_ratio_db_over_payload": [min(rr), max(rr)], "per_client_ratio_db_over_iface": [min(ri), max(ri)],
                  "all_consumed_equals_p3_usage": all(r["consumed_equals_p3_usage"] for r in rows)}, indent=1))
json.dump(rows, open("%s/%s.acct-compare.json" % (EV, name), "w"), indent=1)
# Q200 clients
q = []
for i in range(1700, 1720):
    ip = clients[i]
    ib, ia = b["acct"].get(ip, "").split("|"), a["acct"].get(ip, "").split("|")
    q.append({"idx": i, "status_before": ib[0], "status_after": ia[0], "reason_after": ia[1], "consumed_after": int(ia[2]), "p3_usage_after": int(ia[3]), "acct_sum_after": int(ia[4])})
print(json.dumps({"q200_after": q[:3], "q200_status": sorted(set((x["status_after"], x["reason_after"]) for x in q)),
                  "q200_consumed_min_max": [min(x["consumed_after"] for x in q), max(x["consumed_after"] for x in q)]}, indent=1))
