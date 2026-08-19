#!/usr/bin/env bash
# Fail closed BEFORE a deploy or reboot can crash-loop scd.
#
# A PMS-dependent guest surface enabled without its Phase-3 auth arm makes scd exit at startup. systemd then
# restarts it forever while systemctl is-active still says "active" -- 580 restarts on the DEVELOPMENT
# appliance before anyone noticed. This refuses the combination at config time instead.
set -uo pipefail
DIR="${1:-/etc/stayconnect}"
bad=0
val() { grep -hoE "^$1=.*" "$DIR"/*.env 2>/dev/null | tail -1 | cut -d= -f2- | tr -d ' '; }
auth="$(val STAYCONNECT_PHASE3_PMS_AUTH)"
for guest in STAYCONNECT_PHASE6_DEVICE_SELFSERVICE_GUEST STAYCONNECT_PHASE5_POSTSTAY_GUEST; do
  g="$(val "$guest")"
  if [ "$g" = "true" ] && [ "$auth" != "true" ]; then
    echo "REFUSED: $guest=true requires STAYCONNECT_PHASE3_PMS_AUTH=true (currently '${auth:-unset}')." >&2
    echo "  scd would exit at startup and systemd would restart it indefinitely." >&2
    bad=1
  fi
done
[ "$bad" = "0" ] && echo "phase6/phase5 guest dependency check: OK ($DIR)"
exit "$bad"
