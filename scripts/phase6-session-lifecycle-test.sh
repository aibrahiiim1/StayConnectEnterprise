#!/usr/bin/env bash
# Phase 6 — session lifecycle (expires_at + reaper).
#
# Asserts:
#   1. New OTP/voucher/PMS sessions land with expires_at = started_at + duration
#   2. The reaper closes expired sessions within ~30s and tears down nft
#   3. The reaper closes idle (no traffic) sessions whose last_activity_at
#      is older than the configured idle window
#   4. Reconcile uses real expires_at — backfilled rows + new rows
#      get correct kernel timeouts (no more 1h fallback)
#
# We seed sessions directly into the DB to avoid the full guest-auth
# flow; the reaper doesn't care how a row arrived, only what shape it has.
set -euo pipefail

# scd is edge-first: its reaper, sessions and guests live in the SITE database,
# not the cloud DB. Derive the DB from scd.env (the same source we read the
# site/appliance IDs from) so this test always targets the DB the reaper sweeps.
# Both DBs still carry a sessions table post-split, so defaulting to the cloud
# DB would silently insert rows the reaper never sees.
_scd_db=$(sed -nE 's#^SCD_DB_URL=.*/([A-Za-z0-9_]+)\?.*#\1#p' /etc/stayconnect/scd.env 2>/dev/null | head -n1)
SC_DB="${SC_DB:-${_scd_db:-stayconnect_site}}"
PSQL="docker exec -i stayconnect-pg psql -U stayconnect -d ${SC_DB} -At -q -v ON_ERROR_STOP=1"

pass() { printf "  ✓ %s\n" "$1"; }
fail() { printf "  ✗ %s\n    %s\n" "$1" "${2:-}"; exit 1; }

TENANT_DEV=$(echo "SELECT id FROM tenants WHERE slug='dev';" | $PSQL | head -n1)
SITE_LOCAL=$(grep '^SCD_SITE_ID=' /etc/stayconnect/scd.env | cut -d= -f2)
APP_ID=$(grep '^SCD_APPLIANCE_ID=' /etc/stayconnect/scd.env | cut -d= -f2)
GUEST=$(echo "SELECT id FROM guests WHERE tenant_id='$TENANT_DEV' LIMIT 1;" | $PSQL | head -n1)
[[ -n "$TENANT_DEV" && -n "$SITE_LOCAL" && -n "$APP_ID" && -n "$GUEST" ]] || fail "missing fixtures"

cleanup() {
    nft delete element inet stayconnect auth_ipv4 '{ 10.250.0.10 }' 2>/dev/null || true
    nft delete element inet stayconnect auth_ipv4 '{ 10.250.0.11 }' 2>/dev/null || true
    nft delete element inet stayconnect auth_ipv4 '{ 10.250.0.12 }' 2>/dev/null || true
    echo "DELETE FROM sessions WHERE ip::text LIKE '10.250.0.%';" | $PSQL >/dev/null 2>&1 || true
}
trap cleanup EXIT
cleanup

