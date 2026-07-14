#!/usr/bin/env bash
# Phase 5.5 — HA plumbing (the pieces that are testable on a single VM).
#
# Verifies:
#   - scd publishes `nft.{siteID}` ops when it calls Allow/Deny
#   - scd mirrors peer ops (from a different sender_id) into local nft
#   - scd boot reconciles the nft set from active sessions rows
#
# The full active/passive failover flow (keepalived + conntrackd) is
# covered by the runbook in deploy/ha/ and requires two physical boxes.
set -euo pipefail

PSQL="docker exec -i stayconnect-pg psql -U stayconnect -d stayconnect -At -q -v ON_ERROR_STOP=1"
HELPER=/opt/stayconnect/bin/scd-enroll-test
NATS_URL=${NATS_URL:-nats://127.0.0.1:4222}

pass() { printf "  ✓ %s\n" "$1"; }
fail() { printf "  ✗ %s\n    %s\n" "$1" "${2:-}"; exit 1; }

TENANT_DEV=$(echo "SELECT id FROM tenants WHERE slug='dev';" | $PSQL | head -n1)
SITE_DEV=$(echo "SELECT id FROM sites WHERE tenant_id='$TENANT_DEV' ORDER BY created_at LIMIT 1;" | $PSQL | head -n1)
APP_ID=$(echo "SELECT id FROM appliances WHERE tenant_id='$TENANT_DEV' ORDER BY created_at LIMIT 1;" | $PSQL | head -n1)
GUEST=$(echo "SELECT id FROM guests WHERE tenant_id='$TENANT_DEV' LIMIT 1;" | $PSQL | head -n1)

# ---- 1. scd's subscription on nft.<site> exists ----
subs=$(curl -s http://127.0.0.1:8222/subsz?subs=1 \
    | python3 -c 'import sys,json; d=json.load(sys.stdin); [print(s.get("subject")) for s in d.get("subscriptions_list",[])]')
echo "$subs" | grep -q "^nft.${SITE_DEV}$" \
    && pass "scd subscribed to nft.${SITE_DEV}" \
    || fail "nft subject subscription missing" "subs=$subs"

# ---- 2. publishing a peer op mirrors into scd's local nft set ----
# Pick an IP that isn't already authorized.
MIRROR_IP="10.255.0.99"
nft delete element inet stayconnect auth_ipv4 "{ $MIRROR_IP }" 2>/dev/null || true
NATS_URL="$NATS_URL" SITE_ID="$SITE_DEV" IP="$MIRROR_IP" TTL_SECONDS=60 \
    SENDER_ID="peer-synthetic" \
    "$HELPER" --nft-publish >/dev/null
# Give scd a moment to apply (nft shell-out can briefly block).
landed=no
for i in $(seq 1 50); do
    out=$(nft list set inet stayconnect auth_ipv4 2>/dev/null || true)
    if [[ "$out" == *"$MIRROR_IP"* ]]; then
        landed=yes
        break
    fi
    sleep 0.2
done
[[ "$landed" == "yes" ]] && pass "peer op mirrored into local nft ($MIRROR_IP)" \
                         || fail "mirror didn't land" "set=$out"
nft delete element inet stayconnect auth_ipv4 "{ $MIRROR_IP }" 2>/dev/null || true

# ---- 3. self-echo is suppressed (scd publishes but doesn't re-apply) ----
# Impersonate scd itself (same sender id); nft set stays unchanged.
SELF_IP="10.255.0.77"
NATS_URL="$NATS_URL" SITE_ID="$SITE_DEV" IP="$SELF_IP" TTL_SECONDS=60 \
    SENDER_ID="$APP_ID" \
    "$HELPER" --nft-publish >/dev/null
sleep 0.5
out=$(nft list set inet stayconnect auth_ipv4 2>/dev/null || true)
if [[ "$out" == *"$SELF_IP"* ]]; then
    fail "self-echo was applied (should have been suppressed)"
else
    pass "self-sender op ignored (no loop)"
fi

# ---- 4. a scd-originated auth event publishes on nft.{siteID} ----
# Capture one message into a tempfile, then trigger a revoke.
OUT=$(mktemp)
( NATS_URL="$NATS_URL" SITE_ID="$SITE_DEV" WAIT_SECONDS=8 N=1 \
  "$HELPER" --nft-await > "$OUT" 2>/dev/null ) &
AWAIT_PID=$!
sleep 0.5  # let the subscriber connect

IP_PUB="10.255.0.42"
MAC_PUB="aa:bb:cc:dd:ee:aa"
SESS=$(echo "INSERT INTO sessions(tenant_id,site_id,appliance_id,guest_id,ip,mac,state,started_at,last_activity_at,bytes_up,bytes_down) VALUES ('$TENANT_DEV','$SITE_DEV','$APP_ID','$GUEST','$IP_PUB','$MAC_PUB','active',now(),now(),0,0) RETURNING id;" | $PSQL | head -n1)
curl -s --unix-socket /run/stayconnect/scd.sock -X POST \
    -H 'Content-Type: application/json' \
    -d "{\"ip\":\"$IP_PUB\",\"reason\":\"admin\"}" \
    http://unix/v1/sessions/revoke >/dev/null

wait $AWAIT_PID 2>/dev/null || true
msg=$(cat "$OUT")
rm -f "$OUT"
echo "$msg" | grep -q '"op":"del"' && echo "$msg" | grep -q "\"ip\":\"$IP_PUB\"" \
    && pass "scd published op=del for revoked ip" \
    || fail "scd nft publish missing" "msg=$msg"

echo "DELETE FROM sessions WHERE id='$SESS';" | $PSQL >/dev/null

# ---- 5. boot reconcile populates nft from active sessions ----
# Insert an active session row directly, restart scd, expect the IP to
# appear in the local nft set as a reconcile entry.
RECON_IP="10.255.0.55"
RECON_MAC="aa:bb:cc:dd:ee:bb"
nft delete element inet stayconnect auth_ipv4 "{ $RECON_IP }" 2>/dev/null || true
RECON_SESS=$(echo "INSERT INTO sessions(tenant_id,site_id,appliance_id,guest_id,ip,mac,state,started_at,last_activity_at,bytes_up,bytes_down) VALUES ('$TENANT_DEV','$SITE_DEV','$APP_ID','$GUEST','$RECON_IP','$RECON_MAC','active',now(),now(),0,0) RETURNING id;" | $PSQL | head -n1)
systemctl reset-failed stayconnect-scd stayconnect-portald 2>/dev/null || true
systemctl restart stayconnect-scd
systemctl restart stayconnect-portald
landed=no
for i in $(seq 1 30); do
    out=$(nft list set inet stayconnect auth_ipv4 2>/dev/null || true)
    if [[ "$out" == *"$RECON_IP"* ]]; then
        landed=yes; break
    fi
    sleep 0.3
done
[[ "$landed" == "yes" ]] && pass "boot reconcile repopulated nft ($RECON_IP)" \
                         || fail "reconcile didn't repopulate" "set=$out"
reconcile_log=$(journalctl -u stayconnect-scd --since '15s ago' --no-pager | grep -c '"nft reconciled from DB"' || true)
[[ "$reconcile_log" -ge 1 ]] && pass "scd logged 'nft reconciled from DB'" \
                             || fail "no reconcile log"

# Cleanup
nft delete element inet stayconnect auth_ipv4 "{ $RECON_IP }" 2>/dev/null || true
echo "DELETE FROM sessions WHERE id='$RECON_SESS';" | $PSQL >/dev/null

echo
echo "ALL GREEN"
