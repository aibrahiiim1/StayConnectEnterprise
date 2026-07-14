#!/usr/bin/env bash
# Phase 5.3 — live config reload.
#
# Asserts: creating a pms_providers row via ctrlapi publishes an event on
# config.{tenantID}.pms, scd picks it up, and the new provider is reachable
# immediately (no process restart).
#
# We deliberately capture scd's start PID before any mutation so a silent
# restart would be caught.
set -euo pipefail

BASE=${BASE:-http://127.0.0.1:8080}
ADMIN_EMAIL=${ADMIN_EMAIL:-admin@stayconnect.local}
ADMIN_PASS=${ADMIN_PASSWORD:-adminadmin01}
PSQL="docker exec -i stayconnect-pg psql -U stayconnect -d stayconnect -At -q -v ON_ERROR_STOP=1"

pass() { printf "  ✓ %s\n" "$1"; }
fail() { printf "  ✗ %s\n    %s\n" "$1" "${2:-}"; exit 1; }

TENANT_DEV=$(echo "SELECT id FROM tenants WHERE slug='dev';" | $PSQL | head -n1)
[[ -n "$TENANT_DEV" ]] || fail "no dev tenant"

# Capture the scd main-process PID. If a reload ever becomes a silent
# restart, this PID will change and we'll flag it.
SCD_PID_BEFORE=$(systemctl show -p MainPID --value stayconnect-scd)
[[ "$SCD_PID_BEFORE" -gt 0 ]] || fail "scd not running"
pass "scd main PID before=$SCD_PID_BEFORE"

CJ=$(mktemp)
TEST_NAME="reload-test-$(date +%s)"
trap "rm -f $CJ; echo \"DELETE FROM pms_providers WHERE name='$TEST_NAME';\" | $PSQL >/dev/null 2>&1 || true" EXIT

code=$(curl -s -o /dev/null -w '%{http_code}' -c "$CJ" -X POST \
    -H 'Content-Type: application/json' \
    -d "{\"email\":\"$ADMIN_EMAIL\",\"password\":\"$ADMIN_PASS\"}" \
    "$BASE/v1/auth/login")
[[ "$code" == "200" ]] || fail "admin login" "code=$code"

# ---- sanity: provider not yet known to scd ----
# The ctrlapi PMS test route 404s first on the DB row (before calling scd),
# so we can't use it for the "not yet known" check. Seed a row, bypass the
# NATS publish by writing directly to DB, and expect the subsequent call
# to return 502 (scd doesn't know the name yet).
echo "INSERT INTO pms_providers(tenant_id,name,kind,enabled)
      VALUES ('$TENANT_DEV','$TEST_NAME','stub',true);" | $PSQL >/dev/null
pre_code=$(curl -s -o /dev/null -w '%{http_code}' -b "$CJ" -X POST \
    "$BASE/v1/pms-providers/$TEST_NAME/test?tenant_id=$TENANT_DEV")
[[ "$pre_code" == "502" ]] && pass "DB insert alone → scd 502 (no reload triggered)" \
                           || fail "expected 502 before reload" "code=$pre_code"

# ---- trigger reload via PATCH through ctrlapi (publishes event) ----
patch_code=$(curl -s -o /dev/null -w '%{http_code}' -b "$CJ" -X PATCH \
    -H 'Content-Type: application/json' \
    -d '{"display_name":"Reload Test"}' \
    "$BASE/v1/pms-providers/$TEST_NAME?tenant_id=$TENANT_DEV")
[[ "$patch_code" == "200" ]] || fail "PATCH failed" "code=$patch_code"

# Give scd a moment to pick up the NATS event + reload the registry.
for i in 1 2 3 4 5 6 7 8 9 10; do
    test_code=$(curl -s -o /dev/null -w '%{http_code}' -b "$CJ" -X POST \
        "$BASE/v1/pms-providers/$TEST_NAME/test?tenant_id=$TENANT_DEV")
    [[ "$test_code" == "200" ]] && break
    sleep 0.2
done
[[ "$test_code" == "200" ]] && pass "provider reachable after reload (no restart)" \
                            || fail "reload didn't land" "test_code=$test_code"

# ---- scd must NOT have restarted ----
SCD_PID_AFTER=$(systemctl show -p MainPID --value stayconnect-scd)
[[ "$SCD_PID_BEFORE" == "$SCD_PID_AFTER" ]] \
    && pass "scd PID unchanged ($SCD_PID_AFTER) — reload was in-process" \
    || fail "scd restarted" "before=$SCD_PID_BEFORE after=$SCD_PID_AFTER"

# ---- reload log line fired ----
reload_logs=$(journalctl -u stayconnect-scd --since '30s ago' --no-pager | grep -c '"pms reloaded"' || true)
[[ "$reload_logs" -ge 1 ]] && pass "scd logged 'pms reloaded'" \
                           || fail "no reload log" "journalctl had no 'pms reloaded' entry"

# ---- DELETE path ----
del_code=$(curl -s -o /dev/null -w '%{http_code}' -b "$CJ" -X DELETE \
    "$BASE/v1/pms-providers/$TEST_NAME?tenant_id=$TENANT_DEV")
[[ "$del_code" == "204" ]] || fail "delete failed" "code=$del_code"

# After DELETE, the row is gone from DB, so ctrlapi itself returns 404 —
# we only need to verify scd processed the reload event (evidenced by a
# second 'pms reloaded' in the log since 5s ago).
sleep 1
reload_logs_after=$(journalctl -u stayconnect-scd --since '5s ago' --no-pager | grep -c '"pms reloaded"' || true)
[[ "$reload_logs_after" -ge 1 ]] && pass "delete also triggered a reload" \
                                 || fail "delete reload missing"

echo
echo "ALL GREEN"
