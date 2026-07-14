#!/usr/bin/env bash
# Phase 4.1 — Email OTP smoke tests.
# Assumes phase1 client1 netns is up and has a DHCP lease.
set -euo pipefail

PSQL="docker exec -i stayconnect-pg psql -U stayconnect -d stayconnect -At -q -v ON_ERROR_STOP=1"
TENANT_DEV=$(echo "SELECT id FROM tenants WHERE slug='dev';" | $PSQL | head -n1)

pass() { printf "  ✓ %s\n" "$1"; }
fail() { printf "  ✗ %s\n    %s\n" "$1" "${2:-}"; exit 1; }
ic()   { ip netns exec client1 curl -s "$@"; }

# Latest OTP code for an email address (read from the mail-stub log).
last_code_for() {
    local email=$1
    grep -A1 "To=$email " /var/log/stayconnect/otp-mail.log | tail -1 | grep -oE '[0-9]{6}' | head -n1
}

echo "== probe /api/auth-methods =="
curl -s http://10.10.0.1:8380/api/auth-methods > /tmp/am.json
jq '{voucher: .voucher.enabled, email: .email.enabled, email_template: .email.template_id}' /tmp/am.json | sed 's/^/  /'
[[ "$(jq -r '.email.enabled' /tmp/am.json)" == "true" ]] || fail "email not enabled in tenant cfg"
pass "auth-methods endpoint reachable"

# Tear down any previous session for this client IP so we have a clean slate,
# AND wipe prior OTP rows so the per-IP rate-limit doesn't carry over.
CIP=$(ip -n client1 -4 addr show eth0 | awk '/inet / {print $2}' | cut -d/ -f1)
curl -s --unix-socket /run/stayconnect/scd.sock -X POST \
    -H 'Content-Type: application/json' \
    -d "{\"ip\":\"$CIP\",\"reason\":\"admin\"}" \
    http://unix/v1/sessions/revoke >/dev/null
echo "DELETE FROM auth_otps WHERE ip = '$CIP'::inet;" | $PSQL >/dev/null
echo "  client ip = $CIP"

EMAIL="alice+$(date +%s)@example.com"

echo
echo "== request OTP =="
ic -o /tmp/req.json -w "  http=%{http_code}\n" -X POST -H 'Content-Type: application/json' \
    -d "{\"channel\":\"email\",\"destination\":\"$EMAIL\"}" http://portal.stayconnect.local/auth/otp/request
CHID=$(jq -r .challenge_id /tmp/req.json)
TTL=$(jq -r .ttl_seconds /tmp/req.json)
[[ -n "$CHID" && "$CHID" != "null" ]] || fail "no challenge_id" "$(cat /tmp/req.json)"
pass "challenge_id received (ttl ${TTL}s)"

echo
echo "== mail-stub captured the code =="
sleep 0.3
CODE=$(last_code_for "$EMAIL")
[[ -n "$CODE" ]] || fail "code not found in mail log"
pass "mail stub logged code: $CODE"

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
echo "== session row + guest contact =="
echo "SELECT s.state, g.email, g.email_verified_at IS NOT NULL AS verified
        FROM sessions s JOIN guests g ON g.id = s.guest_id
        WHERE s.id = '$SID';" | $PSQL | sed 's/^/  /'

# Revoke the auth so the client can hit the captive portal again for the
# remaining tests. Subsequent OTP issues will go via the portal (DNAT'd).
curl -s --unix-socket /run/stayconnect/scd.sock -X POST \
    -H 'Content-Type: application/json' \
    -d "{\"ip\":\"$CIP\",\"reason\":\"admin\"}" \
    http://unix/v1/sessions/revoke >/dev/null

echo
echo "== reuse same challenge fails (single-use) — direct via scd socket =="
curl -s --unix-socket /run/stayconnect/scd.sock -o /tmp/vreuse.json -w "  http=%{http_code}\n" \
    -X POST -H 'Content-Type: application/json' \
    -d "{\"challenge_id\":\"$CHID\",\"code\":\"$CODE\",\"ip\":\"$CIP\",\"mac\":\"aa:bb:cc:dd:ee:ff\"}" \
    http://unix/v1/sessions/authorize-otp
[[ "$(jq -r .error /tmp/vreuse.json)" == "code already used" ]] && pass "reuse blocked" || fail "reuse should be blocked" "$(cat /tmp/vreuse.json)"

echo
echo "== rate limit: cooldown blocks immediate re-issue =="
EMAIL2="bob+$(date +%s)@example.com"
ic -o /dev/null -w "" -X POST -H 'Content-Type: application/json' \
    -d "{\"channel\":\"email\",\"destination\":\"$EMAIL2\"}" http://portal.stayconnect.local/auth/otp/request
ic -o /tmp/cool.json -w "  http=%{http_code}\n" -X POST -H 'Content-Type: application/json' \
    -d "{\"channel\":\"email\",\"destination\":\"$EMAIL2\"}" http://portal.stayconnect.local/auth/otp/request
[[ "$(jq -r .error /tmp/cool.json)" == "wait before requesting another code" ]] && pass "cooldown enforced" || fail "cooldown not enforced" "$(cat /tmp/cool.json)"

echo
echo "== attempt cap: 5 wrong codes locks the challenge =="
EMAIL3="carol+$(date +%s)@example.com"
ic -o /tmp/iss3.json -w "" -X POST -H 'Content-Type: application/json' \
    -d "{\"channel\":\"email\",\"destination\":\"$EMAIL3\"}" http://portal.stayconnect.local/auth/otp/request
CHID3=$(jq -r .challenge_id /tmp/iss3.json)
for i in 1 2 3 4 5; do
    ic -o /tmp/att.json -w "" -X POST -H 'Content-Type: application/json' \
        -d "{\"challenge_id\":\"$CHID3\",\"code\":\"000000\"}" http://portal.stayconnect.local/auth/otp/verify
done
ic -o /tmp/att6.json -w "" -X POST -H 'Content-Type: application/json' \
    -d "{\"challenge_id\":\"$CHID3\",\"code\":\"000000\"}" http://portal.stayconnect.local/auth/otp/verify
[[ "$(jq -r .error /tmp/att6.json)" == "too many wrong attempts" ]] && pass "attempt cap enforced" || fail "attempt cap" "$(cat /tmp/att6.json)"

echo
echo "ALL GREEN"
