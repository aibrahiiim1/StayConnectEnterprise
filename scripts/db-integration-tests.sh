#!/usr/bin/env bash
# Runs the appliance database test suites that need a full-schema database and otherwise skip silently
# (audit P3 M5). Builds the factory-clean schema once with scripts/lib-fullschema-testdb.sh, gives every suite
# its own copy of it (CREATE DATABASE ... TEMPLATE), runs the suite with its *_TEST_DSN set, and removes the
# container and its volume at the end. On demand / FULL CHECK only; never a required merge check (D42).
#
# A suite that reports a skip is a failure here: the point is that these tests run.
#
# Usage: scripts/db-integration-tests.sh
#   DBIT_CONTAINER (default sc-dbit-$$)   DBIT_PORT (default 55471)   FULLSCHEMA_IMAGE (see the library)
# Exit: 0 every suite passed · 1 a suite failed or skipped · 2 the database could not be built
set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=lib-fullschema-testdb.sh
. "$ROOT/scripts/lib-fullschema-testdb.sh"
C="${DBIT_CONTAINER:-sc-dbit-$$}"
PORT="${DBIT_PORT:-55471}"
TEMPLATE=dbit_template
trap 'docker rm -f -v "$C" >/dev/null 2>&1 || true' EXIT

# suite: <env var> <package> — superuser-DSN suites; the service-role suites need their own logins.
SUITES=(
  "OTP_TEST_DSN ./internal/otp/"
  "OTPBOOT_TEST_DSN ./internal/keybootstrap/"
  "THROTTLE_TEST_DSN ./internal/throttle/"
)

echo "== building the full appliance schema ($FULLSCHEMA_IMAGE) =="
fullschema_build "$C" "$PORT" "$TEMPLATE"; rc=$?
[ "$rc" = 0 ] || { echo "DB_INTEGRATION = cannot build the schema (rc=$rc)"; exit 2; }

failed=0; n=0
for s in "${SUITES[@]}"; do
  var="${s%% *}"; pkg="${s#* }"; n=$((n+1)); db="dbit_$n"
  # TimescaleDB's background workers connect to the template now and then; end them and retry the copy.
  copied=0
  for _ in $(seq 1 20); do
    docker exec "$C" psql -X -q -U postgres -d postgres -tAc \
      "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '$TEMPLATE' AND pid <> pg_backend_pid()" \
      </dev/null >/dev/null 2>&1
    if docker exec "$C" psql -X -q -U postgres -d postgres -c "CREATE DATABASE $db TEMPLATE $TEMPLATE" </dev/null >/dev/null 2>&1; then
      copied=1; break
    fi
    sleep 0.5
  done
  [ "$copied" = 1 ] || { echo "FAIL: cannot copy the schema for $pkg"; failed=1; continue; }
  echo "== $pkg ($var) =="
  out="$(cd "$ROOT/data-plane" && env "$var=postgres://postgres:postgres@127.0.0.1:$PORT/$db?sslmode=disable" \
         go test -count=1 -v "$pkg" 2>&1)"; trc=$?
  printf '%s\n' "$out" | grep -E '^(--- (PASS|FAIL|SKIP)|ok|FAIL|panic)' | sed 's/^/   /'
  if [ "$trc" != 0 ]; then echo "FAIL: $pkg"; failed=1
  elif printf '%s\n' "$out" | grep -q -- '--- SKIP'; then echo "FAIL: $pkg skipped tests although $var was set"; failed=1
  else echo "ok: $pkg"; fi
done

[ "$failed" = 0 ] && { echo "DB_INTEGRATION = PASS ($n suites)"; exit 0; }
echo "DB_INTEGRATION = FAIL"; exit 1
