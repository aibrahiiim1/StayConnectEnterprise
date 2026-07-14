#!/usr/bin/env bash
# Phase 9 — real Google OAuth E2E.
#
# Asserts:
#   1. Go unit tests for Google impl pass (request shape, success,
#      unverified-email path, token-error surfacing)
#   2. ctrlapi CRUD lifecycle for social_oauth_providers; secret never
#      leaked in GET
#   3. scd loads the configured provider on boot — log line shows
#      "registered provider=google kind=real"
#   4. Social-login metric families surface at /metrics with pre-touched
#      series for every (provider, result) combo
set -euo pipefail

BASE=${BASE:-http://127.0.0.1:8080}
ADMIN_EMAIL=${ADMIN_EMAIL:-admin@stayconnect.local}
ADMIN_PASS=${ADMIN_PASSWORD:-adminadmin01}
PSQL="docker exec -i stayconnect-pg psql -U stayconnect -d ${SC_DB:-stayconnect} -At -q -v ON_ERROR_STOP=1"
SCD_SOCK=${SCD_SOCK:-/run/stayconnect/scd.sock}

pass() { printf "  ✓ %s\n" "$1"; }
fail() { printf "  ✗ %s\n    %s\n" "$1" "${2:-}"; exit 1; }

TENANT_DEV=$(echo "SELECT id FROM tenants WHERE slug='dev';" | $PSQL | head -n1)
[[ -n "$TENANT_DEV" ]] || fail "no dev tenant"

CJ=$(mktemp); trap "rm -f $CJ; echo \"DELETE FROM social_oauth_providers WHERE display_name LIKE '9-test%';\" | $PSQL >/dev/null 2>&1 || true" EXIT

# ---- 1. Go unit tests cover the real Google impl ----
go_out=$(cd /opt/stayconnect/data-plane && go test ./internal/social 2>&1)
echo "$go_out" | grep -qE 'ok\s+.*internal/social' \
    && pass "Go unit tests pass for Google impl" \
    || fail "Go tests failed" "$go_out"

# ---- 2. ctrlapi CRUD ----
code=$(curl -s -o /dev/null -w '%{http_code}' -c "$CJ" -X POST \
    -H 'Content-Type: application/json' \
    -d "{\"email\":\"$ADMIN_EMAIL\",\"password\":\"$ADMIN_PASS\"}" \
    "$BASE/v1/auth/login")
[[ "$code" == "200" ]] || fail "admin login" "code=$code"

cr=$(curl -s -b "$CJ" -X POST "$BASE/v1/social-providers?tenant_id=$TENANT_DEV" \
    -H 'Content-Type: application/json' \
    -d '{"provider":"google","display_name":"9-test","client_id":"phase9.apps.googleusercontent.com","client_secret":"GOCSPX-secret","redirect_uri":"https://portal.stayconnect.local/auth/social/callback"}')
sp_id=$(jq -r '.id' <<<"$cr")
[[ -n "$sp_id" && "$sp_id" != "null" ]] && pass "create google provider" || fail "create" "$cr"

# secret never returned
echo "$cr" | jq -e 'has("client_secret") | not' >/dev/null \
    && pass "client_secret never returned in create response" \
    || fail "client_secret leaked" "$cr"
got=$(curl -s -b "$CJ" "$BASE/v1/social-providers/$sp_id?tenant_id=$TENANT_DEV")
echo "$got" | jq -e 'has("client_secret") | not' >/dev/null \
    && pass "client_secret never returned in GET" \
    || fail "client_secret leaked in GET" "$got"

# Dup enabled per (tenant, provider) → 409.
dup_code=$(curl -s -o /dev/null -w '%{http_code}' -b "$CJ" -X POST \
    "$BASE/v1/social-providers?tenant_id=$TENANT_DEV" \
    -H 'Content-Type: application/json' \
    -d '{"provider":"google","client_id":"x","client_secret":"y","redirect_uri":"https://x"}')
[[ "$dup_code" == "409" ]] && pass "dup enabled provider rejected (409)" \
                           || fail "expected 409 dup" "code=$dup_code"

# Patch toggle.
pcode=$(curl -s -o /dev/null -w '%{http_code}' -b "$CJ" -X PATCH \
    "$BASE/v1/social-providers/$sp_id?tenant_id=$TENANT_DEV" \
    -H 'Content-Type: application/json' -d '{"enabled":false}')
[[ "$pcode" == "200" ]] && pass "patch enabled=false" || fail "patch" "code=$pcode"
got_enabled=$(echo "SELECT enabled FROM social_oauth_providers WHERE id='$sp_id';" | $PSQL)
[[ "$got_enabled" == "f" ]] && pass "DB row reflects patch" || fail "patch didn't land" "got=$got_enabled"
# Re-enable for the loader test.
curl -s -o /dev/null -b "$CJ" -X PATCH \
    "$BASE/v1/social-providers/$sp_id?tenant_id=$TENANT_DEV" \
    -H 'Content-Type: application/json' -d '{"enabled":true}'

# ---- 3. scd loads the real provider on boot ----
systemctl reset-failed stayconnect-scd stayconnect-portald 2>/dev/null
systemctl restart stayconnect-scd stayconnect-portald
sleep 2
load_log=$(journalctl -u stayconnect-scd --since '15s ago' --no-pager | grep '"socialloader: registered"' | tail -n1)
[[ -n "$load_log" ]] && pass "scd logged 'socialloader: registered'" || fail "no load log"
echo "$load_log" | grep -q '"provider":"google"' && pass "google provider registered" || fail "wrong provider" "$load_log"
echo "$load_log" | grep -q '"kind":"real"' && pass "registered as kind=real (not stub)" || fail "wrong kind" "$load_log"

# ---- 4. metrics surface social-login families ----
m=$(curl -s --unix-socket "$SCD_SOCK" http://unix/metrics)
[[ "$m" == *"scd_social_login_total"* ]] && pass "scd_social_login_total exposed" \
                                          || fail "social_login_total missing"
grep -qE 'scd_social_login_total\{[^}]*provider="google"[^}]*result="ok"' <<<"$m" \
    && pass "pre-touched google/ok series present" \
    || fail "expected pre-touched series missing"
grep -qE 'scd_social_login_total\{[^}]*provider="google"[^}]*result="email_unverified"' <<<"$m" \
    && pass "pre-touched google/email_unverified series present" \
    || fail "email_unverified series missing"

# Cleanup
curl -s -o /dev/null -b "$CJ" -X DELETE "$BASE/v1/social-providers/$sp_id?tenant_id=$TENANT_DEV"

echo
echo "ALL GREEN"
