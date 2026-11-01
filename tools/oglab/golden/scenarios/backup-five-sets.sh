#!/usr/bin/env bash
# The five-set retention on the real backup engine, kept inside the 300 s bound with the smallest policy (1 regular + 2 safety):
# two manual backups leave exactly one regular set, whole (dump, configuration, marker, note); earlier backups are untouched.
# A backup of the lab database takes tens of seconds; if two do not fit, the run ends NOT VERIFIED, never PASS.
. "$(dirname "$0")/lib.sh"
D=/opt/stayconnect/backups/db
api PUT /backups/settings '{"retention":{"BACKUP_KEEP":1,"SAFETY_BACKUP_KEEP":2},"schedule":"","password":"@PW"}' >/dev/null
held() { apifull GET /backups/status | sed -n 's/.*"held": \([0-9]*\).*/\1/p' | head -1; }
held_before="$(held)"
[ -n "$held_before" ] || fail "the status does not report how many earlier backups are held"
for _ in 1 2; do
  api POST /backups/run '{"password":"@PW"}' | head -c 300; echo
done
n="$(docker exec "$APPL" sh -c "ls $D/db-*.meta.json 2>/dev/null | xargs -r grep -l '\"kind\": \"manual\"\\|\"kind\": \"scheduled\"' | wc -l")"
[ "$n" -eq 1 ] || fail "$n regular sets kept, want 1"
for f in $(docker exec "$APPL" sh -c "ls $D/db-*.meta.json"); do
  name="$(docker exec "$APPL" sh -c "jq -r .name $f")"
  docker exec "$APPL" test -f "$D/$name" -a -f "$D/$name.verified" || fail "set $name is not whole"
done
total="$(docker exec "$APPL" sh -c "ls $D/db-*.meta.json | wc -l")"
[ "$total" -le 5 ] || fail "$total sets at rest, more than five"
apifull GET /backups/status | grep -q '"max_total": 5' || fail "the status does not report the five-set policy"
[ "$(held)" = "$held_before" ] || fail "earlier backups changed: $held_before before, $(held) after (rotation must never touch them)"
log "ok: one regular set kept whole, $total sets at rest, $held_before earlier backups untouched"
api PUT /backups/settings '{"retention":{"BACKUP_KEEP":3,"SAFETY_BACKUP_KEEP":2},"schedule":"","password":"@PW"}' >/dev/null
