#!/usr/bin/env bash
# A disposable, factory-clean appliance site database for integration tests of migrations newer than the
# production baseline. It builds exactly what a fresh appliance builds (Gate-P roles, the baseline, Gate-P
# ownership and grants), then applies every data-plane migration numbered ABOVE the baseline in order, then
# re-applies Gate-P grants (the grant-durability rule: a grant only a migration makes must survive).
#
#   bash scripts/sitedb-dev.sh up      # start (or restart) the container and print the DSN
#   bash scripts/sitedb-dev.sh down    # remove it
#   bash scripts/sitedb-dev.sh redo    # roll the post-baseline migrations down and up again
#
# Disposable test infrastructure only: no appliance, no Central, no PMS, no provider.
set -euo pipefail
export PATH="$PATH:/c/Program Files/Docker/Docker/resources/bin"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
C="${SITEDB_CONTAINER:-onegate-sitedb-dev}"
PORT="${SITEDB_PORT:-55480}"
DB=stayconnect_site
BASE="$ROOT/data-plane/migrations/baseline/0000_production_baseline.sql"
GATEP="$ROOT/deploy/gatep"
BASELINE_TOP="${SITEDB_BASELINE_TOP:-0100}"

psql_run() { docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 "$@"; }

post_baseline() {
  ls "$ROOT/data-plane/migrations" | grep -E '^[0-9]{4}_.*\.up\.sql$' | sort | while read -r f; do
    n="${f%%_*}"
    if [ "$n" \> "$BASELINE_TOP" ]; then echo "${f%.up.sql}"; fi
  done
}

up() {
  docker rm -f "$C" >/dev/null 2>&1 || true
  docker run -d --name "$C" -e POSTGRES_PASSWORD=postgres -e POSTGRES_DB="$DB" \
    -p "127.0.0.1:$PORT:5432" "${SITEDB_IMAGE:-timescale/timescaledb:2.16.1-pg16}" >/dev/null
  for _ in $(seq 1 180); do
    if [ "$(docker logs "$C" 2>&1 | grep -c 'database system is ready to accept connections')" -ge 2 ] &&
       docker exec "$C" psql -U postgres -d "$DB" -tAqc 'select 1' >/dev/null 2>&1; then break; fi
    sleep 1
  done
  sleep 2
  psql_run < "$GATEP/gatep-roles.sql" >/dev/null
  psql_run -tAqc "DO \$\$ BEGIN CREATE ROLE stayconnect NOLOGIN SUPERUSER; EXCEPTION WHEN duplicate_object THEN NULL; END \$\$;" >/dev/null
  docker cp "$GATEP" "$C:/tmp/gatep" >/dev/null
  MSYS_NO_PATHCONV=1 docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 -q -f /tmp/gatep/gatep-iam-roles.sql >/dev/null
  { printf 'SET ROLE stayconnect;\n'; cat "$BASE"; } | psql_run -q >/dev/null
  MSYS_NO_PATHCONV=1 docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 -q -f /tmp/gatep/gatep-iam-ownership.sql >/dev/null
  for m in $(post_baseline); do
    echo "  applying $m"
    psql_run -q < "$ROOT/data-plane/migrations/$m.up.sql" >/dev/null
    psql_run -tAqc "INSERT INTO public.schema_migrations(version) VALUES ('$m') ON CONFLICT DO NOTHING" >/dev/null 2>&1 || true
  done
  docker cp "$GATEP" "$C:/tmp/gatep" >/dev/null
  MSYS_NO_PATHCONV=1 docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 -q -f /tmp/gatep/gatep-iam-ownership.sql >/dev/null
  MSYS_NO_PATHCONV=1 docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 -q -f /tmp/gatep/gatep-grants.sql >/dev/null
  echo "DSN=postgres://postgres:postgres@127.0.0.1:$PORT/$DB?sslmode=disable"
}

redo() {
  for m in $(post_baseline | sort -r); do
    echo "  down $m"
    psql_run -q < "$ROOT/data-plane/migrations/$m.down.sql" >/dev/null
  done
  for m in $(post_baseline); do
    echo "  up $m"
    psql_run -q < "$ROOT/data-plane/migrations/$m.up.sql" >/dev/null
  done
}

case "${1:-up}" in
  up) up ;;
  down) docker rm -f "$C" >/dev/null 2>&1 || true ;;
  redo) redo ;;
  *) echo "usage: $0 up|down|redo"; exit 2 ;;
esac
