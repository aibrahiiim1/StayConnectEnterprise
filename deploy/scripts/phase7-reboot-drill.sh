#!/usr/bin/env bash
# PHASE-7 REBOOT DRILL — does the appliance come back BY ITSELF?
#
# WHY THIS EXISTS SEPARATELY FROM THE PHASE-6 HARNESS.
#
# The Phase-6 controlled-validation harness verifies the appliance is dark, and part of its restoration is
# `systemctl restart` on the runtime units. That is correct for what it does -- it is putting a captured
# baseline back -- but it makes the harness useless as a reboot drill, because it STARTS the services and then
# reports them active. Run after a reboot it will say "every required service is active" whether or not they
# would have come up on their own.
#
# That mask was found the honest way: immediately after a reboot every unit read `inactive`, and the harness
# reported all six active moments later. Both readings were true and neither was the answer to the question a
# reboot drill asks.
#
# So this drill NEVER starts, stops or restarts anything. It watches. The only thing it can conclude is what
# the appliance did without help, which is the only thing worth concluding.
#
#   usage:  phase7-reboot-drill.sh watch [deadline-seconds]   watch an already-rebooting appliance
#           phase7-reboot-drill.sh reboot [deadline-seconds]  reboot, then watch
#
# It is read-only apart from the reboot it is explicitly asked to perform.
set -uo pipefail

readonly AUTHORIZED_HOSTS="radius"
UNITS="stayconnect-scd stayconnect-acctd stayconnect-edged stayconnect-portald stayconnect-netd stayconnect-hotel-admin"
DEADLINE="${2:-300}"

pass=0; fail=0
ok(){ printf '  [PASS] %s\n' "$1"; pass=$((pass+1)); }
no(){ printf '  [FAIL] %s :: %s\n' "$1" "${2:-}"; fail=$((fail+1)); }

host="$(hostname)"
case " $AUTHORIZED_HOSTS " in
  *" $host "*) : ;;
  *) echo "REFUSED: host '$host' is not in the compiled allow-list ($AUTHORIZED_HOSTS)" >&2; exit 2 ;;
esac

case "${1:-watch}" in
  reboot) echo "== rebooting $host =="; systemctl reboot; exit 0 ;;
  watch)  : ;;
  *) echo "usage: $0 [watch|reboot] [deadline-seconds]" >&2; exit 2 ;;
esac

boot_at="$(uptime -s)"
echo "== Phase-7 reboot drill on $host (booted $boot_at) =="
echo "   watching for up to ${DEADLINE}s. Nothing is started, stopped or restarted by this script."

# scd_code returns ONE status from the running scd, or 000 when nothing answers.
scd_code(){
  local c
  c="$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 --unix-socket /run/stayconnect/scd.sock         -X POST http://localhost/v1/phase6/devices/list -H 'Content-Type: application/json' -d '{}' 2>/dev/null)"
  case "$c" in [1-5][0-9][0-9]) printf '%s' "$c";; *) printf '000';; esac
}

# CONVERGENCE IS "SERVING", NOT "ACTIVE", and the difference is not pedantry: systemd reports a unit active as
# soon as its process is exec'd, while scd binds its socket only after license, NATS and mTLS work. The first
# version of this drill waited for `is-active` and then probed the route, got 000, and reported a failure --
# on an appliance that was perfectly healthy and simply had not finished starting. 000 is silence. Silence is
# neither "the route is absent" nor "the route is present"; it is the absence of an answer, and reading it as
# either is how a drill produces a conclusion nobody can act on.
converged=0
elapsed=0
while [ "$elapsed" -lt "$DEADLINE" ]; do
  down=""
  for u in $UNITS; do systemctl is-active --quiet "$u" || down="$down $u"; done
  if [ -z "$down" ] && [ "$(scd_code)" != "000" ]; then converged=1; break; fi
  sleep 5; elapsed=$((elapsed+5))
done

if [ "$converged" = "1" ]; then
  ok "every service converged to active AND scd is answering, WITHOUT operator action, within ${elapsed}s"
else
  no "the appliance did not converge in ${DEADLINE}s" "units still down:${down:- none}; scd answers $(scd_code)"
fi

# The crash-loop during startup is EXPECTED and is not a failure: the daemons come up before PostgreSQL is
# ready, fail closed, and are restarted by systemd until the database answers. What would be a failure is a
# unit that gave up -- systemd's start limiter latching a unit into `failed` is how an appliance comes back
# from a power cut with one daemon permanently down and no alarm.
latched=""
for u in $UNITS; do
  [ "$(systemctl is-failed "$u" 2>/dev/null)" = "failed" ] && latched="$latched $u"
done
[ -z "$latched" ] && ok "no unit is latched in a failed state (the startup crash-loop is transient by design)" \
  || no "a unit gave up and stayed down" "$latched"

for u in $UNITS; do
  [ "$(systemctl is-enabled "$u" 2>/dev/null)" = "enabled" ] || no "$u is not enabled at boot" "it will not come back"
done
ok "every runtime unit is enabled at boot"

# ---- and the state it came back INTO, read-only ------------------------------------------------------------
if [ -f /root/phase6-flag-coherence.sh ]; then
  bash /root/phase6-flag-coherence.sh >/dev/null 2>&1 \
    && ok "flag coherence after the reboot: every capability OFF and agreeing" \
    || no "flag coherence after the reboot" "run /root/phase6-flag-coherence.sh"
fi

code="$(scd_code)"
case "$code" in
  404) ok "the guest device route is ABSENT in the scd that came back (404)" ;;
  000) no "scd is not answering" "darkness cannot be established from silence" ;;
  *)   no "the guest device route answered $code after the reboot" "it must not exist while dark" ;;
esac

echo "------------------------------------------------------------"
printf 'PHASE7_REBOOT_DRILL pass=%d fail=%d (booted %s)\n' "$pass" "$fail" "$boot_at"
[ "$fail" -eq 0 ]
