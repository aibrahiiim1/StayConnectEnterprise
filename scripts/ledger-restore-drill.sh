#!/usr/bin/env bash
# THE ERASURE LEDGER RESTORE DRILL (GDPR final mission).
#
# Builds a disposable PostgreSQL with the full appliance schema in a container named stayconnect-pg (the name scd's restore uses),
# then runs data-plane/cmd/scd/restore_drill_integration_test.go against it: the REAL restore code, real files for both ledger
# copies and a real encrypted secondary copy. Each scenario takes a backup, erases a guest and answers a request afterwards, damages
# or removes ledger copies in a different way, restores the older backup, and asserts what the restored appliance serves.
#
#   scripts/ledger-restore-drill.sh [go test -run regex]
#
# Needs docker and Go. Touches nothing but the container it creates (removed at the end unless KEEP=1). No appliance, no Central.
set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=lib-fullschema-testdb.sh
. "$ROOT/scripts/lib-fullschema-testdb.sh"
PORT="${DRILL_PORT:-55530}"
RUN="${1:-TestDrill_}"

docker rm -f stayconnect-pg >/dev/null 2>&1 || true
fullschema_build stayconnect-pg "$PORT" stayconnect_site > /tmp/ledger-drill-build.log 2>&1
rc=$?
if [ $rc != 0 ]; then
  echo "drill: the disposable database could not be built (rc=$rc)"; tail -20 /tmp/ledger-drill-build.log; exit $rc
fi
# the platform role may log in on an appliance; the disposable schema creates it NOLOGIN
fullschema_psql stayconnect-pg postgres -q -c "ALTER ROLE stayconnect LOGIN" || exit 1

export LEDGER_DRILL_DSN="postgres://postgres:postgres@127.0.0.1:$PORT/stayconnect_site"
export PHASE3_TEST_DSN="$LEDGER_DRILL_DSN"
cd "$ROOT/data-plane" || exit 1
go test -tags integration -count=1 -p 1 -v -run "$RUN" ./cmd/scd 2>&1 | grep -vE '^(=== RUN|=== PAUSE|=== CONT)|^[0-9]{4}/[0-9]{2}/[0-9]{2} '
st=${PIPESTATUS[0]}
[ "${KEEP:-0}" = 1 ] || docker rm -f stayconnect-pg >/dev/null 2>&1
exit $st
