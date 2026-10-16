#!/bin/bash
# 7 + 9. PMS SETTINGS GATES AND NETWORK / WALLED GARDEN (audit D-D, roster, N-26, garden width). Run as root.
# Nothing real is decided or changed: refusals are proven with requests that cannot succeed (no password, a
# random message id); the one walled-garden rule added is removed again.
. "$(dirname "$0")/lib.sh"
WG=""; garden_cleanup(){ [ -n "$WG" ] && admin -o /dev/null -X DELETE "$B/walled-garden/$WG"; }; RESTORE_HOOK=garden_cleanup
echo "== 7a. roster settings need the password"
admin -o /tmp/acc.rs "$B/pms-roster-reconciliation/"; RS=$(python3 -c 'import json;d=json.load(open("/tmp/acc.rs"));print(json.dumps(d.get("settings",d)))' 2>/dev/null)
BODY=$(python3 - "$RS" <<'PY'
import json,sys
s=json.loads(sys.argv[1]); b={k:s.get(k) for k in ("roster_trust_min","inventory_tolerance","inventory_lookback","max_close_per_run")}
b["expected_config_version"]=s.get("config_version"); b["reason"]="acceptance: is the password required"
print(json.dumps(b))
PY
)
V0=$($PSQL "SELECT count(*) FROM audit_log WHERE action ILIKE '%roster%settings%'")
c=$(code PUT /pms-roster-reconciliation/settings "$BODY"); [ "$c" = 401 ] && ok "roster settings without a password -> 401 $(jget error < /tmp/acc.last)" || bad "roster settings without password -> $c $(last)"
admin -o /tmp/acc.rs2 "$B/pms-roster-reconciliation/"; [ "$(python3 -c 'import json;print(json.dumps(json.load(open("/tmp/acc.rs")).get("settings")))')" = "$(python3 -c 'import json;print(json.dumps(json.load(open("/tmp/acc.rs2")).get("settings")))')" ] && ok "roster settings unchanged" || bad "roster settings changed"
echo "== 7b. PMS review decision actions exist (no real message is decided)"
X=$(cat /proc/sys/kernel/random/uuid)
c=$(code POST "/pms-event-decisions/$X/acknowledge" '{"reason":"acceptance: route check"}'); [ "$c" = 401 ] && ok "acknowledge without a password -> 401" || info "acknowledge without password -> $c $(last 120)"
c=$(code POST "/pms-event-decisions/$X/acknowledge" "{\"reason\":\"acceptance: route check\",$STEP}"); [ "$c" = 404 ] && ok "acknowledge of a random id with password -> 404 $(jget error < /tmp/acc.last) (route + gates live)" || bad "acknowledge random -> $c $(last)"
c=$(code POST "/pms-event-decisions/$X/link" "{\"reason\":\"acceptance: route check\",$STEP}"); [ "$c" = 400 ] || [ "$c" = 404 ] && ok "link without a stay -> $c $(jget error < /tmp/acc.last)" || bad "link -> $c $(last)"
c=$(code GET "/pms-event-decisions/settings"); [ "$c" = 200 ] && ok "auto-expire setting readable: $(python3 -c 'import json;d=json.load(open("/tmp/acc.last"));d=d.get("settings",d);print({k:d.get(k) for k in ("enabled","expire_after_days")})')" || info "decision settings -> $c"

