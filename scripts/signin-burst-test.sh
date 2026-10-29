#!/usr/bin/env bash
# Simultaneous sign-in burst against scd (audit AD-26: 300-500 clients connecting at once).
#
# EVIDENCE-DEPENDENT: run this ON an appliance (PRE-LIVE) by an operator who owns the test window; nothing in CI runs it.
# It sends N concurrent guest sign-in attempts straight to scd's socket, each with its own synthetic source address and
# hardware address, using a voucher code that does not exist, so no session, entitlement or enforcement is created: what
# is exercised is the whole guest request path (capacity queue, method switch, durable throttle, voucher lookup, the
# database pool) and what is measured is how many were answered and how fast. A refusal for "invalid voucher" is the
# EXPECTED answer; "BUSY" (503) beyond the queue bound and any timeout are the findings.
#
# Usage:  scripts/signin-burst-test.sh [clients=500] [socket=/run/stayconnect/scd.sock]
# Needs:  curl (with --unix-socket), xargs, awk, sort. Run as a user that may open the socket (the stayconnect-scd group).
# Reads:  the answers only. Writes: a temporary directory, removed at the end. Contacts nothing but the local socket.
#
# Read the result like this:
#   answered      every request got an HTTP answer      -> should equal clients
#   4xx           refusals (invalid voucher / throttled)-> expected; the per-device and appliance-wide throttle may
#                                                          refuse some once the minute's ceiling is spent (the ceiling
#                                                          follows the licence guest cap, never below 600 a minute)
#   503           busy                                   -> should be 0 for 500 clients; any is a capacity finding
#   p95 / max     latency in ms                          -> p95 should stay well under 2000
set -u
CLIENTS="${1:-500}"
SOCK="${2:-/run/stayconnect/scd.sock}"
case "$CLIENTS" in ''|*[!0-9]*) echo "clients must be a number" >&2; exit 2;; esac
[ "$CLIENTS" -le 2000 ] || { echo "refusing more than 2000 clients" >&2; exit 2; }
[ -S "$SOCK" ] || { echo "no scd socket at $SOCK" >&2; exit 2; }
command -v curl >/dev/null || { echo "curl is required" >&2; exit 2; }

W="$(mktemp -d)"; trap 'rm -rf "$W"' EXIT
one() { # <index>
  i="$1"
  ip="10.250.$(( (i / 250) % 250 )).$(( (i % 250) + 1 ))"
  mac="$(printf '02:00:5e:%02x:%02x:%02x' $(( (i >> 16) & 255 )) $(( (i >> 8) & 255 )) $(( i & 255 )))"
  curl -s -o /dev/null -w '%{http_code} %{time_total}\n' --max-time 15 --unix-socket "$SOCK" \
    -H 'Content-Type: application/json' \
    -d "{\"ip\":\"$ip\",\"mac\":\"$mac\",\"voucher\":\"BURSTTEST\"}" \
    http://unix/v1/sessions/authorize
}
export -f one; export SOCK

echo "sending $CLIENTS simultaneous sign-in attempts to $SOCK ..."
seq 1 "$CLIENTS" | xargs -P "$CLIENTS" -I{} bash -c 'one {}' > "$W/out.txt" 2>/dev/null

answered="$(grep -c '^[0-9]' "$W/out.txt" || true)"
# Portable awk (mawk on Ubuntu has no asort): the latencies are sorted by sort(1) and the percentiles read by position.
awk -v n="$CLIENTS" -v a="$answered" '
  { code[$1]++ }
  END {
    printf "answered %d of %d\n", a, n
    for (c in code) printf "  HTTP %s: %d\n", c, code[c]
  }' "$W/out.txt"
awk '/^[0-9]/ { printf "%.0f\n", $2 * 1000 }' "$W/out.txt" | sort -n > "$W/ms.txt"
m="$(wc -l < "$W/ms.txt" | tr -d ' ')"
if [ "${m:-0}" -gt 0 ]; then
  p50=$(( (m * 50 + 99) / 100 )); p95=$(( (m * 95 + 99) / 100 ))
  echo "latency ms: p50 $(sed -n "${p50}p" "$W/ms.txt")  p95 $(sed -n "${p95}p" "$W/ms.txt")  max $(tail -1 "$W/ms.txt")"
fi
busy="$(grep -c '^503' "$W/out.txt" || true)"
timeouts=$(( CLIENTS - answered ))
echo "busy answers: $busy   no answer (timeout/connection error): $timeouts"
[ "$busy" -eq 0 ] && [ "$timeouts" -eq 0 ] && { echo "RESULT: PASS"; exit 0; }
echo "RESULT: FINDING (see the lines above; compare with docs/architecture on sign-in capacity)"; exit 1
