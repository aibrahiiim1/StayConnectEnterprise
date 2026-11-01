#!/usr/bin/env bash
# Decision D-1 (fail closed): an appliance that restarts while PostgreSQL is unavailable restores NO Internet access. The container
# restart stands in for a reboot (new boot id inside the container is NOT guaranteed: the governor's boot-id check is proven by the
# unit tests; this scenario proves the kernel set stays empty with the database down after the services restart).
. "$(dirname "$0")/lib.sh"
trap 'appl "docker compose -f /opt/stayconnect/deploy/compose/infra.yml start postgres" >/dev/null 2>&1 || true' EXIT

signin 0 20
wait_for 60 "20 guests authorised" sh -c "[ \$(docker exec $APPL sh -c '. /root/cont-lib.sh; authset') -ge 20 ]"
appl "docker compose -f /opt/stayconnect/deploy/compose/infra.yml stop postgres"
log "database stopped"
appl "systemctl restart stayconnect-netd stayconnect-acctd stayconnect-scd" || true
appl "nft flush set inet stayconnect phase3_auth_ipv4"   # what a reboot leaves: an empty set
for _ in $(seq 1 30); do
  [ "$(authcount)" -eq 0 ] || fail "an authorisation appeared with the database down after a restart (unmetered access)"
  sleep 2
done
log "ok: 60 s with the database down after the restart and the set stayed empty"
appl "docker compose -f /opt/stayconnect/deploy/compose/infra.yml start postgres"
signin 0 20
wait_for 120 "guests re-admitted by the normal path once the database is back" sh -c "[ \$(docker exec $APPL sh -c '. /root/cont-lib.sh; authset') -ge 20 ]"
