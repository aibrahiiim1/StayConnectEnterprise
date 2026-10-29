#!/usr/bin/env bash
# Self-test for central-migrate.sh against a disposable PostgreSQL (audit P3 M3, L4, L5, CC4).
#
# Asserts:
#   - the real control-plane migrations apply to an empty database; every ledger row carries its checksum, and
#     status/verify report clean (and do not print "0 awaiting checksum backfill");
#   - a migration whose session dies before COMMIT leaves neither its objects nor its ledger row;
#   - a migration with its own BEGIN;/COMMIT; gets its ledger row inside that transaction; a file with
#     statements after its COMMIT is refused before anything runs;
#   - two concurrent 'up' runs apply each migration exactly once;
#   - 'adopt --through' accepts only an existing migration number; a migration name outside NNNN_name is refused;
#   - a pre-ledger row (no checksum) is reported, then backfilled and marked adopted.
#
# Needs docker. Starts its own container and removes it on exit.
#   CENTRAL_MIGRATE_SELFTEST_CONTAINER (default central-migrate-selftest-$$)
#   CENTRAL_MIGRATE_SELFTEST_IMAGE     (default timescale/timescaledb:2.16.1-pg16, the image Central runs)
#   CENTRAL_MIGRATE_UNDER_TEST         (default ./central-migrate.sh; lets the same cases run against another copy)
set -uo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
RUNNER="${CENTRAL_MIGRATE_UNDER_TEST:-$HERE/central-migrate.sh}"
C="${CENTRAL_MIGRATE_SELFTEST_CONTAINER:-central-migrate-selftest-$$}"
IMAGE="${CENTRAL_MIGRATE_SELFTEST_IMAGE:-timescale/timescaledb:2.16.1-pg16}"
T="$(mktemp -d)"
fail=0
ok()  { echo "PASS: $*"; }
bad() { echo "FAIL: $*"; fail=1; }
cleanup() { docker rm -f -v "$C" >/dev/null 2>&1 || true; rm -rf "$T"; }
trap cleanup EXIT

docker rm -f -v "$C" >/dev/null 2>&1 || true
docker run -d --name "$C" -e POSTGRES_USER=stayconnect -e POSTGRES_PASSWORD=selftest -e POSTGRES_DB=stayconnect \
  "$IMAGE" >/dev/null || { echo "INFRA: cannot start $IMAGE"; exit 2; }
# The image restarts once after initdb; wait for three clean queries in a row.
good=0
for _ in $(seq 1 90); do
  if docker exec "$C" psql -U stayconnect -d stayconnect -tAc 'select 1' >/dev/null 2>&1; then
    good=$((good+1)); [ "$good" -ge 3 ] && break
  else good=0; fi
  sleep 1
done
[ "$good" -ge 3 ] || { echo "INFRA: postgres never became ready"; exit 2; }

Q() { docker exec -i "$C" psql -X -U stayconnect -d "$1" -tAc "$2" </dev/null 2>&1; }
newdb() { Q stayconnect "CREATE DATABASE $1" >/dev/null; }
run() { # run <db> <migrations dir> <args...>
  local db="$1" dir="$2"; shift 2
  CENTRAL_MIGRATIONS_DIR="$dir" bash "$RUNNER" --pg-exec "docker exec -i $C" --db "$db" "$@"
}

# ---- 1. the real migrations
REAL="$(cd "$HERE/../.." && pwd)/control-plane/migrations"
newdb real
n="$(ls "$REAL"/*.up.sql | wc -l | tr -d ' ')"
if run real "$REAL" up > "$T/real.log" 2>&1; then
  rows="$(Q real "SELECT count(*) || '/' || count(NULLIF(checksum,'')) || '/' || count(*) FILTER (WHERE adopted) FROM schema_migrations")"
  [ "$rows" = "$n/$n/0" ] && ok "real migrations: $n applied, each with its checksum, none adopted" \
    || bad "real migrations: ledger rows/checksums/adopted = $rows, want $n/$n/0"
else bad "real migrations: up failed"; tail -5 "$T/real.log"; fi
out="$(run real "$REAL" status 2>&1)"
if printf '%s' "$out" | grep -q "$n applied, 0 pending, 0 modified\$"; then
  ok "status: clean, and no '0 awaiting checksum backfill'"
else bad "status: $(printf '%s' "$out" | tail -1)"; fi
run real "$REAL" verify >/dev/null 2>&1 && ok "verify passes after up" || bad "verify fails after up"