echo "== 9a. walled garden width"
POL=$(admin "$B/walled-garden/policy"); info "policy: $(echo "$POL" | python3 -c 'import json,sys;d=json.load(sys.stdin);d=d.get("policy",d.get("settings",d));print({k:d.get(k) for k in ("widest_prefix_len",)})')"
N0=$(admin $B/walled-garden | python3 -c 'import json,sys;d=json.load(sys.stdin);r=d.get("rules",d) if isinstance(d,dict) else d;print(len(r))')
c=$(code POST /walled-garden '{"kind":"cidr","value":"0.0.0.0/0","ports":[443],"description":"acceptance"}'); [ "$c" = 400 ] && ok "0.0.0.0/0 refused: 400 '$(jget message < /tmp/acc.last | cut -c1-90)'" || { bad "0.0.0.0/0 -> $c $(last)"; WG=$(jget id < /tmp/acc.last); }
c=$(code POST /walled-garden '{"kind":"cidr","value":"8.0.0.0/8","ports":[443],"description":"acceptance"}'); [ "$c" = 400 ] && ok "8.0.0.0/8 (wider than /24) refused: '$(jget message < /tmp/acc.last | cut -c1-90)'" || { bad "8.0.0.0/8 -> $c $(last)"; WG=$(jget id < /tmp/acc.last); garden_cleanup; WG=""; }
c=$(code POST /walled-garden '{"kind":"cidr","value":"93.184.0.0/16","ports":[443],"description":"acceptance"}'); [ "$c" = 400 ] && ok "a /16 is refused too (site allows /24 at most)" || { bad "/16 -> $c"; WG=$(jget id < /tmp/acc.last); garden_cleanup; WG=""; }
[ "$(admin $B/walled-garden | python3 -c 'import json,sys;d=json.load(sys.stdin);r=d.get("rules",d) if isinstance(d,dict) else d;print(len(r))')" = "$N0" ] && ok "no rule was added by the refusals ($N0 rules)" || bad "rule count changed"
echo "== 9b. a domain rule still reaches nft"
admin $B/walled-garden | grep -q '"example.com"' && blk "example.com already a rule; skipping" || {
WG=$(admin -d '{"kind":"domain","value":"example.com","ports":[443],"description":"acceptance"}' $B/walled-garden | python3 -c 'import json,sys;d=json.load(sys.stdin);print(d.get("id") or d.get("rule",{}).get("id") or "")')
[ -n "$WG" ] && ok "domain rule example.com:443 added (${WG:0:8})" || bad "domain rule not added"
T0=$(date +%s); EXS=$(dig +short example.com @"$GW" | grep -E '^[0-9.]+$'); hit=""
inset(){ local ip; for ip in $EXS; do nft list set inet stayconnect walled_garden_tcp 2>/dev/null | grep -q "$ip . 443" && { EX=$ip; return 0; }; done; return 1; }
for _ in $(seq 1 30); do inset && { hit=$(( $(date +%s)-T0 )); break; }; sleep 2; done
[ -n "$hit" ] && ok "resolved address $EX . 443 is in nft walled_garden_tcp ${hit}s after the add" || bad "rule not in nft after 60 s (A: $(echo $EXS))"
admin -o /dev/null -X DELETE "$B/walled-garden/$WG"; WG=""; gone=""
for _ in $(seq 1 30); do inset || { gone=1; break; }; sleep 2; done
[ -n "$gone" ] && ok "removed from nft after delete" || bad "still in nft after delete"; }

echo "== 9c. netd caller gate"
S=/run/stayconnect/netd.sock
r=$(curl -s -o /dev/null -w '%{http_code}' --unix-socket $S http://unix/v1/health); [ "$r" = 200 ] && ok "root -> /v1/health 200" || info "root -> $r"
r=$(runuser -u stayconnect -- curl -s -o /dev/null -w '%{http_code}' --unix-socket $S http://unix/v1/health); [ "$r" = 200 ] && ok "edged's identity (stayconnect) -> 200" || info "stayconnect -> $r"
r=$(runuser -u stayconnect-portald -- curl -s -o /dev/null -w '%{http_code}' --unix-socket $S http://unix/v1/health 2>&1); [ "$r" = 000 ] && ok "portald's identity cannot even open netd's socket (N-26: not in the stayconnect group)" || bad "portald identity -> $r"
r=$(setpriv --reuid=stayconnect-portald --regid=stayconnect-portald --groups=stayconnect curl -s -o /tmp/acc.netd -w '%{http_code}' --unix-socket $S http://unix/v1/health)
[ "$r" = 403 ] && ok "portald's uid with the socket group forced -> 403 '$(head -c 80 /tmp/acc.netd)' (caller gate)" || bad "portald uid + group -> $r $(head -c 120 /tmp/acc.netd)"
r=$(runuser -u stayconnect -- curl -s -o /dev/null -w '%{http_code}' -X POST --unix-socket $S -d '{"ip":"192.0.2.1"}' http://unix/v1/flows/end 2>/dev/null); info "edged identity on the flow-teardown route -> $r (NETD_FLOW_CALLERS=root)"
journalctl -u stayconnect-netd --since "-2min" --no-pager | grep -c "refused a caller" | sed 's/^/  info  netd refusal log lines in the last 2 min: /'
