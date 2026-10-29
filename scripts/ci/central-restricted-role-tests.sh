#!/usr/bin/env bash
# CENTRAL LEAST PRIVILEGE (audit P3 M1): run the control-plane Go tests with the database pool connected as the
# Central RUNTIME role (ctrlapi_rt, migration 0063), not as the owning superuser, in a throwaway TimescaleDB.
#
#   scripts/ci/central-restricted-role-tests.sh                 whole control-plane module as ctrlapi_rt
#   scripts/ci/central-restricted-role-tests.sh --run REGEX     only tests matching REGEX (go test -run)
#   scripts/ci/central-restricted-role-tests.sh --pkgs "./internal/api/..."   only those packages
#   scripts/ci/central-restricted-role-tests.sh --owner-baseline   ALSO run the same selection as the owner first
#   scripts/ci/central-restricted-role-tests.sh --keep          leave the database container for inspection
#
# How it is wired: CTRLAPI_TEST_DB_URL (the owner) names the migrated template database; internal/testdb creates and
# drops a copy per test as the owner and, because CTRLAPI_TEST_DB_APP_ROLE/_PASSWORD are set, hands the test a pool that
# logs in to the copy as ctrlapi_rt. TestRuntimeRoleHasOnlyTheLeastPrivilege then asserts what that role cannot do
# (it skips in a normal run). Passwords are generated here, held in environment variables and never printed.
# Needs docker; Windows Git Bash is handled (MSYS_NO_PATHCONV). Everything it starts is removed on exit.
set -euo pipefail

PROG=central-restricted-role-tests
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
case "$(uname -s 2>/dev/null)" in MINGW*|MSYS*|CYGWIN*) MOUNT_ROOT="$(cd "$ROOT" && pwd -W)" ;; *) MOUNT_ROOT="$ROOT" ;; esac
PG_IMAGE="${PG_IMAGE:-timescale/timescaledb:2.16.1-pg16}"
GO_IMAGE="${GO_IMAGE:-golang:1.26.10}"
TAG="rt$$"
PGC="sc-rt-pg-$TAG"; NET="sc-rt-net-$TAG"
PKGS="./..."; RUN=""; KEEP=0; BASELINE=0
while [ $# -gt 0 ]; do
  case "$1" in
    --run) RUN="$2"; shift 2 ;;
    --pkgs) PKGS="$2"; shift 2 ;;
    --keep) KEEP=1; shift ;;
    --owner-baseline) BASELINE=1; shift ;;
    -h|--help) sed -n '2,19p' "$0"; exit 0 ;;
    *) echo "$PROG: unknown argument $1" >&2; exit 2 ;;
  esac
done
say() { printf '[%s] %s\n' "$PROG" "$*"; }
command -v docker >/dev/null || { echo "$PROG: docker is required" >&2; exit 2; }

cleanup() {
  if [ "$KEEP" = 1 ]; then say "kept container $PGC and network $NET"; return; fi
  docker rm -f "$PGC" >/dev/null 2>&1 || true
  docker network rm "$NET" >/dev/null 2>&1 || true
}
trap cleanup EXIT

OWNER_PW="$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n')"
RT_PW="$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n')"
export OWNER_PW RT_PW

docker network create "$NET" >/dev/null
say "starting $PG_IMAGE"
docker run -d --name "$PGC" --network "$NET" -e POSTGRES_USER=stayconnect -e POSTGRES_DB=stayconnect \
  -e POSTGRES_PASSWORD="$OWNER_PW" "$PG_IMAGE" >/dev/null
ok=0
for _ in $(seq 1 90); do
  if docker exec "$PGC" psql -U stayconnect -d stayconnect -tAc 'SELECT 1' >/dev/null 2>&1; then
    sleep 3
    docker exec "$PGC" psql -U stayconnect -d stayconnect -tAc 'SELECT 1' >/dev/null 2>&1 && { ok=1; break; }
  fi
  sleep 1
done
[ "$ok" = 1 ] || { echo "$PROG: database did not become ready" >&2; exit 1; }

say "migrating the template database (central-migrate.sh up; includes 0063)"
bash "$ROOT/deploy/scripts/central-migrate.sh" --pg-exec "docker exec -i $PGC" --db stayconnect up >/dev/null
bash "$ROOT/deploy/scripts/central-migrate.sh" --pg-exec "docker exec -i $PGC" --db stayconnect verify >/dev/null

# The same step the installer and central-deploy.sh run: the password goes in on stdin, from the generated value.
printf "ALTER ROLE ctrlapi_rt PASSWORD '%s';\n" "$RT_PW" | docker exec -i "$PGC" psql -q -v ON_ERROR_STOP=1 -U stayconnect -d stayconnect >/dev/null

gotest() { # gotest <label> [extra -e flags...]
  local label="$1"; shift
  say "go test $PKGS ${RUN:+-run $RUN }as $label"
  MSYS_NO_PATHCONV=1 docker run --rm --network "$NET" -v "$MOUNT_ROOT:/src" -v sc-gomod-cache:/go/pkg/mod \
    -w /src/control-plane -e CTRLAPI_TEST_DB_URL -e CGO_ENABLED=0 -e GOFLAGS=-buildvcs=false "$@" \
    "$GO_IMAGE" go test -count=1 -p 1 ${RUN:+-run "$RUN"} $PKGS
}

export CTRLAPI_TEST_DB_URL="postgres://stayconnect:${OWNER_PW}@${PGC}:5432/stayconnect?sslmode=disable"
if [ "$BASELINE" = 1 ]; then
  gotest "the owner (baseline)"
fi
export CTRLAPI_TEST_DB_APP_ROLE=ctrlapi_rt CTRLAPI_TEST_DB_APP_PASSWORD="$RT_PW"
gotest "ctrlapi_rt" -e CTRLAPI_TEST_DB_APP_ROLE -e CTRLAPI_TEST_DB_APP_PASSWORD
say "PASS: the selected tests pass with the pool connected as ctrlapi_rt"
