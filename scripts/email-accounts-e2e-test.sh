#!/usr/bin/env bash
# EMAIL + PASSWORD SELF-SERVICE ACCOUNTS, END TO END, FROM GUEST DEVICES (run as root on the appliance).
# Contract: docs/architecture/ONEGATE_EMAIL_ACCOUNTS_SELF_SERVICE.md.
#
# Two network-namespace devices on the guest bridge walk the client journey over the HTTPS portal: register ->
# packages -> free package -> online; the second device continues the SAME entitlement; the verification deadline
# passes (the test moves its own account's deadline into the past -- the only thing it changes behind the
# product's back) -> the sweep suspends Internet -> activation is refused -> "1 more hour" with the password ->
# online again on the same entitlement -> the hour ends -> suspended again, no second hour. Also: resend limits,
# forgot-password genericity, wrong-password genericity, duplicate registration, the per-device registration cap.
# The verification LINK itself is in the email; following it is a real-device step (checklist).
#
#   DEST=<mailbox for the verification email, e.g. a plus-address>  CA=<pem, optional>
set -uo pipefail
PASS=0; FAIL=0
ok(){ echo "  ok   $1"; PASS=$((PASS+1)); }
bad(){ echo "  FAIL $1"; FAIL=$((FAIL+1)); }
[ -n "${DEST:-}" ] || { echo "DEST is required"; exit 2; }
PSQL="docker exec stayconnect-pg psql -U stayconnect -d stayconnect_site -tAc"
CAOPT=(); [ -n "${CA:-}" ] && CAOPT=(--cacert "$CA")
PROBE=http://connectivitycheck.gstatic.com/generate_204
R4=$(printf '%04x' $((RANDOM % 65536))); D1=ea$R4; D2=eb$R4; NS=()
read -r ACTIVE HOST <<<"$($PSQL "SELECT https_active, coalesce(canonical_hostname,'-') FROM portal_tls_state" | tr '|' ' ')"
[ "$ACTIVE" = t ] || { echo "HTTPS is not active"; exit 2; }
P="https://$HOST"; GB=$($PSQL "SELECT bridge_name FROM guest_networks WHERE enabled ORDER BY created_at LIMIT 1")
GW=$($PSQL "SELECT host(gateway_ip) FROM guest_networks WHERE enabled ORDER BY created_at LIMIT 1")
FREE=$($PSQL "SELECT p.id FROM iam_v2.internet_packages p JOIN iam_v2.internet_package_revisions r ON r.id=p.current_revision_id
  WHERE p.active AND NOT p.is_system AND r.price_minor=0 AND 'NOT_REQUIRED' = ANY(r.settlement_methods) LIMIT 1")
EMAIL="$(echo "$DEST" | tr 'A-Z' 'a-z')"; PW="Og-test-$R4-$(head -c4 /dev/urandom | od -An -tx1 | tr -d ' \n')"
FIN0=$($PSQL "SELECT (SELECT count(*) FROM iam_v2.pms_postings)||'/'||(SELECT count(*) FROM iam_v2.payment_transactions)")
cleanup(){
  local a; a=$($PSQL "SELECT id FROM iam_v2.guest_access_accounts WHERE lower(username)='$EMAIL'")
  if [ -n "$a" ]; then
    for e in $($PSQL "SELECT id FROM iam_v2.entitlements WHERE guest_account_id='$a' AND status IN ('PENDING','ACTIVE','SUSPENDED')"); do
      $PSQL "SELECT iam_v2.apply_entitlement_transition('$e','TERMINATED',now(),'ADMIN')" >/dev/null 2>&1; done
    $PSQL "UPDATE iam_v2.guest_access_accounts SET enabled=false WHERE id='$a'" >/dev/null
  fi
  $PSQL "DELETE FROM iam_v2.guest_access_accounts WHERE lower(username) LIKE 'og-cap-$R4-%@example.invalid'" >/dev/null 2>&1
  for n in "${NS[@]:-}"; do [ -n "$n" ] || continue
    ip netns exec $n dhclient -r -lf /var/lib/dhcp/dhclient-$n.leases -pf /run/dhclient-$n.pid ${n}c >/dev/null 2>&1
    ip netns del $n 2>/dev/null; ip link del ${n}a 2>/dev/null; rm -rf /etc/netns/$n /var/lib/dhcp/dhclient-$n.leases /tmp/$n.*; done
  resolver_guard; }
trap cleanup EXIT
client(){ local n=$1; NS+=("$n"); ip link add ${n}a type veth peer name ${n}c; ip link set ${n}a master "$GB"; ip link set ${n}a up
  ip netns add $n; ip link set ${n}c netns $n; ip netns exec $n ip link set lo up; ip netns exec $n ip link set ${n}c up
  mkdir -p /etc/netns/$n; echo "nameserver $GW" > /etc/netns/$n/resolv.conf
  sleep 1; ip netns exec $n timeout 25 dhclient -1 -lf /var/lib/dhcp/dhclient-$n.leases -pf /run/dhclient-$n.pid ${n}c >/dev/null 2>&1
  ip netns exec $n ping -c1 -W2 "$GW" >/dev/null 2>&1; resolver_guard; }
# THE HOST'S RESOLVER. dhclient's exit hook (/etc/dhcp/dhclient-exit-hooks.d/resolved) restarts the HOST's
# systemd-resolved even for a client in a namespace, and a handful of test clients in quick succession hit its start
# limit -- leaving the appliance itself without DNS (mail, Central) until something restarts it. Measured on PRE-LIVE
# 2026-10-15. Every client start and the cleanup put it back.
resolver_guard(){ systemctl reset-failed systemd-resolved >/dev/null 2>&1; systemctl is-active -q systemd-resolved || systemctl start systemd-resolved >/dev/null 2>&1; }
online(){ ip netns exec $1 curl -s -o /dev/null -w '%{http_code}' -m 8 "$PROBE"; }
post(){ local n=$1 path=$2; shift 2  # prints "<code> <location>"
  ip netns exec $n curl -s "${CAOPT[@]}" -o /tmp/$n.html -c /tmp/$n.jar -b /tmp/$n.jar -w '%{http_code} %{redirect_url}' -X POST "$@" "$P$path"; }
says(){ grep -qi "$2" /tmp/$1.html; }
sidof(){ printf '%s' "$1" | sed -n 's/.*[?&]s=\([^&]*\).*/\1/p'; }
ACC(){ $PSQL "SELECT $1 FROM iam_v2.guest_access_accounts WHERE lower(username)='$EMAIL'"; }
ENT(){ $PSQL "SELECT id||'|'||status||'|'||coalesce(window_ends_at::text,'none') FROM iam_v2.entitlements WHERE guest_account_id=(SELECT id FROM iam_v2.guest_access_accounts WHERE lower(username)='$EMAIL') ORDER BY activated_at DESC NULLS LAST LIMIT 1"; }
page(){ python3 -c 'import re,sys;t=open(sys.argv[1],encoding="utf-8",errors="replace").read();t=re.sub(r"<script.*?</script>|<style.*?</style>","",t,flags=re.S);print(re.sub(r"\s+"," ",re.sub(r"<[^>]+>"," ",t)).strip()[:400])' /tmp/$1.html; }

echo "portal $P, free package ${FREE:0:8}"
echo "== 1. register -> packages -> free package -> online"
client $D1
[ "$(online $D1)" != 204 ] && ok "device 1 captive before registering" || bad "device 1 online before registering"
r=$(post $D1 /auth/account/register --data-urlencode "email=$EMAIL" --data-urlencode "password=$PW" --data-urlencode "password_confirm=$PW")
case "$r" in 30[23]*/packages*) ok "registration goes straight to the packages ($r)";; *) bad "register -> $r";; esac
grep -q og_account /tmp/$D1.jar && ok "account cookie set" || bad "no og_account cookie"
[ "$(ACC "self_registered::text||'|'||(email_verified_at IS NULL)::text||'|'||(verification_deadline > now() + interval '23 hours')::text")" = "true|true|true" ] \
  && ok "self-registered, unverified, deadline about 24 h ahead" || bad "account row: $(ACC "self_registered, email_verified_at, verification_deadline")"
