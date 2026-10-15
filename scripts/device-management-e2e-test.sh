#!/usr/bin/env bash
# DEVICE LIMITS, REPLACEMENT AND STAFF DEVICE ACTIONS, END TO END, FROM GUEST DEVICES (run as root on the appliance).
# Contract: docs/architecture/ONEGATE_DEVICE_MANAGEMENT.md (migration 0118).
#
# Network-namespace devices on the guest bridge, over the HTTPS portal, with real enforcement (a device is "online"
# only when the captive probe answers 204 through nft):
#   A. a 1-device package (the site's free package, through a staff-created Personal account): device 1 online;
#      device 2 signs in -> the replacement page -> replaces device 1 -> device 2 online, device 1 offline; the
#      package (window, data, owner) unchanged; sign-out reaches the sign-in page again; the daily change limit;
#      the online page's device list.
#   B. a multi-device package (the site's OneDay package, through one voucher): devices 1..N join the same
#      entitlement; device N+1 is held at the limit and replaces device 1.
#   C. staff: find the device by account name, prepare it for a new sign-in test -> disconnected, its places freed,
#      its next portal visit is the sign-in page; the package and the device's history unchanged.
# What the test creates (one Personal account, one voucher, namespaces) it removes or ends afterwards.
#
#   ADMIN_USER / ADMIN_PASS  the operator the test signs in as (default admin/admin, the PRE-LIVE default)
set -uo pipefail
PASS=0; FAIL=0
ok(){ echo "  ok   $1"; PASS=$((PASS+1)); }
bad(){ echo "  FAIL $1"; FAIL=$((FAIL+1)); }
PSQL="docker exec stayconnect-pg psql -U stayconnect -d stayconnect_site -tAc"
PROBE=http://connectivitycheck.gstatic.com/generate_204
R4=$(printf '%04x' $((RANDOM % 65536))); NS=()
read -r ACTIVE HOST <<<"$($PSQL "SELECT https_active, coalesce(canonical_hostname,'-') FROM portal_tls_state" | tr '|' ' ')"
[ "$ACTIVE" = t ] || { echo "HTTPS is not active"; exit 2; }
P="https://$HOST"; GB=$($PSQL "SELECT bridge_name FROM guest_networks WHERE enabled ORDER BY created_at LIMIT 1")
GW=$($PSQL "SELECT host(gateway_ip) FROM guest_networks WHERE enabled ORDER BY created_at LIMIT 1")
FREE=$($PSQL "SELECT p.id FROM iam_v2.internet_packages p JOIN iam_v2.internet_package_revisions r ON r.id=p.current_revision_id
  JOIN iam_v2.service_plan_revisions s ON s.id=r.service_plan_revision_id
  WHERE p.active AND NOT p.is_system AND r.price_minor=0 AND 'NOT_REQUIRED' = ANY(r.settlement_methods) AND s.max_concurrent_devices=1 LIMIT 1")
MULTI_REV=$($PSQL "SELECT r.id FROM iam_v2.internet_packages p JOIN iam_v2.internet_package_revisions r ON r.id=p.current_revision_id
  JOIN iam_v2.service_plan_revisions s ON s.id=r.service_plan_revision_id
  WHERE p.active AND NOT p.is_system AND 'PREPAID' = ANY(r.settlement_methods) AND s.max_concurrent_devices BETWEEN 2 AND 5 LIMIT 1")
MULTI_N=$($PSQL "SELECT s.max_concurrent_devices FROM iam_v2.internet_package_revisions r JOIN iam_v2.service_plan_revisions s ON s.id=r.service_plan_revision_id WHERE r.id='$MULTI_REV'")
[ -n "$FREE" ] && [ -n "$MULTI_REV" ] || { echo "needs a free 1-device package and a voucher package of 2-5 devices"; exit 2; }
FIN0=$($PSQL "SELECT (SELECT count(*) FROM iam_v2.pms_postings)||'/'||(SELECT count(*) FROM iam_v2.payment_transactions)")
ED=https://hotel.stayconnect.local/api/edge/v1; EDR=(--resolve hotel.stayconnect.local:443:127.0.0.1); JAR=/tmp/og-dm-$R4.ck
admin(){ curl -sk "${EDR[@]}" -b $JAR -c $JAR -H 'Content-Type: application/json' "$@"; }
admin -o /dev/null -d "{\"email\":\"${ADMIN_USER:-admin}\",\"password\":\"${ADMIN_PASS:-admin}\"}" $ED/auth/login
USER="og-dm-$R4"; UPW="Dm-$R4-$(head -c4 /dev/urandom | od -An -tx1 | tr -d ' \n')"
CODE=""; ACC=""
cleanup(){
  for e in $($PSQL "SELECT e.id FROM iam_v2.entitlements e LEFT JOIN iam_v2.guest_access_accounts a ON a.id=e.guest_account_id
      LEFT JOIN iam_v2.vouchers v ON v.id=e.voucher_id WHERE e.status IN ('PENDING','ACTIVE','SUSPENDED')
      AND (a.username='$USER' OR v.notes='og-dm-$R4')"); do
    $PSQL "SELECT iam_v2.apply_entitlement_transition('$e','TERMINATED',now(),'ADMIN')" >/dev/null 2>&1; done
  [ -n "$ACC" ] && admin -o /dev/null -X DELETE $ED/guest-accounts/$ACC
  for n in "${NS[@]:-}"; do [ -n "$n" ] || continue
    ip netns exec $n dhclient -r -lf /var/lib/dhcp/dhclient-$n.leases -pf /run/dhclient-$n.pid ${n}c >/dev/null 2>&1
    ip netns del $n 2>/dev/null; ip link del ${n}a 2>/dev/null; rm -rf /etc/netns/$n /var/lib/dhcp/dhclient-$n.leases /tmp/$n.*; done
  rm -f $JAR; }
trap cleanup EXIT
client(){ local n=$1; NS+=("$n"); ip link add ${n}a type veth peer name ${n}c; ip link set ${n}a master "$GB"; ip link set ${n}a up
  ip netns add $n; ip link set ${n}c netns $n; ip netns exec $n ip link set lo up; ip netns exec $n ip link set ${n}c up
  mkdir -p /etc/netns/$n; echo "nameserver $GW" > /etc/netns/$n/resolv.conf
  sleep 1; ip netns exec $n timeout 25 dhclient -1 -lf /var/lib/dhcp/dhclient-$n.leases -pf /run/dhclient-$n.pid ${n}c >/dev/null 2>&1
  ip netns exec $n ping -c1 -W2 "$GW" >/dev/null 2>&1; }
online(){ ip netns exec $1 curl -s -o /dev/null -w '%{http_code}' -m 8 "$PROBE"; }
waiton(){ for _ in $(seq 1 10); do [ "$(online $1)" = 204 ] && return 0; sleep 2; done; return 1; }
waitoff(){ for _ in $(seq 1 10); do [ "$(online $1)" != 204 ] && return 0; sleep 2; done; return 1; }
post(){ local n=$1 path=$2; shift 2
  ip netns exec $n curl -s -o /tmp/$n.html -c /tmp/$n.jar -b /tmp/$n.jar -w '%{http_code} %{redirect_url}' -X POST "$@" "$P$path"; }
get(){ ip netns exec $1 curl -s -o /tmp/$1.html -c /tmp/$1.jar -b /tmp/$1.jar -w '%{http_code} %{redirect_url}' "$P$2"; }
says(){ grep -qi "$2" /tmp/$1.html; }
devid(){ $PSQL "SELECT id FROM iam_v2.devices WHERE mac=(SELECT ('$(ip netns exec $1 cat /sys/class/net/${1}c/address)')::macaddr) ORDER BY last_seen DESC NULLS LAST LIMIT 1"; }
firstopt(){ sed -n 's/.*name="device_id" value="\([0-9a-f-]\{36\}\)".*/\1/p' /tmp/$1.html | head -1; }
ENTOF(){ $PSQL "SELECT id||'|'||status||'|'||coalesce(window_ends_at::text,'none')||'|'||coalesce(guest_account_id::text,voucher_id::text) FROM iam_v2.entitlements WHERE $1 ORDER BY activated_at DESC NULLS LAST LIMIT 1"; }

echo "portal $P; 1-device package ${FREE:0:8}; $MULTI_N-device voucher package ${MULTI_REV:0:8}"
ACC=$(admin -d "{\"username\":\"$USER\",\"password\":\"$UPW\"}" $ED/guest-accounts | python3 -c 'import json,sys;print(json.load(sys.stdin)["account"]["id"])')
[ -n "$ACC" ] && ok "a Personal account for the test (staff-created)" || { bad "could not create the test account"; exit 1; }

echo "== A. 1-device package: replace"
A1=da$R4; A2=db$R4; client $A1; client $A2
r=$(post $A1 /auth/credentials --data-urlencode "username=$USER" --data-urlencode "password=$UPW")
r=$(post $A1 /packages/acquire --data-urlencode "package_id=$FREE")
waiton $A1 && ok "device 1 takes the 1-device package and is online (204)" || bad "device 1 not online ($r)"
E1=$(ENTOF "guest_account_id='$ACC'"); echo "     entitlement ${E1:0:8}"
r=$(post $A2 /auth/credentials --data-urlencode "username=$USER" --data-urlencode "password=$UPW")
case "$r" in 30[23]*/devices/replace*) ok "device 2 at the limit goes to the replacement page ($r)";; *) bad "device 2 sign-in -> $r";; esac
get $A2 /devices/replace >/dev/null
says $A2 "Choose a device to replace" && says $A2 "1 of 1" && ok "the page lists the package's device and its places (1 of 1)" || bad "replacement page content"
[ "$(online $A2)" != 204 ] && ok "device 2 is still offline before choosing" || bad "device 2 online before choosing"
T=$(firstopt $A2); [ "$T" = "$(devid $A1)" ] && ok "the listed device is device 1" || bad "listed device $T"
r=$(post $A2 /devices/replace --data-urlencode "device_id=$T")
case "$r" in 30[23]*/success*) ok "replace -> online page ($r)";; *) bad "replace -> $r";; esac
waiton $A2 && ok "device 2 online (204) through enforcement" || bad "device 2 not online"
waitoff $A1 && ok "device 1 disconnected by enforcement (no longer 204)" || bad "device 1 still online"
E2=$(ENTOF "guest_account_id='$ACC'")
[ "$E2" = "$E1" ] && ok "same entitlement, same window, same owner" || bad "the package changed: $E1 -> $E2"
[ "$($PSQL "SELECT count(*) FROM iam_v2.device_slot_actions WHERE entitlement_id='${E1%%|*}' AND actor='CLIENT' AND action='REPLACE' AND outcome='OK'")" = 1 ] \
  && ok "the replacement is recorded" || bad "no replacement record"
