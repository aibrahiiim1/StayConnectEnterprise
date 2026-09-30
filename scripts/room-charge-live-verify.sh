#!/usr/bin/env bash
# ONE CONTROLLED REAL ROOM CHARGE, END TO END, ON THE APPLIANCE (run as root on PRE-LIVE).
#
#   bash room-charge-live-verify.sh --room <ROOM> --account-name <NAME> --package-code <CODE> --i-authorise-a-real-charge
#
# THIS POSTS A REAL CHARGE TO THE PMS. It exists so that verifying the room-charge path is one reviewed,
# repeatable procedure instead of a hand-typed one. Use it only with the Product Owner's authorisation for that
# appliance, and only against a house or test account the Front Office knows about (for example the cash
# "PASS" account), never a guest's reservation. The Front Office corrects the charge in the PMS afterwards; there
# is no programmatic reversal.
#
# What it does, as a real guest device would: a client on the guest network signs in with the room and the
# stay's reservation number (read from the mirror here, never printed), is offered Room charge, chooses it for
# the named package, and waits on the portal's own status endpoint while the posting engine, pmsd and the PMS
# decide. It then prints what the appliance recorded -- posting, attempt, PMS answer, settlement, purchase and
# access -- and removes the test device and the access it received. It never retries: an UNKNOWN answer is
# left for Manual review, exactly as the product does.
set -uo pipefail

ROOM=""; ACCT=""; STAYID=""; PKG=""; OK_REAL=0
while [ $# -gt 0 ]; do
  case "$1" in
    --room) ROOM="$2"; shift 2;;
    --account-name) ACCT="$2"; shift 2;;
    --stay-id) STAYID="$2"; shift 2;;
    --package-code) PKG="$2"; shift 2;;
    --i-authorise-a-real-charge) OK_REAL=1; shift;;
    *) echo "unknown argument: $1" >&2; exit 2;;
  esac
done
[ -n "$ROOM" ] && { [ -n "$ACCT" ] || [ -n "$STAYID" ]; } && [ -n "$PKG" ] || { echo "usage: --room ROOM (--account-name NAME | --stay-id UUID) --package-code CODE --i-authorise-a-real-charge" >&2; exit 2; }
[ "$OK_REAL" = 1 ] || { echo "REFUSED: this posts a real charge; pass --i-authorise-a-real-charge" >&2; exit 2; }

R='--resolve hotel.stayconnect.local:443:127.0.0.1'
B=https://hotel.stayconnect.local/api/edge/v1
PSQL="docker exec stayconnect-pg psql -U stayconnect -d stayconnect_site -tAc"
CK=/tmp/ck.rclv; NSN=rclv
GB=$($PSQL "SELECT bridge_name FROM guest_networks WHERE enabled ORDER BY created_at LIMIT 1")
GW=$($PSQL "SELECT host(gateway_ip) FROM guest_networks WHERE enabled ORDER BY created_at LIMIT 1")
PORTAL="http://$GW:8380"
PROBE=http://connectivitycheck.gstatic.com/generate_204
curl -sk $R -c $CK -o /dev/null -H 'Content-Type: application/json' -d '{"email":"admin","password":"admin"}' $B/auth/login
AM0=$($PSQL "SELECT auth_methods::text FROM tenants LIMIT 1")
ENT=""
cleanup() {
  ip netns exec $NSN curl -s -o /dev/null -m 5 -X POST "$PORTAL/logout" 2>/dev/null
  ip netns exec $NSN dhclient -r -lf /tmp/$NSN.lease -pf /tmp/$NSN.pid ${NSN}c >/dev/null 2>&1
  ip netns del $NSN 2>/dev/null; ip link del ${NSN}a 2>/dev/null; rm -rf /etc/netns/$NSN
  [ -n "$ENT" ] && $PSQL "SELECT iam_v2.apply_entitlement_transition('$ENT','TERMINATED',now(),'ADMIN')" >/dev/null 2>&1
  curl -sk $R -b $CK -o /dev/null -X PUT -H 'Content-Type: application/json' -d "$AM0" "$B/auth-methods"
  rm -f $CK
}
trap cleanup EXIT