# ---- 1b. the runtime role (0063, audit P3 M1) runs the read-only commands, and cannot apply
if [ "$(Q real "SELECT count(*) FROM pg_roles WHERE rolname = 'ctrlapi_rt'")" = 1 ]; then
  rt() { PGUSER=ctrlapi_rt run "$@"; }
  rt real "$REAL" verify >/dev/null 2>&1 && ok "ctrlapi_rt: verify passes (no DDL attempted on a complete ledger)" || bad "ctrlapi_rt: verify fails"
  out="$(rt real "$REAL" status 2>&1)"
  printf '%s' "$out" | grep -q "$n applied, 0 pending, 0 modified\$" && ok "ctrlapi_rt: status is clean" || bad "ctrlapi_rt: status: $(printf '%s' "$out" | tail -1)"
  M0="$T/m0"; mkdir -p "$M0"; cp "$REAL"/*.sql "$M0/"; printf 'CREATE TABLE rt_must_not_exist (id int);\n' > "$M0/9999_rt_probe.up.sql"
  rt real "$M0" up >/dev/null 2>&1 && bad "ctrlapi_rt applied a migration" || ok "ctrlapi_rt: cannot apply a migration"
  [ "$(Q real "SELECT to_regclass('public.rt_must_not_exist') IS NULL")" = t ] && ok "ctrlapi_rt: nothing was created" || bad "ctrlapi_rt created a table"
else bad "migration 0063 did not create the ctrlapi_rt role"; fi

# ---- 2. a session that dies before COMMIT
M="$T/m"; mkdir -p "$M"
cat > "$M/0001_first.up.sql" <<'SQL'
CREATE TABLE first_t (id int);
CREATE TABLE IF NOT EXISTS applied_log (v text);
INSERT INTO applied_log VALUES ('0001');
SQL
cat > "$M/0002_dies.up.sql" <<'SQL'
CREATE TABLE dies_t (id int);
SELECT pg_terminate_backend(pg_backend_pid());
SQL
newdb crash
run crash "$M" up > "$T/crash.log" 2>&1 && bad "a migration whose session died reported success"
st="$(Q crash "SELECT (to_regclass('public.dies_t') IS NULL)::text || '/' || (SELECT count(*) FROM schema_migrations WHERE version='0002_dies')")"
[ "$st" = "true/0" ] && ok "session killed mid-migration: neither its objects nor its ledger row remain" \
  || bad "session killed mid-migration left dies_t-absent/ledger-rows = $st"
[ "$(Q crash "SELECT count(*) FROM schema_migrations WHERE version='0001_first' AND checksum <> ''")" = 1 ] \
  && ok "the migration before it stays applied and recorded" || bad "0001 not recorded"
# The window between the migration and its ledger row: make the ledger write fail (as a crash at that moment
# would) and check the migration's objects did not survive without it.
rm "$M/0002_dies.up.sql"
printf 'CREATE TABLE ledger_fails_t (id int);\n' > "$M/0002_ledger_fails.up.sql"
Q crash "CREATE FUNCTION refuse_ledger() RETURNS trigger LANGUAGE plpgsql AS \$\$ BEGIN
           IF NEW.version = '0002_ledger_fails' THEN RAISE EXCEPTION 'ledger write lost'; END IF; RETURN NEW; END \$\$;
         CREATE TRIGGER refuse_ledger BEFORE INSERT ON schema_migrations FOR EACH ROW EXECUTE FUNCTION refuse_ledger();" >/dev/null
run crash "$M" up > "$T/lost.log" 2>&1 && bad "a lost ledger write reported success"
[ "$(Q crash "SELECT (to_regclass('public.ledger_fails_t') IS NULL)::text")" = true ] \
  && ok "ledger write lost: the migration rolled back with it (no migrated schema without a record)" \
  || bad "ledger write lost: ledger_fails_t exists with no ledger row"
rm "$M/0002_ledger_fails.up.sql"

# ---- 3. a migration that manages its own transaction, and one with statements after its COMMIT
cat > "$M/0002_own_tx.up.sql" <<'SQL'
-- has its own transaction control
BEGIN;
CREATE TABLE own_t (id int);
INSERT INTO applied_log VALUES ('0002');
COMMIT;
-- trailing comment is fine
SQL
newdb own
if run own "$M" up > "$T/own.log" 2>&1 \
   && [ "$(Q own "SELECT count(*) FROM schema_migrations WHERE version='0002_own_tx' AND checksum <> '' AND NOT adopted")" = 1 ]; then
  ok "own BEGIN;/COMMIT; migration applied with its ledger row"
else bad "own-transaction migration"; tail -5 "$T/own.log"; fi
cat > "$M/0003_after_commit.up.sql" <<'SQL'
BEGIN;
CREATE TABLE after_t (id int);
COMMIT;
CREATE TABLE outside_t (id int);
SQL
run own "$M" up > "$T/after.log" 2>&1 && bad "statements after COMMIT were accepted"
[ "$(Q own "SELECT (to_regclass('public.after_t') IS NULL AND to_regclass('public.outside_t') IS NULL)::text")" = true ] \
  && grep -q 'statements after its COMMIT' "$T/after.log" \
  && ok "statements after COMMIT: refused before anything ran" || { bad "statements after COMMIT"; tail -3 "$T/after.log"; }
rm "$M/0003_after_commit.up.sql"

# ---- 4. two concurrent runs
for i in 3 4 5; do
  printf 'SELECT pg_sleep(1);\nINSERT INTO applied_log VALUES (%s);\n' "'000$i'" > "$M/000${i}_slow_$i.up.sql"
done
newdb conc
run conc "$M" up > "$T/c1.log" 2>&1 & p1=$!
run conc "$M" up > "$T/c2.log" 2>&1 & p2=$!
wait "$p1"; r1=$?; wait "$p2"; r2=$?
run conc "$M" up > "$T/c3.log" 2>&1   # finish whatever a losing run left pending
dups="$(Q conc "SELECT count(*) FROM (SELECT v FROM applied_log GROUP BY v HAVING count(*) > 1) d")"
total="$(Q conc "SELECT (SELECT count(DISTINCT v) FROM applied_log) || '/' || (SELECT count(*) FROM schema_migrations)")"
if [ "$dups" = 0 ] && [ "$total" = "5/5" ] && { [ "$r1" = 0 ] || [ "$r2" = 0 ]; }; then
  ok "two concurrent runs: every migration applied exactly once (exits $r1/$r2)"
else bad "concurrent runs: duplicates=$dups distinct/ledger=$total exits=$r1/$r2"; fi

# ---- 5. names and --through
out="$(run conc "$M" adopt --through 9999 --yes 2>&1)" && bad "adopt --through 9999 accepted"
printf '%s' "$out" | grep -q 'names no migration' && ok "adopt --through 9999: refused" || bad "adopt --through 9999: $out"
out="$(run conc "$M" adopt --through "0001'; DROP TABLE x; --" --yes 2>&1)" && bad "adopt with a quoted --through accepted"
printf '%s' "$out" | grep -q 'not a migration number' && ok "adopt --through with SQL in it: refused" || bad "quoted --through: $out"
B="$T/bad"; mkdir -p "$B"; cp "$M/0001_first.up.sql" "$B/0001_x'y.up.sql"
newdb badname
out="$(run badname "$B" up 2>&1)" && bad "a migration named outside NNNN_name was applied"
printf '%s' "$out" | grep -q 'does not match' && [ "$(Q badname "SELECT to_regclass('public.first_t') IS NULL")" = t ] \
  && ok "migration name outside NNNN_name: refused, nothing applied" || bad "bad name: $out"

# ---- 6. adopt and the pre-ledger backfill
newdb adopt
Q adopt "CREATE TABLE first_t (id int); CREATE TABLE applied_log (v text);" >/dev/null
run adopt "$M" adopt --through 0001 --yes > "$T/adopt.log" 2>&1 \
  && [ "$(Q adopt "SELECT count(*) FROM schema_migrations WHERE version='0001_first' AND adopted AND checksum <> ''")" = 1 ] \
  && [ "$(Q adopt "SELECT count(*) FROM schema_migrations")" = 1 ] \
  && ok "adopt --through 0001 records exactly 0001, adopted, with its checksum" || { bad "adopt"; cat "$T/adopt.log"; }
Q adopt "UPDATE schema_migrations SET checksum = NULL, adopted = false" >/dev/null
out="$(run adopt "$M" status 2>&1)"
printf '%s' "$out" | grep -q '1 awaiting checksum backfill' && ok "status names a pre-ledger row" || bad "status: $(printf '%s' "$out" | tail -1)"
run adopt "$M" up > "$T/up2.log" 2>&1 \
  && [ "$(Q adopt "SELECT count(*) FROM schema_migrations WHERE version='0001_first' AND adopted AND checksum <> ''")" = 1 ] \
  && [ "$(Q adopt "SELECT count(*) FROM schema_migrations WHERE checksum <> ''")" = 5 ] \
  && ok "up backfills the pre-ledger checksum and applies the rest" || { bad "backfill"; tail -5 "$T/up2.log"; }

exit "$fail"
