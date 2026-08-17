#!/usr/bin/env bash
# PHASE-7 — THE FINAL REAL REBOOT, AND WHAT SURVIVES IT.
#
# THE REBOOT ITSELF IS THE EVIDENCE. A harness that stops and starts units proves that units can be started;
# it proves nothing about whether this machine comes back on its own. So this issues a real reboot and then
# refuses to believe it happened unless the kernel says so: the boot id must CHANGE. If it does not, every
# assertion below would be describing a machine that never went down, and the script says so and stops.
#
# NO OPERATOR REPAIR. Nothing here starts, enables, reloads or fixes anything after the boot. If the appliance
# needs a human to come back, that is the finding, and it must be visible rather than papered over by a script
# that helpfully restarts the thing it is measuring.
#
# WHAT MUST SURVIVE: the services, the schema, the runtime roles, every capability still DARK, the guest
# capability still absent at the layer that decides it, no synthetic access left behind, and the appliance's
# independence from Central.
set -uo pipefail
APPL="${PHASE7_APPLIANCE:-172.21.60.23}"
PGC="${PHASE7_PG_CONTAINER:-stayconnect-pg}"
DB="${PHASE7_SITE_DB:-stayconnect_site}"
WAIT_SECS="${PHASE7_REBOOT_WAIT:-300}"

pass=0; fail=0
ok(){ printf '  [PASS] %s\n' "$1"; pass=$((pass+1)); }
no(){ printf '  [FAIL] %s :: %s\n' "$1" "${2:-}"; fail=$((fail+1)); }
eq(){ [ "$2" = "$3" ] && ok "$1" || no "$1" "expected '$3', got '$2'"; }

sh_(){ ssh -n -o BatchMode=yes -o ConnectTimeout=8 "root@$APPL" "$1" 2>&1; }
q(){   ssh -n -o BatchMode=yes -o ConnectTimeout=8 "root@$APPL" \
        "docker exec -i $PGC psql -U stayconnect -d $DB -tAqc \"$1\"" 2>&1; }

echo "===== PHASE-7: final real reboot of the DEVELOPMENT appliance ($APPL) ====="

# ---- what we expect to find again, read BEFORE the machine goes down ---------------------------------------
BOOT_BEFORE="$(sh_ "cat /proc/sys/kernel/random/boot_id")"
REL_BEFORE="$(sh_ "readlink -f /opt/stayconnect/hotel-admin")"
CNT_BEFORE="$(q "SELECT (SELECT count(*) FROM iam_v2.entitlements)||'|'||(SELECT count(*) FROM iam_v2.sessions)||'|'||(SELECT count(*) FROM iam_v2.guest_device_actions)")"
ROLES_BEFORE="$(q "SELECT string_agg(rolname, ',' ORDER BY rolname) FROM pg_roles WHERE rolname LIKE 'svc\\_%' OR rolname LIKE 'sc\\_%' OR rolname LIKE 'iam\\_v2\\_%'")"
case "$BOOT_BEFORE" in
  ????????-????-????-????-????????????) echo "  boot id before: $BOOT_BEFORE" ;;
  *) echo "REFUSING: could not read a boot id before rebooting ($BOOT_BEFORE)"; exit 2 ;;
esac

# ---- the reboot -------------------------------------------------------------------------------------------
echo "  issuing a real reboot..."
ssh -n -o BatchMode=yes -o ConnectTimeout=8 "root@$APPL" "systemd-run --on-active=1 --timer-property=AccuracySec=100ms /sbin/reboot" >/dev/null 2>&1
sleep 20

waited=0
until [ "$waited" -ge "$WAIT_SECS" ]; do
  probe="$(sh_ "cat /proc/sys/kernel/random/boot_id")"
  case "$probe" in ????????-????-????-????-????????????) break ;; esac
  sleep 10; waited=$((waited+10))
done
BOOT_AFTER="$(sh_ "cat /proc/sys/kernel/random/boot_id")"
echo "  boot id after:  $BOOT_AFTER   (waited ${waited}s for ssh to answer again)"

if [ "$BOOT_AFTER" = "$BOOT_BEFORE" ]; then
  no "THE MACHINE ACTUALLY REBOOTED" "the boot id is unchanged -- everything below would describe a machine that never went down"
  echo "------------------------------------------------------------"
  printf 'PHASE7_FINAL_REBOOT pass=%d fail=%d\n' "$pass" "$fail"
  exit 1
fi
case "$BOOT_AFTER" in ????????-????-????-????-????????????)
  ok "THE MACHINE ACTUALLY REBOOTED: the kernel boot id changed, so this is a real boot and not a restart" ;;
  *) no "the appliance did not come back within ${WAIT_SECS}s" "$BOOT_AFTER"; exit 1 ;;
esac
UPTIME="$(sh_ "cut -d. -f1 /proc/uptime")"
[ "${UPTIME:-99999}" -lt 600 ] && ok "...and it is freshly booted (uptime ${UPTIME}s)" \
  || no "uptime after reboot" "${UPTIME}s does not look like a fresh boot"

# ---- WAIT FOR THE BOOT TO FINISH, WHICH IS MEASUREMENT AND NOT REPAIR --------------------------------------
#
# The first run of this asserted 20 seconds after ssh started answering, at 44s of uptime, and reported five
# services down. They were not down; they were still starting -- all six were active by 74s, at 21:58:56 to
# 21:59:01. Asserting inside a machine's normal startup window measures the stopwatch, not the appliance.
#
# So this waits, bounded, for the system to converge. It starts nothing, enables nothing, reloads nothing and
# fixes nothing: if the appliance needs a human, the wait expires and every assertion below fails, which is the
# finding. CONVERGENCE IS "SERVING", NOT "active" -- a unit can be active while the thing it serves does not
# answer, and it is answering that a guest depends on.
converged=0; waited_up=0
until [ "$waited_up" -ge "${PHASE7_CONVERGE_WAIT:-180}" ]; do
  p_code="$(sh_ "curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:8380/")"
  h_code="$(sh_ "curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:3100/")"
  e_code="$(sh_ "curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:8090/edge/v1/health")"
  if [ "$p_code" = "200" ] && [ "$h_code" = "307" ] && [ "$e_code" = "200" ]; then converged=1; break; fi
  sleep 5; waited_up=$((waited_up+5))