r=$(ip netns exec $A2 curl -s -X POST -H 'Content-Type: application/json' -d '{}' "$P/devices/list")
echo "$r" | python3 -c 'import json,sys;d=json.load(sys.stdin);sys.exit(0 if d.get("ok") and d.get("limit")==1 and any(x.get("this") for x in d["devices"]) else 1)' \
  && ok "the online page lists this device and the package's places" || bad "online device list: $r"
# Sign-out, then the sign-in page (not the packages of the account signed out of).
r=$(post $A1 /auth/credentials --data-urlencode "username=$USER" --data-urlencode "password=$UPW")
case "$r" in 30[23]*/devices/replace*) ok "device 1 back at the limit: replacement page";; *) bad "device 1 re-sign-in -> $r";; esac
post $A1 /logout >/dev/null; r=$(get $A1 /)
case "$r" in 200*) says $A1 'name="username"' && ok "after Sign out, the portal shows the sign-in page again" || bad "after Sign out: not the sign-in page";; *) bad "after Sign out, / -> $r";; esac
# The daily limit (default 3 per package): two more changes are allowed, the next is refused.
for i in 2 3; do
  if [ $((i % 2)) = 0 ]; then dev=$A1; other=$A2; else dev=$A2; other=$A1; fi
  post $dev /auth/credentials --data-urlencode "username=$USER" --data-urlencode "password=$UPW" >/dev/null
  get $dev /devices/replace >/dev/null; r=$(post $dev /devices/replace --data-urlencode "device_id=$(firstopt $dev)")
  waiton $dev && ok "change $i of 3 today: online" || bad "change $i -> $r"
