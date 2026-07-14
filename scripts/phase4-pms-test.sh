#!/usr/bin/env bash
# Phase 4.5 — PMS guest auth (Stub) E2E.
set -euo pipefail

PSQL="docker exec -i stayconnect-pg psql -U stayconnect -d stayconnect -At -q -v ON_ERROR_STOP=1"
TENANT_DEV=$(echo "SELECT id FROM tenants WHERE slug='dev';" | $PSQL | head -n1)

pass() { printf "  ✓ %s\n" "$1"; }
fail() { printf "  ✗ %s\n    %s\n" "$1" "${2:-}"; exit 1; }
ic()   { ip netns exec client1 curl -s "$@"; }

CIP=$(ip -n client1 -4 addr show eth0 | awk '/inet / {print $2}' | cut -d/ -f1)
clean_session_and_attempts() {
    curl -s --unix-socket /run/stayconnect/scd.sock -X POST -H 'Content-Type: application/json' \
        -d "{\"ip\":\"$CIP\",\"reason\":\"admin\"}" http://unix/v1/sessions/revoke >/dev/null
    echo "DELETE FROM pms_attempts WHERE ip = '$CIP'::inet;" | $PSQL >/dev/null
}
clean_session_and_attempts
echo "  client ip = $CIP"

echo
echo "== auth-methods includes pms =="
ic -o /tmp/am.json -w "" http://10.10.0.1:8380/api/auth-methods
[[ "$(jq -r '.pms.enabled' /tmp/am.json)" == "true" ]] && pass "pms enabled" || fail "pms not enabled" "$(cat /tmp/am.json)"
[[ "$(jq -r '.pms.mode' /tmp/am.json)" == "either" ]]  && pass "mode=either"   || fail "mode" "$(cat /tmp/am.json)"

# ---- Mode-OR: room + last name (Anderson) ---------------------------------
echo
echo "== room 101 + last name 'Anderson' (case-insensitive) =="
ic -o /tmp/r.json -w "%{http_code}\n" -X POST -H 'Content-Type: application/json' \
    -d '{"room":"101","last_name":"andersOn"}' http://portal.stayconnect.local/auth/pms/verify
SID=$(jq -r .session_id /tmp/r.json)
DUR=$(jq -r .duration_seconds /tmp/r.json)
[[ -n "$SID" && "$SID" != "null" ]] || fail "verify" "$(cat /tmp/r.json)"
pass "session_id=$SID, duration=${DUR}s (capped to remaining stay)"
clean_session_and_attempts

# ---- Mode-OR: room + first name (Alice) -----------------------------------
echo
echo "== room 101 + first name 'alice' =="
ic -o /tmp/r.json -w "%{http_code}\n" -X POST -H 'Content-Type: application/json' \
    -d '{"room":"101","first_name":"alice"}' http://portal.stayconnect.local/auth/pms/verify
[[ "$(jq -r .session_id /tmp/r.json)" != "null" ]] && pass "first-name match works" || fail "first-name" "$(cat /tmp/r.json)"
clean_session_and_attempts

# ---- Mode-OR: room + reservation number (RES-1002) ------------------------
echo
echo "== room 102 + reservation 'res-1002' =="
ic -o /tmp/r.json -w "%{http_code}\n" -X POST -H 'Content-Type: application/json' \
    -d '{"room":"102","reservation_number":"res-1002"}' http://portal.stayconnect.local/auth/pms/verify
[[ "$(jq -r .session_id /tmp/r.json)" != "null" ]] && pass "reservation match works" || fail "reservation" "$(cat /tmp/r.json)"
clean_session_and_attempts

# ---- Diacritic-insensitive: 'dubois' matches 'Dubois' ---------------------
echo
echo "== room 103 + 'dubois' (record stored as 'Dubois' / Chloé) =="
ic -o /tmp/r.json -w "%{http_code}\n" -X POST -H 'Content-Type: application/json' \
    -d '{"room":"103","last_name":"dubois"}' http://portal.stayconnect.local/auth/pms/verify
[[ "$(jq -r .session_id /tmp/r.json)" != "null" ]] && pass "diacritic-tolerant match" || fail "diacritic" "$(cat /tmp/r.json)"
clean_session_and_attempts

# ---- Wrong last name → 401 ------------------------------------------------
echo
echo "== room 101 + WRONG last name → 401 =="
ic -o /tmp/r.json -w "%{http_code}" -X POST -H 'Content-Type: application/json' \
    -d '{"room":"101","last_name":"Wrong"}' http://portal.stayconnect.local/auth/pms/verify > /tmp/code.txt
