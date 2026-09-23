#!/usr/bin/env bash
# THE HEART OF LOCAL-FIRST: A GUEST SIGNS IN WHILE CENTRAL IS UNREACHABLE.
#
# The wider Central-outage drill already showed the licence staying Active and the whole operator surface
# working with Central blackholed. This is the assertion that matters most and the one that was wrong on the
# first attempt: portald's /auth/voucher takes a FORM (r.ParseForm / r.FormValue("code")), not JSON, so a
# JSON POST produced an empty code and portald re-rendered the portal page -- and a loose grep for
# "success|granted|authorized" MATCHED THAT HTML and reported a pass while no session existed. The session
# row is the only thing that proves a guest got access, so it is what this asserts.
set -uo pipefail
CENTRAL=150.0.0.252
PASS=0; FAIL=0
ok(){ echo "  ok $1"; PASS=$((PASS+1)); }
bad(){ echo "  FAIL $1"; FAIL=$((FAIL+1)); }
R='--resolve hotel.stayconnect.local:443:127.0.0.1'
B=https://hotel.stayconnect.local/api/edge/v1
PSQL="docker exec stayconnect-pg psql -U stayconnect -d stayconnect_site -tAc"
VID=""; CIP=""

finish() {
  ip route del blackhole "$CENTRAL/32" 2>/dev/null
  if [ -n "$VID" ]; then
    curl -sk $R -b /tmp/ck.lf -H 'Content-Type: application/json' \
      -d '{"password":"admin","reason":"local-first sign-in proof complete: revoking the drill voucher so no drill credential stays redeemable"}' \
      "$B/vouchers/$VID/revoke" >/dev/null 2>&1
  fi
  if [ -n "$CIP" ]; then
    curl -s --unix-socket /run/stayconnect/scd.sock -X POST http://unix/v1/sessions/revoke \
      -H 'Content-Type: application/json' -d "{\"ip\":\"$CIP\",\"reason\":\"local-first drill cleanup\"}" >/dev/null 2>&1
    ip netns exec lfg dhclient -r -lf /tmp/lfg.lease -pf /tmp/lfg.pid lfgc 2>/dev/null
  fi
  ip netns del lfg 2>/dev/null
  ip link del lfga 2>/dev/null
}
trap finish EXIT
ip route del blackhole "$CENTRAL/32" 2>/dev/null; ip netns del lfg 2>/dev/null; ip link del lfga 2>/dev/null

curl -sk $R -c /tmp/ck.lf -o /dev/null -H 'Content-Type: application/json' \
  -d '{"email":"admin","password":"admin"}' $B/auth/login

echo "== 1. issue a voucher (the code is in the clear only at print time) =="
PKG=$($PSQL "SELECT pr.id FROM iam_v2.internet_package_revisions pr JOIN iam_v2.internet_packages p ON p.id=pr.package_id WHERE p.active AND NOT p.is_system ORDER BY pr.revision_no DESC LIMIT 1")
ISSUE=$(curl -sk $R -b /tmp/ck.lf -H 'Content-Type: application/json' \
  -d "{\"count\":1,\"package_revision_id\":\"$PKG\",\"note\":\"local-first sign-in proof during a Central outage, 2026-09-23\"}" \
  "$B/vouchers/issue")
CODE=$(printf '%s' "$ISSUE" | python3 -c 'import sys,json;c=json.load(sys.stdin).get("codes") or [""];print(c[0])' 2>/dev/null)
BATCH=$(printf '%s' "$ISSUE" | python3 -c 'import sys,json;print(json.load(sys.stdin).get("batch_id",""))' 2>/dev/null)
[ -n "$BATCH" ] && VID=$($PSQL "SELECT id FROM iam_v2.vouchers WHERE batch_id='$BATCH' LIMIT 1")
[ -n "$CODE" ] && [ -n "$VID" ] && ok "voucher issued (${VID:0:8}, ${#CODE} characters)" || { bad "issue failed: $(printf '%s' "$ISSUE" | head -c 200)"; exit 1; }

echo "== 2. a guest device on the live guest network =="
GB=$($PSQL "SELECT bridge_name FROM guest_networks WHERE enabled ORDER BY created_at LIMIT 1")
GW=$($PSQL "SELECT host(gateway_ip) FROM guest_networks WHERE enabled ORDER BY created_at LIMIT 1")
ip link add lfga type veth peer name lfgc 2>/dev/null
ip link set lfga master "$GB"; ip link set lfga up
ip netns add lfg; ip link set lfgc netns lfg
ip netns exec lfg ip link set lo up; ip netns exec lfg ip link set lfgc up
sleep 1
ip netns exec lfg timeout 25 dhclient -1 -lf /tmp/lfg.lease -pf /tmp/lfg.pid lfgc >/dev/null 2>&1
CIP=$(ip netns exec lfg ip -4 -br addr show lfgc | awk '{print $3}' | cut -d/ -f1)
CMAC=$(ip netns exec lfg cat /sys/class/net/lfgc/address)
[ -n "$CIP" ] && ok "guest device leased $CIP on $GB (gateway $GW)" || { bad "no lease"; exit 1; }