# A ROOM CODE IS NOT AN ACCOUNT. A house room code such as PASS also carries real walk-in customers' reservations,
# so the stay must ALSO be held by the named house account alone, and be the only such stay.
if [ -n "$STAYID" ]; then
  # A guest reservation the Product Owner named: identified by the stay id, so no name travels on a command line.
  STAY=$($PSQL "SELECT s.id FROM iam_v2.stays s WHERE s.id='$STAYID'::uuid AND s.status='IN_HOUSE' AND s.posting_allowed AND s.normalized_room_number='$ROOM'")
  [ -n "$STAY" ] || { echo "REFUSED: stay $STAYID is not an in-house, postable stay in room $ROOM"; exit 1; }
else
STAY=$($PSQL "SELECT s.id FROM iam_v2.stays s WHERE s.status='IN_HOUSE' AND s.posting_allowed AND s.normalized_room_number='$ROOM' AND EXISTS (SELECT 1 FROM iam_v2.stay_guests g WHERE g.stay_id=s.id AND g.last_name_norm=upper('$ACCT')) AND NOT EXISTS (SELECT 1 FROM iam_v2.stay_guests g WHERE g.stay_id=s.id AND g.last_name_norm<>upper('$ACCT')) LIMIT 2")
[ "$(printf '%s\n' "$STAY" | grep -c .)" = 1 ] || { echo "REFUSED: room $ROOM does not hold exactly one in-house, postable stay of account $ACCT"; exit 1; }
fi
RES=$($PSQL "SELECT external_reservation_id FROM iam_v2.stays WHERE id='$STAY'")
REV=$($PSQL "SELECT p.current_revision_id FROM iam_v2.internet_packages p JOIN iam_v2.internet_package_revisions r ON r.id=p.current_revision_id WHERE p.active AND p.code='$PKG' AND 'PMS_POSTING'=ANY(r.settlement_methods)")
[ -n "$REV" ] || { echo "REFUSED: package $PKG is not an active room-charge package"; exit 1; }
echo "== stay ${STAY:0:8} in room $ROOM; package $PKG (${REV:0:8})"

# Room sign-in must be on for the device to reach the offer; the site's own setting is restored on exit.
PMSON=$(printf '%s' "$AM0" | python3 -c 'import sys,json;p=(json.load(sys.stdin).get("pms") or {});p["enabled"]=True;p.setdefault("mode","room_any");p.setdefault("provider","protel-fias");print(json.dumps({"pms":p}))')
curl -sk $R -b $CK -o /dev/null -X PUT -H 'Content-Type: application/json' -d "$PMSON" "$B/auth-methods"

ip link add ${NSN}a type veth peer name ${NSN}c; ip link set ${NSN}a master "$GB"; ip link set ${NSN}a up
ip netns add $NSN; ip link set ${NSN}c netns $NSN; ip netns exec $NSN ip link set lo up; ip netns exec $NSN ip link set ${NSN}c up
sleep 1; ip netns exec $NSN timeout 25 dhclient -1 -lf /tmp/$NSN.lease -pf /tmp/$NSN.pid ${NSN}c >/dev/null 2>&1
mkdir -p /etc/netns/$NSN; echo "nameserver $GW" > /etc/netns/$NSN/resolv.conf
ip netns exec $NSN ping -c1 -W2 "$GW" >/dev/null 2>&1

