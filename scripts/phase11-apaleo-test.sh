#!/usr/bin/env bash
# Phase 11 — Apaleo REST PMS provider E2E.
#
# Asserts:
#   1. Go unit tests for the Apaleo impl pass (OAuth2 grant, token cache,
#      token refresh, ValidateGuest success/miss, 401 invalidation,
#      token-error surfacing)
#   2. ctrlapi CRUD creates an apaleo row that round-trips correctly
#   3. scd's loader recognises kind="apaleo" and registers it on boot
#   4. PMS health admin endpoint reports the apaleo provider
set -euo pipefail

BASE=${BASE:-http://127.0.0.1:8080}
ADMIN_EMAIL=${ADMIN_EMAIL:-admin@stayconnect.local}
ADMIN_PASS=${ADMIN_PASSWORD:-adminadmin01}
PSQL="docker exec -i stayconnect-pg psql -U stayconnect -d stayconnect -At -q -v ON_ERROR_STOP=1"

pass() { printf "  ✓ %s\n" "$1"; }
fail() { printf "  ✗ %s\n    %s\n" "$1" "${2:-}"; exit 1; }

TENANT_DEV=$(echo "SELECT id FROM tenants WHERE slug='dev';" | $PSQL | head -n1)
[[ -n "$TENANT_DEV" ]] || fail "no dev tenant"

APALEO_NAME="apaleo-e2e"
CJ=$(mktemp)
cleanup() {
    rm -f "$CJ"
    echo "DELETE FROM pms_providers WHERE name='$APALEO_NAME';" | $PSQL >/dev/null 2>&1 || true
}
trap cleanup EXIT

# ---- 1. Go unit tests ----
go_out=$(cd /opt/stayconnect/data-plane && go test ./internal/pms -run 'TestApaleo' 2>&1)
echo "$go_out" | grep -qE 'ok\s+.*internal/pms' \
    && pass "Go unit tests pass for Apaleo impl" \
    || fail "Go tests failed" "$go_out"

# ---- 2. ctrlapi CRUD ----
code=$(curl -s -o /dev/null -w '%{http_code}' -c "$CJ" -X POST \
    -H 'Content-Type: application/json' \
    -d "{\"email\":\"$ADMIN_EMAIL\",\"password\":\"$ADMIN_PASS\"}" \
    "$BASE/v1/auth/login")
[[ "$code" == "200" ]] || fail "admin login" "code=$code"

cr=$(curl -s -b "$CJ" -X POST "$BASE/v1/pms-providers?tenant_id=$TENANT_DEV" \
    -H 'Content-Type: application/json' \
    -d "{\"name\":\"$APALEO_NAME\",\"kind\":\"apaleo\",\"display_name\":\"Apaleo E2E\",\"base_url\":\"http://127.0.0.1:59998\",\"api_key\":\"client-secret-test\",\"property_id\":\"HTL\",\"extra\":{\"client_id\":\"cli-test\",\"identity_url\":\"http://127.0.0.1:59998\"}}")
id=$(jq -r '.id' <<<"$cr")
[[ -n "$id" && "$id" != "null" ]] && pass "create apaleo provider row" || fail "create" "$cr"

got=$(curl -s -b "$CJ" "$BASE/v1/pms-providers/$APALEO_NAME?tenant_id=$TENANT_DEV")
echo "$got" | jq -e 'has("api_key") | not' >/dev/null \
    && pass "api_key never returned in GET" \
    || fail "api_key leaked" "$got"

# ---- 3. scd loader registers apaleo ----
sleep 1
reg=$(journalctl -u stayconnect-scd --since '30s ago' --no-pager | grep '"pmsloader: registered"' | grep '"kind":"apaleo"' | tail -n1)
if [[ -z "$reg" ]]; then
    systemctl reset-failed stayconnect-scd stayconnect-portald 2>/dev/null
    systemctl restart stayconnect-scd stayconnect-portald
    sleep 2
    reg=$(journalctl -u stayconnect-scd --since '15s ago' --no-pager | grep '"pmsloader: registered"' | grep '"kind":"apaleo"' | tail -n1)
fi
[[ -n "$reg" ]] && pass "scd registered kind=apaleo" \
                || fail "no apaleo registration log" "check journalctl"
echo "$reg" | grep -q "\"name\":\"$APALEO_NAME\"" && pass "registered under expected name" \
                                                  || fail "wrong name" "$reg"

# ---- 4. PMS health admin endpoint returns the apaleo provider ----
h=$(curl -s -b "$CJ" "$BASE/v1/pms-providers/$APALEO_NAME/health?tenant_id=$TENANT_DEV")
got_kind=$(jq -r '.kind' <<<"$h")
[[ "$got_kind" == "apaleo" ]] && pass "admin health endpoint returns kind=apaleo" \
                              || fail "health kind wrong" "got=$got_kind body=$h"
got_status=$(jq -r '.health.status' <<<"$h")
case "$got_status" in
    degraded|connecting|down) pass "health status reflects unreachable upstream (status=$got_status)";;
    *) fail "unexpected health status" "got=$got_status";;
esac

echo
echo "ALL GREEN"
