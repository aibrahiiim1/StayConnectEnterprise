#!/usr/bin/env bash
# Phase 5.1 — appliance identity + enrollment E2E.
#
# Flow:
#   - admin logs in, mints a bootstrap token for the dev tenant
#   - scd-enroll-test runs the identity flow: generates a keypair, enrolls
#     via the plaintext token, signs a JWT, and calls /v1/appliance/hello
#   - we verify the appliances row is bound to the new public key and
#     identity_verified_at is populated
#   - replay of the same JWT is rejected
#   - a revoked token stops working
set -euo pipefail

BASE=${BASE:-http://127.0.0.1:8080}
ADMIN_EMAIL=${ADMIN_EMAIL:-admin@stayconnect.local}
ADMIN_PASS=${ADMIN_PASSWORD:-adminadmin01}
PSQL="docker exec -i stayconnect-pg psql -U stayconnect -d stayconnect -At -q -v ON_ERROR_STOP=1"

pass() { printf "  ✓ %s\n" "$1"; }
fail() { printf "  ✗ %s\n    %s\n" "$1" "${2:-}"; exit 1; }

TENANT_DEV=$(echo "SELECT id FROM tenants WHERE slug='dev';" | $PSQL | head -n1)
SITE_DEV=$(echo "SELECT id FROM sites WHERE tenant_id='$TENANT_DEV' ORDER BY created_at LIMIT 1;" | $PSQL | head -n1)
[[ -n "$TENANT_DEV" && -n "$SITE_DEV" ]] || fail "no dev tenant/site"

CJ=$(mktemp)
IDENT_DIR=$(mktemp -d)
TEST_SERIAL="enroll-test-$(date +%s)"
trap "rm -rf $CJ $IDENT_DIR; echo \"DELETE FROM appliances WHERE serial LIKE 'enroll-test-%';\" | $PSQL >/dev/null 2>&1 || true; echo \"DELETE FROM appliance_bootstrap_tokens WHERE expected_serial LIKE 'enroll-test-%';\" | $PSQL >/dev/null 2>&1 || true" EXIT

code=$(curl -s -o /dev/null -w '%{http_code}' -c "$CJ" -X POST \
    -H 'Content-Type: application/json' \
    -d "{\"email\":\"$ADMIN_EMAIL\",\"password\":\"$ADMIN_PASS\"}" \
    "$BASE/v1/auth/login")
[[ "$code" == "200" ]] || fail "admin login" "code=$code"
pass "admin session"

# ---- mint bootstrap token ----
mint_resp=$(curl -s -b "$CJ" -X POST "$BASE/v1/appliance-bootstrap-tokens?tenant_id=$TENANT_DEV" \
    -H 'Content-Type: application/json' \
    -d "{\"site_id\":\"$SITE_DEV\",\"expected_serial\":\"$TEST_SERIAL\",\"ttl_hours\":1}")
TOKEN=$(jq -r '.token' <<<"$mint_resp")
TOKEN_ID=$(jq -r '.row.id' <<<"$mint_resp")
[[ -n "$TOKEN" && "$TOKEN" != "null" ]] || fail "mint token" "$mint_resp"
pass "bootstrap token minted (id=$TOKEN_ID, hint=$(jq -r '.row.token_hint' <<<"$mint_resp"))"

# Plaintext should only appear on this create response; a subsequent GET must
# NOT echo it.
list_resp=$(curl -s -b "$CJ" "$BASE/v1/appliance-bootstrap-tokens?tenant_id=$TENANT_DEV")
echo "$list_resp" | jq -e --arg id "$TOKEN_ID" '.data[] | select(.id == $id) | has("token") | not' >/dev/null \
    && pass "token plaintext is write-only" \
    || fail "token leaked in list" "$list_resp"

# ---- run scd-enroll-test ----
ENROLL_OUT=$(SCD_IDENTITY_DIR="$IDENT_DIR" \
    SCD_CTRLAPI_BASE="$BASE" \
    SCD_BOOTSTRAP_TOKEN="$TOKEN" \
    SCD_SERIAL="$TEST_SERIAL" \
    /opt/stayconnect/bin/scd-enroll-test)
APP_ID=$(jq -r '.appliance_id' <<<"$ENROLL_OUT")
[[ -n "$APP_ID" && "$APP_ID" != "null" ]] || fail "enroll" "$ENROLL_OUT"
pass "enrolled (appliance_id=$APP_ID)"

# Identity files persisted.
[[ -f "$IDENT_DIR/identity.json" ]] || fail "identity.json missing"
[[ -f "$IDENT_DIR/ed25519.key"   ]] || fail "ed25519.key missing"
KEY_PERMS=$(stat -c %a "$IDENT_DIR/ed25519.key")
[[ "$KEY_PERMS" == "600" ]] && pass "identity files persisted (key perms 600)" \
                            || fail "key perms" "got=$KEY_PERMS"

# DB row reflects enrollment.
row=$(echo "SELECT status, public_key IS NOT NULL, identity_verified_at IS NOT NULL, serial
              FROM appliances WHERE id='$APP_ID';" | $PSQL)
IFS='|' read -r DB_STATUS DB_HAS_KEY DB_HAS_VERIFY DB_SERIAL <<<"$row"
[[ "$DB_STATUS" == "enrolled" ]] && pass "appliance.status=enrolled" \
                                || fail "status" "got=$DB_STATUS"
[[ "$DB_HAS_KEY" == "t" ]]      && pass "public_key bound"            || fail "public_key missing"
[[ "$DB_HAS_VERIFY" == "t" ]]   && pass "identity_verified_at set"    || fail "verified_at missing"
[[ "$DB_SERIAL" == "$TEST_SERIAL" ]] && pass "serial bound=$TEST_SERIAL" || fail "serial mismatch" "got=$DB_SERIAL"

# Token row: consumed_at populated.
consumed=$(echo "SELECT consumed_at IS NOT NULL FROM appliance_bootstrap_tokens WHERE id='$TOKEN_ID';" | $PSQL)
[[ "$consumed" == "t" ]] && pass "token consumed_at set" || fail "consumed_at missing" "got=$consumed"

# ---- replay rejection ----
# Craft a signed JWT, call /hello twice — second call must 401 (replay).
# scd-enroll-test always signs a fresh token; use a tiny curl loop by
# extracting the token from the running binary: easiest path is to call
# /hello through the real binary once more (which enrolls a NEW jti), and
# separately prove that feeding a repeated jti trips the cache.
HELLO1=$(SCD_IDENTITY_DIR="$IDENT_DIR" SCD_CTRLAPI_BASE="$BASE" \
    /opt/stayconnect/bin/scd-enroll-test 2>&1 | jq -r '.hello.appliance_id // empty')
[[ "$HELLO1" == "$APP_ID" ]] && pass "second signed /hello ok (fresh jti)" \
                             || fail "second hello" "$HELLO1"

# Replay: same JWT re-used → first 200, second 401.
REPLAY_OUT=$(SCD_IDENTITY_DIR="$IDENT_DIR" SCD_CTRLAPI_BASE="$BASE" \
    /opt/stayconnect/bin/scd-enroll-test --replay 2>&1)
call1=$(grep -oE 'call1=[0-9]+' <<<"$REPLAY_OUT" | cut -d= -f2)
call2=$(grep -oE 'call2=[0-9]+' <<<"$REPLAY_OUT" | cut -d= -f2)
[[ "$call1" == "200" && "$call2" == "401" ]] \
    && pass "replay rejected (200 then 401)" \
    || fail "replay" "out=$REPLAY_OUT"

# ---- consumed token can't enroll again ----
dup_code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/v1/appliances/enroll" \
    -H 'Content-Type: application/json' \
    -d "{\"bootstrap_token\":\"$TOKEN\",\"serial\":\"$TEST_SERIAL\",\"public_key\":\"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\"}")
[[ "$dup_code" == "403" ]] && pass "consumed token rejected (403)" \
                           || fail "consumed re-enroll" "code=$dup_code"

# ---- revoke an UNCONSUMED token ----
mint2=$(curl -s -b "$CJ" -X POST "$BASE/v1/appliance-bootstrap-tokens?tenant_id=$TENANT_DEV" \
    -H 'Content-Type: application/json' \
    -d "{\"site_id\":\"$SITE_DEV\",\"expected_serial\":\"enroll-test-revoke\",\"ttl_hours\":1}")
TOK2=$(jq -r '.token' <<<"$mint2")
TOK2_ID=$(jq -r '.row.id' <<<"$mint2")
revoke_code=$(curl -s -o /dev/null -w '%{http_code}' -b "$CJ" -X DELETE \
    "$BASE/v1/appliance-bootstrap-tokens/$TOK2_ID?tenant_id=$TENANT_DEV")
[[ "$revoke_code" == "204" ]] && pass "revoke unconsumed token (204)" \
                              || fail "revoke" "code=$revoke_code"
enroll_code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/v1/appliances/enroll" \
    -H 'Content-Type: application/json' \
    -d "{\"bootstrap_token\":\"$TOK2\",\"serial\":\"enroll-test-revoke\",\"public_key\":\"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\"}")
[[ "$enroll_code" == "403" ]] && pass "revoked token can't enroll (403)" \
                              || fail "revoked enroll" "code=$enroll_code"

# ---- bad signature ----
bad_token="eyJhbGciOiJFZERTQSIsInR5cCI6IkpXVCJ9.eyJpc3MiOiIke0FQUF9JRH0iLCJpYXQiOjEsImV4cCI6OTk5OTk5OTk5OSwianRpIjoiZmFrZSJ9.aW52YWxpZA"
bad_code=$(curl -s -o /dev/null -w '%{http_code}' \
    -H "Authorization: Bearer $bad_token" \
    "$BASE/v1/appliance/hello")
[[ "$bad_code" == "401" ]] && pass "bad signature → 401" \
                           || fail "bad sig" "code=$bad_code"

echo
echo "ALL GREEN"
