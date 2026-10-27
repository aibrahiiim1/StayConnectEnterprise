#!/usr/bin/env bash
# Disposable proof for deploy/scripts/site-db-init.sh — the provisioner's database step.
#
# Runs against a throwaway TimescaleDB container (the appliance's pinned image) and never touches an appliance.
# Each case is a way a factory-clean install has actually gone wrong, or a way a retry could destroy data:
#
#   fresh            a blank database is built from the baseline and converges Gate-P
#   rerun            running it again changes nothing and does not re-apply the baseline (K2)
#   interrupted      a baseline that fails part-way leaves NO schema behind (single transaction), and the next
#                    run with the real baseline succeeds
#   stale            a baseline that does not embody the newest migration is refused before the database is
#                    touched (K1)
#   partial-empty    a half-built schema from an older, non-atomic install is refused, and rebuilt only with
#                    --reinitialise-unused because it holds no data
#   partial-data     a mismatched schema that HOLDS DATA is refused even with --reinitialise-unused
#   upgraded         a schema with migration-ledger rows is left to the upgrade procedure, never rebuilt
#
# Usage: scripts/ci/site-db-init-selftest.sh            (needs docker; ~3 minutes)
set -uo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd -P)"
TOOL="$ROOT/deploy/scripts/site-db-init.sh"
IMAGE="${SITEDB_SELFTEST_IMAGE:-timescale/timescaledb:2.16.1-pg16}"
C="sc-sitedb-selftest-$$"
pass=0; fail=0
ok()  { pass=$((pass+1)); printf '  PASS  %s\n' "$1"; }
bad() { fail=$((fail+1)); printf '  FAIL  %s\n' "$1"; }
W="$(mktemp -d)"
cleanup() { docker rm -f "$C" >/dev/null 2>&1; rm -rf "$W"; }
trap cleanup EXIT

docker run -d --name "$C" -e POSTGRES_USER=stayconnect -e POSTGRES_PASSWORD=selftest -e POSTGRES_DB=stayconnect \
  "$IMAGE" >/dev/null || { echo "cannot start $IMAGE"; exit 2; }
# initdb restarts the server once; wait for a server that answers twice in a row
up=0; for _ in $(seq 1 60); do
  if docker exec "$C" psql -U stayconnect -d stayconnect -tAc 'select 1' >/dev/null 2>&1; then up=$((up+1)); [ "$up" -ge 3 ] && break; else up=0; fi
  sleep 2
done
[ "$up" -ge 3 ] || { echo "container never became ready"; exit 2; }

q() { docker exec -i "$C" psql -X -tAq -U stayconnect -d "$1" -c "$2" </dev/null 2>/dev/null; }
init() { bash "$TOOL" --container "$C" --user stayconnect "$@" >"$W/out" 2>&1; echo $? > "$W/rc"; cat "$W/rc"; }

# A private copy of the tree, so cases can alter the baseline without touching the checkout.
mktree() { # <dir>
  mkdir -p "$1/deploy" "$1/scripts" "$1/data-plane"
  cp -r "$ROOT/deploy/gatep" "$ROOT/deploy/scripts" "$1/deploy/"
  cp "$ROOT/scripts/gatep-reconcile.sh" "$1/scripts/"
  cp -r "$ROOT/data-plane/migrations" "$1/data-plane/"
}

echo "== fresh"
rc="$(init --db site_fresh)"
t="$(q site_fresh "select count(*) from pg_tables where schemaname='iam_v2'")"
h="$(q site_fresh "select count(*) from timescaledb_information.hypertables")"
want="$(grep -cE '^CREATE (UNLOGGED )?TABLE iam_v2\.' "$ROOT/data-plane/migrations/baseline/0000_production_baseline.sql")"
if [ "$rc" = 0 ] && [ "$t" = "$want" ] && [ "$h" = 2 ]; then ok "blank database built ($t iam_v2 tables, $h hypertables)"; else bad "fresh rc=$rc tables=$t/$want hypertables=$h"; sed -n '1,30p' "$W/out"; fi
probe="$(docker exec -i "$C" psql -X -tAq -U stayconnect -d site_fresh -c "insert into public.audit_log(actor_type,action) values ('system','selftest.probe') returning 1" </dev/null 2>&1 | head -1)"
[ "$probe" = 1 ] && ok "audit_log hypertable accepts writes" || bad "audit_log insert: $probe"

