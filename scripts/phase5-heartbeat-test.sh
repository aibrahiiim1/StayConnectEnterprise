#!/usr/bin/env bash
# Phase 5.4 — heartbeat + staleness E2E.
#
# Flow:
#   - confirm scd heartbeats move appliance to status=online on boot
#   - stop scd; after >30s of silence the sweeper flips it to offline
#   - start scd; first heartbeat promotes it back to online
#
# The test stops scd for ~35s; portald has a systemd Requires= on scd so
# it's taken down alongside. Always restart both before exit.
set -euo pipefail

PSQL="docker exec -i stayconnect-pg psql -U stayconnect -d stayconnect -At -q -v ON_ERROR_STOP=1"

pass() { printf "  ✓ %s\n" "$1"; }
fail() { printf "  ✗ %s\n    %s\n" "$1" "${2:-}"; exit 1; }

TENANT_DEV=$(echo "SELECT id FROM tenants WHERE slug='dev';" | $PSQL | head -n1)
APP_ID=$(echo "SELECT id FROM appliances WHERE tenant_id='$TENANT_DEV' ORDER BY created_at LIMIT 1;" | $PSQL | head -n1)
[[ -n "$APP_ID" ]] || fail "no dev appliance"

# Always restore services on exit even if a test step fails.
cleanup() {
    systemctl reset-failed stayconnect-scd stayconnect-portald 2>/dev/null || true
    systemctl start stayconnect-scd   2>/dev/null || true
    systemctl start stayconnect-portald 2>/dev/null || true
}
trap cleanup EXIT

appliance_status() {
    echo "SELECT status FROM appliances WHERE id='$APP_ID';" | $PSQL
}
last_seen_age_seconds() {
    echo "SELECT COALESCE(extract(epoch from (now() - last_seen_at))::int, 9999) FROM appliances WHERE id='$APP_ID';" | $PSQL
}

# ---- 1. heartbeat bumps last_seen_at + status=online ----
# scd should have published at least one heartbeat on boot; give it a moment.
sleep 2
st=$(appliance_status)
[[ "$st" == "online" ]] && pass "initial status=online (heartbeat landed)" \
                        || fail "initial status" "got=$st"

age=$(last_seen_age_seconds)
[[ "$age" -lt 15 ]] && pass "last_seen_at within 15s (age=${age}s)" \
                    || fail "last_seen_at stale" "age=${age}s"

# ---- 2. stop scd → sweeper flips to offline within StaleAfter + SweepInterval ----
echo "  → stopping scd for stale-timeout test (45s)…"
systemctl stop stayconnect-scd stayconnect-portald
# Wait for the sweeper cycle. StaleAfter=30s + SweepInterval=15s + slack.
end=$((SECONDS + 50))
while (( SECONDS < end )); do
    st=$(appliance_status)
    [[ "$st" == "offline" ]] && break
    sleep 2
done
[[ "$st" == "offline" ]] && pass "silence → status=offline (after ~$((SECONDS - 0))s)" \
                         || fail "stale detection" "final=$st"

# ---- 3. restart scd → first heartbeat promotes back to online ----
systemctl reset-failed stayconnect-scd stayconnect-portald
systemctl start stayconnect-scd
systemctl start stayconnect-portald
end=$((SECONDS + 15))
while (( SECONDS < end )); do
    st=$(appliance_status)
    [[ "$st" == "online" ]] && break
    sleep 1
done
[[ "$st" == "online" ]] && pass "scd restart → back online within 15s" \
                        || fail "reboot recovery" "final=$st"

# ---- 4. ctrlapi log confirms sweep and consumer wiring ----
sw=$(journalctl -u stayconnect-ctrlapi --since '2m ago' --no-pager | grep -c '"heartbeat sweep"' || true)
[[ "$sw" -ge 1 ]] && pass "ctrlapi logged heartbeat sweep ($sw entries)" \
                  || fail "no sweep log"

echo
echo "ALL GREEN"
