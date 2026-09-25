#!/usr/bin/env bash
# Migration 0091 behaviour, against a FACTORY-CLEAN schema: real roles, owners and Gate-P grants, built from the
# repository by scripts/clean-install-reconstruction.sh. Every mutation is attempted as svc_edged.
#
# Exit 0 only if every check in scripts/catalogue-delete-integration.sql printed PASS and none printed FAIL.
# Exit 2 if the disposable database could not be built (infrastructure, not a verdict).
set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
C="sc-catalogue-delete-$$"
cleanup() { docker rm -f "$C" >/dev/null 2>&1 || true; }
trap cleanup EXIT

CLEANROOM_KEEP=1 CLEANROOM_NAME="$C" CLEANROOM_OUT="${TMPDIR:-/tmp}/cleanroom-$C" \
  bash "$ROOT/scripts/clean-install-reconstruction.sh" > "${TMPDIR:-/tmp}/cleanroom-$C.log" 2>&1 \
  || { echo "INFRA: the factory-clean schema could not be built"; tail -5 "${TMPDIR:-/tmp}/cleanroom-$C.log"; exit 2; }

docker cp "$ROOT/scripts/catalogue-delete-integration.sql" "$C:/tmp/t.sql" >/dev/null
out=$(MSYS_NO_PATHCONV=1 docker exec "$C" psql -U postgres -d stayconnect_site -f /tmp/t.sql 2>&1)
echo "$out" | grep -E "PASS|FAIL|FIXTURE_FAILED" | sed 's/^.*WARNING:  //'

want=$(grep -c "pg_temp.check('" "$ROOT/scripts/catalogue-delete-integration.sql")
pass=$(echo "$out" | grep -c "WARNING:  PASS ")
fail=$(echo "$out" | grep -cE "WARNING:  (FAIL |FIXTURE_FAILED)")
echo "CATALOGUE_DELETE_INTEGRATION pass=$pass fail=$fail expected=$want"
[ "$fail" -eq 0 ] && [ "$pass" -eq "$want" ] && exit 0
exit 1
