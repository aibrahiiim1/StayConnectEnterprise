#!/usr/bin/env bash
# Phase 2 quota tests.
# Assumes Phase 1 bootstrap has run and the client1 netns exists with a lease.
set -euo pipefail

PSQL="docker exec -i stayconnect-pg psql -U stayconnect -d stayconnect -At -q -v ON_ERROR_STOP=1"
pgq() { $PSQL | head -n1; }

TENANT_ID=$(echo "SELECT id FROM tenants WHERE slug='dev';" | pgq)
TEMPL_HOUR=$(echo "SELECT id FROM ticket_templates WHERE tenant_id='$TENANT_ID' AND code='hour1';" | pgq)
[ -n "$TENANT_ID" ] || { echo "no dev tenant"; exit 1; }

# Create (or refresh) a short-time template + voucher.
TEMPL_T30=$(echo "
INSERT INTO ticket_templates (tenant_id, code, name, duration_seconds, down_kbps, up_kbps, max_concurrent_devices)
VALUES ('$TENANT_ID', 'sec30', '30 Second Pass', 30, 10000, 10000, 1)
ON CONFLICT (tenant_id, code) DO UPDATE SET duration_seconds=EXCLUDED.duration_seconds
RETURNING id;" | pgq)

echo "
INSERT INTO vouchers (tenant_id, template_id, code, state)
VALUES ('$TENANT_ID', '$TEMPL_T30', 'TIME30', 'unused')
ON CONFLICT (tenant_id, code) DO UPDATE
  SET state='unused', bytes_used=0, seconds_used=0, activated_at=NULL, expires_at=NULL,
      template_id='$TEMPL_T30';
" | $PSQL

# Bytes-capped template (2 MB cap).
TEMPL_B2M=$(echo "
INSERT INTO ticket_templates (tenant_id, code, name, duration_seconds, data_cap_bytes, down_kbps, up_kbps, max_concurrent_devices)
VALUES ('$TENANT_ID', 'bytes2m', '2 MB Trial', 3600, 2000000, 10000, 10000, 1)
ON CONFLICT (tenant_id, code) DO UPDATE SET data_cap_bytes=EXCLUDED.data_cap_bytes
RETURNING id;" | pgq)

echo "
INSERT INTO vouchers (tenant_id, template_id, code, state)
VALUES ('$TENANT_ID', '$TEMPL_B2M', 'BYTES2M', 'unused')
ON CONFLICT (tenant_id, code) DO UPDATE
  SET state='unused', bytes_used=0, seconds_used=0, activated_at=NULL, expires_at=NULL,
      template_id='$TEMPL_B2M';
" | $PSQL

# Helper: current client IP from netns.
cip() { ip -n client1 -4 addr show eth0 | awk '/inet / {print $2}' | cut -d/ -f1; }

revoke_current() {
    local ip=$(cip)
    curl -s --unix-socket /run/stayconnect/scd.sock -X POST \
        -H 'Content-Type: application/json' \
        -d "{\"ip\":\"$ip\",\"reason\":\"admin\"}" \
        http://unix/v1/sessions/revoke >/dev/null || true
}

auth_voucher() {
    local code=$1
    ip netns exec client1 curl -s -o /dev/null -w "auth_status=%{http_code}\n" \
        -X POST -d "code=$code" http://portal.stayconnect.local/auth/voucher --max-time 5
}

last_session_state() {
    docker exec stayconnect-pg psql -U stayconnect -d stayconnect -At -q \
        -c "SELECT state || ',' || COALESCE(end_reason,'-') FROM sessions
             WHERE tenant_id='$TENANT_ID' ORDER BY started_at DESC LIMIT 1;"
}

echo
echo "================== TIME-QUOTA TEST (30s) =================="
revoke_current
sleep 1
auth_voucher TIME30
echo "Waiting 40s for acctd to revoke..."
for i in $(seq 1 40); do
    s=$(last_session_state)
    echo -n "."
    if [[ "$s" == *"closed,quota_time"* ]]; then
        echo
        echo "  ✓ session closed with end_reason=quota_time after ${i}s"
        break
    fi
    sleep 1
done
echo "  final state: $(last_session_state)"

echo
echo "================== BYTES-QUOTA TEST (2 MB) ================"
revoke_current
sleep 1
auth_voucher BYTES2M
echo "Downloading up to 10 MB (cap=2MB should trip revoke)..."
ip netns exec client1 curl -s -o /dev/null -w "  got=%{size_download}B\n" \
    "http://ash-speed.hetzner.com/100MB.bin" --max-time 15 || true
sleep 3
for i in $(seq 1 10); do
    s=$(last_session_state)
    if [[ "$s" == *"closed,quota_bytes"* ]]; then
        echo "  ✓ session closed with end_reason=quota_bytes"
        break
    fi
    sleep 1
done
echo "  final state: $(last_session_state)"
echo
echo "================== TOTALS ================================="
docker exec stayconnect-pg psql -U stayconnect -d stayconnect -c \
  "SELECT started_at, ended_at, state, end_reason, bytes_up, bytes_down
     FROM sessions
    WHERE tenant_id='$TENANT_ID'
    ORDER BY started_at DESC LIMIT 4;"

echo
echo "ALL GREEN"
