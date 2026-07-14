#!/usr/bin/env bash
# Phase 5.6 — per-site PMS overrides E2E.
#
# Asserts:
#   - a site-scoped row with the same name as a tenant-wide row overrides
#     it at that site (scd registers the site-scoped one)
#   - a second site that has no override continues to see the tenant-wide row
#   - CRUD via ctrlapi respects ?site_id= targeting
#   - the partial unique indexes reject two tenant-wide rows with the same name
#     AND reject two site-scoped rows for the same (site, name)
set -euo pipefail

BASE=${BASE:-http://127.0.0.1:8080}
ADMIN_EMAIL=${ADMIN_EMAIL:-admin@stayconnect.local}
ADMIN_PASS=${ADMIN_PASSWORD:-adminadmin01}
PSQL="docker exec -i stayconnect-pg psql -U stayconnect -d stayconnect -At -q -v ON_ERROR_STOP=1"

pass() { printf "  ✓ %s\n" "$1"; }
fail() { printf "  ✗ %s\n    %s\n" "$1" "${2:-}"; exit 1; }

TENANT_DEV=$(echo "SELECT id FROM tenants WHERE slug='dev';" | $PSQL | head -n1)
SITE_A=$(echo "SELECT id FROM sites WHERE tenant_id='$TENANT_DEV' ORDER BY created_at LIMIT 1;" | $PSQL | head -n1)
[[ -n "$TENANT_DEV" && -n "$SITE_A" ]] || fail "no dev tenant/site"

# Pick (or create) a second site so we can prove override is site-scoped,
# not tenant-scoped. Phase-1 bootstrap only creates one site.
SITE_B=$(echo "SELECT id FROM sites WHERE tenant_id='$TENANT_DEV' AND code='test-lobby-b';" | $PSQL | head -n1)
CLEANUP_SITE_B=no
if [[ -z "$SITE_B" ]]; then
    SITE_B=$(echo "INSERT INTO sites(tenant_id,code,name,timezone) VALUES('$TENANT_DEV','test-lobby-b','Test Lobby B','UTC') RETURNING id;" | $PSQL | head -n1)
    CLEANUP_SITE_B=yes
fi

# Discover which site scd at THIS appliance thinks it's at.
SCD_SITE=$(grep '^SCD_SITE_ID=' /etc/stayconnect/scd.env | cut -d= -f2)
[[ -n "$SCD_SITE" ]] || fail "scd site id not in env"
# Use whichever of SITE_A/SITE_B matches scd's configured site as "ours".
if [[ "$SCD_SITE" == "$SITE_A" ]]; then
    SITE_LOCAL="$SITE_A"; SITE_OTHER="$SITE_B"
elif [[ "$SCD_SITE" == "$SITE_B" ]]; then
    SITE_LOCAL="$SITE_B"; SITE_OTHER="$SITE_A"
else
    fail "scd site $SCD_SITE matches neither SITE_A nor SITE_B"
fi

CJ=$(mktemp)
NAME="override-test"
cleanup() {
    rm -f "$CJ"
    echo "DELETE FROM pms_providers WHERE name='$NAME';" | $PSQL >/dev/null 2>&1 || true
    if [[ "$CLEANUP_SITE_B" == "yes" ]]; then
        echo "DELETE FROM sites WHERE id='$SITE_B';" | $PSQL >/dev/null 2>&1 || true
    fi
}
trap cleanup EXIT

code=$(curl -s -o /dev/null -w '%{http_code}' -c "$CJ" -X POST \
    -H 'Content-Type: application/json' \
    -d "{\"email\":\"$ADMIN_EMAIL\",\"password\":\"$ADMIN_PASS\"}" \
    "$BASE/v1/auth/login")
[[ "$code" == "200" ]] || fail "admin login" "code=$code"
pass "admin session"

# ---- 1. create a tenant-wide stub row ----
tw=$(curl -s -b "$CJ" -X POST "$BASE/v1/pms-providers?tenant_id=$TENANT_DEV" \
    -H 'Content-Type: application/json' \
    -d "{\"name\":\"$NAME\",\"kind\":\"stub\",\"display_name\":\"Tenant-Wide\"}")
tw_id=$(jq -r '.id' <<<"$tw")
tw_scope=$(jq -r '.site_id' <<<"$tw")
[[ -n "$tw_id" && "$tw_id" != "null" ]] || fail "tenant-wide create" "$tw"
[[ -z "$tw_scope" || "$tw_scope" == "null" ]] && pass "tenant-wide row created ($tw_id)" \
                                               || fail "tenant-wide has site_id" "got=$tw_scope"

# ---- 2. dup tenant-wide fails (partial unique index) ----
dup_code=$(curl -s -o /dev/null -w '%{http_code}' -b "$CJ" -X POST \
    "$BASE/v1/pms-providers?tenant_id=$TENANT_DEV" \
    -H 'Content-Type: application/json' \
    -d "{\"name\":\"$NAME\",\"kind\":\"stub\"}")
[[ "$dup_code" == "409" ]] && pass "dup tenant-wide rejected (409)" \
                            || fail "dup tenant-wide" "code=$dup_code"

# ---- 3. site override on SITE_LOCAL (same name, different display) ----
sc=$(curl -s -b "$CJ" -X POST "$BASE/v1/pms-providers?tenant_id=$TENANT_DEV" \
    -H 'Content-Type: application/json' \
    -d "{\"name\":\"$NAME\",\"kind\":\"stub\",\"site_id\":\"$SITE_LOCAL\",\"display_name\":\"Site Override\"}")
sc_id=$(jq -r '.id' <<<"$sc")
sc_scope=$(jq -r '.site_id' <<<"$sc")
[[ -n "$sc_id" && "$sc_id" != "null" ]] || fail "site-scoped create" "$sc"
[[ "$sc_scope" == "$SITE_LOCAL" ]] && pass "site override created on SITE_LOCAL ($sc_id)" \
                                   || fail "site scope mismatch" "got=$sc_scope"

# ---- 4. scd reloads and registers the SITE-scoped row ----
# The pmsloader resolves by scope — because scd's site is SITE_LOCAL, the
# override wins. Confirm via the scd log: look for "scope":"site" for this name.
# Reload is triggered by the ctrlapi configpush above; wait for it to land.
for i in $(seq 1 25); do
    logs=$(journalctl -u stayconnect-scd --since '20s ago' --no-pager | grep "pmsloader: registered" | grep "\"name\":\"$NAME\"" || true)
    scoped=$(echo "$logs" | grep '"scope":"site"' || true)
    [[ -n "$scoped" ]] && break
    sleep 0.2
done
[[ -n "$scoped" ]] && pass "scd loaded override with scope=site" \
                   || fail "scd didn't register site-scoped row" "logs=$logs"

# Also confirm the override log line appears — tenant-wide was skipped.
over=$(journalctl -u stayconnect-scd --since '20s ago' --no-pager | grep "overridden by site-scoped row" | grep "\"name\":\"$NAME\"" || true)
[[ -n "$over" ]] && pass "tenant-wide row skipped in favour of override" \
                 || fail "no override-skipped log" "expected 'overridden by site-scoped row' for $NAME"

# ---- 5. site override for the OTHER site — doesn't affect us ----
sc2=$(curl -s -b "$CJ" -X POST "$BASE/v1/pms-providers?tenant_id=$TENANT_DEV" \
    -H 'Content-Type: application/json' \
    -d "{\"name\":\"$NAME\",\"kind\":\"stub\",\"site_id\":\"$SITE_OTHER\",\"display_name\":\"Other Site Override\"}")
sc2_id=$(jq -r '.id' <<<"$sc2")
[[ -n "$sc2_id" && "$sc2_id" != "null" ]] && pass "override for SITE_OTHER created" \
                                          || fail "SITE_OTHER create" "$sc2"

# Dup for same (site, name) fails (partial unique index).
dup2=$(curl -s -o /dev/null -w '%{http_code}' -b "$CJ" -X POST \
    "$BASE/v1/pms-providers?tenant_id=$TENANT_DEV" \
    -H 'Content-Type: application/json' \
    -d "{\"name\":\"$NAME\",\"kind\":\"stub\",\"site_id\":\"$SITE_OTHER\"}")
[[ "$dup2" == "409" ]] && pass "dup site-scoped (same site, same name) rejected (409)" \
                        || fail "dup site-scoped" "code=$dup2"

# ---- 6. patch with ?site_id targets only the scoped row ----
pcode=$(curl -s -o /dev/null -w '%{http_code}' -b "$CJ" -X PATCH \
    "$BASE/v1/pms-providers/$NAME?tenant_id=$TENANT_DEV&site_id=$SITE_LOCAL" \
    -H 'Content-Type: application/json' \
    -d '{"display_name":"Patched Override"}')
[[ "$pcode" == "200" ]] || fail "patch site-scoped" "code=$pcode"
patched=$(echo "SELECT display_name FROM pms_providers WHERE id='$sc_id';" | $PSQL | head -n1)
[[ "$patched" == "Patched Override" ]] && pass "patch (scope=SITE_LOCAL) hit only the override" \
                                       || fail "patch didn't land on override" "got=$patched"
# Tenant-wide row's display_name unchanged.
tw_display=$(echo "SELECT display_name FROM pms_providers WHERE id='$tw_id';" | $PSQL | head -n1)
[[ "$tw_display" == "Tenant-Wide" ]] && pass "tenant-wide display_name untouched" \
                                     || fail "tenant-wide was modified" "got=$tw_display"

# ---- 7. list returns all three rows with correct scopes ----
list=$(curl -s -b "$CJ" "$BASE/v1/pms-providers?tenant_id=$TENANT_DEV")
n=$(jq --arg n "$NAME" '[.data[] | select(.name==$n)] | length' <<<"$list")
[[ "$n" == "3" ]] && pass "list shows 3 rows (1 tenant-wide, 2 site overrides)" \
                  || fail "list row count" "got=$n list=$(echo \"$list\" | head -c 400)"

# ---- 8. delete site-scoped override → tenant-wide takes effect again ----
dcode=$(curl -s -o /dev/null -w '%{http_code}' -b "$CJ" -X DELETE \
    "$BASE/v1/pms-providers/$NAME?tenant_id=$TENANT_DEV&site_id=$SITE_LOCAL")
[[ "$dcode" == "204" ]] || fail "delete site override" "code=$dcode"
# After delete + reload, the tenant-wide row should register with scope=tenant.
for i in $(seq 1 25); do
    logs=$(journalctl -u stayconnect-scd --since '10s ago' --no-pager | grep "pmsloader: registered" | grep "\"name\":\"$NAME\"" || true)
    tenant_scope=$(echo "$logs" | grep '"scope":"tenant"' || true)
    [[ -n "$tenant_scope" ]] && break
    sleep 0.2
done
[[ -n "$tenant_scope" ]] && pass "after override deleted, tenant-wide takes over (scope=tenant)" \
                         || fail "fallback didn't land" "logs=$logs"

echo
echo "ALL GREEN"
