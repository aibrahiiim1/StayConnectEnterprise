#!/usr/bin/env bash
# Shared by the accelerated scenarios. Runs on the Docker host; acts inside the lab containers. Every scenario is a bounded
# functional simulation (golden.sh enforces 300 s): it proves behaviour and boundaries, not sustained real-time performance.
set -euo pipefail
APPL="${OGLAB_APPLIANCE:-oglab-appl}"
GEN="${OGLAB_GENERATOR:-oglab-gen}"
OGLAB="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

log() { printf '%s %s\n' "$(date -u +%H:%M:%S)" "$*"; }
fail() { log "FAIL: $*"; exit 1; }

# The appliance-side helpers are copied in every run, so a scenario never depends on what an earlier one left behind.
docker cp "$OGLAB/finalmission/cont-lib.sh" "$APPL:/root/cont-lib.sh" >/dev/null
docker cp "$OGLAB/labapi.py" "$APPL:/root/labapi.py" >/dev/null

appl() { docker exec "$APPL" sh -c ". /root/cont-lib.sh; $*"; }
sql() { docker exec -i "$APPL" sh -c "docker exec -i stayconnect-pg psql -X -At -U stayconnect -d stayconnect_site -c \"$1\""; }
api() { docker exec "$APPL" python3 /root/labapi.py "$@"; }
authcount() { appl authset 2>/dev/null || echo 0; }

# wait_for <seconds> <description> <command...>: polls every 2 s; fails the scenario at the bound.
wait_for() {
  local secs="$1" what="$2"; shift 2
  local end=$(( $(date +%s) + secs ))
  until "$@"; do
    [ "$(date +%s)" -ge "$end" ] && fail "$what not reached within ${secs} s"
    sleep 2
  done
  log "ok: $what"
}

# signin <from> <to>: the generator signs in that range of clients with the lab's voucher file (bounded rate).
signin() { docker exec "$GEN" /root/ogload signin -from "$1" -to "$2" -vouchers /root/v-long.txt -rate 600 >/dev/null; }
