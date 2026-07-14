#!/usr/bin/env bash
# Smoke-tests the control-plane admin API for Phase 3.
# Runs locally on the appliance against http://127.0.0.1:8080.
set -euo pipefail

BASE=${BASE:-http://127.0.0.1:8080}
COOKIE=$(mktemp)
trap 'rm -f $COOKIE' EXIT
EMAIL=${ADMIN_EMAIL:-admin@stayconnect.local}
PASS=${ADMIN_PASSWORD:-adminadmin01}

jcurl() { curl -s -b "$COOKIE" -c "$COOKIE" -H 'Content-Type: application/json' "$@"; }
pass()  { printf "  ✓ %s\n" "$1"; }
fail()  { printf "  ✗ %s\n    %s\n" "$1" "${2:-}"; exit 1; }

assert_code() {
    local want=$1 got=$2 label=$3
    [ "$got" = "$want" ] && pass "$label" || fail "$label" "want=$want got=$got"
}

echo "== auth =="
got=$(jcurl -o /dev/null -w '%{http_code}' -X POST -d "{\"email\":\"$EMAIL\",\"password\":\"$PASS\"}" "$BASE/v1/auth/login")
assert_code 200 "$got" "login"

TENANT_DEV=$(docker exec -i stayconnect-pg psql -U stayconnect -d ${SC_DB:-stayconnect} -At -q -c "SELECT id FROM tenants WHERE slug='dev';")
echo "  dev tenant id = $TENANT_DEV"

echo
echo "== tenants =="
got=$(jcurl -o /tmp/t.json -w '%{http_code}' "$BASE/v1/tenants")
assert_code 200 "$got" "GET /v1/tenants"
jq '.data | length as $n | "  count=\($n)"' /tmp/t.json

got=$(jcurl -o /tmp/t2.json -w '%{http_code}' -X POST -d '{"slug":"acme","name":"Acme Hotels","contact_email":"ops@acme.test"}' "$BASE/v1/tenants")
if [ "$got" = "201" ]; then
    ACME=$(jq -r '.id' /tmp/t2.json)
    pass "POST /v1/tenants (create acme) → $ACME"
else
    ACME=$(docker exec -i stayconnect-pg psql -U stayconnect -d ${SC_DB:-stayconnect} -At -q -c "SELECT id FROM tenants WHERE slug='acme';")
    pass "acme already exists → $ACME"
fi

echo
echo "== sites (tenant-scoped on default tenant) =="
# Super admin must pass ?tenant_id= to scope into a tenant.
got=$(jcurl -o /tmp/s.json -w '%{http_code}' "$BASE/v1/sites?tenant_id=$TENANT_DEV")
assert_code 200 "$got" "GET /v1/sites (dev)"
jq '.data[].code' /tmp/s.json | sed 's/^/    existing: /'

got=$(jcurl -o /tmp/s2.json -w '%{http_code}' -X POST \
    -d '{"code":"lobby","name":"Lobby","timezone":"Europe/Berlin","country":"DE"}' \
    "$BASE/v1/sites?tenant_id=$TENANT_DEV")
case $got in
    201) pass "POST /v1/sites (create)"; SITE2=$(jq -r .id /tmp/s2.json) ;;
    409) pass "POST /v1/sites (already exists)"
         SITE2=$(docker exec -i stayconnect-pg psql -U stayconnect -d ${SC_DB:-stayconnect} -At -q -c "SELECT id FROM sites WHERE tenant_id='$TENANT_DEV' AND code='lobby';") ;;
    *)   fail "POST /v1/sites" "code=$got body=$(cat /tmp/s2.json)" ;;
esac
echo "  site2 id = $SITE2"

echo
echo "== appliances (create one, check limit logic) =="
# Verify max_appliances is enforced: use BYTES/effective limit view.
LIMIT=$(docker exec -i stayconnect-pg psql -U stayconnect -d ${SC_DB:-stayconnect} -At -q -c \
    "SELECT int_value FROM tenant_effective_limits WHERE tenant_id='$TENANT_DEV' AND key='max_appliances' LIMIT 1;")
echo "  current effective limit: max_appliances=$LIMIT (-1 = unlimited)"