r=$(post $D1 /packages/acquire --data-urlencode "package_id=$FREE"); S1=$(sidof "$r")
[ -n "$S1" ] && [ "$(online $D1)" = 204 ] && ok "free package activated; device 1 online (204)" || bad "acquire -> $r"
E1=$(ENT); echo "     entitlement ${E1:0:8}"
sleep 20
[ "$($PSQL "SELECT count(*) FROM iam_v2.account_email_tokens WHERE account_id=(SELECT id FROM iam_v2.guest_access_accounts WHERE lower(username)='$EMAIL') AND purpose='VERIFY' AND sent_at IS NOT NULL")" -ge 1 ] \
  && ok "verification email sent from the outbox" || bad "verification email not sent: $($PSQL "SELECT send_attempts||' '||coalesce(last_send_error,'') FROM iam_v2.account_email_tokens WHERE account_id=(SELECT id FROM iam_v2.guest_access_accounts WHERE lower(username)='$EMAIL')")"

echo "== 2. another device is held to the package's device limit; the first device continues the SAME entitlement"
ip netns exec $D1 curl -s "${CAOPT[@]}" -o /dev/null -X POST "$P/logout"; sleep 6
client $D2
r=$(post $D2 /auth/account/signin --data-urlencode "email=$EMAIL" --data-urlencode "password=$PW")
MAXD=$($PSQL "SELECT s.max_concurrent_devices FROM iam_v2.entitlements e JOIN iam_v2.internet_package_revisions r ON r.id=e.package_revision_id JOIN iam_v2.service_plan_revisions s ON s.id=r.service_plan_revision_id WHERE e.id='${E1%%|*}'")
if [ "$MAXD" = 1 ]; then
  # Since 0118 the device-limit answer is the choice of which device to replace (device-management-e2e-test.sh).
  case "$r" in 30[23]*/devices/replace*) [ "$(online $D2)" != 204 ] && ok "a 1-device package: device 2 is offered the replacement choice, stays captive" || bad "device 2 online at the limit";;
    *) bad "device 2 on a 1-device package -> $r"; page $D2;; esac