got=$(cat /tmp/code.txt)
[[ "$got" == "401" ]] && pass "wrong last name → 401" || fail "wrong-name" "code=$got body=$(cat /tmp/r.json)"

# ---- Lockout: 5 failures on room 101 → 429 ---------------------------------
echo
echo "== lockout: 5 failures on room 101, 6th attempt blocked =="
echo "DELETE FROM pms_attempts WHERE tenant_id = '$TENANT_DEV' AND lower(room_number) = '101';" | $PSQL >/dev/null
for i in 1 2 3 4 5; do
    ic -o /dev/null -w "" -X POST -H 'Content-Type: application/json' \
        -d '{"room":"101","last_name":"Wrong"}' http://portal.stayconnect.local/auth/pms/verify
done
got=$(ic -o /tmp/r.json -w '%{http_code}' -X POST -H 'Content-Type: application/json' \
    -d '{"room":"101","last_name":"Wrong"}' http://portal.stayconnect.local/auth/pms/verify)
[[ "$got" == "429" ]] && pass "room locked after 5 failures (got 429)" || fail "lockout" "code=$got body=$(cat /tmp/r.json)"

# Even with the RIGHT secondary, the lockout still bites until the window slides.
got=$(ic -o /tmp/r.json -w '%{http_code}' -X POST -H 'Content-Type: application/json' \
    -d '{"room":"101","last_name":"Anderson"}' http://portal.stayconnect.local/auth/pms/verify)
[[ "$got" == "429" ]] && pass "lockout blocks even valid attempts" || fail "lockout-valid" "code=$got"

echo "DELETE FROM pms_attempts WHERE tenant_id = '$TENANT_DEV' AND lower(room_number) = '101';" | $PSQL >/dev/null

# ---- Stay window: room 201 hasn't started yet → 403 -----------------------
echo
echo "== stay window: room 201 starts in 7 days → 403 =="
got=$(ic -o /tmp/r.json -w '%{http_code}' -X POST -H 'Content-Type: application/json' \
    -d '{"room":"201","last_name":"Guest"}' http://portal.stayconnect.local/auth/pms/verify)
[[ "$got" == "403" && "$(jq -r .error /tmp/r.json)" == "stay has not started yet" ]] && pass "future stay rejected" || fail "before checkin" "code=$got body=$(cat /tmp/r.json)"

# ---- Stay window: room 202 ended → 403 ------------------------------------
echo
echo "== stay window: room 202 ended 3 days ago → 403 =="
got=$(ic -o /tmp/r.json -w '%{http_code}' -X POST -H 'Content-Type: application/json' \
    -d '{"room":"202","last_name":"Guest"}' http://portal.stayconnect.local/auth/pms/verify)
[[ "$got" == "403" && "$(jq -r .error /tmp/r.json)" == "stay has ended" ]] && pass "past stay rejected" || fail "after checkout" "code=$got body=$(cat /tmp/r.json)"

# ---- Per-IP rate limit: spam attempts until throttled ---------------------
echo
echo "== per-IP rate limit: 30 attempts in 15m =="
echo "DELETE FROM pms_attempts WHERE ip = '$CIP'::inet;" | $PSQL >/dev/null
for i in $(seq 1 31); do
    ic -o /dev/null -w "" -X POST -H 'Content-Type: application/json' \
        -d "{\"room\":\"9$i\",\"last_name\":\"NoOne\"}" http://portal.stayconnect.local/auth/pms/verify
done
got=$(ic -o /tmp/r.json -w '%{http_code}' -X POST -H 'Content-Type: application/json' \
    -d '{"room":"999","last_name":"Anybody"}' http://portal.stayconnect.local/auth/pms/verify)
[[ "$got" == "429" ]] && pass "per-IP rate limit fires" || fail "ip-rate" "code=$got body=$(cat /tmp/r.json)"

echo
echo "== pms_attempts audit trail (last 10) =="
docker exec -i stayconnect-pg psql -U stayconnect -d stayconnect -c \
  "SELECT room_number, success, error_code, secondary_kind
     FROM pms_attempts WHERE tenant_id = '$TENANT_DEV'
                         AND attempted_at > now() - interval '5 minutes'
     ORDER BY attempted_at DESC LIMIT 10;" | sed 's/^/  /'

echo
echo "ALL GREEN"
