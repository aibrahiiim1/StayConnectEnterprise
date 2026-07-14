#!/usr/bin/env bash
# Phase 10 — Mews REST PMS provider E2E.
#
# Asserts:
#   1. Go unit tests for the Mews impl pass (auth header shape, space
#      map refresh, ValidateGuest success/miss, refetch-on-map-miss,
#      upstream error surfacing)
#   2. ctrlapi CRUD creates a mews row that round-trips correctly
#   3. scd's loader recognises kind="mews" and registers it on boot
#   4. /metrics exposes scd_pms_validate_total + scd_pms_validate_duration_seconds
#      with pre-touched label sets
#   5. PMS health admin endpoint reports the mews provider
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

# Clear pms_attempts for the ad-hoc validate IP used in section 4.
# Otherwise running phase10 after phase4-pms (same room/last-name pair)
# trips the per-network throttle; the validate returns early without
# bumping the scd_pms_validate_* metrics, and the per-provider series
# assertion false-fails.
TEST_IP=10.10.0.205
echo "DELETE FROM pms_attempts WHERE ip = '$TEST_IP'::inet;" | $PSQL >/dev/null 2>&1 || true

MEWS_NAME="mews-e2e"
CJ=$(mktemp)
cleanup() {
    rm -f "$CJ"
    echo "DELETE FROM pms_providers WHERE name='$MEWS_NAME';" | $PSQL >/dev/null 2>&1 || true
}
trap cleanup EXIT

# ---- 1. Go unit tests ----
go_out=$(cd /opt/stayconnect/data-plane && go test ./internal/pms -run 'TestMews' 2>&1)
echo "$go_out" | grep -qE 'ok\s+.*internal/pms' \
    && pass "Go unit tests pass for Mews impl" \
    || fail "Go tests failed" "$go_out"

# ---- 2. ctrlapi CRUD ----
code=$(curl -s -o /dev/null -w '%{http_code}' -c "$CJ" -X POST \
    -H 'Content-Type: application/json' \
    -d "{\"email\":\"$ADMIN_EMAIL\",\"password\":\"$ADMIN_PASS\"}" \
    "$BASE/v1/auth/login")
[[ "$code" == "200" ]] || fail "admin login" "code=$code"

# Point at an unreachable base URL on purpose — the initial refreshSpaces
# will fail, scd will log degraded, but the registration itself must
# succeed so we can observe the loader path.
cr=$(curl -s -b "$CJ" -X POST "$BASE/v1/pms-providers?tenant_id=$TENANT_DEV" \
    -H 'Content-Type: application/json' \
    -d "{\"name\":\"$MEWS_NAME\",\"kind\":\"mews\",\"display_name\":\"Mews E2E\",\"base_url\":\"http://127.0.0.1:59999\",\"api_key\":\"access-token-test\",\"property_id\":\"ent-1\",\"extra\":{\"client_token\":\"client-token-test\"}}")
id=$(jq -r '.id' <<<"$cr")
[[ -n "$id" && "$id" != "null" ]] && pass "create mews provider row" || fail "create" "$cr"

# Secrets must not round-trip.
got=$(curl -s -b "$CJ" "$BASE/v1/pms-providers/$MEWS_NAME?tenant_id=$TENANT_DEV")
echo "$got" | jq -e 'has("api_key") | not' >/dev/null \
    && pass "api_key never returned in GET" \
    || fail "api_key leaked" "$got"

# ---- 3. scd loader registers mews on boot ----
# Already registered via live-reload event from the create — check the log.
sleep 1
reg=$(journalctl -u stayconnect-scd --since '30s ago' --no-pager | grep '"pmsloader: registered"' | grep '"kind":"mews"' | tail -n1)
if [[ -z "$reg" ]]; then
    # Fallback: force a reload by toggling enabled via patch (fires another
    # config push), or just restart scd.
    systemctl reset-failed stayconnect-scd stayconnect-portald 2>/dev/null
    systemctl restart stayconnect-scd stayconnect-portald
    sleep 2
    reg=$(journalctl -u stayconnect-scd --since '15s ago' --no-pager | grep '"pmsloader: registered"' | grep '"kind":"mews"' | tail -n1)
fi
[[ -n "$reg" ]] && pass "scd registered kind=mews" \
                || fail "no mews registration log" "check journalctl"
echo "$reg" | grep -q "\"name\":\"$MEWS_NAME\"" && pass "registered under expected name" \
                                                || fail "wrong name" "$reg"

# ---- 4. trigger a real validate via the stub provider so the counter +
#         histogram materialise, then scrape and assert ----
curl -s --unix-socket "$SCD_SOCK" -X POST -H 'Content-Type: application/json' \
    -d '{"room":"101","last_name":"Anderson","ip":"10.10.0.205","mac":"aa:bb:cc:dd:ee:ff"}' \
    http://unix/v1/auth/pms/verify >/dev/null || true
sleep 0.5
m=$(curl -s --unix-socket "$SCD_SOCK" http://unix/metrics)
# here-strings, not pipes — see phase12 for the `echo|grep -q` SIGPIPE +
# pipefail bug that silently false-fails these assertions.
grep -qE 'scd_pms_validate_total\{[^}]*provider="stub-dev"' <<<"$m" \
    && pass "scd_pms_validate_total has per-provider series" \
    || fail "pms_validate_total missing per-provider series"
grep -qE 'scd_pms_validate_duration_seconds_count\{[^}]*provider="stub-dev"' <<<"$m" \
    && pass "scd_pms_validate_duration_seconds histogram materialised" \
    || fail "duration histogram not emitted"

# ---- 5. PMS health admin endpoint returns the mews provider ----
h=$(curl -s -b "$CJ" "$BASE/v1/pms-providers/$MEWS_NAME/health?tenant_id=$TENANT_DEV")
got_kind=$(jq -r '.kind' <<<"$h")
[[ "$got_kind" == "mews" ]] && pass "admin health endpoint returns kind=mews" \
                            || fail "health kind wrong" "got=$got_kind body=$h"
# Status will be 'degraded' or 'down' because we pointed at a dead URL.
got_status=$(jq -r '.health.status' <<<"$h")
case "$got_status" in
    degraded|connecting|down) pass "health status reflects unreachable upstream (status=$got_status)";;
    *) fail "unexpected health status" "got=$got_status";;
esac

echo
echo "ALL GREEN"