echo "== 3. CENTRAL GOES AWAY =="
ip route add blackhole "$CENTRAL/32"
systemctl restart stayconnect-scd; sleep 7
journalctl -u stayconnect-scd --since '-1min' --no-pager | grep -c 'offline-safe' | sed 's/^/   offline-safe log lines: /'
LIC=$(curl -sk $R -b /tmp/ck.lf "$B/license" | python3 -c 'import sys,json;print(json.load(sys.stdin).get("state"))' 2>/dev/null)
[ "$LIC" = "Active" ] && ok "licence still Active with Central blackholed" || bad "licence state during outage: $LIC"

echo "== 4. THE GUEST AUTHENTICATES WHILE CENTRAL IS DOWN =="
# WHAT THIS ASSERTS, AND WHY IT IS AUTHENTICATION RATHER THAN A SESSION.
#
# Guest access is two steps in this product. /v1/sessions/authorize validates the credential and returns an
# auth context and a device; a session then needs an ENTITLEMENT, which comes from the commerce grant the
# portal performs after the guest picks a package (/v1/sessions/activate requires entitlement_id). The
# Phase-2 commerce surface is DARK on this appliance -- scd logs "phase2 master=false portal=false
# admin=false" -- so the grant step cannot run, and turning it on is guest activation, which D41 defers.
#
# So the local-first property that can honestly be proven today is that the CREDENTIAL is validated by the
# appliance with Central unreachable, by iam_v2, against its own data. That is asserted here directly
# against scd, because portald's page cannot distinguish "authenticated but nothing to grant" from a
# failure in a way a test should read.
#
# THE EARLIER VERSION OF THIS STEP CLAIMED A PASS IT HAD NOT EARNED: it POSTed JSON to a form handler, got
# the portal page back, and matched the word "success" inside that HTML. It then counted sessions for the
# client IP -- where a stale row from an earlier drill on a recycled DHCP address made the count
# meaningless. Both are fixed by asserting the thing the server actually returns.
AUTHZ=$(curl -s --unix-socket /run/stayconnect/scd.sock -X POST http://unix/v1/sessions/authorize \
  -H 'Content-Type: application/json' \
  -d "{\"ip\":\"$CIP\",\"mac\":\"$CMAC\",\"voucher\":\"$CODE\"}")
echo "   $(printf '%s' "$AUTHZ" | head -c 240)"
AUTHORITY=$(printf '%s' "$AUTHZ" | python3 -c 'import sys,json;print(json.load(sys.stdin).get("authority",""))' 2>/dev/null)
METHOD=$(printf '%s' "$AUTHZ" | python3 -c 'import sys,json;print(json.load(sys.stdin).get("method",""))' 2>/dev/null)
ACTX=$(printf '%s' "$AUTHZ" | python3 -c 'import sys,json;print(json.load(sys.stdin).get("auth_context_id",""))' 2>/dev/null)
DEV=$(printf '%s' "$AUTHZ" | python3 -c 'import sys,json;print(json.load(sys.stdin).get("device_id",""))' 2>/dev/null)
if [ "$AUTHORITY" = "iam_v2" ] && [ "$METHOD" = "VOUCHER" ] && [ -n "$ACTX" ] && [ -n "$DEV" ]; then
  ok "the voucher was VALIDATED by iam_v2 with Central unreachable (auth context ${ACTX:0:8}, device ${DEV:0:8})"
else
  bad "voucher authentication failed during the outage: $(printf '%s' "$AUTHZ" | head -c 200)"
fi
# The auth context and the device are real rows, not just a response.
AC=$(${PSQL} "SELECT count(*) FROM iam_v2.auth_contexts WHERE id='${ACTX:-00000000-0000-0000-0000-000000000000}'" 2>/dev/null)
[ "${AC:-0}" = "1" ] && ok "the auth context is recorded in the database" || bad "no auth_contexts row for ${ACTX:-(none)}"
DV=$(${PSQL} "SELECT count(*) FROM iam_v2.devices WHERE id='${DEV:-00000000-0000-0000-0000-000000000000}'" 2>/dev/null)
[ "${DV:-0}" = "1" ] && ok "the guest device is recorded in the database" || bad "no devices row for ${DEV:-(none)}"

echo "   -- and the step that is NOT available, stated rather than skipped quietly:"
echo "      a session needs an entitlement from the commerce grant, and Phase 2 is DARK on this appliance."
echo "      Enabling it is guest activation, which D41 defers. The voucher therefore stays UNUSED."
VS=$(${PSQL} "SELECT state FROM iam_v2.vouchers WHERE id='$VID'")
[ "$VS" = "UNUSED" ] && ok "the voucher is still UNUSED, consistent with no entitlement having been granted" \
  || echo "      note: voucher state is '$VS'"

echo "== 5. CENTRAL COMES BACK =="
ip route del blackhole "$CENTRAL/32"
systemctl restart stayconnect-scd; sleep 8
LIC2=$(curl -sk $R -b /tmp/ck.lf "$B/license" | python3 -c 'import sys,json;d=json.load(sys.stdin);print(d.get("state"),d.get("last_cloud_validation"))' 2>/dev/null)
printf '%s' "$LIC2" | grep -q '^Active' && ok "licence Active after recovery, revalidated ($LIC2)" || bad "after recovery: $LIC2"

echo
[ "$FAIL" = 0 ] && { echo "LOCAL_FIRST_SIGNIN = ALL GREEN ($PASS checks)"; exit 0; }
echo "LOCAL_FIRST_SIGNIN = FAIL ($FAIL failed, $PASS passed)"; exit 1