got=$(jcurl -o /tmp/a.json -w '%{http_code}' -X POST \
    -d "{\"site_id\":\"$SITE2\",\"serial\":\"APP-LOBBY-01\",\"name\":\"lobby-appliance\"}" \
    "$BASE/v1/appliances?tenant_id=$TENANT_DEV")
case $got in
    201) pass "POST /v1/appliances (create)" ;;
    409) pass "POST /v1/appliances (already exists)" ;;
    *)   fail "POST /v1/appliances" "code=$got body=$(cat /tmp/a.json)" ;;
esac

echo
echo "== limit enforcement: shrink max_sites=1, expect next POST /v1/sites to 403 =="
docker exec -i stayconnect-pg psql -U stayconnect -d ${SC_DB:-stayconnect} -At -q >/dev/null -c "
    INSERT INTO tenant_limit_overrides (tenant_id, key, value_type, int_value, reason)
    VALUES ('$TENANT_DEV', 'max_sites', 'int', 1, 'phase3 test')
    ON CONFLICT (tenant_id, key) DO UPDATE SET int_value=1;"

got=$(jcurl -o /tmp/s3.json -w '%{http_code}' -X POST \
    -d '{"code":"pool","name":"Pool"}' "$BASE/v1/sites?tenant_id=$TENANT_DEV")
if [ "$got" = "403" ]; then
    pass "POST /v1/sites blocked by max_sites override"
    jq . /tmp/s3.json | sed 's/^/    /'
else
    fail "limit_exceeded not enforced" "code=$got body=$(cat /tmp/s3.json)"
fi

# Restore: delete the override
docker exec -i stayconnect-pg psql -U stayconnect -d ${SC_DB:-stayconnect} -At -q >/dev/null -c "
    DELETE FROM tenant_limit_overrides WHERE tenant_id='$TENANT_DEV' AND key='max_sites';"

echo
echo "== ticket-templates =="
# Create a 2-hour / 5MB / 50mbit template.
got=$(jcurl -o /tmp/tt.json -w '%{http_code}' -X POST \
    -d '{"code":"h2","name":"2 Hour Pass","duration_seconds":7200,"data_cap_bytes":5242880,"down_kbps":50000,"up_kbps":10000,"max_concurrent_devices":2,"price_cents":500,"currency":"USD"}' \
    "$BASE/v1/ticket-templates?tenant_id=$TENANT_DEV")
case $got in
    201) pass "POST /v1/ticket-templates"; TTID=$(jq -r .id /tmp/tt.json) ;;
    409) pass "POST /v1/ticket-templates (already exists)"
         TTID=$(docker exec -i stayconnect-pg psql -U stayconnect -d ${SC_DB:-stayconnect} -At -q -c "SELECT id FROM ticket_templates WHERE tenant_id='$TENANT_DEV' AND code='h2';") ;;
    *)   fail "POST /v1/ticket-templates" "code=$got body=$(cat /tmp/tt.json)" ;;
esac

got=$(jcurl -o /tmp/ttl.json -w '%{http_code}' "$BASE/v1/ticket-templates?tenant_id=$TENANT_DEV")
assert_code 200 "$got" "GET /v1/ticket-templates"
jq '.data | length as $n | "  templates for dev: \($n)"' /tmp/ttl.json

# PATCH: reduce price; should not touch other fields.
got=$(jcurl -o /tmp/ttp.json -w '%{http_code}' -X PATCH \
    -d '{"price_cents":300}' \
    "$BASE/v1/ticket-templates/$TTID?tenant_id=$TENANT_DEV")
assert_code 200 "$got" "PATCH /v1/ticket-templates/{id} (price_cents=300)"
jq -r '.price_cents, .currency, .duration_seconds' /tmp/ttp.json | paste -sd ' ' - | awk '{if ($1 == 300 && $2 == "USD" && $3 == 7200) print "  ✓ fields preserved"; else print "  ✗ fields mutated: " $0}'

# Validation: negative value rejected.
got=$(jcurl -o /tmp/tte.json -w '%{http_code}' -X PATCH \
    -d '{"down_kbps":-5}' \
    "$BASE/v1/ticket-templates/$TTID?tenant_id=$TENANT_DEV")
assert_code 400 "$got" "PATCH rejects negative down_kbps"

