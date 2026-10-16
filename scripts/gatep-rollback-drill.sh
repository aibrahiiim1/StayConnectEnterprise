#!/usr/bin/env bash
# THE GATE-P RECONCILE MUST RUN ON A ROLLED-BACK SCHEMA (audit B-20).
#
# deploy/gatep/gatep-grants.sql is one transaction that revokes every runtime privilege and re-grants the
# allowlist. A GRANT on an object that does not exist fails that transaction -- so before B-20, rolling back
# 0116, 0117 or 0118 (or any later migration whose objects a grant file names) left a database on which the
# reconcile could not run at all, and the only way to re-assert least privilege was to roll forward again.
# Every grant on an object created by those migrations is now guarded on the object.
#
# This drill proves it on the real chain: build the full appliance schema in a disposable container, roll the
# migrations above DRILL_FLOOR back one at a time in reverse order, running the complete reconcile after each
# step, then roll them forward one at a time, reconciling after each again. Any failed step fails the drill.
#
#   scripts/gatep-rollback-drill.sh            (DRILL_FLOOR=0115, DRILL_PORT=55409)
#
# Disposable infrastructure only: no appliance, no Central, no PMS. The container is removed on exit.
set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
. "$ROOT/scripts/lib-fullschema-testdb.sh"
C="sc-gatep-rollback-drill-$$"
DB=stayconnect_site
PORT="${DRILL_PORT:-55409}"
FLOOR="${DRILL_FLOOR:-0115}"
MIG="$ROOT/data-plane/migrations"

cleanup() { docker rm -f "$C" >/dev/null 2>&1 || true; }
trap cleanup EXIT

echo "== full appliance schema (container $C, port $PORT) =="
fullschema_build "$C" "$PORT" "$DB"; rc=$?
[ "$rc" = 0 ] || { echo "INFRA/SCHEMA: build failed (rc=$rc)"; exit "$rc"; }

sql()       { docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 -q "$@"; }
reconcile() {
  local f
  for f in gatep-iam-roles.sql gatep-iam-ownership.sql gatep-grants.sql; do
    MSYS_NO_PATHCONV=1 docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 -q -f "/tmp/gatep/$f" \
      >/tmp/drill-reconcile.log 2>&1 || { grep -E 'ERROR|psql:' /tmp/drill-reconcile.log | head -3; return 1; }
  done
}

mapfile -t MIGS < <(ls "$MIG" | grep -E '^[0-9]{4}_.*\.down\.sql$' | sed 's/\.down\.sql$//' | sort -r \
                    | while read -r m; do [ "${m%%_*}" \> "$FLOOR" ] && echo "$m"; done)
[ "${#MIGS[@]}" -gt 0 ] || { echo "no migrations above $FLOOR"; exit 1; }
echo "== ${#MIGS[@]} migrations above $FLOOR, rolled back newest first =="

fail=0
for m in "${MIGS[@]}"; do
  sql < "$MIG/$m.down.sql" >/tmp/drill-step.log 2>&1 || { echo "  FAIL down $m"; tail -3 /tmp/drill-step.log; exit 1; }
  sql -tAc "DELETE FROM public.schema_migrations WHERE version='$m'" >/dev/null
  if reconcile; then echo "  ok: reconcile after rolling back $m"; else echo "  FAIL: reconcile after rolling back $m"; fail=1; break; fi
done
if [ "$fail" = 0 ]; then
  for (( i=${#MIGS[@]}-1; i>=0; i-- )); do
    m="${MIGS[$i]}"
    sql < "$MIG/$m.up.sql" >/tmp/drill-step.log 2>&1 || { echo "  FAIL up $m"; tail -3 /tmp/drill-step.log; exit 1; }
    sql -tAc "INSERT INTO public.schema_migrations(version) VALUES ('$m') ON CONFLICT DO NOTHING" >/dev/null
    if reconcile; then echo "  ok: reconcile after applying $m"; else echo "  FAIL: reconcile after applying $m"; fail=1; break; fi
  done
fi
[ "$fail" = 0 ] && { echo "GATEP_ROLLBACK_DRILL = PASS"; exit 0; }
echo "GATEP_ROLLBACK_DRILL = FAIL"; exit 1
