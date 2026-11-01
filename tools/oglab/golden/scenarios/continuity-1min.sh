#!/usr/bin/env bash
# Outage continuity, accelerated: the window is set to its smallest real value (1 minute) instead of 60-120, so the cut-off at the
# window is observed in about 3 minutes. The 60/120-minute arithmetic is the same code path; it is proven by the netd unit simulation
# (cmd/netd phase3_continuity_test.go, simulated clock), not here. A 60- or 120-minute REAL run stays NOT VERIFIED unless the
# Product Owner authorises one separately.
. "$(dirname "$0")/lib.sh"
trap 'appl "systemctl start stayconnect-acctd" || true; appl "setwin 60 restore" >/dev/null || true' EXIT

appl "setwin 1 accelerated-continuity" >/dev/null
signin 0 50
wait_for 60 "50 guests authorised" sh -c "[ \$(docker exec $APPL sh -c '. /root/cont-lib.sh; authset') -ge 50 ]"
sleep 35   # one acctd setting read (30 s) so the plans carry the 1-minute window
stop_at=$(date +%s)
appl "systemctl stop stayconnect-acctd"
log "acctd stopped"
sleep 75   # past the 90 s lease would be strict mode; the governor must still hold them at +75 s
[ "$(authcount)" -ge 50 ] || fail "guests were lost before the 1-minute window ended (strict lease would have kept them 90 s anyway)"
wait_for 90 "guests removed at the window (+60 s after the last plan, within a tick)" sh -c "[ \$(docker exec $APPL sh -c '. /root/cont-lib.sh; authset') -eq 0 ]"
log "window ended after $(( $(date +%s) - stop_at )) s"
appl "systemctl start stayconnect-acctd"
signin 0 50
wait_for 60 "guests re-admitted after acctd returned" sh -c "[ \$(docker exec $APPL sh -c '. /root/cont-lib.sh; authset') -ge 50 ]"
appl "contstat" | head -c 600; echo
