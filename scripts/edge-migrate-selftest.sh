#!/usr/bin/env bash
# A MIGRATION RUNNER THAT CANNOT RUN ON A SUPPORTED INSTALLATION IS NOT A GUARD.
#
# edge-migrate.sh refused every factory-clean appliance. Its baseline precondition read the ledger for
# 0009_phase2_commerce — an event that, on a baseline install, correctly never happens: such an appliance is
# built from a dump of the upgrade path's END STATE, so 0001..0049 are never applied individually and are not
# in its ledger. The authoritative runner therefore could not apply a migration to the very appliances the
# baseline exists to produce, and 0061 had to go around it during a live window.
#
# These cases pin the fix AND pin that nothing else was spent to buy it: the upgrade path still demands its
# predecessor evidence, and the checksum, target-identity, least-privilege, advisory-lock and ledger
# protections all still fail closed.
#
# DISPOSABLE PostgreSQL only. No appliance, no live database, no repository migration is applied to anything
# that outlives this script.
set -uo pipefail
export PATH="$PATH:/c/Program Files/Docker/Docker/resources/bin"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
RUNNER="$ROOT/scripts/edge-migrate.sh"
C="edge-migrate-selftest-$$"; DB="edge_selftest"; PORT="${EDGE_SELFTEST_PORT:-55461}"
WORK="$(mktemp -d)"
pass=0; fail=0
ok(){ echo "  ok: $1"; pass=$((pass+1)); }
no(){ echo "  *** FAIL: $1"; [ -n "${2:-}" ] && printf '%s\n' "$2" | sed 's/^/        /' | head -6; fail=1; }
cleanup(){ docker rm -f "$C" >/dev/null 2>&1 || true; rm -rf "$WORK"; }
trap cleanup EXIT

# ---------------------------------------------------------------------------------------------------------
# disposable database
# ---------------------------------------------------------------------------------------------------------
docker rm -f "$C" >/dev/null 2>&1 || true
docker run -d --name "$C" -e POSTGRES_PASSWORD=postgres -e POSTGRES_DB="$DB" \
  -p 127.0.0.1:$PORT:5432 postgres:16-alpine >/dev/null 2>&1 || { echo "INFRA: container"; exit 2; }
ready=0
for _ in $(seq 1 60); do docker exec "$C" psql -U postgres -d "$DB" -tAqc 'select 1' >/dev/null 2>&1 && { ready=1; break; }; sleep 1; done
[ "$ready" = 1 ] || { echo "INFRA: postgres never became ready"; exit 2; }

PSQL="docker exec -i $C psql -U postgres -d $DB"
Q(){ docker exec "$C" psql -U postgres -d "$DB" -tAqc "$1" 2>&1; }

# A least-privilege apply role, because live-site mode requires a non-superuser holding exactly SELECT+INSERT
# on the ledger and no public CREATE. Building it here is what makes the least-privilege assertions real
# rather than skipped.
Q "CREATE SCHEMA IF NOT EXISTS iam_v2;" >/dev/null
Q "CREATE TABLE IF NOT EXISTS public.schema_migrations(version text PRIMARY KEY NOT NULL,
     applied_at timestamptz NOT NULL DEFAULT now());" >/dev/null
Q "DO \$\$ BEGIN IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='edge_apply') THEN
     CREATE ROLE edge_apply NOLOGIN; END IF; END \$\$;" >/dev/null
Q "REVOKE CREATE ON SCHEMA public FROM edge_apply, PUBLIC;
   GRANT USAGE ON SCHEMA public, iam_v2 TO edge_apply;
   GRANT CREATE ON SCHEMA iam_v2 TO edge_apply;
   GRANT SELECT, INSERT ON public.schema_migrations TO edge_apply;" >/dev/null

# 0009's own artefacts: the two writers it creates. Their presence is the schema-side evidence the fixed
# precondition accepts in place of a ledger row.
mk_commerce(){
  Q "CREATE OR REPLACE FUNCTION iam_v2.trg_purchase_quote_pin_equal() RETURNS trigger
       LANGUAGE plpgsql AS \$f\$ BEGIN RETURN NEW; END \$f\$;
     CREATE OR REPLACE FUNCTION iam_v2.trg_offer_quote_immutable() RETURNS trigger
       LANGUAGE plpgsql AS \$f\$ BEGIN RETURN NEW; END \$f\$;" >/dev/null
}
rm_commerce(){
  Q "DROP FUNCTION IF EXISTS iam_v2.trg_purchase_quote_pin_equal();
     DROP FUNCTION IF EXISTS iam_v2.trg_offer_quote_immutable();" >/dev/null
}
ledger_reset(){ Q "TRUNCATE public.schema_migrations;" >/dev/null; }
ledger_add(){ Q "INSERT INTO public.schema_migrations(version) VALUES ('$1') ON CONFLICT DO NOTHING;" >/dev/null; }

