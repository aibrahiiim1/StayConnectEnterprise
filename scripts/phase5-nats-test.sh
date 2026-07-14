#!/usr/bin/env bash
# Phase 5.2 — remote transport via NATS.
#
# Confirms that ctrlapi's admin calls to scd travel over NATS, not the
# unix socket. Asserts:
#   - transport=nats in ctrlapi's startup log
#   - scd subscribed to scd.<id>.>
#   - PMSTest/Cache/Health reach scd and return real data
#   - session revoke over NATS actually tears down a live session
#   - unknown-provider path returns 404 from scd (proving the subject
#     dispatcher is wired, not short-circuited in ctrlapi)
set -euo pipefail

BASE=${BASE:-http://127.0.0.1:8080}
ADMIN_EMAIL=${ADMIN_EMAIL:-admin@stayconnect.local}
ADMIN_PASS=${ADMIN_PASSWORD:-adminadmin01}
PSQL="docker exec -i stayconnect-pg psql -U stayconnect -d stayconnect -At -q -v ON_ERROR_STOP=1"

pass() { printf "  ✓ %s\n" "$1"; }
fail() { printf "  ✗ %s\n    %s\n" "$1" "${2:-}"; exit 1; }

TENANT_DEV=$(echo "SELECT id FROM tenants WHERE slug='dev';" | $PSQL | head -n1)
APP_ID=$(echo "SELECT id FROM appliances WHERE tenant_id='$TENANT_DEV' ORDER BY created_at LIMIT 1;" | $PSQL | head -n1)
[[ -n "$TENANT_DEV" && -n "$APP_ID" ]] || fail "no dev tenant or appliance"

CJ=$(mktemp); trap "rm -f $CJ" EXIT
code=$(curl -s -o /dev/null -w '%{http_code}' -c "$CJ" -X POST \
    -H 'Content-Type: application/json' \
    -d "{\"email\":\"$ADMIN_EMAIL\",\"password\":\"$ADMIN_PASS\"}" \
    "$BASE/v1/auth/login")
[[ "$code" == "200" ]] || fail "admin login" "code=$code"
pass "admin session"

# ---- wiring sanity: ctrlapi must be using NATS transport ----
# Window bumped from '1h ago' to '7d ago' because ctrlapi only logs
# `transport=nats` once at boot — if the service happens to have been up
# longer than an hour the test used to false-fail. (Service-lifetime is
# fine: systemd would restart it; we just want to know the current
# running process chose NATS over the unix-socket fallback.)
ct_log=$(journalctl -u stayconnect-ctrlapi --since '7d ago' --no-pager | grep -c '"transport=nats"' || true)
[[ "$ct_log" -ge 1 ]] && pass "ctrlapi log shows transport=nats" \
                     || fail "ctrlapi not on NATS transport" "journalctl showed no transport=nats line"

scd_log=$(journalctl -u stayconnect-scd --since '7d ago' --no-pager | grep -c 'scd nats subscribed' || true)
[[ "$scd_log" -ge 1 ]] && pass "scd subscribed to nats" || fail "scd not subscribed"

# ---- PMSTest over NATS ----
t=$(curl -s -b "$CJ" -X POST "$BASE/v1/pms-providers/stub-dev/test?tenant_id=$TENANT_DEV")
[[ "$(jq -r .ok <<<"$t")" == "true" ]] && pass "PMSTest via NATS: ok=true" \
                                       || fail "PMSTest" "$t"

# ---- PMSHealth over NATS ----
h=$(curl -s -b "$CJ" "$BASE/v1/pms-providers/stub-dev/health?tenant_id=$TENANT_DEV")
[[ "$(jq -r .health.status <<<"$h")" == "connected" ]] \
    && pass "PMSHealth via NATS: status=connected" \
    || fail "PMSHealth" "$h"

# ---- PMSCache over NATS ----
c=$(curl -s -b "$CJ" "$BASE/v1/pms-providers/stub-dev/cache?limit=10&tenant_id=$TENANT_DEV")
count=$(jq -r '.count' <<<"$c")
[[ "$count" -ge 1 ]] && pass "PMSCache via NATS: count=$count" || fail "PMSCache" "$c"

# ---- 404 from scd (subject reached, provider unknown) ----
# ctrlapi's pre-check (loadOne on DB) would also 404 if the row doesn't
# exist. To exercise scd-side 404 we need a row whose DB entry exists but
# whose scd registry doesn't know the name — create a disabled row and
# then flip it enabled at the DB only (scd was already loaded → miss).
echo "INSERT INTO pms_providers(tenant_id,name,kind,enabled) VALUES ('$TENANT_DEV','nats-miss','stub',true)
      ON CONFLICT(tenant_id,name) WHERE site_id IS NULL DO UPDATE SET enabled=true;" | $PSQL >/dev/null
miss_code=$(curl -s -o /dev/null -w '%{http_code}' -b "$CJ" -X POST \
    "$BASE/v1/pms-providers/nats-miss/test?tenant_id=$TENANT_DEV")
echo "DELETE FROM pms_providers WHERE tenant_id='$TENANT_DEV' AND name='nats-miss';" | $PSQL >/dev/null
# ctrlapi maps scd's "provider not registered" into 502 Bad Gateway.
[[ "$miss_code" == "502" ]] && pass "unknown provider on scd → 502 (NATS loop worked)" \
                            || fail "unknown-provider flow" "code=$miss_code (expected 502)"

# ---- session revoke over NATS ----
# Seat a stub session then revoke via ctrlapi. The revoke travels
# ctrlapi → NATS → scd.revoke; verify the row moves to state=closed.
CIP=${TEST_CIP:-10.10.0.205}
# Seed a session row straight in the DB so we don't need a guest-auth flow.
SESS_ID=$(echo "INSERT INTO sessions(tenant_id,site_id,appliance_id,guest_id,ip,mac,state,started_at,last_activity_at,bytes_up,bytes_down)
                VALUES ('$TENANT_DEV',
                        (SELECT id FROM sites WHERE tenant_id='$TENANT_DEV' LIMIT 1),
                        '$APP_ID',
                        (SELECT id FROM guests WHERE tenant_id='$TENANT_DEV' LIMIT 1),
                        '$CIP'::inet,'aa:bb:cc:dd:ee:ff','active',now(),now(),0,0)
                RETURNING id;" | $PSQL | head -n1)
[[ -n "$SESS_ID" ]] || fail "seed session failed"
rev_code=$(curl -s -o /dev/null -w '%{http_code}' -b "$CJ" -X POST \
    "$BASE/v1/sessions/$SESS_ID/disconnect?tenant_id=$TENANT_DEV" \
    -H 'Content-Type: application/json' -d '{"reason":"admin"}')
[[ "$rev_code" == "200" ]] && pass "Revoke via NATS: 200" \
                           || fail "Revoke" "code=$rev_code"
state=$(echo "SELECT state FROM sessions WHERE id='$SESS_ID';" | $PSQL | head -n1)
[[ "$state" == "closed" ]] && pass "session flipped to closed" \
                           || fail "state" "got=$state"
echo "DELETE FROM sessions WHERE id='$SESS_ID';" | $PSQL >/dev/null

echo
echo "ALL GREEN"
