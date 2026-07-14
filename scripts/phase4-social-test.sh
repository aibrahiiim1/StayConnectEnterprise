#!/usr/bin/env bash
# Phase 4.3 — Social login (stubbed Google) E2E.
set -euo pipefail

PSQL="docker exec -i stayconnect-pg psql -U stayconnect -d stayconnect -At -q -v ON_ERROR_STOP=1"
TENANT_DEV=$(echo "SELECT id FROM tenants WHERE slug='dev';" | $PSQL | head -n1)

pass() { printf "  ✓ %s\n" "$1"; }
fail() { printf "  ✗ %s\n    %s\n" "$1" "${2:-}"; exit 1; }
ic()   { ip netns exec client1 curl -s "$@"; }

# Reset client state so prior runs don't leak.
CIP=$(ip -n client1 -4 addr show eth0 | awk '/inet / {print $2}' | cut -d/ -f1)
CMAC=$(ip -n client1 link show eth0 | awk '/link\/ether/ {print $2}')
curl -s --unix-socket /run/stayconnect/scd.sock -X POST -H 'Content-Type: application/json' \
    -d "{\"ip\":\"$CIP\",\"reason\":\"admin\"}" http://unix/v1/sessions/revoke >/dev/null
echo "DELETE FROM social_oauth_states WHERE client_ip = '$CIP'::inet;" | $PSQL >/dev/null
echo "  client ip = $CIP, mac = $CMAC"

echo
echo "== auth-methods includes social.google =="
ic -o /tmp/am.json -w "" http://10.10.0.1:8380/api/auth-methods
[[ "$(jq -r '.social.google.enabled' /tmp/am.json)" == "true" ]] && pass "google enabled" || fail "google not enabled" "$(cat /tmp/am.json)"

# ---- helper: drive a full social flow; echoes the resulting session_id ------
drive_social() {
    local email=$1 verified=${2:-true}
    # 1. start (via scd directly so we can grab state without browser scraping)
    local resp; resp=$(curl -s --unix-socket /run/stayconnect/scd.sock -X POST \
        -H 'Content-Type: application/json' \
        -d "{\"provider\":\"google\",\"ip\":\"$CIP\",\"mac\":\"$CMAC\",\"redirect_uri\":\"http://portal.stayconnect.local:8380/auth/social/callback\"}" \
        http://unix/v1/auth/social/start)
    local state=$(echo "$resp" | jq -r .state)
    local auth_url=$(echo "$resp" | jq -r .authorize_url)
    [[ -n "$state" && "$state" != "null" ]] || { echo "FAIL: no state from start: $resp" >&2; return 1; }

    # 2. fetch stub authorize with auto=1 + chosen identity → 302 to /auth/social/callback → 302 to /success
    ic -L -o /dev/null -w "%{http_code}|%{url_effective}\n" \
        "${auth_url}&email=${email}&email_verified=${verified}&auto=1" > /tmp/social_drive.txt
    cat /tmp/social_drive.txt
}

echo
echo "== happy path: alice@example.com, email_verified=true =="
result=$(drive_social "alice+$(date +%s)@example.com" "true")
code=${result%%|*}; final=${result##*|}
echo "  final: $code  $final"
if [[ "$code" == "200" && "$final" == *"/success"* ]]; then
    pass "redirected to /success"
else
    fail "happy path" "code=$code final=$final"
fi

# Verify session created with email + email_verified_at.
echo
echo "== session row stamped =="
echo "SELECT s.state, g.email, g.email_verified_at IS NOT NULL AS verified
        FROM sessions s JOIN guests g ON g.id = s.guest_id
        WHERE s.tenant_id='$TENANT_DEV' AND s.state='active'
        ORDER BY s.started_at DESC LIMIT 1;" | $PSQL | sed 's/^/  /'

# Revoke for next test.
curl -s --unix-socket /run/stayconnect/scd.sock -X POST -H 'Content-Type: application/json' \
    -d "{\"ip\":\"$CIP\",\"reason\":\"admin\"}" http://unix/v1/sessions/revoke >/dev/null

echo
echo "== email_verified=false is rejected by callback =="
result=$(drive_social "spammer+$(date +%s)@example.com" "false")
code=${result%%|*}; final=${result##*|}
# When the provider says verified=false the callback returns 403 and renders an error HTML.
[[ "$code" == "403" ]] && pass "unverified email blocked (HTTP 403)" || fail "unverified gate" "code=$code final=$final"

echo
echo "== reuse same state fails (CSRF / replay) =="
# Drive again, reuse the SAME state on a second callback hit.
resp=$(curl -s --unix-socket /run/stayconnect/scd.sock -X POST \
    -H 'Content-Type: application/json' \
    -d "{\"provider\":\"google\",\"ip\":\"$CIP\",\"mac\":\"$CMAC\",\"redirect_uri\":\"http://portal.stayconnect.local:8380/auth/social/callback\"}" \
    http://unix/v1/auth/social/start)
ST=$(echo "$resp" | jq -r .state)
AU=$(echo "$resp" | jq -r .authorize_url)
EM="bob+$(date +%s)@example.com"
# First exchange — succeed.
ic -L -o /dev/null -w "first=%{http_code}\n" \
    "${AU}&email=${EM}&email_verified=true&auto=1"
# Revoke for next request.
curl -s --unix-socket /run/stayconnect/scd.sock -X POST -H 'Content-Type: application/json' \
    -d "{\"ip\":\"$CIP\",\"reason\":\"admin\"}" http://unix/v1/sessions/revoke >/dev/null

# Second attempt with same state — must fail with consumed.
ic -L -o /dev/null -w "second=%{http_code}\n" \
    "${AU}&email=${EM}&email_verified=true&auto=1"
# (Should land on the callback, which sees consumed state and returns 409 → portald renders error page.)
got=$(ic -L -o /tmp/replay.html -w '%{http_code}' \
    "${AU}&email=${EM}&email_verified=true&auto=1")
[[ "$got" == "409" ]] && pass "replay blocked (state consumed)" || fail "replay" "code=$got"

echo
echo "== bogus state is rejected (CSRF) =="
got=$(ic -o /dev/null -w '%{http_code}' \
    "http://portal.stayconnect.local:8380/auth/social/callback?provider=google&state=deadbeef&code=foo")
# Callback proxies to scd which returns 400 'unknown state'; portald passes the
# status through unchanged for non-200.
[[ "$got" == "400" ]] && pass "unknown state → 400" || fail "csrf gate" "code=$got"

echo
echo "== expired state is rejected =="
# Insert an expired state row directly, then hit callback.
EXPSTATE="expired_$(date +%s)"
echo "INSERT INTO social_oauth_states (state, tenant_id, provider, redirect_uri, expires_at, client_ip, client_mac)
      VALUES ('$EXPSTATE', '$TENANT_DEV', 'google', 'http://portal.stayconnect.local:8380/auth/social/callback',
              now() - interval '1 minute', '$CIP'::inet, '$CMAC'::macaddr);" | $PSQL >/dev/null
EXPCODE=$(node -e "process.stdout.write(Buffer.from(JSON.stringify({Sub:'x',Email:'e@e.com',EmailVerified:true})).toString('base64url'))")
got=$(ic -o /dev/null -w '%{http_code}' \
    "http://portal.stayconnect.local:8380/auth/social/callback?provider=google&state=$EXPSTATE&code=$EXPCODE")
[[ "$got" == "410" ]] && pass "expired state → 410" || fail "expiry gate" "code=$got"

echo
echo "ALL GREEN"
