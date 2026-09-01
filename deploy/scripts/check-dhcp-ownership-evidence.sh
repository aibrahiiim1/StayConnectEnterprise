#!/usr/bin/env bash
# DHCP IS A SAFETY AUTHORITY ON THIS APPLIANCE. THIS CHECKS IT IS ANSWERING.
#
# Address ownership is what stops the appliance authorizing an address after the device that held it has gone.
# It is decided from Kea's leases. So "is Kea running" is the wrong question, and asking it is exactly how the
# PRE-LIVE appliance spent three days in the following state on 2026-08-31:
#
#   /var/lib/kea/kea-leases4.csv.pid was ZERO BYTES. memfile refuses to open its lease database while it
#   cannot read a PID from that file, so kea-dhcp4 started with NO LEASE MANAGER. systemd showed it active.
#   status-get answered normally. Every lease command returned:
#
#       { "result": 1, "text": "no current lease manager is available" }
#
#   No lease had been issued for two days, and address ownership was answering UNKNOWN for every session.
#
# This asks the question that matters - CAN THE LEASES BE READ - by reading them.
#
# IT REPAIRS NOTHING. It does not delete the PID file, restart Kea, or compact the lease file. Silently
# repairing the authority you are judging destroys the evidence and turns a recurring fault into a self-healing
# mystery. It reports, names the artifact, and leaves the state exactly as found.
#
# Usage: check-dhcp-ownership-evidence.sh [--socket PATH] [--json]
# Exit:  0 evidence available · 1 evidence UNAVAILABLE · 2 cannot check (no socket, no tool)
set -uo pipefail

SOCKET="${KEA_CTRL_SOCKET:-/run/kea/kea4-ctrl-socket}"
JSON=0
while [ $# -gt 0 ]; do
  case "$1" in
    --socket) SOCKET="${2:-}"; shift 2 ;;
    --json)   JSON=1; shift ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

FAULT=""; DETAIL=""; LEASES=0

emit() { # emit <exit>
  if [ "$JSON" = "1" ]; then
    printf '{"available":%s,"fault":"%s","detail":"%s","leases":%s}\n' \
      "$([ -z "$FAULT" ] && echo true || echo false)" "$FAULT" \
      "$(printf '%s' "$DETAIL" | sed 's/\\/\\\\/g; s/"/\\"/g')" "$LEASES"
  elif [ -z "$FAULT" ]; then
    echo "dhcp ownership evidence: OK ($LEASES lease(s) readable on $SOCKET)"
  else
    echo "DHCP OWNERSHIP EVIDENCE UNAVAILABLE [$FAULT]" >&2
    echo "  $DETAIL" >&2
    echo "  Address ownership cannot be verified, so netd withholds every authorization renewal and guests" >&2
    echo "  run down their bounded leases. Nothing here was repaired: inspect the state as it stands." >&2
  fi
  exit "$1"
}

# A SAFETY CHECK MUST NOT DEPEND ON ONE TOOL BEING INSTALLED. nc is the obvious way to speak to a unix socket
# and is present on the appliance; python3 is present wherever this is tested. Either will do, and a box with
# neither is reported as UNCHECKABLE rather than quietly passing.
if command -v nc >/dev/null 2>&1; then
  TALK=nc
elif command -v python3 >/dev/null 2>&1; then
  TALK=python3
else
  TALK=""
fi

ask() { # ask <json-command> -> raw reply on stdout
  case "$TALK" in
    nc) printf '%s' "$1" | timeout 10 nc -U "$SOCKET" 2>/dev/null ;;
    python3) SC_SOCK="$SOCKET" SC_REQ="$1" timeout 10 python3 -c '
import os, socket, sys
s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
s.settimeout(8)
try:
    s.connect(os.environ["SC_SOCK"])
    s.sendall(os.environ["SC_REQ"].encode())
    try:
        s.shutdown(socket.SHUT_WR)
    except OSError:
        pass
    out = b""
    while True:
        chunk = s.recv(65536)
        if not chunk:
            break
        out += chunk
    sys.stdout.write(out.decode("utf-8", "replace"))
except Exception:
    sys.exit(1)
' 2>/dev/null ;;
  esac
}

[ -n "$TALK" ] || { FAULT="CANNOT_CHECK"; DETAIL="neither nc nor python3 is available, so the Kea control socket cannot be queried"; emit 2; }
[ -S "$SOCKET" ] || { FAULT="CONTROL_SOCKET_ABSENT"; DETAIL="$SOCKET is not a socket: Kea is not running, or is not configured with a control socket"; emit 1; }

REPLY="$(ask '{"command":"lease4-get-all"}')"
if [ -z "$REPLY" ]; then
  FAULT="LEASE_QUERY_FAILED"; DETAIL="the lease query to $SOCKET returned nothing"
elif printf '%s' "$REPLY" | grep -qi "no current lease manager"; then
  # The .25 condition, in Kea's own words.
  FAULT="LEASE_MANAGER_UNAVAILABLE"
  DETAIL="Kea is answering its control socket but has NO LEASE MANAGER: it is serving no DHCP and can answer no ownership question"
elif printf '%s' "$REPLY" | grep -qE '"result"[[:space:]]*:[[:space:]]*0'; then
  LEASES="$(printf '%s' "$REPLY" | grep -o '"ip-address"' | wc -l | tr -d ' ')"
elif printf '%s' "$REPLY" | grep -qE '"result"[[:space:]]*:[[:space:]]*3'; then
  LEASES=0   # result 3 = empty. A quiet appliance with no guests is healthy, not broken.
else
  FAULT="LEASE_QUERY_FAILED"
  DETAIL="the lease query was refused: $(printf '%s' "$REPLY" | tr -d '\n' | cut -c1-200)"
fi

# Only when the query failed do we look on disk — and then only to name the cause. A working Kea needs no
# forensics, and an odd-looking file next to a lease database that opens fine is not a fault.
if [ -n "$FAULT" ]; then
  CFG="$(ask '{"command":"config-get"}')"
  LEASE_FILE="$(printf '%s' "$CFG" | tr ',' '\n' | grep -m1 '"name"[[:space:]]*:[[:space:]]*"[^"]*lease' | sed 's/.*"name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/')"
  [ -z "$LEASE_FILE" ] && [ -f /var/lib/kea/kea-leases4.csv ] && LEASE_FILE=/var/lib/kea/kea-leases4.csv
  if [ -n "$LEASE_FILE" ] && [ -e "$LEASE_FILE.pid" ]; then
    if [ ! -s "$LEASE_FILE.pid" ]; then
      FAULT="MEMFILE_STATE_UNUSABLE"
      DETAIL="$DETAIL; $LEASE_FILE.pid exists but is EMPTY — memfile refuses to open $LEASE_FILE while it cannot read a PID from it, which is what leaves Kea running with no lease manager. PRESERVED, not repaired"
    elif [ ! -r "$LEASE_FILE.pid" ]; then
      FAULT="MEMFILE_STATE_UNUSABLE"
      DETAIL="$DETAIL; $LEASE_FILE.pid cannot be read — memfile refuses to open $LEASE_FILE without it. PRESERVED, not repaired"
    fi
  fi
  emit 1
fi

emit 0