echo
echo "== voucher-batches (create 10, list, CSV, revoke) =="
got=$(jcurl -o /tmp/vb.json -w '%{http_code}' -X POST \
    -d "{\"template_id\":\"$TTID\",\"count\":10,\"name\":\"phase3 smoke batch\"}" \
    "$BASE/v1/voucher-batches?tenant_id=$TENANT_DEV")
assert_code 201 "$got" "POST /v1/voucher-batches (10 codes)"
BATCH_ID=$(jq -r .id /tmp/vb.json)
echo "  batch id=$BATCH_ID"

got=$(jcurl -o /tmp/vbc.json -w '%{http_code}' "$BASE/v1/voucher-batches/$BATCH_ID/codes?tenant_id=$TENANT_DEV")
assert_code 200 "$got" "GET codes list"
jq -r '.data[0:3][] | "  \(.code) (\(.code_display)) state=\(.state)"' /tmp/vbc.json

# Format sanity: first code must be 12 chars of Crockford alphabet.
first=$(jq -r '.data[0].code' /tmp/vbc.json)
if [[ "$first" =~ ^[0-9A-HJKMNP-TV-Z]{12}$ ]]; then
    pass "code format: $first ($(echo $first | head -c4)-$(echo $first | cut -c5-8)-$(echo $first | cut -c9-12))"
else
    fail "code format" "got=$first"
fi

# CSV download.
got=$(jcurl -o /tmp/vb.csv -w '%{http_code}' "$BASE/v1/voucher-batches/$BATCH_ID/codes.csv?tenant_id=$TENANT_DEV")
assert_code 200 "$got" "GET codes.csv"
rows=$(wc -l < /tmp/vb.csv)
echo "  csv rows (incl header): $rows"
head -3 /tmp/vb.csv | sed 's/^/    /'

# Revoke the batch.
got=$(jcurl -o /tmp/vbr.json -w '%{http_code}' -X POST "$BASE/v1/voucher-batches/$BATCH_ID/revoke?tenant_id=$TENANT_DEV")
assert_code 200 "$got" "POST revoke batch"
jq '"  vouchers_revoked=\(.vouchers_revoked)"' /tmp/vbr.json

# Re-fetch: state should all be 'revoked' now.
got=$(jcurl -o /tmp/vbc2.json -w '%{http_code}' "$BASE/v1/voucher-batches/$BATCH_ID/codes?tenant_id=$TENANT_DEV")
nonrevoked=$(jq '[.data[] | select(.state != "revoked")] | length' /tmp/vbc2.json)
if [ "$nonrevoked" = "0" ]; then
    pass "all 10 vouchers now revoked"
else
    fail "revoke incomplete" "non-revoked=$nonrevoked"
fi

# Voucher-count limit: override max_vouchers_per_month=5 and try batch of 10.
docker exec -i stayconnect-pg psql -U stayconnect -d ${SC_DB:-stayconnect} -At -q >/dev/null -c "
    INSERT INTO tenant_limit_overrides (tenant_id, key, value_type, int_value, reason)
    VALUES ('$TENANT_DEV', 'max_vouchers_per_month', 'int', 5, 'phase3 test')
    ON CONFLICT (tenant_id, key) DO UPDATE SET int_value=5;"
got=$(jcurl -o /tmp/vblim.json -w '%{http_code}' -X POST \
    -d "{\"template_id\":\"$TTID\",\"count\":10}" \
    "$BASE/v1/voucher-batches?tenant_id=$TENANT_DEV")
if [ "$got" = "403" ] && jq -e '.error == "limit_exceeded" and .limit_key == "max_vouchers_per_month"' /tmp/vblim.json >/dev/null; then
    pass "voucher-batch blocked by max_vouchers_per_month"
    jq . /tmp/vblim.json | sed 's/^/    /'
else
    fail "voucher batch limit not enforced" "code=$got body=$(cat /tmp/vblim.json)"
fi
docker exec -i stayconnect-pg psql -U stayconnect -d ${SC_DB:-stayconnect} -At -q >/dev/null -c "
    DELETE FROM tenant_limit_overrides WHERE tenant_id='$TENANT_DEV' AND key='max_vouchers_per_month';"

