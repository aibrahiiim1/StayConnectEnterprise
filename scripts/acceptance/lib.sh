#!/bin/bash
# SHARED HELPERS FOR THE PRE-LIVE REMEDIATION ACCEPTANCE HARNESSES (run as root on the appliance).
#
# The operator is the acceptance site_admin (ACC_EMAIL); its password is read from $ACC_PW_FILE (0600, never
# printed). Guest data is never printed: stays and accounts are referred to by id prefixes and counts only.
# Every harness that sources this file registers what it creates in the cleanup arrays and restores what it
# changes; the EXIT trap runs lib_cleanup and then the harness's own restore hook (RESTORE_HOOK).
set -uo pipefail
PASS=${PASS:-0}; FAIL=${FAIL:-0}; BLOCK=${BLOCK:-0}
ok(){ echo "  PASS  $1"; PASS=$((PASS+1)); }
bad(){ echo "  FAIL  $1"; FAIL=$((FAIL+1)); }
blk(){ echo "  BLOCK $1"; BLOCK=$((BLOCK+1)); }
info(){ echo "  info  $1"; }
PSQL="docker exec stayconnect-pg psql -U stayconnect -d stayconnect_site -tAc"
R="--resolve hotel.stayconnect.local:443:127.0.0.1"; B=https://hotel.stayconnect.local/api/edge/v1
ACC_EMAIL=${ACC_EMAIL:-acceptance-audit@onegate.invalid}
ACC_PW_FILE=${ACC_PW_FILE:-/root/acceptance/.pw}
ACC_PW=$(cat "$ACC_PW_FILE")
CK=/tmp/acc.ck.$$
# admin [curl args...]  -- the acceptance operator's session; same-origin header on every call.
admin(){ curl -skL $R -b $CK -c $CK -H 'Content-Type: application/json' -H 'Origin: https://hotel.stayconnect.local' "$@"; }
# code METHOD PATH [BODY] -> prints "HTTP body" ; body saved to /tmp/acc.last
code(){ local m=$1 p=$2 d=${3:-}; if [ -n "$d" ]; then admin -o /tmp/acc.last -w '%{http_code}' -X "$m" -d "$d" "$B$p"; else admin -o /tmp/acc.last -w '%{http_code}' -X "$m" "$B$p"; fi; }
last(){ head -c "${1:-220}" /tmp/acc.last | tr '\n' ' '; }
jget(){ python3 -c "import json,sys
d=json.load(sys.stdin)
for k in sys.argv[1].split('.'):
  d = d[int(k)] if isinstance(d,list) else (d.get(k) if isinstance(d,dict) else None)
  if d is None: break
print('' if d is None else (json.dumps(d) if isinstance(d,(dict,list)) else d))" "$1" 2>/dev/null; }
login_as(){ # login_as EMAIL PASSWORD COOKIEFILE -> http code
  curl -sk $R -c "$3" -o /tmp/acc.login -w '%{http_code}' -H 'Content-Type: application/json' -d "{\"email\":\"$1\",\"password\":\"$2\"}" $B/auth/login; }
c=$(login_as "$ACC_EMAIL" "$ACC_PW" $CK); [ "$c" = 200 ] || { echo "acceptance operator login failed: $c $(head -c 200 /tmp/acc.login)"; exit 2; }
STEP="\"password\":$(python3 -c 'import json,sys;print(json.dumps(sys.argv[1]))' "$ACC_PW")"   # json fragment for step-up

read -r HTTPS_ACTIVE HOST <<<"$($PSQL "SELECT https_active, coalesce(canonical_hostname,'-') FROM portal_tls_state" | tr '|' ' ')"; P="https://$HOST"
GB=$($PSQL "SELECT bridge_name FROM guest_networks WHERE enabled ORDER BY created_at LIMIT 1"); GW=$($PSQL "SELECT host(gateway_ip) FROM guest_networks WHERE enabled ORDER BY created_at LIMIT 1")
PROBE=http://connectivitycheck.gstatic.com/generate_204
R4=${R4:-$(printf '%04x' $((RANDOM % 65536)))}
FIN0=$($PSQL "SELECT (SELECT count(*) FROM iam_v2.pms_postings)||'/'||(SELECT count(*) FROM iam_v2.posting_outbox)")
NS=(); PKGS=(); ACCS=(); OPS=()
resolver_guard(){ systemctl reset-failed systemd-resolved >/dev/null 2>&1; systemctl is-active -q systemd-resolved || systemctl start systemd-resolved >/dev/null 2>&1; }
end_account_access(){ # terminate live entitlements of an account and revoke its sessions
  local a=$1 e s
  for s in $($PSQL "SELECT host(se.ip) FROM iam_v2.sessions se JOIN iam_v2.entitlements e ON e.id=se.entitlement_id WHERE e.guest_account_id='$a' AND se.ended IS NULL"); do
    curl -s --unix-socket /run/stayconnect/scd.sock -X POST -H 'Content-Type: application/json' -d "{\"ip\":\"$s\",\"reason\":\"admin\"}" http://x/v1/sessions/revoke >/dev/null; done
  for e in $($PSQL "SELECT id FROM iam_v2.entitlements WHERE guest_account_id='$a' AND status IN ('PENDING','ACTIVE','SUSPENDED')"); do
    $PSQL "SELECT iam_v2.apply_entitlement_transition('$e','TERMINATED',now(),'ADMIN')" >/dev/null 2>&1; done; }
lib_cleanup(){
  local a p o n
  for a in "${ACCS[@]:-}"; do [ -n "$a" ] || continue; end_account_access "$a"
    admin -o /dev/null -X DELETE -d "{$STEP}" "$B/guest-accounts/$a"; done
  for p in "${PKGS[@]:-}"; do [ -n "$p" ] || continue
    admin -o /dev/null -d "{\"active\":false,\"reason\":\"acceptance fixture retired\",$STEP}" "$B/commercial-packages/$p/active"; done
  for o in "${OPS[@]:-}"; do [ -n "$o" ] || continue; admin -o /dev/null -X DELETE "$B/operators/$o"; done
  for n in "${NS[@]:-}"; do [ -n "$n" ] || continue; rmclient "$n"; done
  resolver_guard
  [ "$($PSQL "SELECT (SELECT count(*) FROM iam_v2.pms_postings)||'/'||(SELECT count(*) FROM iam_v2.posting_outbox)")" = "$FIN0" ] && info "PMS postings/outbox unchanged ($FIN0)" || echo "  FAIL  PMS posting counts changed during the run"
  rm -f $CK /tmp/acc.last /tmp/acc.login; }
on_exit(){ [ -n "${RESTORE_HOOK:-}" ] && $RESTORE_HOOK; lib_cleanup; echo; echo "RESULT PASS=$PASS FAIL=$FAIL BLOCKED=$BLOCK"; }
trap on_exit EXIT

# ---- guest-network clients (network namespaces on the guest bridge) ----
client(){ local n=$1; NS+=("$n"); ip link add ${n}a type veth peer name ${n}c; ip link set ${n}a master "$GB"; ip link set ${n}a up
  ip netns add $n; ip link set ${n}c netns $n; ip netns exec $n ip link set lo up; ip netns exec $n ip link set ${n}c up
  mkdir -p /etc/netns/$n; echo "nameserver $GW" > /etc/netns/$n/resolv.conf
  sleep 1; ip netns exec $n timeout 25 dhclient -1 -lf /var/lib/dhcp/dhclient-$n.leases -pf /run/dhclient-$n.pid ${n}c >/dev/null 2>&1
  ip netns exec $n ping -c1 -W2 "$GW" >/dev/null 2>&1; resolver_guard; }
rmclient(){ local n=$1; ip netns exec $n dhclient -r -lf /var/lib/dhcp/dhclient-$n.leases -pf /run/dhclient-$n.pid ${n}c >/dev/null 2>&1
  ip netns pids $n 2>/dev/null | xargs -r kill 2>/dev/null
  ip netns del $n 2>/dev/null; ip link del ${n}a 2>/dev/null; rm -rf /etc/netns/$n /var/lib/dhcp/dhclient-$n.leases /tmp/$n.*; }
nsip(){ ip netns exec $1 ip -4 -o addr show ${1}c | awk '{print $4}' | cut -d/ -f1; }
nsmac(){ ip netns exec $1 cat /sys/class/net/${1}c/address; }
online(){ ip netns exec $1 curl -s -o /dev/null -w '%{http_code}' -m 6 "$PROBE"; }
waiton(){ for _ in $(seq 1 ${2:-10}); do [ "$(online $1)" = 204 ] && return 0; sleep 2; done; return 1; }
waitoff(){ local n=$1 tries=${2:-20}; for _ in $(seq 1 $tries); do [ "$(online $n)" != 204 ] && return 0; sleep 3; done; return 1; }
post(){ local n=$1 path=$2; shift 2; ip netns exec $n curl -s -o /tmp/$n.html -c /tmp/$n.jar -b /tmp/$n.jar -w '%{http_code} %{redirect_url}' -X POST "$@" "$P$path"; }
gget(){ ip netns exec $1 curl -s -c /tmp/$1.jar -b /tmp/$1.jar "$P$2"; }
pagetext(){ python3 -c 'import re,sys;t=open(sys.argv[1],encoding="utf-8",errors="replace").read();t=re.sub(r"<script.*?</script>|<style.*?</style>","",t,flags=re.S);print(re.sub(r"\s+"," ",re.sub(r"<[^>]+>"," ",t)).strip()[:int(sys.argv[2])])' "$1" "${2:-200}"; }

# ---- fixtures ----
mkplan(){ admin -d "$1" $B/commercial-packages/plans | python3 -c 'import json,sys;d=json.load(sys.stdin);print(d["current_revision_id"] if d.get("current_revision_id") else "ERR "+str(d.get("error",""))+" "+str(d.get("message","")))'; }
mkpkg(){ local out; out=$(admin -d "$1" $B/commercial-packages | python3 -c 'import json,sys;d=json.load(sys.stdin);print(d["package_id"] if d.get("package_id") else "ERR "+str(d.get("error",""))+" "+str(d.get("message","")))'); case "$out" in ERR*) ;; *) PKGS+=("$out");; esac; echo "$out"; }
mkacc(){ local id; id=$(admin -d "{\"username\":\"$1\",\"password\":\"Acc-pw-$R4\"}" $B/guest-accounts | python3 -c 'import json,sys;print(json.load(sys.stdin)["account"]["id"])'); echo "$id"; }
signin(){ post $1 /auth/credentials --data-urlencode "username=$2" --data-urlencode "password=Acc-pw-$R4"; }
acquire(){ post $1 /packages/acquire --data-urlencode "package_id=$2" "${@:3}"; }
ENT(){ $PSQL "SELECT e.id||'|'||e.status||'|'||coalesce(e.terminal_reason,'-')||'|'||e.consumed_data_bytes||'|'||e.consumed_online_seconds FROM iam_v2.entitlements e WHERE e.guest_account_id='$1' ORDER BY e.activated_at DESC NULLS FIRST LIMIT 1"; }
