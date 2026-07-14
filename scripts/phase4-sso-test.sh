#!/usr/bin/env bash
# Phase 4.4 — Operator SSO (stubbed OIDC) E2E.
set -euo pipefail

# Hit the Next.js proxy so the SSO redirect_uri (which embeds "/api/...") is
# valid from the browser's perspective and the cookie lands on the UI origin.
BASE=${BASE:-http://127.0.0.1:3000/api}
PSQL="docker exec -i stayconnect-pg psql -U stayconnect -d stayconnect -At -q -v ON_ERROR_STOP=1"
TENANT_DEV=$(echo "SELECT id FROM tenants WHERE slug='dev';" | $PSQL | head -n1)

pass() { printf "  ✓ %s\n" "$1"; }
fail() { printf "  ✗ %s\n    %s\n" "$1" "${2:-}"; exit 1; }

# Helper: drive the full SSO flow with curl following redirects, ending with
# a session cookie. Echoes the resulting Set-Cookie value for sc_session.
drive_sso() {
    local email=$1 verified=${2:-true} groups=${3:-} jar=$4

    rm -f "$jar"
    # 1. /v1/auth/sso/start → 302 to stub authorize
    local start_resp; start_resp=$(curl -s -o /dev/null -w "%{http_code} %{redirect_url}" \
        "$BASE/v1/auth/sso/start?tenant=dev&provider=stub")
    local code1=${start_resp%% *}; local auth_url=${start_resp##* }
    [[ "$code1" == "302" ]] || { echo "start failed: $start_resp" >&2; return 1; }

    # 2. Hit stub authorize with auto=1 + identity → 302 back to callback
    local cb_resp; cb_resp=$(curl -s -o /dev/null -w "%{http_code} %{redirect_url}" \
        -G --data-urlencode "auto=1" \
           --data-urlencode "email=$email" \
           --data-urlencode "email_verified=$verified" \
           --data-urlencode "name=Test User" \
           --data-urlencode "groups=$groups" \
        "$auth_url")
    local code2=${cb_resp%% *}; local callback_url=${cb_resp##* }
    [[ "$code2" == "302" ]] || { echo "stub authorize failed: $cb_resp" >&2; return 1; }

    # 3. Hit callback → 302 to /dashboard, sets sc_session
    curl -s -c "$jar" -o /dev/null -w "%{http_code} %{redirect_url}\n" "$callback_url"
}

# ------------------------------------------------------------------- Tests --

echo "== providers list =="
got=$(curl -s -o /tmp/p.json -w "%{http_code}" "$BASE/v1/auth/sso/providers?tenant=dev")
[[ "$got" == "200" ]] || fail "GET providers" "$got"
NAME=$(jq -r '.data[0].name' /tmp/p.json)
[[ "$NAME" == "stub" ]] && pass "providers list returns stub" || fail "providers" "$(cat /tmp/p.json)"

# ---- 1. auto-provision a brand-new operator -------------------------------
NEW_EMAIL="newuser+$(date +%s)@example.com"
echo
echo "== auto-provision: $NEW_EMAIL with groups=sc-admins =="
echo "DELETE FROM operators WHERE email = '$NEW_EMAIL';" | $PSQL >/dev/null
JAR=$(mktemp)
trap "rm -f $JAR" EXIT
result=$(drive_sso "$NEW_EMAIL" "true" "sc-admins" "$JAR")
echo "  callback: $result"
[[ "$result" == 302* && "$result" == *"/dashboard"* ]] && pass "redirected to /dashboard" || fail "auto-provision" "$result"

OP_ROW=$(echo "SELECT email || '|' || auth_method || '|' || COALESCE(oidc_sub,'') FROM operators WHERE email = '$NEW_EMAIL';" | $PSQL)
echo "  operator: $OP_ROW"
[[ "$OP_ROW" == *"|sso|stub:$NEW_EMAIL" ]] && pass "operator created with sso + sub" || fail "op fields" "$OP_ROW"

# Roles: sc-admins → tenant_admin (per claims_map)
ROLES=$(echo "SELECT string_agg(role,',' ORDER BY role) FROM operator_roles
              WHERE operator_id = (SELECT id FROM operators WHERE email = '$NEW_EMAIL');" | $PSQL)
echo "  roles: $ROLES"
[[ "$ROLES" == *"tenant_admin"* ]] && pass "role mapping: sc-admins → tenant_admin" || fail "role mapping" "$ROLES"

# Whoami must work with the cookie set on the previous step.
WHO=$(curl -s -b "$JAR" "$BASE/v1/auth/whoami")
[[ "$(jq -r .email <<< "$WHO")" == "$NEW_EMAIL" ]] && pass "whoami via cookie" || fail "whoami" "$WHO"

# ---- 2. account linking: pre-existing local operator gets oidc_sub stamped --
LINK_EMAIL="linkme+$(date +%s)@example.com"
echo
echo "== account linking: pre-create local operator $LINK_EMAIL =="
echo "INSERT INTO operators (tenant_id, email, display_name, password_hash, status)
      VALUES ('$TENANT_DEV', '$LINK_EMAIL', 'Link Me', 'never-used', 'active');" | $PSQL >/dev/null
echo "INSERT INTO operator_roles (operator_id, tenant_id, role)
      SELECT id, '$TENANT_DEV', 'viewer' FROM operators WHERE email = '$LINK_EMAIL';" | $PSQL >/dev/null

JAR2=$(mktemp); trap "rm -f $JAR $JAR2" EXIT
result=$(drive_sso "$LINK_EMAIL" "true" "" "$JAR2")
[[ "$result" == 302* && "$result" == *"/dashboard"* ]] || fail "link redirect" "$result"
LINKED=$(echo "SELECT auth_method || '|' || COALESCE(oidc_sub,'') FROM operators WHERE email = '$LINK_EMAIL';" | $PSQL)
[[ "$LINKED" == "sso|stub:$LINK_EMAIL" ]] && pass "linked: auth_method=sso, sub stamped" || fail "linking" "$LINKED"

# Existing 'viewer' role is preserved (we don't strip it on first link).
ROLES2=$(echo "SELECT string_agg(role,',' ORDER BY role) FROM operator_roles
               WHERE operator_id = (SELECT id FROM operators WHERE email = '$LINK_EMAIL');" | $PSQL)
[[ "$ROLES2" == *"viewer"* ]] && pass "existing role preserved on link" || fail "roles" "$ROLES2"

# ---- 3. unverified email is rejected --------------------------------------
echo
echo "== email_verified=false is rejected =="
JAR3=$(mktemp); trap "rm -f $JAR $JAR2 $JAR3" EXIT
result=$(drive_sso "spammer+$(date +%s)@example.com" "false" "" "$JAR3")
echo "  callback: $result"
[[ "$result" == 403* ]] && pass "unverified blocked" || fail "unverified gate" "$result"

# ---- 4. CSRF: bogus state → 400 -------------------------------------------
echo
echo "== bogus state → 400 =="
got=$(curl -s -o /dev/null -w "%{http_code}" "$BASE/v1/auth/sso/callback?state=deadbeef&code=foo")
[[ "$got" == "400" ]] && pass "bogus state rejected" || fail "csrf" "$got"

# ---- 5. State replay: reuse same state → 409 ------------------------------
echo
echo "== state replay → 409 =="
RU="$BASE/v1/auth/sso/callback"
ST=$(openssl rand -hex 16)
NC=$(openssl rand -hex 8)
PROVIDER_ID=$(echo "SELECT id FROM idp_providers WHERE tenant_id = '$TENANT_DEV' AND name='stub';" | $PSQL | head -n1)
echo "INSERT INTO auth_oidc_states (state, nonce, tenant_id, provider_id, redirect_uri, expires_at)
      VALUES ('$ST', '$NC', '$TENANT_DEV', '$PROVIDER_ID', '$RU', now() + interval '5 minutes');" | $PSQL >/dev/null
CODE=$(node -e "
const c = { claims: { Sub: 'stub:r@x', Email: 'r@x.test', EmailVerified: true, Name: 'R' }, nonce: '$NC' };
process.stdout.write(Buffer.from(JSON.stringify(c)).toString('base64url'));
")
# First callback succeeds (creates op).
echo "DELETE FROM operators WHERE email = 'r@x.test';" | $PSQL >/dev/null
curl -s -o /dev/null -L "$BASE/v1/auth/sso/callback?state=$ST&code=$CODE"
# Second one must 409.
got=$(curl -s -o /dev/null -w "%{http_code}" "$BASE/v1/auth/sso/callback?state=$ST&code=$CODE")
[[ "$got" == "409" ]] && pass "replay blocked" || fail "replay" "$got"

# ---- 6. Audit log captured the SSO events ---------------------------------
echo
echo "== audit trail =="
docker exec -i stayconnect-pg psql -U stayconnect -d stayconnect -c \
  "SELECT action, COALESCE(payload->>'email','-') AS email
     FROM audit_log
    WHERE tenant_id = '$TENANT_DEV' AND action LIKE 'operator.sso%'
      AND ts > now() - interval '5 minutes'
    ORDER BY ts DESC LIMIT 5;" | sed 's/^/  /'

echo
echo "ALL GREEN"