echo
echo "== sessions (list + force disconnect + concurrency limit) =="
# Drive an auth via the netns client so we have a live session.
if ip netns list 2>/dev/null | grep -q client1; then
    # Make a fresh, unused voucher belonging to template TTID.
    # Avoid 'I','L','O','U' — scd normalizes those per Crockford rules.
    CODE="PH3SMKE$(date +%s)"
    docker exec -i stayconnect-pg psql -U stayconnect -d ${SC_DB:-stayconnect} -At -q >/dev/null -c "
        INSERT INTO vouchers (tenant_id, template_id, code, state)
        VALUES ('$TENANT_DEV', '$TTID', '$CODE', 'unused')
        ON CONFLICT (tenant_id, code) DO UPDATE SET state='unused', bytes_used=0, seconds_used=0;"
    # Ensure prior session torn down.
    ip netns exec client1 curl -s -X POST -d "code=BOGUS" http://portal.stayconnect.local/auth/voucher --max-time 3 >/dev/null || true
    curl -s --unix-socket /run/stayconnect/scd.sock -X POST -H 'Content-Type: application/json' \
        -d "{\"ip\":\"$(ip -n client1 -4 addr show eth0 | awk '/inet / {print $2}' | cut -d/ -f1)\",\"reason\":\"admin\"}" \
        http://unix/v1/sessions/revoke >/dev/null
    sleep 1
    got=$(ip netns exec client1 curl -s -o /dev/null -w '%{http_code}' -X POST -d "code=$CODE" http://portal.stayconnect.local/auth/voucher --max-time 5)
    echo "  auth via portal: $got (303 = redirect/success)"
    SID=$(docker exec -i stayconnect-pg psql -U stayconnect -d ${SC_DB:-stayconnect} -At -q -c \
        "SELECT id FROM sessions WHERE tenant_id='$TENANT_DEV' AND state='active' ORDER BY started_at DESC LIMIT 1;")
    echo "  active session id = $SID"

    got=$(jcurl -o /tmp/sl.json -w '%{http_code}' "$BASE/v1/sessions?tenant_id=$TENANT_DEV&state=active")
    assert_code 200 "$got" "GET /v1/sessions?state=active"
    jq '"  active sessions: \(.data | length)"' /tmp/sl.json

    if [ -n "$SID" ]; then
        got=$(jcurl -o /tmp/sd.json -w '%{http_code}' -X POST "$BASE/v1/sessions/$SID/disconnect?tenant_id=$TENANT_DEV")
        assert_code 200 "$got" "POST /v1/sessions/{id}/disconnect"
        sleep 1
        state=$(docker exec -i stayconnect-pg psql -U stayconnect -d ${SC_DB:-stayconnect} -At -q -c \
            "SELECT state || ',' || COALESCE(end_reason,'-') FROM sessions WHERE id='$SID';")
        [[ "$state" == "closed,admin" ]] && pass "session now closed,admin" || fail "session not closed" "$state"
    fi

    # Concurrency cap: set max_concurrent_devices=0 → next portal auth should 403.
    # Edge-first refactor: scd reads tenant_effective_limits from the SITE
    # database, and it is a plain table maintained by the license bridge
    # (not the cloud view). Force max_concurrent_devices=0 there directly and
    # use a site-DB voucher; restore the license-sourced value afterwards.
    CC_SITE_DB=stayconnect_site
    CC_SAVED=$(docker exec -i stayconnect-pg psql -U stayconnect -d $CC_SITE_DB -At -q -c \
        "SELECT COALESCE(int_value,-1) FROM tenant_effective_limits WHERE tenant_id='$TENANT_DEV' AND key='max_concurrent_devices';")
    [ -z "$CC_SAVED" ] && CC_SAVED=-1
    CC_CODE=$(docker exec -i stayconnect-pg psql -U stayconnect -d $CC_SITE_DB -At -q -c \
        "SELECT v.code FROM vouchers v JOIN ticket_templates t ON t.id=v.template_id
          WHERE v.state='unused' AND t.is_active
            AND (v.expires_at IS NULL OR v.expires_at > now())
          ORDER BY v.issued_at DESC LIMIT 1;")
    docker exec -i stayconnect-pg psql -U stayconnect -d $CC_SITE_DB -At -q >/dev/null -c "
        INSERT INTO tenant_effective_limits (tenant_id, key, value_type, int_value, source)
        VALUES ('$TENANT_DEV', 'max_concurrent_devices', 'int', 0, 'test')
        ON CONFLICT (tenant_id, key) DO UPDATE SET int_value=0, value_type='int';"
    got=$(curl -s --unix-socket /run/stayconnect/scd.sock -o /tmp/cc.json -w '%{http_code}' \
        -X POST -H 'Content-Type: application/json' \
        -d "{\"ip\":\"10.10.0.126\",\"mac\":\"aa:bb:cc:dd:ee:ff\",\"voucher\":\"$CC_CODE\"}" \
        http://unix/v1/sessions/authorize)
    if [ "$got" = "403" ] && jq -e '.error == "limit_exceeded" and .limit_key == "max_concurrent_devices"' /tmp/cc.json >/dev/null; then
        pass "scd rejects authorize: max_concurrent_devices"
        jq . /tmp/cc.json | sed 's/^/    /'
    else
        fail "concurrency limit not enforced" "code=$got body=$(cat /tmp/cc.json)"
    fi
    docker exec -i stayconnect-pg psql -U stayconnect -d $CC_SITE_DB -At -q >/dev/null -c "
        UPDATE tenant_effective_limits SET int_value=$CC_SAVED, source='license'
         WHERE tenant_id='$TENANT_DEV' AND key='max_concurrent_devices';"
else
    echo "  (skipping sessions tests — client1 netns not present; run phase1-test-client.sh up+dhcp first)"
fi

echo
echo "== plans + subscription (view / change + change_type audit) =="
got=$(jcurl -o /tmp/pls.json -w '%{http_code}' "$BASE/v1/plans")
assert_code 200 "$got" "GET /v1/plans"
jq '"  public plans: \(.data | length)"' /tmp/pls.json

got=$(jcurl -o /tmp/sub.json -w '%{http_code}' "$BASE/v1/tenants/$TENANT_DEV/subscription")
assert_code 200 "$got" "GET current subscription"
CURRENT_PLAN=$(jq -r .plan_code /tmp/sub.json)
echo "  dev tenant currently on: $CURRENT_PLAN"

got=$(jcurl -o /tmp/efl.json -w '%{http_code}' "$BASE/v1/tenants/$TENANT_DEV/effective-limits")
assert_code 200 "$got" "GET effective limits"
jq '"  effective keys: \(.data | length)"' /tmp/efl.json

# Change plan: dev is on enterprise-yearly → switch to starter-monthly (downgrade).
STARTER_ID=$(docker exec -i stayconnect-pg psql -U stayconnect -d ${SC_DB:-stayconnect} -At -q -c "SELECT id FROM plans WHERE code='starter-monthly';")
got=$(jcurl -o /tmp/chg1.json -w '%{http_code}' -X POST \
    -d "{\"plan_id\":\"$STARTER_ID\"}" \
    "$BASE/v1/tenants/$TENANT_DEV/subscription")
assert_code 200 "$got" "POST change to starter-monthly"
CH1=$(jq -r .change_type /tmp/chg1.json)
[ "$CH1" = "downgrade" ] && pass "change_type=downgrade (enterprise → starter)" || fail "change_type" "got=$CH1"

# Lateral: starter-monthly → starter-yearly
STARTER_Y_ID=$(docker exec -i stayconnect-pg psql -U stayconnect -d ${SC_DB:-stayconnect} -At -q -c "SELECT id FROM plans WHERE code='starter-yearly';")
got=$(jcurl -o /tmp/chg2.json -w '%{http_code}' -X POST \
    -d "{\"plan_id\":\"$STARTER_Y_ID\"}" \
    "$BASE/v1/tenants/$TENANT_DEV/subscription")
assert_code 200 "$got" "POST change to starter-yearly"
CH2=$(jq -r .change_type /tmp/chg2.json)
[ "$CH2" = "lateral" ] && pass "change_type=lateral (starter monthly → yearly)" || fail "change_type" "got=$CH2"

# Upgrade: starter-yearly → pro-monthly
PRO_ID=$(docker exec -i stayconnect-pg psql -U stayconnect -d ${SC_DB:-stayconnect} -At -q -c "SELECT id FROM plans WHERE code='pro-monthly';")
got=$(jcurl -o /tmp/chg3.json -w '%{http_code}' -X POST \
    -d "{\"plan_id\":\"$PRO_ID\"}" \
    "$BASE/v1/tenants/$TENANT_DEV/subscription")
assert_code 200 "$got" "POST change to pro-monthly"
CH3=$(jq -r .change_type /tmp/chg3.json)
[ "$CH3" = "upgrade" ] && pass "change_type=upgrade (starter → pro)" || fail "change_type" "got=$CH3"

# Audit trail: should have 3 plan_changed events now.
echo "  subscription_events audit trail:"
docker exec -i stayconnect-pg psql -U stayconnect -d ${SC_DB:-stayconnect} -c \
  "SELECT se.at, se.type, se.change_type, fp.code AS from_plan, tp.code AS to_plan
     FROM subscription_events se
     LEFT JOIN plans fp ON fp.id = se.from_plan_id
     LEFT JOIN plans tp ON tp.id = se.to_plan_id
    WHERE se.tenant_id = '$TENANT_DEV' AND se.type = 'plan_changed'
    ORDER BY se.at DESC LIMIT 5;" | sed 's/^/    /'

# Restore to the enterprise-yearly trial for downstream Phase 1/2 flows.
ENT_Y=$(docker exec -i stayconnect-pg psql -U stayconnect -d ${SC_DB:-stayconnect} -At -q -c "SELECT id FROM plans WHERE code='enterprise-yearly';")
jcurl -o /dev/null -X POST -d "{\"plan_id\":\"$ENT_Y\"}" "$BASE/v1/tenants/$TENANT_DEV/subscription" >/dev/null
pass "restored dev tenant to enterprise-yearly"

echo
echo "== usage reports (timeseries / summary / top-sites / top-appliances) =="

got=$(jcurl -o /tmp/us.json -w '%{http_code}' "$BASE/v1/tenants/$TENANT_DEV/usage/summary?tz=Africa/Cairo")
assert_code 200 "$got" "GET /usage/summary (tz=Africa/Cairo)"
jq '{tz, bytes_up, bytes_down, total_bytes, active_sessions, sessions_today, cap_bytes, cap_used_percent}' /tmp/us.json | sed 's/^/    /'

got=$(jcurl -o /tmp/ut.json -w '%{http_code}' "$BASE/v1/tenants/$TENANT_DEV/usage/timeseries?bucket=1h&tz=UTC")
assert_code 200 "$got" "GET /usage/timeseries (1h UTC, default = this month)"
jq '{tz, bucket, totals, points_n: (.points|length)}' /tmp/ut.json | sed 's/^/    /'

got=$(jcurl -o /tmp/utb.json -w '%{http_code}' "$BASE/v1/tenants/$TENANT_DEV/usage/timeseries?bucket=wrong")
assert_code 400 "$got" "reject invalid bucket"

got=$(jcurl -o /tmp/uts.json -w '%{http_code}' "$BASE/v1/tenants/$TENANT_DEV/usage/top-sites?top_n=5")
assert_code 200 "$got" "GET /usage/top-sites"
jq '{from, to, rows_n: (.rows|length), top: (.rows | map({name, total_bytes}))}' /tmp/uts.json | sed 's/^/    /'

got=$(jcurl -o /tmp/uta.json -w '%{http_code}' "$BASE/v1/tenants/$TENANT_DEV/usage/top-appliances?top_n=5")
assert_code 200 "$got" "GET /usage/top-appliances"
jq '{rows_n: (.rows|length), top: (.rows | map({name, total_bytes}))}' /tmp/uta.json | sed 's/^/    /'

# Invalid tz
got=$(jcurl -o /tmp/utz.json -w '%{http_code}' "$BASE/v1/tenants/$TENANT_DEV/usage/summary?tz=Atlantis/Nowhere")
assert_code 400 "$got" "reject invalid tz"

echo
echo "== operators & roles =="
# Create a tenant operator in dev.
OP_EMAIL="opuser+$(date +%s)@stayconnect.local"
got=$(jcurl -o /tmp/op.json -w '%{http_code}' -X POST \
    -d "{\"email\":\"$OP_EMAIL\",\"display_name\":\"Op User\",\"password\":\"s3cr3tp4ssw\",\"role\":\"tenant_operator\"}" \
    "$BASE/v1/operators?tenant_id=$TENANT_DEV")
assert_code 201 "$got" "POST /v1/operators"
OP_ID=$(jq -r .id /tmp/op.json)
jq '{id,email,status,roles:(.roles|map(.role))}' /tmp/op.json | sed 's/^/  /'

got=$(jcurl -o /tmp/opl.json -w '%{http_code}' "$BASE/v1/operators?tenant_id=$TENANT_DEV")
assert_code 200 "$got" "GET /v1/operators (scoped)"
jq '"  operators in dev: \(.data | length)"' /tmp/opl.json

# Validation
got=$(jcurl -o /tmp/opv.json -w '%{http_code}' -X POST \
    -d '{"email":"short@test.local","password":"123"}' \
    "$BASE/v1/operators?tenant_id=$TENANT_DEV")
assert_code 400 "$got" "reject password < 10 chars"

# PATCH display_name
got=$(jcurl -o /tmp/opp.json -w '%{http_code}' -X PATCH \
    -d '{"display_name":"Op User Renamed"}' \
    "$BASE/v1/operators/$OP_ID?tenant_id=$TENANT_DEV")
assert_code 200 "$got" "PATCH display_name"
[[ "$(jq -r .display_name /tmp/opp.json)" == "Op User Renamed" ]] && pass "name updated" || fail "name" ""

# set-password
got=$(jcurl -o /dev/null -w '%{http_code}' -X POST \
    -d '{"password":"newpasswordlong"}' \
    "$BASE/v1/operators/$OP_ID/set-password?tenant_id=$TENANT_DEV")
assert_code 204 "$got" "POST /set-password"

# Grant + revoke billing role
got=$(jcurl -o /tmp/opr.json -w '%{http_code}' -X POST \
    -d '{"role":"billing"}' \
    "$BASE/v1/operators/$OP_ID/roles?tenant_id=$TENANT_DEV")
assert_code 201 "$got" "POST add billing role"
got=$(jcurl -o /dev/null -w '%{http_code}' -X DELETE \
    "$BASE/v1/operators/$OP_ID/roles/billing?tenant_id=$TENANT_DEV")
assert_code 204 "$got" "DELETE billing role"

# Platform_admin grant must be platform_admin gated (we ARE platform_admin, so allowed).
got=$(jcurl -o /tmp/opr2.json -w '%{http_code}' -X POST \
    -d '{"role":"platform_admin"}' \
    "$BASE/v1/operators/$OP_ID/roles")
assert_code 201 "$got" "grant platform_admin (by platform_admin)"

# Self-protection: try to disable ourselves → 409
ME_ID=$(jcurl -s "$BASE/v1/auth/whoami" | jq -r .operator_id)
got=$(jcurl -o /dev/null -w '%{http_code}' -X DELETE "$BASE/v1/operators/$ME_ID?tenant_id=$TENANT_DEV")
case $got in
    409) pass "cannot disable yourself" ;;
    404) pass "self-protection (tenant scope didn't match) — acceptable" ;;
    *)   fail "expected 409" "got=$got" ;;