body(){ python3 -c 'import json,sys,uuid;d={"room":sys.argv[1],"verification":sys.argv[2],"request_id":str(uuid.uuid4())};
[d.update({k:v}) for k,v in zip(("auth_context_id","package_revision_id","method"),sys.argv[3:]) if v];print(json.dumps(d))' "$@"; }
J=$(ip netns exec $NSN curl -s -H 'Content-Type: application/json' -d "$(body "$ROOM" "$RES")" "$PORTAL/auth/pms/phase3")
printf '%s' "$J" | grep -q '"method":"PMS_POSTING"' || { echo "FAIL: room charge was not offered: $(printf '%s' "$J" | head -c 200)"; exit 1; }
ACX=$(printf '%s' "$J" | python3 -c 'import sys,json;print(json.load(sys.stdin)["auth_context_id"])')
echo "   offered: $(printf '%s' "$J" | python3 -c 'import sys,json;print(", ".join(c["method"]+" "+(c.get("price") or "free") for c in json.load(sys.stdin)["choices"]))')"

T0=$($PSQL "SELECT now()")
C=$(ip netns exec $NSN curl -s -H 'Content-Type: application/json' -d "$(body "$ROOM" "$RES" "$ACX" "$REV" PMS_POSTING)" "$PORTAL/auth/pms/phase3")
TO=$(printf '%s' "$C" | python3 -c 'import sys,json;print(json.load(sys.stdin).get("redirect_to",""))' 2>/dev/null)
P=$(printf '%s' "$TO" | sed -n 's/.*[?&]p=\([0-9a-f-]*\).*/\1/p')
[ -n "$P" ] || { echo "FAIL: choosing room charge did not start a purchase: $(printf '%s' "$C" | head -c 200)"; exit 1; }
echo "== purchase ${P:0:8} started; waiting on the portal's status endpoint"
ST=""
for i in $(seq 1 60); do
  S=$(ip netns exec $NSN curl -s "$PORTAL/api/pay/status?p=$P")
  N=$(printf '%s' "$S" | python3 -c 'import sys,json;print(json.load(sys.stdin).get("state",""))' 2>/dev/null)
  [ "$N" != "$ST" ] && { echo "   $(date -u +%H:%M:%S) state=$N"; ST=$N; }
  case "$N" in connected|failed|unknown|manual_review) break;; esac
  sleep 2
done

echo "== what the appliance recorded"
$PSQL "SELECT 'posting '||left(p.id::text,8)||' '||p.posting_type||' outbox='||coalesce(o.state,'-')||' amount='||p.amount_minor||' '||p.currency
         FROM iam_v2.pms_postings p LEFT JOIN iam_v2.posting_outbox o ON o.posting_id=p.id
        WHERE p.stay_id='$STAY' AND p.created_at >= '$T0'"
$PSQL "SELECT 'attempt #'||a.attempt_no||' P#'||a.p_number||' rn_is_room='||(a.rn='$ROOM')||' g_is_reservation='||(a.g_number='$RES')||' outcome='||a.outcome||' PA='||coalesce(a.pa_as_status,'-')||' sent='||to_char(a.sent_at,'HH24:MI:SS')||' answered='||coalesce(to_char(a.response_at,'HH24:MI:SS'),'-')
         FROM iam_v2.posting_attempts a JOIN iam_v2.pms_postings p ON p.id=a.internal_posting_id
        WHERE p.stay_id='$STAY' AND p.created_at >= '$T0' ORDER BY a.attempt_no"
$PSQL "SELECT 'settlement '||s.method||' '||s.status||' | purchase '||pu.state FROM iam_v2.purchases pu JOIN iam_v2.settlements s ON s.purchase_id=pu.id WHERE pu.id='$P'"
ENT=$($PSQL "SELECT id FROM iam_v2.entitlements WHERE purchase_id='$P'")
echo "   access: entitlement=${ENT:0:8} internet=$(ip netns exec $NSN curl -s -o /dev/null -w '%{http_code}' -m 8 "$PROBE")"
$PSQL "SELECT 'stay blocks open: '||count(*) FROM iam_v2.stay_posting_blocks WHERE stay_id='$STAY' AND cleared_at IS NULL"
[ "$ST" = connected ] && echo "ROOM_CHARGE_LIVE = POSTED_AND_CONNECTED" || echo "ROOM_CHARGE_LIVE = $ST (see the recorded answer above; nothing is retried)"
