#!/usr/bin/env bash
# Disposable proof for deploy/scripts/apply-pending-migrations.sh — the upgrade path of a FACTORY-CLEAN appliance.
#
# A database built from the baseline has an empty migration ledger. The first migration published after that
# baseline must still go through the authoritative runner in LIVE-SITE mode (positive identity, sha256,
# iam_v2_owner executor, one ledger row) — not a disposable-mode shortcut. This proves it on a throwaway
# TimescaleDB container: build the site database with site-db-init.sh, publish a synthetic next migration in a
# private copy of the tree, and upgrade.
#
#   no-marker   an empty ledger with no recorded baseline is refused (nothing guessed)
#   list        the pending set is exactly the migrations above the recorded baseline
#   apply       the next migration applies in live-site mode, records one ledger row, Gate-P re-converges
#   again       a second run finds nothing pending and changes nothing
#   no-backup   a failing pre-change backup refuses with exit 4 and applies nothing
set -uo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd -P)"
IMAGE="${SITEDB_SELFTEST_IMAGE:-timescale/timescaledb:2.16.1-pg16}"
C="${APPLY_PENDING_SELFTEST_CONTAINER:-sc-migrate-selftest-$$}"
pass=0; fail=0
ok()  { pass=$((pass+1)); printf '  PASS  %s\n' "$1"; }
bad() { fail=$((fail+1)); printf '  FAIL  %s\n' "$1"; }
W="$(mktemp -d)"
cleanup() { docker rm -f -v "$C" >/dev/null 2>&1; rm -rf "$W"; }
trap cleanup EXIT
docker run -d --name "$C" -e POSTGRES_USER=stayconnect -e POSTGRES_PASSWORD=selftest -e POSTGRES_DB=stayconnect "$IMAGE" >/dev/null || exit 2
up=0; for _ in $(seq 1 60); do
  if docker exec "$C" psql -U stayconnect -d stayconnect -tAc 'select 1' >/dev/null 2>&1; then up=$((up+1)); [ "$up" -ge 3 ] && break; else up=0; fi
  sleep 2
done
[ "$up" -ge 3 ] || { echo "container never became ready"; exit 2; }
q() { docker exec -i "$C" psql -X -tAq -U stayconnect -d stayconnect_site -c "$1" </dev/null 2>/dev/null; }

bash "$ROOT/deploy/scripts/site-db-init.sh" --container "$C" --user stayconnect >"$W/out" 2>&1 || { echo "site-db-init failed"; cat "$W/out"; exit 2; }
THROUGH="$(. "$ROOT/deploy/scripts/lib-baseline.sh"; baseline_through "$ROOT/data-plane/migrations/baseline/0000_production_baseline.sql")"
NEXT="$(printf '%04d' $((10#$THROUGH + 1)))_selftest_next_migration"

# A private tree that publishes one more migration than the baseline embodies.
T="$W/tree"; mkdir -p "$T/deploy" "$T/scripts" "$T/data-plane"
cp -r "$ROOT/deploy/gatep" "$ROOT/deploy/scripts" "$T/deploy/"
cp "$ROOT/scripts/edge-migrate.sh" "$ROOT/scripts/gatep-reconcile.sh" "$T/scripts/"
cp -r "$ROOT/data-plane/migrations" "$T/data-plane/"
cat > "$T/data-plane/migrations/$NEXT.up.sql" <<'SQL'
BEGIN;
CREATE TABLE iam_v2.selftest_next_migration (id integer PRIMARY KEY);
COMMIT;
SQL
printf 'BEGIN;\nDROP TABLE iam_v2.selftest_next_migration;\nCOMMIT;\n' > "$T/data-plane/migrations/$NEXT.down.sql"
MARK="$W/schema-baseline"
run() { SC_SCHEMA_BASELINE_FILE="$MARK" bash "$T/deploy/scripts/apply-pending-migrations.sh" --root "$T" --container "$C" --user stayconnect "$@" >"$W/out" 2>&1; echo $?; }

echo "== no-marker"
rc="$(run --list)"
[ "$rc" = 1 ] && grep -q 'does not say which baseline' "$W/out" && ok "an empty ledger with no recorded baseline is refused" || { bad "no-marker rc=$rc"; cat "$W/out"; }

echo "$THROUGH" > "$MARK"
echo "== list"
rc="$(run --list)"
if [ "$rc" = 0 ] && grep -q "pending: 1" "$W/out" && grep -q "$NEXT" "$W/out"; then ok "exactly the migration above baseline $THROUGH is pending"; else bad "list rc=$rc"; cat "$W/out"; fi

echo "== no-backup"
rc="$(EDGE_PRECHANGE_BACKUP=false run)"
t="$(q "select to_regclass('iam_v2.selftest_next_migration') is not null")"
if [ "$rc" = 4 ] && [ "$t" = f ]; then ok "a failing pre-change backup refuses (exit 4) and applies nothing"; else bad "no-backup rc=$rc table=$t"; tail -20 "$W/out"; fi

echo "== apply"
rc="$(EDGE_PRECHANGE_BACKUP=true run)"
t="$(q "select to_regclass('iam_v2.selftest_next_migration') is not null")"
l="$(q "select count(*) from public.schema_migrations where version='$NEXT'")"
o="$(q "select r.rolname from pg_class c join pg_roles r on r.oid=c.relowner where c.oid='iam_v2.selftest_next_migration'::regclass")"
if [ "$rc" = 0 ] && [ "$t" = t ] && [ "$l" = 1 ] && [ "$o" = iam_v2_owner ] && grep -q 'GATEP_RECONCILE = OK' "$W/out" && grep -q 'live-site\|LIVE' "$W/out"; then
  ok "the next migration applies in live-site mode as iam_v2_owner, one ledger row, Gate-P reconciled"
else bad "apply rc=$rc table=$t ledger=$l owner=$o"; tail -30 "$W/out"; fi

echo "== again"
rc="$(EDGE_PRECHANGE_BACKUP=true run)"
if [ "$rc" = 0 ] && grep -q 'pending: 0' "$W/out"; then ok "a second run finds nothing pending"; else bad "again rc=$rc"; tail -10 "$W/out"; fi

echo
echo "apply-pending-migrations selftest: $pass passed, $fail failed"
[ "$fail" = 0 ]