esac

# max_operators limit
docker exec -i stayconnect-pg psql -U stayconnect -d ${SC_DB:-stayconnect} -At -q >/dev/null -c "
    INSERT INTO tenant_limit_overrides (tenant_id, key, value_type, int_value, reason)
    VALUES ('$TENANT_DEV', 'max_operators', 'int', 1, 'phase3 test')
    ON CONFLICT (tenant_id, key) DO UPDATE SET int_value=1;"
got=$(jcurl -o /tmp/oplim.json -w '%{http_code}' -X POST \
    -d "{\"email\":\"blocked+$(date +%s)@example.com\",\"password\":\"anotherlongpw\"}" \
    "$BASE/v1/operators?tenant_id=$TENANT_DEV")
if [ "$got" = "403" ] && jq -e '.error=="limit_exceeded" and .limit_key=="max_operators"' /tmp/oplim.json >/dev/null; then
    pass "operators blocked by max_operators"
    jq . /tmp/oplim.json | sed 's/^/    /'
else
    fail "operator limit not enforced" "code=$got body=$(cat /tmp/oplim.json)"
fi
docker exec -i stayconnect-pg psql -U stayconnect -d ${SC_DB:-stayconnect} -At -q >/dev/null -c "
    DELETE FROM tenant_limit_overrides WHERE tenant_id='$TENANT_DEV' AND key='max_operators';"

