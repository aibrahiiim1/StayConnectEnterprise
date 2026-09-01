#!/usr/bin/env bash
# A CHECK THAT CANNOT FAIL IS NOT A CHECK.
#
# check-dhcp-ownership-evidence.sh exists because of one live state: a Kea that answered status-get for three
# days while every lease command returned "no current lease manager is available", because a zero-byte
# kea-leases4.csv.pid had stopped memfile opening its database. This proves the checker actually refuses that
# state - and that it refuses it for the right reason, naming the artifact rather than shrugging.
#
# It stands up a FAKE Kea control socket, so the cases are the real replies Kea sends, without a DHCP server.
set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CHECK="$HERE/check-dhcp-ownership-evidence.sh"
WORK="$(mktemp -d)"
trap 'pkill -P $$ >/dev/null 2>&1; rm -rf "$WORK"' EXIT
fail=0

# A UNIX SOCKET IS THE ONE THING THIS CANNOT FAKE. Where the platform has no AF_UNIX (Windows), the cases are
# unrunnable and this SKIPS AND SAYS SO. A self-test that silently reported PASS on a platform where it never
# executed a single case would be the exact failure it exists to prevent; the appliance and CI are Linux, and
# that is where the result counts.
if ! python3 -c 'import socket, sys; sys.exit(0 if hasattr(socket, "AF_UNIX") else 1)' 2>/dev/null; then
  echo "SKIP: this platform has no AF_UNIX, so a fake Kea control socket cannot be served"
  echo "DHCP_OWNERSHIP_EVIDENCE_SELFTEST = SKIP"
  exit 0
fi

# serve <name> <reply-for-lease4-get-all> [reply-for-config-get] -> echoes the socket path
#
# One connection per request, which is how Kea's control socket behaves: it answers and closes. The responder
# keeps accepting so a single case can answer the lease query and then the config query that follows it.
serve() {
  local name="$1" sock="$WORK/$1.sock"
  SC_SOCK="$sock" SC_LEASE="$2" SC_CFG="${3:-}" python3 -c '
import os, socket, threading
sock, lease, cfg = os.environ["SC_SOCK"], os.environ["SC_LEASE"], os.environ.get("SC_CFG", "")
srv = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
srv.bind(sock)
srv.listen(8)
def run():
    for _ in range(8):
        try:
            c, _a = srv.accept()
        except OSError:
            return
        try:
            req = c.recv(65536).decode("utf-8", "replace")
            c.sendall((cfg if "config-get" in req else lease).encode())
        finally:
            c.close()
threading.Thread(target=run, daemon=True).start()
import time
time.sleep(20)
' >/dev/null 2>&1 &
  # The redirect is not tidiness. serve() is called inside a command substitution, and a background child that
  # inherits the captured stdout keeps that substitution waiting until the child exits - which would hand back
  # the socket path twenty seconds after the socket had already closed, and every case would then be testing an
  # absent socket rather than the reply it staged.
  for _ in 1 2 3 4 5 6 7 8 9 10; do [ -S "$sock" ] && break; sleep 0.2; done
  echo "$sock"
}

expect() { # expect <want-exit> <sock> <want-fault-or-empty> <description>
  local want="$1" sock="$2" want_fault="$3" what="$4" got out
  out="$(bash "$CHECK" --socket "$sock" --json 2>&1)"; got=$?
  if [ "$got" != "$want" ]; then
    echo "  *** FAIL: $what — exit $got, want $want"; echo "      $out"; fail=1; return
  fi
  if [ -n "$want_fault" ] && ! printf '%s' "$out" | grep -q "\"fault\":\"$want_fault\""; then
    echo "  *** FAIL: $what — fault is not $want_fault"; echo "      $out"; fail=1; return
  fi
  echo "  ok: $what"
}

# 1. THE LIVE FAILURE, reproduced in Kea's own words. No lease file on disk, so the checker can only report
#    the symptom — and must still refuse.
s="$(serve leasemgr '{ "result": 1, "text": "no current lease manager is available" }' '{ "result": 0, "arguments": {} }')"
expect 1 "$s" "LEASE_MANAGER_UNAVAILABLE" "a Kea with no lease manager is REFUSED"

# 2. THE SAME FAILURE WITH THE ARTIFACT PRESENT. The cause outranks the symptom: an operator needs the file,
#    not the error text. And the file must still be there afterwards.
LEASES="$WORK/kea-leases4.csv"; : > "$LEASES"; : > "$LEASES.pid"   # zero bytes, exactly as found on .25
cfg="{ \"result\": 0, \"arguments\": { \"Dhcp4\": { \"lease-database\": { \"type\": \"memfile\", \"name\": \"$LEASES\" } } } }"
s="$(serve memfile '{ "result": 1, "text": "no current lease manager is available" }' "$cfg")"
expect 1 "$s" "MEMFILE_STATE_UNUSABLE" "an empty lease-file PID companion is NAMED as the cause"
if [ ! -e "$LEASES.pid" ] || [ ! -e "$LEASES" ]; then
  echo "  *** FAIL: the check REPAIRED DHCP state — the evidence is gone"; fail=1
else
  echo "  ok: nothing was repaired, deleted or restarted"
fi

# 3. A healthy Kea with leases passes, and counts them.
s="$(serve healthy '{ "result": 0, "text": "2 IPv4 lease(s) found.", "arguments": { "leases": [ { "ip-address": "192.168.77.102", "hw-address": "96:48:f9:7a:9b:09" }, { "ip-address": "192.168.77.103", "hw-address": "d6:5a:1c:d2:12:d6" } ] } }')"
expect 0 "$s" "" "a Kea whose leases can be read is accepted"

# 4. An EMPTY lease set is healthy, not broken: an appliance with no guests on it is the ordinary quiet case.
s="$(serve empty '{ "result": 3, "text": "0 IPv4 lease(s) found." }')"
expect 0 "$s" "" "an appliance with no current leases is accepted"

# 5. A socket that is not there at all is refused rather than passed over in silence.
expect 1 "$WORK/not-a-socket" "CONTROL_SOCKET_ABSENT" "an absent control socket is REFUSED"

# 6. Any other refusal is a query failure, and must NOT be reported as the lease-manager condition — the two
#    send an operator to different places.
s="$(serve other '{ "result": 2, "text": "'"'"'lease4-get-all'"'"' command not supported." }' '{ "result": 0, "arguments": {} }')"
expect 1 "$s" "LEASE_QUERY_FAILED" "an unrelated refusal is not mistaken for a missing lease manager"

if [ "$fail" = "0" ]; then
  echo "DHCP_OWNERSHIP_EVIDENCE_SELFTEST = PASS"
  exit 0
fi
echo "DHCP_OWNERSHIP_EVIDENCE_SELFTEST = FAIL"
exit 1
