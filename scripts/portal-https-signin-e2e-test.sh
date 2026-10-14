#!/usr/bin/env bash
# SIGN-IN OVER THE HTTPS PORTAL, END TO END, FROM GUEST DEVICES (run as root on the appliance).
#
# Two real sign-ins through https://<canonical>/ from network namespaces on the guest bridge: a voucher (one
# step: redeem -> grant -> activation) and a guest account choosing a free package (quote -> confirm ->
# activation). Each is asserted on what the SERVER holds (session, entitlement, nft enforcement) and on real
# packets (the connectivity probe answers 204). Then sign-out takes the device offline again.
#
# Uses the site's OWN catalogue: the voucher is issued on the active package whose current revision allows
# PREPAID; the account takes the active free package. Nothing is created in the catalogue. Every credential this
# makes is revoked or disabled on exit. No PMS posting, payment or financial action happens (asserted).
#
#   CA=<pem>   trust anchor for the portal certificate (a test CA such as Pebble's); default: the system roots.
set -uo pipefail
PASS=0; FAIL=0
ok(){ echo "  ok   $1"; PASS=$((PASS+1)); }
bad(){ echo "  FAIL $1"; FAIL=$((FAIL+1)); }
R='--resolve hotel.stayconnect.local:443:127.0.0.1'; B=https://hotel.stayconnect.local/api/edge/v1; CK=/tmp/ck.hs
PSQL="docker exec stayconnect-pg psql -U stayconnect -d stayconnect_site -tAc"
PROBE=http://connectivitycheck.gstatic.com/generate_204
CAOPT=(); [ -n "${CA:-}" ] && CAOPT=(--cacert "$CA")
NS=(); VIDS=(); ACCTS=()
# UNIQUE DEVICE NAMES PER RUN. systemd derives a virtual interface's MAC from its NAME, so a fixed name is the same
# "device" on every run -- and a free package that is once per device then (correctly) refuses the second run.
R4=$(printf '%04x' $((RANDOM % 65536)))
cleanup(){
  for v in "${VIDS[@]:-}"; do [ -n "$v" ] && curl -sk $R -b $CK -o /dev/null -H 'Content-Type: application/json' \
      -d '{"password":"admin","reason":"https sign-in e2e cleanup"}' "$B/vouchers/$v/revoke"; done
  for a in "${ACCTS[@]:-}"; do [ -n "$a" ] && curl -sk $R -b $CK -o /dev/null -X PATCH -H 'Content-Type: application/json' \
      -d '{"enabled":false}' "$B/guest-accounts/$a"; done
  for n in "${NS[@]:-}"; do [ -n "$n" ] || continue
    ip netns exec $n curl -s "${CAOPT[@]}" -o /dev/null -m 5 -X POST "https://$HOST/logout" 2>/dev/null
    ip netns exec $n dhclient -r -lf /var/lib/dhcp/dhclient-$n.leases -pf /run/dhclient-$n.pid ${n}c >/dev/null 2>&1
    ip netns del $n 2>/dev/null; ip link del ${n}a 2>/dev/null; rm -rf /etc/netns/$n /var/lib/dhcp/dhclient-$n.leases; done
}
trap cleanup EXIT
GB=$($PSQL "SELECT bridge_name FROM guest_networks WHERE enabled ORDER BY created_at LIMIT 1")
GW=$($PSQL "SELECT host(gateway_ip) FROM guest_networks WHERE enabled ORDER BY created_at LIMIT 1")
read -r ACTIVE HOST <<<"$($PSQL "SELECT https_active, coalesce(canonical_hostname,'-') FROM portal_tls_state" | tr '|' ' ')"
[ "$ACTIVE" = t ] || { echo "HTTPS is not active; nothing to test"; exit 2; }
P="https://$HOST"
FIN0=$($PSQL "SELECT (SELECT count(*) FROM iam_v2.pms_postings)||'/'||(SELECT count(*) FROM iam_v2.payment_transactions)")

client(){ local n=$1; NS+=("$n"); ip netns del $n 2>/dev/null; ip link del ${n}a 2>/dev/null
  ip link add ${n}a type veth peer name ${n}c; ip link set ${n}a master "$GB"; ip link set ${n}a up
  ip netns add $n; ip link set ${n}c netns $n; ip netns exec $n ip link set lo up; ip netns exec $n ip link set ${n}c up
  mkdir -p /etc/netns/$n; echo "nameserver $GW" > /etc/netns/$n/resolv.conf
  sleep 1; ip netns exec $n timeout 25 dhclient -1 -lf /var/lib/dhcp/dhclient-$n.leases -pf /run/dhclient-$n.pid ${n}c >/dev/null 2>&1
  CIP=$(ip netns exec $n ip -4 -br addr show ${n}c | awk '{print $3}' | cut -d/ -f1); ip netns exec $n ping -c1 -W2 "$GW" >/dev/null 2>&1; }
online(){ ip netns exec $1 curl -s -o /dev/null -w '%{http_code}' -m 8 "$PROBE"; }
inset(){ local s; s=$(nft list set inet stayconnect phase3_auth_ipv4 2>/dev/null); grep -qE "\"?$GB\"? \. $1([^0-9]|\$)" <<<"$s"; }
post(){ local n=$1 path=$2; shift 2  # -> the Location header (relative or absolute)
  ip netns exec $n curl -s "${CAOPT[@]}" -o /tmp/$n.html -D - -c /tmp/$n.jar -b /tmp/$n.jar "$@" "$P$path" | tr -d '\r' | awk 'tolower($1)=="location:"{print $2}'; }