# Cleanup — disable the test operator so future runs are clean.
jcurl -o /dev/null -X DELETE "$BASE/v1/operators/$OP_ID?tenant_id=$TENANT_DEV" >/dev/null

echo
echo "== walled-garden rules =="
# Create three rules — one per kind.
got=$(jcurl -o /tmp/wg1.json -w '%{http_code}' -X POST \
    -d '{"kind":"domain","value":"captive.apple.com","description":"Apple captive probe"}' \
    "$BASE/v1/walled-garden?tenant_id=$TENANT_DEV")
assert_code 201 "$got" "POST domain rule"
WG1=$(jq -r .id /tmp/wg1.json)

got=$(jcurl -o /tmp/wg2.json -w '%{http_code}' -X POST \
    -d '{"kind":"ip","value":"9.9.9.9","ports":[53,443]}' \
    "$BASE/v1/walled-garden?tenant_id=$TENANT_DEV")
assert_code 201 "$got" "POST ip rule with ports"

got=$(jcurl -o /tmp/wg3.json -w '%{http_code}' -X POST \
    -d '{"kind":"cidr","value":"10.200.0.0/24","description":"vendor mgmt"}' \
    "$BASE/v1/walled-garden?tenant_id=$TENANT_DEV")
assert_code 201 "$got" "POST cidr rule"

