#!/usr/bin/env bash
# THE COMBINED INTEGRATION RUN: every package that has integration tests, against ONE shared full-schema database, N times in a row.
#
# Why it exists. Each integration suite used to be proven on a database of its own, fresh, so a fixture that is only unique for
# the first run (a fixed primary key, a subnet taken from a finite counter and never released) passed every time it was tried and
# failed in the first combined run: scd's TestActivationRefusedWithoutARegisteredAccountingOrigin ("no pending session to
# activate" / "no free fixture subnet remains"), the event-table trim test (duplicate primary key on the second run). A suite is
# only isolated if the SECOND run on the SAME database passes, and this is the run that proves it.
#
# Builds the factory-clean schema once (scripts/lib-fullschema-testdb.sh), then runs
#   go test -tags integration -p 1 ./cmd/... ./internal/...
# RUNS times against the same database with PHASE3_TEST_DSN (and the PHASE2/PHASE4 aliases) set. Nothing is skipped or filtered. A
# run that reports a failure fails the script; the failing test names are printed. On demand / FULL CHECK only (D42).
#
# Usage: scripts/db-integration-combined.sh
#   COMBINED_RUNS (default 2)   COMBINED_CONTAINER (default sc-combined-$$)   COMBINED_PORT (default 55481)
#   COMBINED_DSN_HOST (default 127.0.0.1: how the Go test process reaches the container)
#   COMBINED_GO (default "go": a command prefix, e.g. a docker run wrapper that executes `go test ...` in the data-plane directory)
#   COMBINED_PKGS (default "./cmd/... ./internal/...")   FULLSCHEMA_IMAGE (see the library)
# Exit: 0 every run passed · 1 a run failed · 2 the database could not be built
set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=lib-fullschema-testdb.sh
. "$ROOT/scripts/lib-fullschema-testdb.sh"
C="${COMBINED_CONTAINER:-sc-combined-$$}"
PORT="${COMBINED_PORT:-55481}"
RUNS="${COMBINED_RUNS:-2}"
HOST="${COMBINED_DSN_HOST:-127.0.0.1}"
GO_CMD="${COMBINED_GO:-go}"
PKGS="${COMBINED_PKGS:-./cmd/... ./internal/...}"
TEMPLATE=combined_template
DB=combined_shared
trap 'docker rm -f -v "$C" >/dev/null 2>&1 || true' EXIT

echo "== building the full appliance schema ($FULLSCHEMA_IMAGE) =="
fullschema_build "$C" "$PORT" "$TEMPLATE"; rc=$?
[ "$rc" = 0 ] || { echo "DB_INTEGRATION_COMBINED = cannot build the schema (rc=$rc)"; exit 2; }
copied=0
# TimescaleDB's background workers keep reconnecting to the template, and CREATE DATABASE ... TEMPLATE needs it unused: refuse new
# connections to it for the copy, then end the ones that are there.
docker exec "$C" psql -X -q -U postgres -d postgres -c "ALTER DATABASE $TEMPLATE WITH ALLOW_CONNECTIONS false" </dev/null >/dev/null 2>&1
for _ in $(seq 1 30); do
  docker exec "$C" psql -X -q -U postgres -d postgres -tAc     "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '$TEMPLATE' AND pid <> pg_backend_pid()" </dev/null >/dev/null 2>&1
  if docker exec "$C" psql -X -q -U postgres -d postgres -c "CREATE DATABASE $DB TEMPLATE $TEMPLATE" </dev/null >/dev/null 2>&1; then copied=1; break; fi
  sleep 1
done
[ "$copied" = 1 ] || { echo "DB_INTEGRATION_COMBINED = cannot copy the schema"; exit 2; }

DSN="postgres://postgres:postgres@$HOST:$PORT/$DB?sslmode=disable"
failed=0
for run in $(seq 1 "$RUNS"); do
  echo "== run $run of $RUNS on the SAME database =="
  logf="${TMPDIR:-/tmp}/db-integration-combined-run$run.log"
  ( cd "$ROOT/data-plane" && env PHASE3_TEST_DSN="$DSN" PHASE4_TEST_DSN="$DSN" PHASE2_TEST_DSN="$DSN"       $GO_CMD test -v -count=1 -tags integration -p 1 -timeout 90m $PKGS ) >"$logf" 2>&1; trc=$?
  grep -E '^(--- FAIL|FAIL|panic)' "$logf" | sed 's/^/   /'
  skips="$(grep -cE '^\s*--- SKIP' "$logf")"
  exhausted="$(grep -ciE 'no free .*(subnet|octet)|fixture .*(exhaust|remains)' "$logf")"
  echo "   skipped tests: $skips (fixture subnets exhausted: $exhausted)"
  # A test skipped because the shared database ran out of fixtures is a test that did not run: the exact defect this script exists to catch.
  if [ "$exhausted" != 0 ]; then echo "FAIL: run $run skipped tests for lack of fixture subnets"; failed=1; break; fi
  if [ "$trc" != 0 ]; then echo "FAIL: run $run (log: $logf)"; failed=1; break; fi
  echo "ok: run $run ($(grep -c '^ok ' "$logf") packages ok; log: $logf)"
done

[ "$failed" = 0 ] && { echo "DB_INTEGRATION_COMBINED = PASS ($RUNS runs on one database)"; exit 0; }
echo "DB_INTEGRATION_COMBINED = FAIL"; exit 1