done
UP_AT_CONV="$(sh_ "cut -d. -f1 /proc/uptime")"
if [ "$converged" = "1" ]; then
  ok "the appliance converged to SERVING by itself, ${waited_up}s after ssh answered (uptime ${UP_AT_CONV}s), with no operator action"
else
  no "the appliance converged to SERVING on its own" "still not serving after ${waited_up}s (portal=$p_code admin=$h_code edge=$e_code)"
fi

echo
echo "-- after the boot, with no operator action of any kind --"

for u in stayconnect-scd stayconnect-acctd stayconnect-edged stayconnect-portald stayconnect-netd stayconnect-hotel-admin; do
  eq "$u came back by itself" "$(sh_ "systemctl is-active $u")" "active"
done

# SERVING, not merely active: a unit can be 'active' while the thing it serves is not answering.
eq "the guest portal is SERVING after the boot" "$(sh_ "curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:8380/")" "200"
eq "Hotel Admin is SERVING after the boot" "$(sh_ "curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:3100/")" "307"
EH="$(sh_ "curl -s http://127.0.0.1:8090/edge/v1/health")"
case "$EH" in *'"db":true'*) ok "the edge API is SERVING and its database is reachable" ;;
              *) no "edge API after boot" "$(printf '%s' "$EH" | head -c 120)" ;; esac

eq "the schema survived: the same iam_v2 table count" "$(q "SELECT count(*) FROM information_schema.tables WHERE table_schema='iam_v2'")" "81"
eq "the durable state survived unchanged" "$(q "SELECT (SELECT count(*) FROM iam_v2.entitlements)||'|'||(SELECT count(*) FROM iam_v2.sessions)||'|'||(SELECT count(*) FROM iam_v2.guest_device_actions)")" "$CNT_BEFORE"
eq "the runtime roles survived exactly" "$(q "SELECT string_agg(rolname, ',' ORDER BY rolname) FROM pg_roles WHERE rolname LIKE 'svc\\_%' OR rolname LIKE 'sc\\_%' OR rolname LIKE 'iam\\_v2\\_%'")" "$ROLES_BEFORE"
eq "the Hotel Admin release is still the expected DARK release" "$(sh_ "readlink -f /opt/stayconnect/hotel-admin")" "$REL_BEFORE"

# ---- still dark ---------------------------------------------------------------------------------------------
eq "the guest device capability is still ABSENT at the layer that decides it (scd does not mount it)" \
   "$(sh_ "curl -s -o /dev/null -w '%{http_code}' --unix-socket /run/stayconnect/scd.sock -X POST -H 'Content-Type: application/json' -d '{}' http://unix/v1/phase6/devices/list")" "404"
eq "...and the per-appliance setting is still OFF" \
   "$(q "SELECT coalesce(bool_or(guest_device_self_service),false)::text FROM iam_v2.appliance_product_settings")" "false"
DEVB="$(sh_ "curl -s -X POST -H 'Content-Type: application/json' -d '{}' http://127.0.0.1:8380/devices/list")"
case "$DEVB" in *'"ok":false'*) ok "the guest device route still returns the uniform non-success" ;;
                *) no "guest device route after boot" "$(printf '%s' "$DEVB" | head -c 140)" ;; esac
eq "financial egress is still disabled: no outbox row, no attempt, no payment" \
   "$(q "SELECT (SELECT count(*) FROM iam_v2.posting_outbox)||'|'||(SELECT count(*) FROM iam_v2.posting_attempts)||'|'||(SELECT count(*) FROM iam_v2.payment_transactions)")" "0|0|0"
# iam_v2.sessions has no created_at; its timestamps are started/ended/expires_at. The first version named a
# column that does not exist, so the case failed on a SQL error rather than measuring anything -- which is the
# right way round, but it measured nothing.
eq "no live synthetic access remains: no session was started by this validation, and none is still open" \
   "$(q "SELECT (SELECT count(*) FROM iam_v2.sessions WHERE started > now() - interval '3 hours')||'|'||(SELECT count(*) FROM iam_v2.sessions WHERE ended IS NULL AND started > now() - interval '3 hours')")" "0|0"

# ---- still independent of Central -----------------------------------------------------------------------------
eq "the appliance still names no Central URL in the guest or portal configuration" \
   "$(sh_ "grep -hoE '^(CLOUD|CENTRAL)[A-Z_]*_URL=' /etc/stayconnect/scd.env /etc/stayconnect/portald.env 2>/dev/null | wc -l")" "0"
for name in api.stayconnect.local admin.stayconnect.local; do
  eq "the Central-only name $name is still refused (404)" \
     "$(sh_ "curl -sk -o /dev/null -w '%{http_code}' -H 'Host: $name' https://$APPL/edge/v1/health")" "404"
done

echo "------------------------------------------------------------"
printf 'PHASE7_FINAL_REBOOT boot_before=%s boot_after=%s pass=%d fail=%d\n' \
  "${BOOT_BEFORE:0:8}" "${BOOT_AFTER:0:8}" "$pass" "$fail"
[ "$fail" -eq 0 ]
