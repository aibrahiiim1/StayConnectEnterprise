#!/usr/bin/env bash
# Phase 16 — signed license & entitlement E2E (edge-first refactor).
#
# Exercises the full commercial-enforcement loop on a live appliance:
#   issue (cloud, Ed25519-signed) → fetch (edge, appliance JWT) → verify
#   offline → gate guest auth → revoke → refuse → re-issue → recover.
#
# Time-based states (GracePeriod/Restricted/Expired) are covered by the
# license module's unit tests — they cannot be reached here without clock
# travel, and the store's anti-rollback design intentionally refuses
# back-dated documents.
set -euo pipefail

API=http://127.0.0.1:8080
SCD_SOCK=/run/stayconnect/scd.sock
PSQLC="docker exec -i stayconnect-pg psql -U stayconnect -d stayconnect -At -q -v ON_ERROR_STOP=1"
PSQLS="docker exec -i stayconnect-pg psql -U stayconnect -d stayconnect_site -At -q -v ON_ERROR_STOP=1"
CJ=$(mktemp)
trap 'rm -f $CJ' EXIT

PASS=0; FAIL=0
ok()  { echo "  ✓ $1"; PASS=$((PASS+1)); }
bad() { echo "  ✗ $1"; FAIL=$((FAIL+1)); }

TEN=$(echo "SELECT id FROM tenants WHERE slug='dev';" | $PSQLC)
SITE=$(echo "SELECT id FROM sites WHERE code='hq' AND tenant_id='$TEN';" | $PSQLC)

echo "== 16.1 login =="
curl -s -c "$CJ" -X POST $API/v1/auth/login -H 'Content-Type: application/json' \
  -d '{"email":"admin@stayconnect.local","password":"adminadmin01"}' -o /dev/null
grep -q sc_session "$CJ" && ok "admin session" || bad "admin session"

echo "== 16.2 current license Active on appliance =="
state=$(curl -s --unix-socket $SCD_SOCK http://unix/v1/license/status | python3 -c 'import sys,json;print(json.load(sys.stdin)["state"])')
[ "$state" = "Active" ] && ok "scd state Active" || bad "scd state = $state (want Active)"

echo "== 16.3 license-gated method visible =="
m=$(curl -s --unix-socket $SCD_SOCK http://unix/v1/tenant/auth-methods)
grep -q '"pms"' <<<"$m" && ok "pms method offered (entitled)" || bad "pms method missing while entitled"

echo "== 16.4 revoke current license =="
LIC=$(echo "SELECT id FROM licenses WHERE site_id='$SITE' AND status IN ('active','suspended') ORDER BY issued_at DESC LIMIT 1;" | $PSQLC)
code=$(curl -s -b "$CJ" -X POST "$API/cloud/v1/licenses/$LIC/revoke" -o /dev/null -w '%{http_code}')
[ "$code" = "200" ] && ok "cloud revoke 200" || bad "cloud revoke HTTP $code"

echo "== 16.5 appliance refresh applies revocation =="
curl -s --unix-socket $SCD_SOCK -X POST http://unix/v1/license/refresh -o /dev/null || true
state=$(curl -s --unix-socket $SCD_SOCK http://unix/v1/license/status | python3 -c 'import sys,json;print(json.load(sys.stdin)["state"])')
[ "$state" = "Revoked" ] && ok "scd state Revoked after refresh" || bad "scd state = $state (want Revoked)"

echo "== 16.6 guest auth refused under Revoked =="
CODE=$(echo "SELECT v.code FROM vouchers v JOIN ticket_templates t ON t.id=v.template_id WHERE v.state='unused' AND t.is_active AND (v.expires_at IS NULL OR v.expires_at > now()) ORDER BY v.issued_at DESC LIMIT 1;" | $PSQLS)
resp=$(curl -s --unix-socket $SCD_SOCK -X POST http://unix/v1/sessions/authorize \
  -H 'Content-Type: application/json' \
  -d "{\"ip\":\"10.10.0.199\",\"mac\":\"02:16:00:00:00:99\",\"voucher\":\"$CODE\"}")
grep -q license_expired <<<"$resp" && ok "voucher auth blocked (license_expired)" || bad "voucher auth not blocked: $resp"

echo "== 16.7 re-issue → appliance recovers =="
curl -s -b "$CJ" -X POST $API/cloud/v1/licenses -H 'Content-Type: application/json' \
  -d "{\"tenant_id\":\"$TEN\",\"site_id\":\"$SITE\",\"valid_days\":365,\"offline_grace_days\":30}" -o /dev/null
curl -s --unix-socket $SCD_SOCK -X POST http://unix/v1/license/refresh -o /dev/null || true
state=$(curl -s --unix-socket $SCD_SOCK http://unix/v1/license/status | python3 -c 'import sys,json;print(json.load(sys.stdin)["state"])')
[ "$state" = "Active" ] && ok "scd state Active after re-issue" || bad "scd state = $state (want Active)"

resp=$(curl -s --unix-socket $SCD_SOCK -X POST http://unix/v1/sessions/authorize \
  -H 'Content-Type: application/json' \
  -d "{\"ip\":\"10.10.0.199\",\"mac\":\"02:16:00:00:00:99\",\"voucher\":\"$CODE\"}")
grep -q session_id <<<"$resp" && ok "voucher auth works again" || bad "voucher auth still failing: $resp"
# cleanup: revoke that session
curl -s --unix-socket $SCD_SOCK -X POST http://unix/v1/sessions/revoke \
  -H 'Content-Type: application/json' -d '{"ip":"10.10.0.199","reason":"admin"}' -o /dev/null || true

echo "== 16.8 rollback protection (older envelope rejected) =="
# Any non-current envelope with an older issued_at (superseded OR revoked)
# must be refused by the store's monotonic issued_at check.
OLD_ENV=$(echo "SELECT signed_envelope FROM licenses WHERE site_id='$SITE' AND status IN ('superseded','revoked') ORDER BY issued_at ASC LIMIT 1;" | $PSQLC)
if [ -n "$OLD_ENV" ]; then
  code=$(curl -s --unix-socket $SCD_SOCK -X POST http://unix/v1/license/install \
    -H 'Content-Type: application/json' --data-raw "$OLD_ENV" -o /tmp/rb.json -w '%{http_code}')
  if [ "$code" = "400" ] && grep -q rollback /tmp/rb.json; then ok "older envelope rejected (anti-rollback)"; else bad "rollback not rejected (HTTP $code)"; fi
else
  bad "no superseded envelope found to test rollback"
fi

echo
if [ $FAIL -eq 0 ]; then echo "ALL GREEN ($PASS checks)"; else echo "$FAIL FAILED / $PASS passed"; exit 1; fi
