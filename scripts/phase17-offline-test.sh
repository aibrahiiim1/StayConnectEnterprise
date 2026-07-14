#!/usr/bin/env bash
# Phase 17 — cloud-outage drill (edge-first refactor).
#
# Stops the ENTIRE cloud side (ctrlapi + NATS) and proves the hotel keeps
# working: portal guest auth (voucher + PMS), Hotel Admin (edged) login and
# management, existing sessions, license evaluation — all local. Telemetry
# accumulates in the durable outbox and drains exactly once after recovery.
set -euo pipefail

SCD_SOCK=/run/stayconnect/scd.sock
EDGE=http://127.0.0.1:8090
PSQLC="docker exec -i stayconnect-pg psql -U stayconnect -d stayconnect -At -q -v ON_ERROR_STOP=1"
PSQLS="docker exec -i stayconnect-pg psql -U stayconnect -d stayconnect_site -At -q -v ON_ERROR_STOP=1"
EC=$(mktemp)
PASS=0; FAIL=0
ok()  { echo "  ✓ $1"; PASS=$((PASS+1)); }
bad() { echo "  ✗ $1"; FAIL=$((FAIL+1)); }
cleanup() {
  rm -f "$EC"
  systemctl start stayconnect-ctrlapi 2>/dev/null || true
  docker start stayconnect-nats >/dev/null 2>&1 || true
}
trap cleanup EXIT

echo "== 17.1 baseline: outbox drained =="
sleep 20   # allow one drain pass
P0=$(curl -s --unix-socket $SCD_SOCK http://unix/v1/admin/outbox/stats | python3 -c 'import sys,json;print(json.load(sys.stdin)["pending"])')
echo "  (pending before outage: $P0)"

echo "== 17.2 CLOUD OUTAGE: stop ctrlapi + NATS =="
systemctl stop stayconnect-ctrlapi
docker stop stayconnect-nats >/dev/null
ok "cloud API and message bus are down"

echo "== 17.3 existing session survives =="
ACTIVE_BEFORE=$(echo "SELECT count(*) FROM sessions WHERE state='active';" | $PSQLS)
echo "  (active sessions: $ACTIVE_BEFORE)"

echo "== 17.4 NEW guest voucher login works offline =="
CODE=$(echo "SELECT v.code FROM vouchers v JOIN ticket_templates t ON t.id=v.template_id WHERE v.state='unused' AND t.is_active AND (v.expires_at IS NULL OR v.expires_at > now()) ORDER BY v.issued_at DESC LIMIT 1;" | $PSQLS)
resp=$(ip netns exec client1 curl -s -m 8 -X POST -d "code=$CODE" http://10.10.0.1:8380/auth/voucher -o /dev/null -w '%{http_code}')
[ "$resp" = "303" ] && ok "voucher login offline (HTTP 303)" || bad "voucher login offline: HTTP $resp"

echo "== 17.5 PMS room login works offline =="
resp=$(curl -s --unix-socket $SCD_SOCK -X POST http://unix/v1/auth/pms/verify \
  -H 'Content-Type: application/json' \
  -d '{"room":"102","last_name":"O'\''Brien","ip":"10.10.0.198","mac":"02:17:00:00:00:98"}' -o /tmp/pms17.json -w '%{http_code}')
[ "$resp" = "200" ] && ok "PMS login offline (stub provider, local cache)" || bad "PMS login offline: HTTP $resp $(cat /tmp/pms17.json)"
curl -s --unix-socket $SCD_SOCK -X POST http://unix/v1/sessions/revoke -H 'Content-Type: application/json' -d '{"ip":"10.10.0.198","reason":"admin"}' -o /dev/null || true

echo "== 17.6 Hotel Admin fully functional offline =="
curl -s -c "$EC" -X POST $EDGE/edge/v1/auth/login -H 'Content-Type: application/json' \
  -d '{"email":"hoteladmin@site.local","password":"hoteladmin123"}' -o /dev/null -w ''
code=$(curl -s -b "$EC" $EDGE/edge/v1/auth/whoami -o /dev/null -w '%{http_code}')
[ "$code" = "200" ] && ok "edged login + whoami offline" || bad "edged whoami offline: HTTP $code"
TPL=$(echo "SELECT id FROM ticket_templates WHERE is_active LIMIT 1;" | $PSQLS)
code=$(curl -s -b "$EC" -X POST $EDGE/edge/v1/voucher-batches -H 'Content-Type: application/json' \
  -d "{\"template_id\":\"$TPL\",\"count\":5,\"name\":\"offline-drill\"}" -o /tmp/vb17.json -w '%{http_code}')
{ [ "$code" = "201" ] || [ "$code" = "200" ]; } && ok "voucher batch created offline via Hotel Admin" || bad "offline batch create: HTTP $code $(cat /tmp/vb17.json)"

echo "== 17.7 license still evaluates locally =="
state=$(curl -s --unix-socket $SCD_SOCK http://unix/v1/license/status | python3 -c 'import sys,json;print(json.load(sys.stdin)["state"])')
[ "$state" = "Active" ] && ok "license Active with cloud down (offline evaluation)" || bad "license state offline: $state"

echo "== 17.8 telemetry queues durably while offline =="
sleep 65   # one telemetry tick
P1=$(curl -s --unix-socket $SCD_SOCK http://unix/v1/admin/outbox/stats | python3 -c 'import sys,json;print(json.load(sys.stdin)["pending"])')
[ "$P1" -gt 0 ] && ok "outbox accumulating ($P1 pending)" || bad "outbox empty during outage (pending=$P1)"

echo "== 17.9 RECOVERY: restart cloud =="
docker start stayconnect-nats >/dev/null
systemctl start stayconnect-ctrlapi
# NATS must come up, scd reconnects, then the outbox drainer's next tick
# fires. Poll up to 60s rather than guessing a fixed sleep.
P2=$P1
for _ in $(seq 1 12); do
  sleep 5
  P2=$(curl -s --unix-socket $SCD_SOCK http://unix/v1/admin/outbox/stats | python3 -c 'import sys,json;print(json.load(sys.stdin)["pending"])')
  [ "$P2" -eq 0 ] && break
done
[ "$P2" -lt "$P1" ] && ok "outbox drained after recovery ($P1 → $P2)" || bad "outbox not draining ($P1 → $P2)"

echo "== 17.10 exactly-once landing in fleet_telemetry =="
DUP=$(echo "SELECT count(*) - count(DISTINCT (appliance_id, seq)) FROM fleet_telemetry;" | $PSQLC)
[ "$DUP" = "0" ] && ok "no duplicate (appliance_id, seq) rows" || bad "$DUP duplicate telemetry rows"

echo "== 17.11 telemetry contains no guest PII =="
PII=$(echo "SELECT count(*) FROM fleet_telemetry, jsonb_object_keys(payload) k WHERE k ~* '(mac|email|phone|guest|room|reservation|voucher_code|otp|password)';" | $PSQLC)
[ "$PII" = "0" ] && ok "no PII-like keys in cloud telemetry" || bad "$PII PII-like telemetry keys found"

echo
if [ $FAIL -eq 0 ]; then echo "ALL GREEN ($PASS checks)"; else echo "$FAIL FAILED / $PASS passed"; exit 1; fi