# A disposable migration of our own. The runner is never pointed at the repository's real migrations here:
# this suite proves the RUNNER, and a suite that applied real migrations would be a second, unreviewed
# install path.
MIGDIR="$WORK/data-plane/migrations"; mkdir -p "$MIGDIR"
cat > "$MIGDIR/0099_selftest_noop.up.sql" <<'SQL'
BEGIN;
CREATE TABLE IF NOT EXISTS iam_v2.edge_selftest_marker(id int PRIMARY KEY);
COMMIT;
SQL
SHA="$(sha256sum "$MIGDIR/0099_selftest_noop.up.sql" | awk '{print $1}')"
# The runner resolves its migration directory from its own location, so it is copied beside the fake tree.
mkdir -p "$WORK/scripts"; cp "$RUNNER" "$WORK/scripts/edge-migrate.sh"
RUN="$WORK/scripts/edge-migrate.sh"

run_apply(){ # run_apply <extra args...> ; always live-site, always the disposable migration
  EDGE_PSQL="$PSQL" bash "$RUN" --apply-role edge_apply --only 0099_selftest_noop \
    --expect-db "$DB" --target-kind live-site --ack-target I_UNDERSTAND_LIVE_DARK_SITE_MIGRATION \
    --expect-sha256 "$SHA" "$@" 2>&1
}
applied(){ [ "$(Q "SELECT count(*) FROM public.schema_migrations WHERE version='0099_selftest_noop'")" = 1 ]; }
undo(){ Q "DELETE FROM public.schema_migrations WHERE version='0099_selftest_noop';
           DROP TABLE IF EXISTS iam_v2.edge_selftest_marker;" >/dev/null; }

echo "== 1. a FACTORY-CLEAN baseline can apply its next migration =="
ledger_reset; mk_commerce
out="$(run_apply)"
if applied; then ok "a factory-clean install applies its next migration through the authoritative runner"
else no "the runner still refuses a factory-clean baseline" "$out"; fi
undo

echo "== 2. an UPGRADE-PATH install still needs its predecessor evidence =="
# The ledger holds an early migration but NOT 0009: a genuine gap, and still refused even though the
# commerce structures happen to be present.
ledger_reset; ledger_add 0007_auth_throttle_buckets; mk_commerce
out="$(run_apply)"
if applied; then no "an upgrade-path install missing 0009 was allowed to proceed" "$out"
else
  case "$out" in
    *"must be applied before"*) ok "an upgrade-path install with a 0009 gap is REFUSED" ;;
    *) no "refused, but not for the missing predecessor" "$out" ;;
  esac
fi
undo

echo "== 2b. ...and the normal upgrade path still works when 0009 IS recorded =="
ledger_reset; ledger_add 0007_auth_throttle_buckets; ledger_add 0009_phase2_commerce; rm_commerce
out="$(run_apply)"
if applied; then ok "an upgrade-path install with 0009 recorded applies normally"
else no "the ordinary upgrade path broke" "$out"; fi
undo

echo "== 2c. neither ledger row nor structures = refused =="
ledger_reset; rm_commerce
out="$(run_apply)"
if applied; then no "a database with no commerce baseline at all was accepted" "$out"
else
  case "$out" in
    *"commerce structures"*|*"must be applied before"*) ok "a database with no commerce baseline is REFUSED" ;;
    *) no "refused, but not for the absent baseline" "$out" ;;
  esac
fi
undo; mk_commerce; ledger_reset

echo "== 3. CHECKSUM is still fail-closed =="
out="$(EDGE_PSQL="$PSQL" bash "$RUN" --apply-role edge_apply --only 0099_selftest_noop \
        --expect-db "$DB" --target-kind live-site --ack-target I_UNDERSTAND_LIVE_DARK_SITE_MIGRATION \
        --expect-sha256 0000000000000000000000000000000000000000000000000000000000000000 2>&1)"
if applied; then no "a wrong --expect-sha256 still applied the migration" "$out"
else case "$out" in *"checksum mismatch"*) ok "a checksum mismatch is REFUSED" ;;
                    *) no "refused, but not for the checksum" "$out" ;; esac; fi
undo

echo "== 4. TARGET IDENTITY is still fail-closed =="
out="$(EDGE_PSQL="$PSQL" bash "$RUN" --apply-role edge_apply --only 0099_selftest_noop \
        --expect-db not_this_database --target-kind live-site \
        --ack-target I_UNDERSTAND_LIVE_DARK_SITE_MIGRATION --expect-sha256 "$SHA" 2>&1)"