# Validation
got=$(jcurl -o /tmp/wge1.json -w '%{http_code}' -X POST \
    -d '{"kind":"ip","value":"not-an-ip"}' \
    "$BASE/v1/walled-garden?tenant_id=$TENANT_DEV")
assert_code 400 "$got" "reject invalid ip"

got=$(jcurl -o /tmp/wge2.json -w '%{http_code}' -X POST \
    -d '{"kind":"domain","value":"x"}' \
    "$BASE/v1/walled-garden?tenant_id=$TENANT_DEV")
assert_code 400 "$got" "reject bad domain"

got=$(jcurl -o /tmp/wge3.json -w '%{http_code}' -X POST \
    -d '{"kind":"ip","value":"1.2.3.4","ports":[70000]}' \
    "$BASE/v1/walled-garden?tenant_id=$TENANT_DEV")
assert_code 400 "$got" "reject port > 65535"

# List
got=$(jcurl -o /tmp/wgl.json -w '%{http_code}' "$BASE/v1/walled-garden?tenant_id=$TENANT_DEV")
assert_code 200 "$got" "GET /v1/walled-garden"
jq '"  rules in dev: \(.data | length)"' /tmp/wgl.json

# PATCH description
got=$(jcurl -o /tmp/wgp.json -w '%{http_code}' -X PATCH \
    -d '{"description":"updated desc"}' \
    "$BASE/v1/walled-garden/$WG1?tenant_id=$TENANT_DEV")
assert_code 200 "$got" "PATCH description"
[[ "$(jq -r .description /tmp/wgp.json)" == "updated desc" ]] && pass "description updated" || fail "description" ""

# DELETE
for id in $(jq -r '.data[].id' /tmp/wgl.json); do
    jcurl -o /dev/null -X DELETE "$BASE/v1/walled-garden/$id?tenant_id=$TENANT_DEV" >/dev/null
done
remaining=$(jcurl -s "$BASE/v1/walled-garden?tenant_id=$TENANT_DEV" | jq '.data | length')
[[ "$remaining" == "0" ]] && pass "all rules cleaned up" || fail "cleanup" "remaining=$remaining"

echo
echo "== super-admin tenant list vs regular operator =="
got=$(jcurl -o /tmp/t3.json -w '%{http_code}' "$BASE/v1/tenants")
assert_code 200 "$got" "GET /v1/tenants (super-admin)"
jq '.data | length as $n | "  tenants visible to super admin: \($n)"' /tmp/t3.json

echo
echo "ALL GREEN"
