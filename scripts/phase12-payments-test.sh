#!/usr/bin/env bash
# Phase 12 — Stripe payments E2E.
#
# We don't drive Stripe's checkout-create path end-to-end (that would
# require a real account or a fake API server wired into ctrlapi). What
# we DO cover — and this is the security- + correctness-critical half:
#
#   1. Go unit tests: request shape, error surfacing, signature verify
#      (happy, tampered, wrong secret, stale, missing, multi-v1)
#   2. CRUD for stripe_accounts; secrets write-only
#   3. Webhook signature verification end-to-end using openssl-generated
#      HMAC
#   4. Idempotent webhook processing: replay the SAME event → no second
#      voucher
#   5. Bad signature → 400; unknown tenant → 403
#   6. GET /v1/checkout/{session_id} returns the voucher code once issued
#   7. Metrics families visible with expected pre-touched series
set -euo pipefail

BASE=${BASE:-http://127.0.0.1:8080}
ADMIN_EMAIL=${ADMIN_EMAIL:-admin@stayconnect.local}
ADMIN_PASS=${ADMIN_PASSWORD:-adminadmin01}
PSQL="docker exec -i stayconnect-pg psql -U stayconnect -d stayconnect -At -q -v ON_ERROR_STOP=1"

pass() { printf "  ✓ %s\n" "$1"; }
fail() { printf "  ✗ %s\n    %s\n" "$1" "${2:-}"; exit 1; }

TENANT_DEV=$(echo "SELECT id FROM tenants WHERE slug='dev';" | $PSQL | head -n1)
[[ -n "$TENANT_DEV" ]] || fail "no dev tenant"

TEMPLATE_ID=$(echo "SELECT id FROM ticket_templates WHERE tenant_id='$TENANT_DEV' AND is_active=true ORDER BY created_at LIMIT 1;" | $PSQL | head -n1)
if [[ -z "$TEMPLATE_ID" ]]; then
    # Seed a minimal paid template so the test can self-contain.
    TEMPLATE_ID=$(echo "INSERT INTO ticket_templates(tenant_id,code,name,duration_seconds,price_cents,currency,is_active)
        VALUES('$TENANT_DEV','p12-test','Phase 12 Test',3600,500,'EUR',true) RETURNING id;" | $PSQL | head -n1)
fi
[[ -n "$TEMPLATE_ID" ]] || fail "no ticket template"

WEBHOOK_SECRET="whsec_phase12e2e_$(date +%s)"
SECRET_KEY="sk_test_phase12_$(date +%s)"
CJ=$(mktemp)
cleanup() {
    rm -f "$CJ"
    echo "DELETE FROM stripe_events WHERE tenant_id='$TENANT_DEV' AND event_type LIKE 'checkout.session.completed';" | $PSQL >/dev/null 2>&1 || true
    echo "DELETE FROM payments WHERE tenant_id='$TENANT_DEV' AND stripe_session_id LIKE 'cs_test_p12_%';" | $PSQL >/dev/null 2>&1 || true
    echo "DELETE FROM vouchers WHERE tenant_id='$TENANT_DEV' AND code LIKE 'P12%';" | $PSQL >/dev/null 2>&1 || true
    echo "DELETE FROM stripe_accounts WHERE display_name='p12-test';" | $PSQL >/dev/null 2>&1 || true
}
trap cleanup EXIT
cleanup

# ---- 1. Go unit tests ----
go_out=$(cd /opt/stayconnect/control-plane && go test ./internal/stripe 2>&1)
echo "$go_out" | grep -qE 'ok\s+.*internal/stripe' \
    && pass "Go unit tests pass for Stripe client" \
    || fail "Go tests failed" "$go_out"

# ---- 2. ctrlapi CRUD ----
code=$(curl -s -o /dev/null -w '%{http_code}' -c "$CJ" -X POST \
    -H 'Content-Type: application/json' \
    -d "{\"email\":\"$ADMIN_EMAIL\",\"password\":\"$ADMIN_PASS\"}" "$BASE/v1/auth/login")
[[ "$code" == "200" ]] || fail "admin login" "code=$code"

cr=$(curl -s -b "$CJ" -X POST "$BASE/v1/stripe-accounts?tenant_id=$TENANT_DEV" \
    -H 'Content-Type: application/json' \
    -d "{\"display_name\":\"p12-test\",\"publishable_key\":\"pk_test_phase12\",\"secret_key\":\"$SECRET_KEY\",\"webhook_secret\":\"$WEBHOOK_SECRET\",\"success_url\":\"https://portal/thanks\",\"cancel_url\":\"https://portal/cancel\"}")
acct_id=$(jq -r '.id' <<<"$cr")
[[ -n "$acct_id" && "$acct_id" != "null" ]] && pass "create stripe account" || fail "create" "$cr"

echo "$cr" | jq -e 'has("secret_key") | not' >/dev/null \
    && pass "secret_key never in create response" \
    || fail "secret_key leaked" "$cr"
echo "$cr" | jq -e 'has("webhook_secret") | not' >/dev/null \
    && pass "webhook_secret never in create response" \
    || fail "webhook_secret leaked"

# Dup per-tenant enabled rejected.
dup=$(curl -s -o /dev/null -w '%{http_code}' -b "$CJ" -X POST \
    "$BASE/v1/stripe-accounts?tenant_id=$TENANT_DEV" \
    -H 'Content-Type: application/json' \
    -d "{\"publishable_key\":\"pk\",\"secret_key\":\"sk\",\"webhook_secret\":\"w\",\"success_url\":\"https://x\",\"cancel_url\":\"https://x\"}")
[[ "$dup" == "409" ]] && pass "dup enabled per tenant rejected (409)" || fail "dup" "code=$dup"

# ---- 3. Seed a pending payment row (what /checkout/create would have
#         produced) so the webhook has something to flip to paid. ----
SESSION_ID="cs_test_p12_$(date +%s)"
PI_ID="pi_test_p12_$(date +%s)"
PAYMENT_ID=$(echo "INSERT INTO payments(tenant_id, template_id, stripe_session_id, status, amount_cents, currency)
    VALUES('$TENANT_DEV','$TEMPLATE_ID','$SESSION_ID','pending',500,'EUR') RETURNING id;" | $PSQL | head -n1)
[[ -n "$PAYMENT_ID" ]] || fail "seed payment"

# ---- 4. Send a signed webhook ----
sign_and_post() {
    local body="$1"
    local secret="$2"
    local ts=$(date +%s)
    local sig=$(printf '%s.%s' "$ts" "$body" | openssl dgst -sha256 -hmac "$secret" -hex | awk '{print $2}')
    local header="t=$ts,v1=$sig"
    curl -s -o /tmp/wh.out -w '%{http_code}' -X POST \
        -H 'Content-Type: application/json' \
        -H "Stripe-Signature: $header" \
        --data-raw "$body" \
        "$BASE/v1/webhooks/stripe/$TENANT_DEV"
}

EVT_ID="evt_test_p12_$(date +%s)"
BODY=$(jq -c -n --arg eid "$EVT_ID" --arg sid "$SESSION_ID" --arg pi "$PI_ID" --arg pay "$PAYMENT_ID" '{
    id: $eid,
    type: "checkout.session.completed",
    created: (now | floor),
    data: { object: {
        id: $sid,
        payment_intent: $pi,
        payment_status: "paid",
        amount_total: 500,
        currency: "eur",
        metadata: { stayconnect_payment_id: $pay }
    }}
}')

wh_code=$(sign_and_post "$BODY" "$WEBHOOK_SECRET")
[[ "$wh_code" == "200" ]] && pass "signed webhook accepted (200)" || fail "webhook" "code=$wh_code body=$(cat /tmp/wh.out)"

# Payment row now paid + voucher attached.
row=$(echo "SELECT status || '|' || COALESCE(voucher_id::text,'') FROM payments WHERE id='$PAYMENT_ID';" | $PSQL)
status_after=${row%|*}
vid_after=${row#*|}
[[ "$status_after" == "paid" ]] && pass "payment status flipped to paid" || fail "status" "got=$status_after"
[[ -n "$vid_after" ]] && pass "voucher_id attached to payment" || fail "no voucher_id"

# ---- 5. Idempotent replay → 200 + no second voucher ----
before_count=$(echo "SELECT count(*) FROM vouchers WHERE metadata->>'payment_id'='$PAYMENT_ID';" | $PSQL)
[[ "$before_count" == "1" ]] || fail "unexpected voucher count before replay: $before_count"

rep_code=$(sign_and_post "$BODY" "$WEBHOOK_SECRET")
[[ "$rep_code" == "200" ]] && pass "replay still 200 (idempotency gate)" || fail "replay" "code=$rep_code"
after_count=$(echo "SELECT count(*) FROM vouchers WHERE metadata->>'payment_id'='$PAYMENT_ID';" | $PSQL)
[[ "$after_count" == "1" ]] && pass "replay did not issue a second voucher" \
                            || fail "dup voucher on replay" "count=$after_count"

# ---- 6. Tampered body → 400 ----
TAMPERED=$(jq -c --arg sid "$SESSION_ID" '.data.object.id = "different"' <<<"$BODY")
ts=$(date +%s)
# Sign the ORIGINAL body but post the tampered one.
orig_sig=$(printf '%s.%s' "$ts" "$BODY" | openssl dgst -sha256 -hmac "$WEBHOOK_SECRET" -hex | awk '{print $2}')
tamper_code=$(curl -s -o /dev/null -w '%{http_code}' -X POST \
    -H 'Content-Type: application/json' \
    -H "Stripe-Signature: t=$ts,v1=$orig_sig" \
    --data-raw "$TAMPERED" \
    "$BASE/v1/webhooks/stripe/$TENANT_DEV")
[[ "$tamper_code" == "400" ]] && pass "tampered body rejected (400)" || fail "tamper" "code=$tamper_code"

# ---- 7. Unknown tenant → 403 ----
unk_code=$(sign_and_post "$BODY" "$WEBHOOK_SECRET" | head -1 || true)
# sign_and_post hits this tenant; to test an unknown tenant, swap the URL.
unk_code=$(curl -s -o /dev/null -w '%{http_code}' -X POST \
    -H 'Content-Type: application/json' \
    -H "Stripe-Signature: $(printf '%s.%s' $(date +%s) "$BODY" | openssl dgst -sha256 -hmac "$WEBHOOK_SECRET" -hex | awk '{print "t="strftime("%s",systime())",v1="$2}')" \
    --data-raw "$BODY" \
    "$BASE/v1/webhooks/stripe/00000000-0000-0000-0000-000000000000")
[[ "$unk_code" == "403" ]] && pass "unknown tenant rejected (403)" || fail "unknown tenant" "code=$unk_code"

# ---- 8. Poll /checkout/{id} returns voucher code ----
poll=$(curl -s "$BASE/v1/checkout/$SESSION_ID")
got_status=$(jq -r '.status' <<<"$poll")
got_code=$(jq -r '.voucher_code' <<<"$poll")
[[ "$got_status" == "paid" ]] && pass "poll returns status=paid" || fail "poll status" "got=$got_status"
[[ -n "$got_code" && "$got_code" != "null" ]] && pass "poll returns voucher_code ($got_code)" \
                                              || fail "no voucher_code" "$poll"

# ---- 9. Metrics: pre-touched series + bumped counters ----
m=$(curl -s "$BASE/metrics")
# NOTE: use here-string (<<<), NOT `echo "$m" | grep -q`. With
# `set -euo pipefail`, grep -q exits early on first match, echo gets
# SIGPIPE, and the pipeline returns 141 — which makes the `&& pass
# || fail` chain always fire `fail` even when the pattern matched.
grep -qE 'ctrlapi_payment_webhook_total\{result="ok"\} [1-9]' <<<"$m" \
    && pass "webhook_total{ok} incremented" || fail "webhook_total ok missing"
grep -qE 'ctrlapi_payment_webhook_total\{result="idempotent_skip"\} [1-9]' <<<"$m" \
    && pass "webhook_total{idempotent_skip} incremented" || fail "idempotent_skip missing"
grep -qE 'ctrlapi_payment_webhook_total\{result="signature_fail"\} [1-9]' <<<"$m" \
    && pass "webhook_total{signature_fail} incremented" || fail "signature_fail missing"
grep -qE 'ctrlapi_payment_amount_cents_total\{' <<<"$m" \
    && pass "payment_amount_cents_total series present" || fail "amount_cents missing"

echo
echo "ALL GREEN"