if applied; then no "a wrong --expect-db still applied the migration" "$out"
else case "$out" in *"--expect-db"*|*"connected to"*) ok "a mismatched target database is REFUSED" ;;
                    *) no "refused, but not for the target identity" "$out" ;; esac; fi
undo

out="$(EDGE_PSQL="$PSQL" bash "$RUN" --apply-role edge_apply --only 0099_selftest_noop \
        --expect-db "$DB" --target-kind live-site --ack-target WRONG_ACKNOWLEDGEMENT \
        --expect-sha256 "$SHA" 2>&1)"
if applied; then no "a wrong acknowledgement still applied the migration" "$out"
else ok "a wrong --ack-target is REFUSED"; fi
undo

echo "== 5. LEAST PRIVILEGE is still fail-closed =="
# A superuser executor is refused for a live site, whatever else is correct.
out="$(EDGE_PSQL="$PSQL" bash "$RUN" --only 0099_selftest_noop \
        --expect-db "$DB" --target-kind live-site --ack-target I_UNDERSTAND_LIVE_DARK_SITE_MIGRATION \
        --expect-sha256 "$SHA" 2>&1)"
if applied; then no "a superuser applied a live-site migration" "$out"
else case "$out" in *"NON-superuser"*) ok "a superuser executor is REFUSED for a live site" ;;
                    *) no "refused, but not for superuser" "$out" ;; esac; fi
undo

# ...and an apply role holding destructive ledger rights is refused too.
Q "GRANT DELETE ON public.schema_migrations TO edge_apply;" >/dev/null
out="$(run_apply)"
if applied; then no "an apply role holding DELETE on the ledger was accepted" "$out"
else case "$out" in *"must NOT hold DELETE"*) ok "an apply role with destructive ledger rights is REFUSED" ;;
                    *) no "refused, but not for the ledger rights" "$out" ;; esac; fi
Q "REVOKE DELETE ON public.schema_migrations FROM edge_apply;" >/dev/null
undo

echo "== 6. LEDGER protections are still fail-closed =="
# A ledger whose version column is not the primary key is not a ledger.
Q "ALTER TABLE public.schema_migrations DROP CONSTRAINT schema_migrations_pkey;" >/dev/null
out="$(run_apply)"
if applied; then no "a ledger with no primary key was accepted" "$out"
else case "$out" in *"PRIMARY KEY"*) ok "a ledger without its primary key is REFUSED" ;;
                    *) no "refused, but not for the ledger structure" "$out" ;; esac; fi
Q "ALTER TABLE public.schema_migrations ADD PRIMARY KEY (version);" >/dev/null
undo

# Re-applying an already-recorded version records nothing twice.
ledger_reset; mk_commerce
run_apply >/dev/null
first="$(Q "SELECT count(*) FROM public.schema_migrations WHERE version='0099_selftest_noop'")"
run_apply >/dev/null
second="$(Q "SELECT count(*) FROM public.schema_migrations WHERE version='0099_selftest_noop'")"
if [ "$first" = "1" ] && [ "$second" = "1" ]; then ok "re-applying an applied version leaves exactly one ledger row"
else no "ledger rows went $first -> $second on re-apply"; fi
undo

echo "== 7. THE ADVISORY LOCK is still taken =="
# The runner derives its key as hashtextextended('stayconnect_edge_migrate:'||version, 0). Holding THAT key
# in another session must make the apply wait rather than proceed concurrently, which is the property the
# lock exists for: two operators cannot apply the same migration at once.
LOCKKEY="$(Q "SELECT hashtextextended('stayconnect_edge_migrate:'||'0099_selftest_noop', 0)")"
ledger_reset; mk_commerce
docker exec -d "$C" psql -U postgres -d "$DB" -c   "SELECT pg_advisory_lock($LOCKKEY); SELECT pg_sleep(20);" >/dev/null 2>&1
sleep 2
held="$(Q "SELECT count(*) FROM pg_locks WHERE locktype='advisory'")"
if [ "${held:-0}" -lt 1 ]; then
  no "the selftest could not hold the advisory lock, so this case proves nothing"
else
  start=$(date +%s)
  out="$(run_apply)"
  elapsed=$(( $(date +%s) - start ))
  if applied && [ "$elapsed" -ge 10 ]; then
    ok "an apply WAITS for a held migration lock (${elapsed}s) instead of running concurrently"
  elif applied; then
    no "the apply completed in ${elapsed}s while the migration lock was held: it did not wait" "$out"
  else
    ok "an apply refuses while the migration lock is held"
  fi
fi
undo

echo "============================================================"
if [ "$fail" = "0" ]; then echo "EDGE_MIGRATE_SELFTEST = PASS ($pass cases)"; exit 0; fi
echo "EDGE_MIGRATE_SELFTEST = FAIL"; exit 1