done
# After change 3 device 2 holds the place: device 1 asks for a 4th.
post $A1 /auth/credentials --data-urlencode "username=$USER" --data-urlencode "password=$UPW" >/dev/null; get $A1 /devices/replace >/dev/null
says $A1 "limit for changing devices" && ok "a 4th change today is refused with the daily-limit message" || bad "no daily limit"
r=$(post $A1 /devices/replace --data-urlencode "device_id=$(devid $A2)")
[ "$(online $A1)" != 204 ] && [ "$(online $A2)" = 204 ] && ok "and nothing moved: device 1 offline, device 2 online" || bad "4th change moved the place ($r)"

echo "== B. $MULTI_N-device package (voucher): join, then replace at the limit"
CODE=$(admin -d "{\"package_revision_id\":\"$MULTI_REV\",\"count\":1,\"note\":\"og-dm-$R4\"}" $ED/vouchers/issue | python3 -c 'import json,sys;d=json.load(sys.stdin);c=d.get("codes") or [v.get("code") for v in d.get("vouchers",[])];print(c[0] if c else "")')
[ -n "$CODE" ] && ok "one voucher for the $MULTI_N-device package" || { bad "voucher issue failed"; }
VID=$($PSQL "SELECT id FROM iam_v2.vouchers WHERE notes='og-dm-$R4' LIMIT 1")
B=(); for i in $(seq 1 $((MULTI_N + 1))); do B+=("v$i$R4"); client v$i$R4; done
for i in $(seq 0 $((MULTI_N - 1))); do post ${B[$i]} /auth/voucher --data-urlencode "code=$CODE" >/dev/null; done
on=0; for i in $(seq 0 $((MULTI_N - 1))); do waiton ${B[$i]} && on=$((on+1)); done
[ "$on" = "$MULTI_N" ] && ok "$MULTI_N devices online on the one voucher" || bad "only $on of $MULTI_N devices online"
EV=$(ENTOF "voucher_id='$VID'")
[ "$($PSQL "SELECT count(DISTINCT entitlement_id) FROM iam_v2.sessions WHERE entitlement_id IN (SELECT id FROM iam_v2.entitlements WHERE voucher_id='$VID') AND state='active'")" = 1 ] \
  && ok "all of them share one entitlement" || bad "devices on more than one entitlement"