else
  S2=$(sidof "$r"); [ -n "$S2" ] && [ "$(ENT | cut -d'|' -f1)" = "${E1%%|*}" ] && [ "$(online $D2)" = 204 ] && ok "device 2 joins the same entitlement ($MAXD devices)" || { bad "device 2 join -> $r"; page $D2; }
fi
r=$(post $D1 /auth/account/signin --data-urlencode "email=$EMAIL" --data-urlencode "password=$PW"); E2=$(ENT)
[ "${E2%%|*}" = "${E1%%|*}" ] && [ "$(online $D1)" = 204 ] && ok "device 1 signs in again: same entitlement, online" || { bad "device 1 re-sign-in -> $r ($E2 vs $E1)"; page $D1; }
[ "${E2##*|}" = "${E1##*|}" ] && ok "the window end did not move" || bad "window moved: $E1 -> $E2"
r=$(post $D2 /auth/account/signin --data-urlencode "email=$EMAIL" --data-urlencode "password=wrong-$PW")
grep -qi "invalid" /tmp/$D2.html && ok "wrong password: generic refusal" || bad "wrong password -> $r"

echo "== 3. deadline passes -> suspended; activation refused"
$PSQL "UPDATE iam_v2.guest_access_accounts SET verification_deadline=now()-interval '1 minute' WHERE lower(username)='$EMAIL'" >/dev/null
for _ in $(seq 1 20); do [ "$(online $D1)" != 204 ] && break; sleep 3; done
[ "$(online $D1)" != 204 ] && ok "the sweep ended the account's Internet after the deadline" || bad "still online after the deadline"
[ "$(ENT | cut -d'|' -f2)" = ACTIVE ] && ok "the entitlement itself is untouched (ACTIVE)" || bad "entitlement state: $(ENT)"
r=$(post $D1 /auth/account/signin --data-urlencode "email=$EMAIL" --data-urlencode "password=$PW")
says $D1 "verif" && [ "$(online $D1)" != 204 ] && ok "sign-in shows the verification-required page, no Internet" || bad "suspended sign-in -> $r"

