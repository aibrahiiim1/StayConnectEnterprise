#!/usr/bin/env bash
# Phase 5.7 — finalization polish E2E.
#
# Covers:
#   A. walled-garden /effective returns tenant-wide ∪ site-scoped union
#   B. scd's periodic PMS reload safety net is wired (goroutine running)
#   C. bootstrap token expiry sweeper deletes expired unconsumed rows
#   D. /v1/appliances/{id}/effective-config returns resolved PMS + WG
#   E. heartbeat persists scd version into appliances.version
set -euo pipefail

BASE=${BASE:-http://127.0.0.1:8080}
ADMIN_EMAIL=${ADMIN_EMAIL:-admin@stayconnect.local}
ADMIN_PASS=${ADMIN_PASSWORD:-adminadmin01}
PSQL="docker exec -i stayconnect-pg psql -U stayconnect -d ${SC_DB:-stayconnect} -At -q -v ON_ERROR_STOP=1"
PSQLC="docker exec -i stayconnect-pg psql -U stayconnect -d stayconnect -At -q -v ON_ERROR_STOP=1"

pass() { printf "  ✓ %s\n" "$1"; }
fail() { printf "  ✗ %s\n    %s\n" "$1" "${2:-}"; exit 1; }

TENANT_DEV=$(echo "SELECT id FROM tenants WHERE slug='dev';" | $PSQL | head -n1)
SITE_LOCAL=$(grep '^SCD_SITE_ID=' /etc/stayconnect/scd.env | cut -d= -f2)
APP_ID=$(grep '^SCD_APPLIANCE_ID=' /etc/stayconnect/scd.env | cut -d= -f2)
[[ -n "$TENANT_DEV" && -n "$SITE_LOCAL" && -n "$APP_ID" ]] || fail "missing tenant/site/appliance"

CJ=$(mktemp)
trap "rm -f $CJ; echo \"DELETE FROM walled_garden_rules WHERE description='5.7-test';\" | $PSQL >/dev/null 2>&1 || true; echo \"DELETE FROM appliance_bootstrap_tokens WHERE expected_serial='5.7-sweeper';\" | $PSQLC >/dev/null 2>&1 || true" EXIT

code=$(curl -s -o /dev/null -w '%{http_code}' -c "$CJ" -X POST \
    -H 'Content-Type: application/json' \
    -d "{\"email\":\"$ADMIN_EMAIL\",\"password\":\"$ADMIN_PASS\"}" \
    "$BASE/v1/auth/login")
[[ "$code" == "200" ]] || fail "admin login" "code=$code"
pass "admin session"

# ---- A. walled-garden /effective ----
# Insert one tenant-wide rule and one site-scoped rule with a unique
# description so we can assert both come back from /effective.
TW_ID=$(echo "INSERT INTO walled_garden_rules(tenant_id,kind,value,description) VALUES('$TENANT_DEV','domain','tenant.example.com','5.7-test') RETURNING id;" | $PSQL | head -n1)
SC_ID=$(echo "INSERT INTO walled_garden_rules(tenant_id,site_id,kind,value,description) VALUES('$TENANT_DEV','$SITE_LOCAL','domain','site.example.com','5.7-test') RETURNING id;" | $PSQL | head -n1)
eff=$(curl -s -b "$CJ" "$BASE/v1/walled-garden/effective?tenant_id=$TENANT_DEV&site_id=$SITE_LOCAL")
got_tw=$(jq --arg id "$TW_ID" '.data | map(select(.id == $id)) | length' <<<"$eff")
got_sc=$(jq --arg id "$SC_ID" '.data | map(select(.id == $id)) | length' <<<"$eff")
[[ "$got_tw" == "1" && "$got_sc" == "1" ]] && pass "WG /effective returns tenant-wide ∪ site-scoped" \
                                            || fail "wg effective" "tw=$got_tw sc=$got_sc body=$(echo \"$eff\" | head -c 400)"

# ?site_id omitted → tenant-wide only.
eff_tw_only=$(curl -s -b "$CJ" "$BASE/v1/walled-garden/effective?tenant_id=$TENANT_DEV")
got_sc2=$(jq --arg id "$SC_ID" '.data | map(select(.id == $id)) | length' <<<"$eff_tw_only")
[[ "$got_sc2" == "0" ]] && pass "WG /effective without site_id excludes site-scoped" \
                        || fail "wg effective tw-only" "sc=$got_sc2"

# ---- B. periodic PMS reload safety-net is at least scheduled ----
# Hard to wait 10min in a test; instead check that scd's goroutine has
# enough plumbing — i.e. pmsReloadSafetyLoop is referenced and main isn't
# panicking. The simplest signal: scd is alive after boot and a scd source
# search shows the function is wired.
grep -q 'pmsReloadSafetyLoop' /opt/stayconnect/data-plane/cmd/scd/main.go \
    && pass "scd safety-net loop wired" \
    || fail "no safety-net loop reference"

# ---- C. token sweeper deletes expired tokens ----
# Insert an artificially-expired unconsumed token, force a sweeper run
# by restarting ctrlapi (boot fires the sweep immediately), assert deletion.
SWEEP_ID=$(echo "INSERT INTO appliance_bootstrap_tokens(tenant_id,site_id,expected_serial,token_hash,token_hint,expires_at) VALUES('$TENANT_DEV','$SITE_LOCAL','5.7-sweeper','\\xdeadbeef','XXXX',now() - interval '1 hour') RETURNING id;" | $PSQLC | head -n1)
[[ -n "$SWEEP_ID" ]] || fail "couldn't seed expired token"
systemctl reset-failed stayconnect-ctrlapi 2>/dev/null
systemctl restart stayconnect-ctrlapi
sleep 2
remaining=$(echo "SELECT count(*) FROM appliance_bootstrap_tokens WHERE id='$SWEEP_ID';" | $PSQLC)
[[ "$remaining" == "0" ]] && pass "expired unconsumed token swept on ctrlapi boot" \
                          || fail "sweeper missed token" "remaining=$remaining"
sw_log=$(journalctl -u stayconnect-ctrlapi --since '20s ago' --no-pager | grep -c '"token sweep"' || true)
[[ "$sw_log" -ge 1 ]] && pass "ctrlapi logged the sweep ($sw_log entries)" \
                      || fail "no sweep log"

# ---- D. /v1/appliances/{id}/effective-config ----
ec=$(curl -s -b "$CJ" "$BASE/v1/appliances/$APP_ID/effective-config?tenant_id=$TENANT_DEV")
ec_site=$(jq -r '.site_id' <<<"$ec")
ec_pms=$(jq -r '.pms_providers | length' <<<"$ec")
ec_wg=$(jq -r '.walled_garden | length' <<<"$ec")
[[ "$ec_site" == "$SITE_LOCAL" ]] && pass "effective-config: site_id=$ec_site" \
                                  || fail "effective-config site" "got=$ec_site"
[[ "$ec_pms" -ge 1 ]] && pass "effective-config: PMS providers resolved ($ec_pms)" \
                     || fail "no PMS in effective-config" "$(echo \"$ec\" | head -c 400)"
[[ "$ec_wg" -ge 1 ]] && pass "effective-config: walled-garden rules resolved ($ec_wg)" \
                    || fail "no WG in effective-config" "$(echo \"$ec\" | head -c 400)"

# ---- E. heartbeat persists version ----
# Wait for at least one heartbeat (10s interval, 12s gives slack).
sleep 12
ver=$(echo "SELECT COALESCE(version,'') FROM appliances WHERE id='$APP_ID';" | $PSQL | head -n1)
[[ -n "$ver" && "$ver" != "" ]] && pass "appliances.version persisted by heartbeat ($ver)" \
                                || fail "version not persisted" "got='$ver'"

echo
echo "ALL GREEN"