LAST=${B[$MULTI_N]}
r=$(post $LAST /auth/voucher --data-urlencode "code=$CODE")
case "$r" in 30[23]*/devices/replace*) ok "device $((MULTI_N + 1)) held at the limit -> replacement page";; *) bad "device $((MULTI_N + 1)) -> $r";; esac
get $LAST /devices/replace >/dev/null; says $LAST "$MULTI_N of $MULTI_N" && ok "the page shows $MULTI_N of $MULTI_N places in use" || bad "places line"
r=$(post $LAST /devices/replace --data-urlencode "device_id=$(devid ${B[0]})")
waiton $LAST && ok "device $((MULTI_N + 1)) online after replacing device 1" || bad "replace -> $r"
waitoff ${B[0]} && ok "device 1 disconnected" || bad "device 1 still online"
others=0; for i in $(seq 1 $((MULTI_N - 1))); do [ "$(online ${B[$i]})" = 204 ] && others=$((others+1)); done
[ "$others" = $((MULTI_N - 1)) ] && ok "the other $((MULTI_N - 1)) devices stayed online" || bad "only $others other devices online"
[ "$(ENTOF "voucher_id='$VID'")" = "$EV" ] && ok "same entitlement, same window" || bad "the voucher's package changed"

echo "== C. staff: find the device, prepare it for a new sign-in test"
D=$(devid $A2)   # device 2 holds the place now and is online
admin -o /tmp/dm-$R4.json -H "X-Device-Search: $USER" $ED/client-devices
python3 -c 'import json,sys;d=json.load(open(sys.argv[1]));sys.exit(0 if any(x["id"]==sys.argv[2] for x in d["data"]) else 1)' /tmp/dm-$R4.json "$D" \
  && ok "staff find the device by the account name" || bad "device not found by account"
before=$(ENTOF "guest_account_id='$ACC'")
r=$(admin -X POST -d '{"reason":"PRE-LIVE sign-in test"}' $ED/client-devices/$D/prepare-test)
echo "$r" | grep -q '"status":"prepared"' && ok "prepare for a sign-in test: $r" || bad "prepare -> $r"
waitoff $A2 && ok "the device is disconnected" || bad "still online after prepare"
[ "$($PSQL "SELECT status FROM iam_v2.entitlement_devices WHERE device_id='$D' AND entitlement_id='${E1%%|*}'")" = DISCONNECTED ] && ok "its place is free" || bad "place still held"
rm -f /tmp/$A2.jar; r=$(get $A2 /)
case "$r" in 200*) says $A2 'name="username"' && ok "its next visit shows the sign-in page" || bad "not the sign-in page";; *) bad "/ -> $r";; esac
[ "$(ENTOF "guest_account_id='$ACC'")" = "$before" ] && ok "the package is unchanged" || bad "the package changed"
[ "$($PSQL "SELECT count(*) FROM iam_v2.sessions WHERE device_id='$D'")" -ge 1 ] && ok "the device's session history is kept" || bad "history lost"
[ "$($PSQL "SELECT (SELECT count(*) FROM iam_v2.pms_postings)||'/'||(SELECT count(*) FROM iam_v2.payment_transactions)")" = "$FIN0" ] && ok "no posting or payment happened" || bad "financial counts changed"
echo; echo "PASS=$PASS FAIL=$FAIL"; [ "$FAIL" = 0 ]