sidof(){ printf '%s' "$1" | sed -n 's/.*[?&]s=\([^&]*\).*/\1/p'; }

curl -sk $R -c $CK -o /dev/null -H 'Content-Type: application/json' -d '{"email":"admin","password":"admin"}' $B/auth/login
VREV=$($PSQL "SELECT p.current_revision_id FROM iam_v2.internet_packages p JOIN iam_v2.internet_package_revisions r ON r.id=p.current_revision_id
  WHERE p.active AND NOT p.is_system AND 'PREPAID' = ANY(r.settlement_methods) ORDER BY r.revision_no DESC LIMIT 1")
FREE=$($PSQL "SELECT p.id FROM iam_v2.internet_packages p JOIN iam_v2.internet_package_revisions r ON r.id=p.current_revision_id
  WHERE p.active AND NOT p.is_system AND r.price_minor=0 AND 'NOT_REQUIRED' = ANY(r.settlement_methods) LIMIT 1")
echo "portal $P; voucher package revision ${VREV:0:8}; free package ${FREE:0:8}"

echo "== voucher over HTTPS"
if [ -z "$VREV" ]; then echo "  SKIP no active package allows vouchers"; else
  out=$(curl -sk $R -b $CK -H 'Content-Type: application/json' -d "{\"count\":1,\"package_revision_id\":\"$VREV\",\"note\":\"https sign-in e2e\"}" "$B/vouchers/issue")
  CODE=$(printf '%s' "$out" | python3 -c 'import sys,json;print((json.load(sys.stdin).get("codes") or [""])[0])' 2>/dev/null)
  BID=$(printf '%s' "$out" | python3 -c 'import sys,json;print(json.load(sys.stdin).get("batch_id",""))' 2>/dev/null)
  VID=$([ -n "$BID" ] && $PSQL "SELECT id FROM iam_v2.vouchers WHERE batch_id='$BID' LIMIT 1"); VIDS+=("$VID")
  [ -n "$CODE" ] && ok "voucher issued through the Admin Console API" || bad "voucher issue: $out"
  client hv$R4; c=$(online hv$R4); [ "$c" != 204 ] && ok "device captive before sign-in ($c)" || bad "online before sign-in"
  loc=$(post hv$R4 /auth/voucher --data-urlencode "code=$CODE"); SID=$(sidof "$loc")
  [ -n "$SID" ] && ok "voucher sign-in over HTTPS -> $loc" || bad "voucher sign-in -> '$loc'"
  [ "$($PSQL "SELECT state FROM iam_v2.sessions WHERE id='$SID'")" = active ] && ok "session active" || bad "session not active"
  inset "$CIP" && ok "device enforced (nft phase3_auth_ipv4)" || bad "device not enforced"
  c=$(online hv$R4); [ "$c" = 204 ] && ok "REAL INTERNET after an HTTPS sign-in (204)" || bad "no internet ($c)"
  A=$(ip netns exec hv$R4 curl -s "${CAOPT[@]}" "$P/captive-portal/api"); printf '%s' "$A" | grep -q '"captive":false' \
    && ok "captive API now answers captive=false ($A)" || bad "captive API after sign-in: $A"
  ip netns exec hv$R4 curl -s "${CAOPT[@]}" -o /dev/null -X POST "$P/logout"; sleep 6
  c=$(online hv$R4); [ "$c" != 204 ] && ok "sign-out over HTTPS takes the device offline ($c)" || bad "still online after sign-out"
fi

echo "== guest account + free package over HTTPS"
if [ -z "$FREE" ]; then echo "  SKIP no active free package"; else
  UN="hs-$(date +%s%N | cut -c10-16)"
  out=$(curl -sk $R -b $CK -H 'Content-Type: application/json' -d "{\"username\":\"$UN\",\"generate\":true,\"notes\":\"https sign-in e2e\"}" "$B/guest-accounts")
  AID=$(printf '%s' "$out" | python3 -c 'import sys,json;d=json.load(sys.stdin);print(d.get("id") or (d.get("account") or {}).get("id",""))' 2>/dev/null)
  PW=$(printf '%s' "$out" | python3 -c 'import sys,json;d=json.load(sys.stdin);print(d.get("password") or d.get("generated_password",""))' 2>/dev/null)
  ACCTS+=("$AID")
  client ha$R4
  loc=$(post ha$R4 /auth/credentials --data-urlencode "username=$UN" --data-urlencode "password=$PW")
  [ "$loc" = "/packages" ] && ok "account authenticated over HTTPS -> package selection" || bad "account sign-in -> '$loc'"
  loc=$(post ha$R4 /packages/acquire --data-urlencode "package_id=$FREE"); SID=$(sidof "$loc")
  [ -n "$SID" ] && ok "free package acquired over HTTPS (quote -> confirm -> activation)" || bad "acquire -> '$loc'"
  inset "$CIP" && [ "$(online ha$R4)" = 204 ] && ok "account device enforced and online (204)" || bad "account device offline"
fi
[ "$($PSQL "SELECT (SELECT count(*) FROM iam_v2.pms_postings)||'/'||(SELECT count(*) FROM iam_v2.payment_transactions)")" = "$FIN0" ] \
  && ok "no PMS posting or payment happened" || bad "financial counts changed"
echo; echo "PASS=$PASS FAIL=$FAIL"; [ "$FAIL" = 0 ]
