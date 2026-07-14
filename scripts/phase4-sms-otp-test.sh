#!/usr/bin/env bash
# Phase 4.2 — SMS OTP smoke tests.
# Mirrors the email flow but for SMS, plus phone normalization checks.
set -euo pipefail

PSQL="docker exec -i stayconnect-pg psql -U stayconnect -d stayconnect -At -q -v ON_ERROR_STOP=1"
TENANT_DEV=$(echo "SELECT id FROM tenants WHERE slug='dev';" | $PSQL | head -n1)

pass() { printf "  ✓ %s\n" "$1"; }
fail() { printf "  ✗ %s\n    %s\n" "$1" "${2:-}"; exit 1; }
ic()   { ip netns exec client1 curl -s "$@"; }

last_sms_for() {
    local phone=$1
    grep -A1 "To=$phone " /var/log/stayconnect/otp-sms.log | tail -1 | grep -oE '[0-9]{6}' | head -n1
}

CIP=$(ip -n client1 -4 addr show eth0 | awk '/inet / {print $2}' | cut -d/ -f1)
curl -s --unix-socket /run/stayconnect/scd.sock -X POST \
    -H 'Content-Type: application/json' \
    -d "{\"ip\":\"$CIP\",\"reason\":\"admin\"}" \
    http://unix/v1/sessions/revoke >/dev/null
echo "DELETE FROM auth_otps WHERE ip = '$CIP'::inet;" | $PSQL >/dev/null
echo "  client ip = $CIP"

echo
echo "== auth-methods includes sms =="
ic -o /tmp/am.json -w "" http://10.10.0.1:8380/api/auth-methods
[[ "$(jq -r '.sms.enabled' /tmp/am.json)" == "true" ]] && pass "sms enabled" || fail "sms not enabled" "$(cat /tmp/am.json)"

echo
echo "== phone normalization rejects malformed input =="
for bad in '5551234567' '+12' 'phone' '+1-abc-def-ghij'; do
    ic -o /tmp/bad.json -w "" -X POST -H 'Content-Type: application/json' \
        -d "{\"channel\":\"sms\",\"destination\":\"$bad\"}" http://portal.stayconnect.local/auth/otp/request
    if [[ "$(jq -r '.error' /tmp/bad.json)" == invalid\ phone:* ]]; then
        pass "rejected: $bad"
    else
        fail "should have rejected: $bad" "$(cat /tmp/bad.json)"
    fi
done

echo
echo "== request OTP for +1 (555) 123-4567 → normalizes to +15551234567 =="
RAW='+1 (555) 123-4567'
NORM='+15551234567'
ic -o /tmp/req.json -w "  http=%{http_code}\n" -X POST -H 'Content-Type: application/json' \
    -d "{\"channel\":\"sms\",\"destination\":\"$RAW\"}" http://portal.stayconnect.local/auth/otp/request
CHID=$(jq -r .challenge_id /tmp/req.json)
[[ -n "$CHID" && "$CHID" != "null" ]] || fail "no challenge_id" "$(cat /tmp/req.json)"
pass "challenge_id received"

# Confirm DB stored the normalized destination.
STORED=$(echo "SELECT destination FROM auth_otps WHERE id='$CHID';" | $PSQL | head -n1)
[[ "$STORED" == "$NORM" ]] && pass "stored normalized: $STORED" || fail "stored wrong" "got=$STORED want=$NORM"

echo
echo "== sms-stub captured the code =="
sleep 0.3
CODE=$(last_sms_for "$NORM")
[[ -n "$CODE" ]] || fail "code not in sms log"
pass "sms stub logged code: $CODE"

echo
echo "== verify wrong code =="
ic -o /tmp/vbad.json -w "  http=%{http_code}\n" -X POST -H 'Content-Type: application/json' \
    -d "{\"challenge_id\":\"$CHID\",\"code\":\"000000\"}" http://portal.stayconnect.local/auth/otp/verify
[[ "$(jq -r .error /tmp/vbad.json)" == "incorrect code" ]] && pass "wrong code rejected" || fail "wrong code not rejected" "$(cat /tmp/vbad.json)"

echo
echo "== verify correct code =="
ic -o /tmp/vok.json -w "  http=%{http_code}\n" -X POST -H 'Content-Type: application/json' \
    -d "{\"challenge_id\":\"$CHID\",\"code\":\"$CODE\"}" http://portal.stayconnect.local/auth/otp/verify
SID=$(jq -r .session_id /tmp/vok.json)
[[ -n "$SID" && "$SID" != "null" ]] || fail "verify failed" "$(cat /tmp/vok.json)"
pass "session_id=$SID"

echo
echo "== guest row stamped with phone + phone_verified_at =="
echo "SELECT g.phone, g.phone_verified_at IS NOT NULL AS verified
        FROM sessions s JOIN guests g ON g.id = s.guest_id
        WHERE s.id = '$SID';" | $PSQL | sed 's/^/  /'

# Revoke for subsequent tests via portal.
curl -s --unix-socket /run/stayconnect/scd.sock -X POST \
    -H 'Content-Type: application/json' \
    -d "{\"ip\":\"$CIP\",\"reason\":\"admin\"}" \
    http://unix/v1/sessions/revoke >/dev/null

echo
echo "== cooldown blocks immediate re-issue (same phone) =="
PHONE2='+15555550100'
ic -o /dev/null -w "" -X POST -H 'Content-Type: application/json' \
    -d "{\"channel\":\"sms\",\"destination\":\"$PHONE2\"}" http://portal.stayconnect.local/auth/otp/request
ic -o /tmp/cool.json -w "  http=%{http_code}\n" -X POST -H 'Content-Type: application/json' \
    -d "{\"channel\":\"sms\",\"destination\":\"$PHONE2\"}" http://portal.stayconnect.local/auth/otp/request
[[ "$(jq -r .error /tmp/cool.json)" == "wait before requesting another code" ]] && pass "cooldown enforced" || fail "cooldown not enforced" "$(cat /tmp/cool.json)"

echo
echo "== per-IP cap fires when 21 issues from same client (different dests) =="
# We've already issued some — push to 21 total to trigger IPHourlyCap=20.
COUNT=$(echo "SELECT count(*) FROM auth_otps WHERE ip = '$CIP'::inet AND issued_at > now() - interval '1 hour';" | $PSQL | head -n1)
NEED=$((21 - COUNT))
if (( NEED > 0 )); then
    for i in $(seq 1 $NEED); do
        ic -o /dev/null -w "" -X POST -H 'Content-Type: application/json' \
            -d "{\"channel\":\"sms\",\"destination\":\"+155555502$(printf '%02d' $i)\"}" \
            http://portal.stayconnect.local/auth/otp/request
    done
fi
ic -o /tmp/iphit.json -w "  http=%{http_code}\n" -X POST -H 'Content-Type: application/json' \
    -d "{\"channel\":\"sms\",\"destination\":\"+15555559999\"}" http://portal.stayconnect.local/auth/otp/request
case "$(jq -r '.error' /tmp/iphit.json)" in
    "too many requests from your network") pass "per-IP cap enforced" ;;
    "too many requests this hour")          pass "per-destination cap fired first (still rate-limited)" ;;
    *) fail "expected ip rate limit" "$(cat /tmp/iphit.json)" ;;
esac

echo
echo "ALL GREEN"
