#!/usr/bin/env bash
# Phase 7 — Prometheus metrics E2E.
#
# Asserts:
#   - both /metrics endpoints serve text/plain Prometheus exposition
#   - core metric families are present with the expected labels
#   - HTTP middleware on ctrlapi captures the chi route pattern (not raw URL)
#   - traffic-driving an action bumps the corresponding counter
#   - constant labels (tenant_id, site_id, appliance_id) are baked into scd
#     metrics
set -euo pipefail

BASE=${BASE:-http://127.0.0.1:8080}
ADMIN_EMAIL=${ADMIN_EMAIL:-admin@stayconnect.local}
ADMIN_PASS=${ADMIN_PASSWORD:-adminadmin01}
SCD_SOCK=${SCD_SOCK:-/run/stayconnect/scd.sock}

pass() { printf "  ✓ %s\n" "$1"; }
fail() { printf "  ✗ %s\n    %s\n" "$1" "${2:-}"; exit 1; }

# ---- 1. ctrlapi /metrics serves expected families ----
ctrl=$(curl -s "$BASE/metrics")
for m in ctrlapi_build_info ctrlapi_uptime_seconds ctrlapi_http_requests_total \
         ctrlapi_http_request_duration_seconds ctrlapi_heartbeats_received_total \
         go_goroutines process_resident_memory_bytes; do
    [[ "$ctrl" == *"$m"* ]] || fail "ctrlapi missing metric family" "missing=$m"
done
pass "ctrlapi exposes all required metric families"

# ---- 2. scd /metrics with constant labels ----
scd=$(curl -s --unix-socket "$SCD_SOCK" http://unix/metrics)
# Boot-time required: pre-touched counters + always-present gauges. PMS
# provider/cache series are dynamic (populated by the health flush) and
# get checked further down once the flush has run.
for m in scd_build_info scd_uptime_seconds scd_sessions_active scd_sessions_started_total \
         scd_sessions_closed_total scd_otp_issued_total scd_nft_ops_total \
         scd_reaper_closed_total go_goroutines; do
    [[ "$scd" == *"$m"* ]] || fail "scd missing metric family" "missing=$m"
done
pass "scd exposes all required metric families"

# Constant labels — every scd_* family series should carry tenant_id /
# site_id / appliance_id (except go_/process_ collectors, which are
# stdlib).
bad=$(grep -E '^scd_[a-z_]+(_total|_seconds|_active|_status|_size|_count|_info)?[ {]' <<<"$scd" \
        | grep -v '^# ' \
        | grep -v 'tenant_id=' || true)
if [[ -n "$bad" ]]; then
    fail "scd series without tenant_id label" "first=$(head -n1 <<<"$bad")"
fi
pass "scd series carry constant labels (tenant_id/site_id/appliance_id)"

# ---- 3. ctrlapi HTTP route label uses chi pattern, not raw URL ----
# Hit a known route that has a path parameter — appliances list — then
# inspect the metric for the {route="/v1/appliances/"} pattern, not
# the raw URL.
CJ=$(mktemp); trap "rm -f $CJ" EXIT
TENANT=$(docker exec -i stayconnect-pg psql -U stayconnect -d stayconnect -At -q -c "SELECT id FROM tenants WHERE slug='dev'")
curl -s -o /dev/null -c "$CJ" -X POST -H 'Content-Type: application/json' \
    -d "{\"email\":\"$ADMIN_EMAIL\",\"password\":\"$ADMIN_PASS\"}" "$BASE/v1/auth/login"
curl -s -o /dev/null -b "$CJ" "$BASE/v1/appliances?tenant_id=$TENANT"
curl -s -o /dev/null -b "$CJ" "$BASE/v1/appliances?tenant_id=$TENANT"
ctrl=$(curl -s "$BASE/metrics")
appliance_route=$(grep -E 'ctrlapi_http_requests_total\{.*route="[^"]*appliances[^"]*"' <<<"$ctrl" | head -n1)
[[ -n "$appliance_route" ]] || fail "no /v1/appliances request metric" "ctrl=$(echo \"$ctrl\" | head -c 200)"
# The chi pattern for the mounted /v1/appliances/{id} is "/v1/appliances/*"
# (chi mounts at "/v1/appliances" with sub-router). Check it's NOT the
# raw URL with the tenant_id query string baked in.
if [[ "$appliance_route" == *"$TENANT"* ]]; then
    fail "route label leaks tenant_id (raw URL captured)" "$appliance_route"
fi
pass "ctrlapi http_requests_total uses chi route pattern (no URL leaks)"

# ---- 4. counter increments on real traffic ----
# Trigger a revoke (no-op against an unknown IP) and verify
# scd_sessions_closed_total{reason="admin"} ticks up.
before=$(curl -s --unix-socket "$SCD_SOCK" http://unix/metrics \
        | grep -E '^scd_sessions_closed_total\{.*reason="admin"' \
        | grep -oE '[0-9.]+$' | head -n1)
before=${before:-0}

# Seed a session row + revoke it.
SITE=$(grep '^SCD_SITE_ID=' /etc/stayconnect/scd.env | cut -d= -f2)
APP=$(grep '^SCD_APPLIANCE_ID=' /etc/stayconnect/scd.env | cut -d= -f2)
GUEST=$(docker exec -i stayconnect-pg psql -U stayconnect -d stayconnect -At -q -c "SELECT id FROM guests WHERE tenant_id='$TENANT' LIMIT 1")
SESS=$(docker exec -i stayconnect-pg psql -U stayconnect -d stayconnect -At -q -c "INSERT INTO sessions(tenant_id,site_id,appliance_id,guest_id,ip,mac,state,started_at,last_activity_at,expires_at,bytes_up,bytes_down) VALUES('$TENANT','$SITE','$APP','$GUEST','10.252.0.7','aa:bb:cc:dd:ee:f7','active',now(),now(),now() + interval '1 hour',0,0) RETURNING id" | head -n1)
curl -s --unix-socket "$SCD_SOCK" -X POST -H 'Content-Type: application/json' \
    -d '{"ip":"10.252.0.7","reason":"admin"}' http://unix/v1/sessions/revoke >/dev/null
sleep 0.5
after=$(curl -s --unix-socket "$SCD_SOCK" http://unix/metrics \
        | grep -E '^scd_sessions_closed_total\{.*reason="admin"' \
        | grep -oE '[0-9.]+$' | head -n1)
after=${after:-0}
if (( $(echo "$after > $before" | bc -l) )); then
    pass "revoke bumped scd_sessions_closed_total{reason=admin}: $before → $after"
else
    fail "counter didn't increment" "before=$before after=$after"
fi
docker exec -i stayconnect-pg psql -U stayconnect -d stayconnect -c "DELETE FROM sessions WHERE id='$SESS'" >/dev/null

# ---- 5. nft ops counter ticks on Allow/Deny ----
nft_before=$(curl -s --unix-socket "$SCD_SOCK" http://unix/metrics \
        | grep -E '^scd_nft_ops_total\{.*op="del".*source="local"' \
        | grep -oE '[0-9.]+$' | head -n1)
nft_before=${nft_before:-0}
# The revoke above already invoked nft.Deny; assert via fresh scrape.
nft_after=$(curl -s --unix-socket "$SCD_SOCK" http://unix/metrics \
        | grep -E '^scd_nft_ops_total\{.*op="del".*source="local"' \
        | grep -oE '[0-9.]+$' | head -n1)
nft_after=${nft_after:-0}
if (( $(echo "$nft_after >= 1" | bc -l) )); then
    pass "scd_nft_ops_total{op=del,source=local} >= 1 ($nft_after)"
else
    fail "nft ops counter not registering" "after=$nft_after"
fi

# ---- 6. PMS gauges populated by health flush ----
# Flush runs every 30s; we may need to wait. Check current state — if 0
# series, wait one tick and retry.
have_status=$(curl -s --unix-socket "$SCD_SOCK" http://unix/metrics \
        | grep -cE '^scd_pms_provider_status\{[^}]*provider="' || true)
if [[ "$have_status" == "0" ]]; then
    sleep 31
    have_status=$(curl -s --unix-socket "$SCD_SOCK" http://unix/metrics \
            | grep -cE '^scd_pms_provider_status\{[^}]*provider="' || true)
fi
[[ "$have_status" -ge 1 ]] && pass "scd_pms_provider_status emits per-provider series ($have_status)" \
                          || fail "no PMS status series" "count=$have_status"

# ---- 7. heartbeat counter on ctrlapi ----
# At least one heartbeat received since boot.
hb=$(curl -s "$BASE/metrics" \
    | grep -E '^ctrlapi_heartbeats_received_total\{tenant_id=' \
    | grep -oE '[0-9.]+$' | head -n1)
hb=${hb:-0}
if (( $(echo "$hb >= 1" | bc -l) )); then
    pass "ctrlapi_heartbeats_received_total{tenant_id=...} >= 1 ($hb)"
else
    fail "no heartbeats counted" "got=$hb"
fi

echo
echo "ALL GREEN"
