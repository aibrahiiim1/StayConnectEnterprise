#!/usr/bin/env python3
"""Appliance-side actions for the continuity runs. Prints one JSON object per line with t=epoch seconds.
usage: cont_actions.py disconnect <ip>... | entend <ip>... | sessinfo <ip>... | entusage <ip>..."""
import json, subprocess, sys, time
sys.path.insert(0, "/root/oglab")
import labapi

PSQL = ["docker", "exec", "-i", "stayconnect-pg", "psql", "-X", "-At", "-F", "|", "-U", "stayconnect", "-d", "stayconnect_site", "-c"]


def q(sql):
    return subprocess.run(PSQL + [sql], capture_output=True, text=True, timeout=60).stdout.strip()


def live_session(ip):
    r = q("select id, entitlement_id, state, coalesce(ended::text,'') from iam_v2.sessions where host(ip)='%s' order by started desc limit 1" % ip)
    if not r:
        return None
    a = r.split("|")
    return {"session": a[0], "entitlement": a[1], "state": a[2], "ended": a[3]}


cmd, ips = sys.argv[1], sys.argv[2:]
for ip in ips:
    s = live_session(ip)
    if not s:
        print(json.dumps({"ip": ip, "error": "no session", "t": time.time()}))
        continue
    if cmd == "disconnect":
        t0 = time.time()
        st, out = labapi.call("POST", "/sessions/%s/disconnect" % s["session"], {"your_password": labapi.PW, "password": labapi.PW})
        print(json.dumps({"ip": ip, "action": "disconnect", "t": t0, "t_done": time.time(), "status": st, "body": str(out)[:120]}))
    elif cmd == "entend":
        t0 = time.time()
        r = q("select iam_v2.terminate_entitlement_at_boundary('%s'::uuid, now(), 'ADMIN')" % s["entitlement"])
        print(json.dumps({"ip": ip, "action": "entend", "t": t0, "t_done": time.time(), "result": r[:80]}))
    elif cmd == "sessinfo":
        r = q("select se.state, coalesce(se.end_reason,''), coalesce(se.ended::text,''), e.status, coalesce(e.consumed_data_bytes,0), coalesce(e.terminal_reason,'') "
              "from iam_v2.sessions se join iam_v2.entitlements e on e.id=se.entitlement_id where se.id='%s'" % s["session"])
        print(json.dumps({"ip": ip, "session": s["session"], "info": r, "t": time.time()}))
    elif cmd == "entusage":
        r = q("select e.status, coalesce(e.terminal_reason,''), coalesce(e.consumed_data_bytes,0), iam_v2.p3_entitlement_data_usage(e.id), "
              "(select coalesce(sum(bytes_up+bytes_down),0) from iam_v2.accounting_records ar where ar.session_id='%s') "
              "from iam_v2.entitlements e where e.id='%s'" % (s["session"], s["entitlement"]))
        print(json.dumps({"ip": ip, "session": s["session"], "ent": s["entitlement"], "info": r, "t": time.time()}))
