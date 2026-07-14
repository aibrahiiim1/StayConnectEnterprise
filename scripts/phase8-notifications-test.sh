#!/usr/bin/env bash
# Phase 8 — real notification providers E2E.
#
# Asserts:
#   1. Go unit tests for SendGrid + Twilio impls pass (request shape +
#      auth + error-message extraction)
#   2. ctrlapi CRUD lifecycle: create email + sms providers, list, patch,
#      delete; secrets never leak in GET responses
#   3. scd loads the configured provider on boot — log line shows the
#      resolved kind per channel
#   4. Notification metrics are pre-touched and visible at /metrics
#   5. notification_providers.last_success_at / last_error_at are
#      updated by the notifyloader wrapper after a Send (use the stub
#      kind to avoid hitting external APIs)
set -euo pipefail

BASE=${BASE:-http://127.0.0.1:8080}
ADMIN_EMAIL=${ADMIN_EMAIL:-admin@stayconnect.local}
ADMIN_PASS=${ADMIN_PASSWORD:-adminadmin01}
PSQL="docker exec -i stayconnect-pg psql -U stayconnect -d stayconnect -At -q -v ON_ERROR_STOP=1"
SCD_SOCK=${SCD_SOCK:-/run/stayconnect/scd.sock}

pass() { printf "  ✓ %s\n" "$1"; }
fail() { printf "  ✗ %s\n    %s\n" "$1" "${2:-}"; exit 1; }

TENANT_DEV=$(echo "SELECT id FROM tenants WHERE slug='dev';" | $PSQL | head -n1)
[[ -n "$TENANT_DEV" ]] || fail "no dev tenant"

# Clear per-IP rate-limit state for the ad-hoc test IP used below.
# Without this, running phase8 after earlier phases (which also fire
# OTP/PMS traffic) trips the per-network throttle silently, the Send
# never actually runs, and `last_success_at` stays NULL.
TEST_IP=10.10.0.205
echo "DELETE FROM auth_otps   WHERE ip = '$TEST_IP'::inet;" | $PSQL >/dev/null 2>&1 || true
echo "DELETE FROM pms_attempts WHERE ip = '$TEST_IP'::inet;" | $PSQL >/dev/null 2>&1 || true

CJ=$(mktemp); trap "rm -f $CJ; echo \"DELETE FROM notification_providers WHERE tenant_id='$TENANT_DEV' AND display_name LIKE '5.7%' OR display_name LIKE '8-test%';\" | $PSQL >/dev/null 2>&1 || true" EXIT

# ---- 1. Go unit tests cover the real provider impls ----
go_out=$(cd /opt/stayconnect/data-plane && go test ./internal/mail ./internal/sms 2>&1)
echo "$go_out" | grep -qE 'ok\s+.*internal/mail' && echo "$go_out" | grep -qE 'ok\s+.*internal/sms' \
    && pass "Go unit tests pass for SendGrid + Twilio impls" \
    || fail "Go tests failed" "$go_out"

# ---- 2. ctrlapi CRUD ----
code=$(curl -s -o /dev/null -w '%{http_code}' -c "$CJ" -X POST \
    -H 'Content-Type: application/json' \
    -d "{\"email\":\"$ADMIN_EMAIL\",\"password\":\"$ADMIN_PASS\"}" \
    "$BASE/v1/auth/login")
[[ "$code" == "200" ]] || fail "admin login" "code=$code"

# Create an email row pointing at the stub kind (so scd can use it
# without hitting a real API).
em=$(curl -s -b "$CJ" -X POST "$BASE/v1/notification-providers?tenant_id=$TENANT_DEV" \
    -H 'Content-Type: application/json' \
    -d '{"channel":"email","kind":"stub","display_name":"8-test-email"}')
em_id=$(jq -r '.id' <<<"$em")
[[ -n "$em_id" && "$em_id" != "null" ]] && pass "create email provider" \
                                       || fail "create email" "$em"

sm=$(curl -s -b "$CJ" -X POST "$BASE/v1/notification-providers?tenant_id=$TENANT_DEV" \
    -H 'Content-Type: application/json' \
    -d '{"channel":"sms","kind":"stub","display_name":"8-test-sms"}')
sm_id=$(jq -r '.id' <<<"$sm")
[[ -n "$sm_id" && "$sm_id" != "null" ]] && pass "create sms provider" \
                                       || fail "create sms" "$sm"

# Dup (same channel, enabled) → 409.
dup_code=$(curl -s -o /dev/null -w '%{http_code}' -b "$CJ" -X POST \
    "$BASE/v1/notification-providers?tenant_id=$TENANT_DEV" \
    -H 'Content-Type: application/json' \
    -d '{"channel":"email","kind":"stub","display_name":"8-test-email-dup"}')
[[ "$dup_code" == "409" ]] && pass "dup enabled provider per channel rejected (409)" \
                           || fail "expected 409 dup" "code=$dup_code"

# Secrets: GET response must NOT include api_key.
got=$(curl -s -b "$CJ" "$BASE/v1/notification-providers/$em_id?tenant_id=$TENANT_DEV")
echo "$got" | jq -e 'has("api_key") | not' >/dev/null \
    && pass "secret api_key never returned in GET" \
    || fail "api_key leaked" "$got"

# Patch: toggle enabled.
pcode=$(curl -s -o /dev/null -w '%{http_code}' -b "$CJ" -X PATCH \
    "$BASE/v1/notification-providers/$em_id?tenant_id=$TENANT_DEV" \
    -H 'Content-Type: application/json' \
    -d '{"enabled":false}')
[[ "$pcode" == "200" ]] && pass "patch enabled=false" || fail "patch failed" "code=$pcode"
got_enabled=$(echo "SELECT enabled FROM notification_providers WHERE id='$em_id';" | $PSQL)
[[ "$got_enabled" == "f" ]] && pass "DB row reflects patch" || fail "patch didn't land" "got=$got_enabled"

# Re-enable for the rest of the test.
curl -s -o /dev/null -b "$CJ" -X PATCH "$BASE/v1/notification-providers/$em_id?tenant_id=$TENANT_DEV" \
    -H 'Content-Type: application/json' -d '{"enabled":true}'

# ---- 3. scd loads providers on restart ----
systemctl reset-failed stayconnect-scd stayconnect-portald 2>/dev/null
systemctl restart stayconnect-scd stayconnect-portald
sleep 2
load_log=$(journalctl -u stayconnect-scd --since '15s ago' --no-pager | grep '"notification providers loaded"' | tail -n1)
[[ -n "$load_log" ]] && pass "scd logged 'notification providers loaded'" || fail "no load log"
# Both channels resolve to "stub" since that's what we configured.
echo "$load_log" | grep -q '"email":"stub"' && pass "email resolved to stub" || fail "email resolution" "$load_log"
echo "$load_log" | grep -q '"sms":"stub"'   && pass "sms resolved to stub"   || fail "sms resolution" "$load_log"

# ---- 4. metrics: counter family + pre-touched series ----
m=$(curl -s --unix-socket "$SCD_SOCK" http://unix/metrics)
[[ "$m" == *"scd_notification_send_total"* ]] && pass "scd_notification_send_total exposed" \
                                              || fail "send_total missing"
# Pre-touched series for stub provider, ok result (Inc+Add(0) at registration).
echo "$m" | grep -qE 'scd_notification_send_total\{[^}]*channel="email"[^}]*provider="stub"[^}]*result="ok"' \
    && pass "pre-touched email/stub/ok series present" \
    || fail "expected pre-touched series missing"

# ---- 5. trigger a real Send → wrapper updates DB + bumps counter +
#         materializes the histogram series ----
curl -s --unix-socket "$SCD_SOCK" -X POST -H 'Content-Type: application/json' \
    -d '{"channel":"email","destination":"test+8e2e@example.com","ip":"10.10.0.205"}' \
    http://unix/v1/auth/otp/issue >/dev/null
sleep 1
last=$(echo "SELECT last_success_at IS NOT NULL FROM notification_providers WHERE id='$em_id';" | $PSQL)
[[ "$last" == "t" ]] && pass "wrapper updated last_success_at after OTP issue" \
                     || fail "health columns not updated" "got=$last"

# Counter for the actual send should be > 0.
val=$(curl -s --unix-socket "$SCD_SOCK" http://unix/metrics \
        | grep -E '^scd_notification_send_total\{[^}]*channel="email"[^}]*provider="stub"[^}]*result="ok"' \
        | grep -oE '[0-9.]+$' | head -n1)
val=${val:-0}
if (( $(echo "$val >= 1" | bc -l) )); then
    pass "send_total{email,stub,ok} bumped after OTP issue ($val)"
else
    fail "counter not incremented" "val=$val"
fi

# Histogram now has a sample → bucket/sum/count series appear.
m2=$(curl -s --unix-socket "$SCD_SOCK" http://unix/metrics)
echo "$m2" | grep -qE 'scd_notification_send_duration_seconds_count\{[^}]*channel="email"[^}]*provider="stub"' \
    && pass "duration histogram materialized after first send" \
    || fail "histogram still empty post-send" "$(echo \"$m2\" | grep duration_seconds | head -3)"

# ---- delete to clean up ----
curl -s -o /dev/null -b "$CJ" -X DELETE "$BASE/v1/notification-providers/$em_id?tenant_id=$TENANT_DEV"
curl -s -o /dev/null -b "$CJ" -X DELETE "$BASE/v1/notification-providers/$sm_id?tenant_id=$TENANT_DEV"

echo
echo "ALL GREEN"