echo "== 4. resend limits"
r=$(post $D1 /auth/account/resend); echo "     resend #1 -> ${r%% *}"
r=$(post $D1 /auth/account/resend); says $D1 "wait\|minute\|second\|again" && ok "an immediate second resend is held back" || bad "second resend -> $r"

echo "== 5. one more hour, once"
r=$(post $D1 /auth/account/grace --data-urlencode "email=$EMAIL" --data-urlencode "password=wrong-$PW")
[ "$(online $D1)" != 204 ] && ok "grace refuses a wrong password" || bad "grace with a wrong password gave Internet"
r=$(post $D1 /auth/account/grace --data-urlencode "email=$EMAIL" --data-urlencode "password=$PW")
for _ in $(seq 1 10); do [ "$(online $D1)" = 204 ] && break; sleep 2; done
[ "$(online $D1)" = 204 ] && ok "1 more hour: online again" || { bad "grace -> $r"; page $D1; }
[ "$(ACC "grace_extensions_used::text||'|'||(grace_until > now() + interval '55 minutes')::text")" = "1|true" ] && ok "grace recorded: once, about 60 minutes" || bad "grace row: $(ACC "grace_extensions_used, grace_until")"
[ "$(ENT)" = "$E2" ] && ok "same entitlement, same window: the hour is not a package" || bad "entitlement changed: $(ENT)"
$PSQL "UPDATE iam_v2.guest_access_accounts SET grace_until=now()-interval '1 second' WHERE lower(username)='$EMAIL'" >/dev/null
for _ in $(seq 1 20); do [ "$(online $D1)" != 204 ] && break; sleep 3; done
[ "$(online $D1)" != 204 ] && ok "when the hour ends, Internet is suspended again" || bad "still online after the hour"
r=$(post $D1 /auth/account/grace --data-urlencode "email=$EMAIL" --data-urlencode "password=$PW")
[ "$(online $D1)" != 204 ] && ok "no second hour (max 1 by default)" || bad "a second hour was granted"

echo "== 6. registration and recovery answers"
r=$(post $D2 /auth/account/register --data-urlencode "email=$EMAIL" --data-urlencode "password=$PW" --data-urlencode "password_confirm=$PW")
says $D2 "already" && ok "duplicate registration: already registered (sign in / reset offered)" || bad "duplicate register -> $r"
post $D2 /auth/account/forgot --data-urlencode "email=$EMAIL" >/dev/null; cp /tmp/$D2.html /tmp/$D2.f1
post $D2 /auth/account/forgot --data-urlencode "email=nobody-$R4@example.invalid" >/dev/null
python3 - /tmp/$D2.f1 /tmp/$D2.html <<'EOF' && ok "forgot password answers the same for known and unknown addresses" || bad "forgot password answers differ"
import re, sys
def text(p):
    t = open(p, encoding="utf-8", errors="replace").read()
    t = re.sub(r"<script.*?</script>|<style.*?</style>", "", t, flags=re.S)
    return re.sub(r"\s+", " ", re.sub(r"<[^>]+>", " ", t)).strip()
sys.exit(0 if text(sys.argv[1]) == text(sys.argv[2]) else 1)
EOF
lim=""
for i in 1 2 3; do r=$(post $D1 /auth/account/register --data-urlencode "email=og-cap-$R4-$i@example.invalid" --data-urlencode "password=$PW" --data-urlencode "password_confirm=$PW"); lim="$lim ${r%% *}"; done
says $D1 "too many\|limit\|later" && ok "per-device registration cap reached on device 1 (answers:$lim)" || bad "no registration cap (answers:$lim)"
[ "$($PSQL "SELECT (SELECT count(*) FROM iam_v2.pms_postings)||'/'||(SELECT count(*) FROM iam_v2.payment_transactions)")" = "$FIN0" ] && ok "no posting or payment happened" || bad "financial counts changed"
echo; echo "PASS=$PASS FAIL=$FAIL"; [ "$FAIL" = 0 ]