echo "== rerun"
rc="$(init --db site_fresh)"
if [ "$rc" = 0 ] && grep -q 'not re-applied' "$W/out"; then ok "second run converges without re-applying the baseline"; else bad "rerun rc=$rc"; sed -n '1,20p' "$W/out"; fi

echo "== interrupted"
T1="$W/t1"; mktree "$T1"
printf '\nSELECT 1/0; -- selftest: fail after every object was created\n' >> "$T1/data-plane/migrations/baseline/0000_production_baseline.sql"
bash "$T1/deploy/scripts/site-db-init.sh" --container "$C" --user stayconnect --db site_interrupted >"$W/out" 2>&1; rc=$?
s="$(q site_interrupted "select count(*) from pg_namespace where nspname='iam_v2'")"
if [ "$rc" = 2 ] && [ "$s" = 0 ] && grep -q "the baseline did not apply" "$W/out"; then ok "a failing baseline leaves no schema behind (rc=2, iam_v2 absent)"; else bad "interrupted rc=$rc schema=$s"; sed -n '1,20p' "$W/out"; fi
rc="$(init --db site_interrupted)"
[ "$rc" = 0 ] && ok "the next run with the real baseline completes" || { bad "retry after interruption rc=$rc"; sed -n '1,20p' "$W/out"; }

echo "== stale"
T2="$W/t2"; mktree "$T2"
sed -i 's#data-plane/migrations/0001\.\.[0-9]\{4\}#data-plane/migrations/0001..0001#' "$T2/data-plane/migrations/baseline/0000_production_baseline.sql"
bash "$T2/deploy/scripts/site-db-init.sh" --container "$C" --user stayconnect --db site_stale >"$W/out" 2>&1; rc=$?
e="$(q postgres "select count(*) from pg_database where datname='site_stale'")"
if [ "$rc" = 1 ] && [ "$e" = 0 ] && grep -q 'regenerate' "$W/out"; then ok "a stale baseline is refused before any database is created"; else bad "stale rc=$rc db_exists=$e"; sed -n '1,10p' "$W/out"; fi

echo "== partial-empty"
q postgres "create database site_partial" >/dev/null
q site_partial "create schema iam_v2; create table iam_v2.leftover(x int)" >/dev/null
rc="$(init --db site_partial)"
l="$(q site_partial "select count(*) from pg_tables where schemaname='iam_v2'")"
if [ "$rc" = 1 ] && [ "$l" = 1 ]; then ok "a half-built schema is refused and left untouched without the flag"; else bad "partial rc=$rc tables=$l"; sed -n '1,12p' "$W/out"; fi
rc="$(init --db site_partial --reinitialise-unused)"
l="$(q site_partial "select count(*) from pg_tables where schemaname='iam_v2'")"
if [ "$rc" = 0 ] && [ "$l" = "$want" ]; then ok "--reinitialise-unused rebuilds a half-built schema that holds no data"; else bad "reinit rc=$rc tables=$l"; sed -n '1,20p' "$W/out"; fi

echo "== partial-data"
q postgres "create database site_data" >/dev/null
q site_data "create schema iam_v2; create table iam_v2.sessions(id int); insert into iam_v2.sessions values (1)" >/dev/null
rc="$(init --db site_data --reinitialise-unused)"
r="$(q site_data "select count(*) from iam_v2.sessions")"
if [ "$rc" = 1 ] && [ "$r" = 1 ] && grep -q 'HOLDS' "$W/out"; then ok "a mismatched schema holding data is never rebuilt, even with --reinitialise-unused"; else bad "data rc=$rc rows=$r"; sed -n '1,12p' "$W/out"; fi

echo "== upgraded"
q site_fresh "insert into public.schema_migrations(version) values ('9999_selftest_upgrade_marker')" >/dev/null
rc="$(init --db site_fresh)"
if [ "$rc" = 0 ] && grep -q 'live, upgraded database' "$W/out"; then ok "a database with ledger rows is treated as upgraded, never rebuilt"; else bad "upgraded rc=$rc"; sed -n '1,12p' "$W/out"; fi

echo
echo "site-db-init selftest: $pass passed, $fail failed"
[ "$fail" = 0 ]