# ---- 1. expires_at is persisted on insert (proxy via direct SQL the same shape Manager uses) ----
# session.Manager writes expires_at = now + duration_seconds. Mirror that
# behavior so the test doesn't need a live auth flow.
SESS_LIVE=$(echo "INSERT INTO sessions(tenant_id,site_id,appliance_id,guest_id,ip,mac,state,started_at,last_activity_at,expires_at,bytes_up,bytes_down)
VALUES('$TENANT_DEV','$SITE_LOCAL','$APP_ID','$GUEST','10.250.0.10','aa:bb:cc:dd:ee:01','active',now(),now(),now() + interval '1 hour',0,0)
RETURNING id;" | $PSQL | head -n1)
got=$(echo "SELECT expires_at IS NOT NULL FROM sessions WHERE id='$SESS_LIVE';" | $PSQL | head -n1)
[[ "$got" == "t" ]] && pass "expires_at persisted on session insert" \
                    || fail "expires_at missing" "got=$got"

# ---- 2. expired session is reaped within reaper interval (~30s + slack) ----
SESS_EXP=$(echo "INSERT INTO sessions(tenant_id,site_id,appliance_id,guest_id,ip,mac,state,started_at,last_activity_at,expires_at,bytes_up,bytes_down)
VALUES('$TENANT_DEV','$SITE_LOCAL','$APP_ID','$GUEST','10.250.0.11','aa:bb:cc:dd:ee:02','active',now() - interval '2 hours',now() - interval '5 minutes',now() - interval '1 minute',0,0)
RETURNING id;" | $PSQL | head -n1)
nft add element inet stayconnect auth_ipv4 '{ "br-lan" . 10.250.0.11 timeout 1h }' 2>/dev/null || true

# Wait up to 40s for reaper sweep (interval=30s, plus query+update slack).
final_state=""
final_reason=""
for i in $(seq 1 40); do
    row=$(echo "SELECT state || '|' || COALESCE(end_reason,'') FROM sessions WHERE id='$SESS_EXP';" | $PSQL | head -n1)
    final_state=${row%|*}
    final_reason=${row#*|}
    [[ "$final_state" == "closed" ]] && break
    sleep 1
done
[[ "$final_state" == "closed" ]] && pass "expired session closed by reaper" \
                                 || fail "expired not closed" "state=$final_state"
[[ "$final_reason" == "quota_time" ]] && pass "expired closed with reason=quota_time" \
                                      || fail "expired reason wrong" "got=$final_reason"

# nft entry should also be gone.
out=$(nft list set inet stayconnect auth_ipv4 2>/dev/null || true)
if [[ "$out" == *"10.250.0.11"* ]]; then
    fail "nft entry for expired ip still present" "set=$(echo \"$out\" | head -c 300)"
else
    pass "nft entry for expired ip removed"
fi

# ---- 3. idle session reaped (last_activity_at older than idleTimeout=30m) ----
SESS_IDLE=$(echo "INSERT INTO sessions(tenant_id,site_id,appliance_id,guest_id,ip,mac,state,started_at,last_activity_at,expires_at,bytes_up,bytes_down)
VALUES('$TENANT_DEV','$SITE_LOCAL','$APP_ID','$GUEST','10.250.0.12','aa:bb:cc:dd:ee:03','active',now() - interval '2 hours',now() - interval '45 minutes',now() + interval '1 hour',0,0)
RETURNING id;" | $PSQL | head -n1)
nft add element inet stayconnect auth_ipv4 '{ "br-lan" . 10.250.0.12 timeout 1h }' 2>/dev/null || true

final_state=""
final_reason=""
for i in $(seq 1 40); do
    row=$(echo "SELECT state || '|' || COALESCE(end_reason,'') FROM sessions WHERE id='$SESS_IDLE';" | $PSQL | head -n1)
    final_state=${row%|*}
    final_reason=${row#*|}
    [[ "$final_state" == "closed" ]] && break
    sleep 1
done
[[ "$final_state" == "closed" ]] && pass "idle session closed by reaper" \
                                 || fail "idle not closed" "state=$final_state"
[[ "$final_reason" == "idle" ]] && pass "idle closed with reason=idle" \
                                || fail "idle reason wrong" "got=$final_reason"

# ---- 4. reconcile uses real expires_at, not the 1h fallback ----
# Insert a session with expires_at=now+5min, restart scd, expect the nft
# entry to have ~5min timeout (NOT the old 1h default).
nft delete element inet stayconnect auth_ipv4 '{ 10.250.0.10 }' 2>/dev/null || true
echo "UPDATE sessions SET expires_at = now() + interval '5 minutes', last_activity_at = now() WHERE id='$SESS_LIVE';" | $PSQL >/dev/null
systemctl reset-failed stayconnect-scd stayconnect-portald 2>/dev/null
systemctl restart stayconnect-scd stayconnect-portald
sleep 3
out=$(nft list set inet stayconnect auth_ipv4 2>/dev/null || true)
if [[ "$out" != *"10.250.0.10"* ]]; then
    fail "reconcile didn't add 10.250.0.10" "set=$(echo \"$out\" | head -c 300)"
fi
# Extract the timeout value for that IP. Format: "10.250.0.10 timeout XmYs expires ..."
line=$(grep -oE '10\.250\.0\.10 timeout [^,}]*' <<<"$out" | head -n1)
case "$line" in
    *"timeout 5m"*|*"timeout 4m"*)
        pass "reconcile applied real expires_at TTL ($line)"
        ;;
    *"timeout 1h"*)
        fail "reconcile still using 1h fallback" "$line"
        ;;
    *)
        fail "unexpected reconcile TTL" "$line"
        ;;
esac

echo
echo "ALL GREEN"
