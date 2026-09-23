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
# THE DISPOSABLE DATABASE IS NAMED stayconnect_site ON PURPOSE.
#
# live-site mode - the mode that carries the least-privilege, superuser-refusal and destructive-ledger-rights
# checks this suite exists to prove - requires --expect-db stayconnect_site. Testing in any other mode would
# skip exactly the assertions that matter. The database lives inside a container this script creates with a
# unique name, on its own port, and destroys in its EXIT trap; EDGE_PSQL names that container explicitly, so
# nothing here can reach an appliance.
C="edge-migrate-selftest-$$"; DB="stayconnect_site"; PORT="${EDGE_SELFTEST_PORT:-55461}"
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
else case "$out" in *"--expect-db"*|*"connected to"*|*"requires --expect-db"*) ok "a mismatched target database is REFUSED" ;;
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

echo "== 8. A MIGRATION THAT FAILS IS NOT REPORTED AS APPLIED =="
# The case this check was added for, and it is not hypothetical: the runner used to decide the outcome by
# grepping its own pre-apply echo out of psql's output. APPLYING_UNDER_LOCK is printed BEFORE the migration
# body runs, so a migration that aborted under ON_ERROR_STOP and rolled back -- taking the ledger INSERT with
# it -- still produced EDGE_MIGRATE_OK applied=1 over a database the runner had not changed. A deployment
# that trusted it would carry on to restart daemons against an unmigrated schema. That is what happened with
# 0069 against a live appliance, where the applying role could not create in the target schema.
#
# The ledger row is written in the same transaction as the migration, so it exists if and only if the
# migration committed. Here a deliberately broken migration must be REFUSED, not reported as applied.
ledger_reset; mk_commerce
cat > "$MIGDIR/0098_selftest_fails.up.sql" <<'SQL'
BEGIN;
CREATE TABLE public.selftest_should_not_survive (id int);
SELECT 1 FROM this_relation_does_not_exist;
COMMIT;
SQL
BADSHA="$(sha256sum "$MIGDIR/0098_selftest_fails.up.sql" | awk '{print $1}')"
badout="$(EDGE_PSQL="$PSQL" bash "$RUN" --apply-role edge_apply --only 0098_selftest_fails   --expect-db "$DB" --target-kind live-site --ack-target I_UNDERSTAND_LIVE_DARK_SITE_MIGRATION   --expect-sha256 "$BADSHA" 2>&1)"; badrc=$?
badledger="$(Q "SELECT count(*) FROM public.schema_migrations WHERE version='0098_selftest_fails'")"
badtable="$(Q "SELECT count(*) FROM pg_class WHERE relname='selftest_should_not_survive'")"
if [ "$badrc" != "0" ] && ! echo "$badout" | grep -q "EDGE_MIGRATE_OK"; then
  ok "a failing migration is refused, not reported as applied"
else
  no "a failing migration was reported as applied (rc=$badrc)" "$badout"
fi
if [ "${badledger:-0}" = "0" ] && [ "${badtable:-0}" = "0" ]; then
  ok "...and it left neither a ledger row nor a half-created object"
else
  no "a failed migration left ledger=$badledger object=$badtable behind"
fi
Q "DELETE FROM public.schema_migrations WHERE version='0098_selftest_fails';" >/dev/null
rm -f "$MIGDIR/0098_selftest_fails.up.sql"
undo

# =========================================================================================================
# DOWN MODE. There was no --down: the Phase-3 rollback runbook printed the command and the runner answered
# `REFUSED: unknown arg: --down`, so the documented live rollback was unexecutable and the real one was a
# hand-run psql outside every guard in this file. These cases pin the mode AND pin that it did not buy its
# existence by weakening anything.
# =========================================================================================================
echo "== 7. down mode =="
ledger_reset; mk_commerce
cat > "$MIGDIR/0099_selftest_noop.down.sql" <<'SQL'
BEGIN;
DROP TABLE IF EXISTS iam_v2.edge_selftest_marker;
COMMIT;
SQL
DSHA="$(sha256sum "$MIGDIR/0099_selftest_noop.down.sql" | awk '{print $1}')"
ZEROSHA="0000000000000000000000000000000000000000000000000000000000000000"

# The rollback/admin role is a DIFFERENT role from the forward apply role, which is exactly the point: the
# forward path REFUSES DELETE on the ledger for a live site, so it cannot roll back.
Q "DO \$r\$ BEGIN IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='edge_rollback') THEN
     CREATE ROLE edge_rollback NOLOGIN; END IF; END \$r\$;" >/dev/null
Q "REVOKE CREATE ON SCHEMA public FROM edge_rollback;
   GRANT USAGE ON SCHEMA public, iam_v2 TO edge_rollback;
   GRANT CREATE ON SCHEMA iam_v2 TO edge_rollback;
   GRANT SELECT, DELETE ON public.schema_migrations TO edge_rollback;" >/dev/null
# THE ROLLBACK ROLE MUST BE ABLE TO ADMINISTER WHAT THE APPLY ROLE CREATED, and modelling that is part of
# modelling the operation. The forward apply creates its objects under SET ROLE edge_apply, so they are
# owned by edge_apply; a DROP by a non-owner fails with 'must be owner of table'. On the appliance the
# equivalent is that the rollback/admin role is the iam_v2 owner or a member of it.
#
# THIS WAS FOUND BY THIS SUITE FAILING, and the failure was worth having: the runner refused, reported
# psql exit 3, and left BOTH the object and the ledger row intact -- which is case 7j's property arriving
# unprompted in case 7a.
Q "GRANT edge_apply TO edge_rollback;" >/dev/null

run_down(){ # run_down <extra args...> ; always live-site, always the disposable migration
  EDGE_PSQL="$PSQL" bash "$RUN" --down --apply-role edge_rollback --only 0099_selftest_noop \
    --expect-db "$DB" --target-kind live-site --ack-target I_UNDERSTAND_LIVE_DARK_SITE_MIGRATION \
    --expect-sha256 "$DSHA" "$@" 2>&1
}
marker(){ Q "SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='iam_v2' AND c.relname='edge_selftest_marker'"; }

# 7a. The happy path: apply, then roll back, and BOTH the object and the ledger row must go.
out="$(run_apply)"
if applied && [ "$(marker)" = "1" ]; then ok "down fixture: the migration applied and its object exists"
else no "down fixture did not apply" "$out"; fi
out="$(run_down --ack-down I_UNDERSTAND_LIVE_SITE_DOWN_MIGRATION)"
if echo "$out" | grep -q "EDGE_MIGRATE_DOWN_OK reverted=1" && ! applied && [ "$(marker)" = "0" ]; then
  ok "a down migration removes BOTH its object and its ledger row"
else no "the down did not complete (ledger/object still present)" "$out"; fi

# 7b. FORWARD RECOVERY. A rollback that cannot be re-applied is a one-way door.
out="$(run_apply)"
if applied && [ "$(marker)" = "1" ]; then ok "...and the same migration re-applies afterwards"
else no "the migration did not re-apply after its down" "$out"; fi

# 7c. The down-specific acknowledgement is REQUIRED; --ack-target alone must not be enough.
out="$(run_down)"
case "$out" in
  *"requires --ack-down I_UNDERSTAND_LIVE_SITE_DOWN_MIGRATION"*) ok "a down without --ack-down is refused" ;;
  *) no "a down without --ack-down was not refused for the right reason" "$out" ;;
esac
out="$(run_down --ack-down I_UNDERSTAND_DISPOSABLE_DOWN_MIGRATION)"
case "$out" in
  *"requires --ack-down I_UNDERSTAND_LIVE_SITE_DOWN_MIGRATION"*) ok "the disposable ack does not satisfy a live-site down" ;;
  *) no "the wrong-kind ack was accepted or refused wrongly" "$out" ;;
esac

# 7d. HEAD OF LEDGER. Rolling back out of order leaves later migrations standing on dropped objects.
ledger_add 0100_selftest_later
out="$(run_down --ack-down I_UNDERSTAND_LIVE_SITE_DOWN_MIGRATION)"
case "$out" in
  *"is not the head of the ledger"*) ok "a non-head down is refused, and the message names the head" ;;
  *) no "a non-head down was not refused for the right reason" "$out" ;;
esac
Q "DELETE FROM public.schema_migrations WHERE version='0100_selftest_later';" >/dev/null

# 7e. The checksum is mandatory and binding for the DOWN file too.
out="$(EDGE_PSQL="$PSQL" bash "$RUN" --down --apply-role edge_rollback --only 0099_selftest_noop \
  --expect-db "$DB" --target-kind live-site --ack-target I_UNDERSTAND_LIVE_DARK_SITE_MIGRATION \
  --ack-down I_UNDERSTAND_LIVE_SITE_DOWN_MIGRATION 2>&1)"
case "$out" in
  *"--expect-sha256 is mandatory for a down-migration"*) ok "a down without --expect-sha256 is refused" ;;
  *) no "a down without a checksum was not refused" "$out" ;;
esac
out="$(EDGE_PSQL="$PSQL" bash "$RUN" --down --apply-role edge_rollback --only 0099_selftest_noop \
  --expect-db "$DB" --target-kind live-site --ack-target I_UNDERSTAND_LIVE_DARK_SITE_MIGRATION \
  --ack-down I_UNDERSTAND_LIVE_SITE_DOWN_MIGRATION --expect-sha256 "$ZEROSHA" 2>&1)"
case "$out" in
  *"checksum mismatch"*) ok "a down with the wrong checksum is refused" ;;
  *) no "a wrong down checksum was accepted" "$out" ;;
esac

# 7f. THE PRIVILEGE MIRROR. The forward apply role is refused DELETE on the ledger for a live site, so it
# must be unable to roll back -- and the refusal has to name the remedy and say it is not the same role.
out="$(EDGE_PSQL="$PSQL" bash "$RUN" --down --apply-role edge_apply --only 0099_selftest_noop \
  --expect-db "$DB" --target-kind live-site --ack-target I_UNDERSTAND_LIVE_DARK_SITE_MIGRATION \
  --ack-down I_UNDERSTAND_LIVE_SITE_DOWN_MIGRATION --expect-sha256 "$DSHA" 2>&1)"
case "$out" in
  *"lacks required DELETE on public.schema_migrations"*)
    case "$out" in
      *"NOT the forward apply role"*) ok "the forward apply role cannot roll back, and the refusal says so" ;;
      *) no "refused for DELETE but did not explain it is the wrong role" "$out" ;;
    esac ;;
  *) no "the forward apply role was allowed to roll back, or refused wrongly" "$out" ;;
esac

# 7g. A down for a migration that is NOT applied has nothing to undo.
Q "DELETE FROM public.schema_migrations WHERE version='0099_selftest_noop';" >/dev/null
out="$(run_down --ack-down I_UNDERSTAND_LIVE_SITE_DOWN_MIGRATION)"
case "$out" in
  *"is not applied to this database"*) ok "a down for an unapplied migration is refused" ;;
  *) no "a down for an unapplied migration was not refused" "$out" ;;
esac

# 7h. --all and --down cannot be combined: a down sweep is a schema deletion with a loop around it.
out="$(EDGE_PSQL="$PSQL" bash "$RUN" --down --all --apply-role edge_rollback \
  --expect-db "$DB" --target-kind live-site --ack-target I_UNDERSTAND_LIVE_DARK_SITE_MIGRATION \
  --ack-down I_UNDERSTAND_LIVE_SITE_DOWN_MIGRATION 2>&1)"
case "$out" in
  *"--all cannot be combined with --down"*) ok "a down sweep is refused" ;;
  *) no "--down --all was not refused" "$out" ;;
esac

# 7i. --ack-down is meaningless without --down. Accepting and discarding a flag is the defect this project
# keeps finding, so it is refused rather than ignored.
out="$(run_apply --ack-down I_UNDERSTAND_LIVE_SITE_DOWN_MIGRATION)"
case "$out" in
  *"--ack-down is only meaningful with --down"*) ok "--ack-down without --down is refused, not ignored" ;;
  *) no "--ack-down was accepted on a forward apply" "$out" ;;
esac

# 7j. A FAILING down must not remove the ledger row. Without forced ON_ERROR_STOP the body's COMMIT degrades
# to ROLLBACK -- the objects survive -- and the appended DELETE then commits on its own, leaving a database
# whose objects the ledger denies. The next forward apply would try to create what is already there.
ledger_reset; mk_commerce
out="$(run_apply)"
cat > "$MIGDIR/0099_selftest_noop.down.sql" <<'SQL'
BEGIN;
DROP TABLE IF EXISTS iam_v2.edge_selftest_marker;
SELECT 1 FROM this_relation_does_not_exist;
COMMIT;
SQL
BADDSHA="$(sha256sum "$MIGDIR/0099_selftest_noop.down.sql" | awk '{print $1}')"
out="$(EDGE_PSQL="$PSQL" bash "$RUN" --down --apply-role edge_rollback --only 0099_selftest_noop \
  --expect-db "$DB" --target-kind live-site --ack-target I_UNDERSTAND_LIVE_DARK_SITE_MIGRATION \
  --ack-down I_UNDERSTAND_LIVE_SITE_DOWN_MIGRATION --expect-sha256 "$BADDSHA" 2>&1)"; drc=$?
if [ "$drc" != "0" ] && ! echo "$out" | grep -q "EDGE_MIGRATE_DOWN_OK"; then
  ok "a failing down is refused, not reported as reverted"
else no "a failing down was reported as reverted (rc=$drc)" "$out"; fi
if applied && [ "$(marker)" = "1" ]; then
  ok "...and it left the ledger row AND the object intact"
else no "a failed down removed the ledger row or the object" "$out"; fi
rm -f "$MIGDIR/0099_selftest_noop.down.sql"
undo

# ---------------------------------------------------------------------------------------------------------
# 8. THE LEDGER LOCK -- the race the head-of-ledger check could not close on its own
# ---------------------------------------------------------------------------------------------------------
# The down path refuses anything but the head. That check ran BEFORE the advisory lock, and the lock key was
# derived from the version being reverted -- so a forward apply of a HIGHER version took a different key,
# was never blocked, and could commit in between. The rollback then removed a migration that was no longer
# the head. Both directions now take one ledger-wide key first.

# ---------------------------------------------------------------------------------------------------------
# 7k. THE RUNNER MUST EXECUTE THE FILE IT VERIFIED, even when that file is not the last one in the directory
# ---------------------------------------------------------------------------------------------------------
# This suite could not have caught the bug it now tests for. Every apply case above names 0099_selftest_noop,
# which is the alphabetically LAST .up.sql in the fixture directory -- so a runner that executed "the last
# file" instead of "the selected file" would pass all of them.
#
# The bug was real and it reached a live appliance: a helper function iterated `for f in ...` without
# declaring f, bash is dynamically scoped, and the caller's f was overwritten -- so an apply of 0087 read
# 0088's file, which was already applied and whose body is CREATE OR REPLACE. psql succeeded, the ledger row
# for 0087 was written, and none of 0087's objects existed. The checksum guard did not help, because the sha
# was compared BEFORE the helper ran: the runner verified one file and executed another.
#
# So this case applies a migration that is NOT the last file, and asserts the object THAT migration creates
# is the one that appears.
echo "== 7k. the selected file is the one that runs, even when a higher-numbered file exists =="
ledger_reset; mk_commerce; undo
cat > "$MIGDIR/0097_selftest_lower.up.sql" <<'SQL'
BEGIN;
CREATE TABLE IF NOT EXISTS iam_v2.edge_selftest_lower_marker (id int PRIMARY KEY);
COMMIT;
SQL
LOWSHA="$(sha256sum "$MIGDIR/0097_selftest_lower.up.sql" | awk '{print $1}')"
out="$(EDGE_PSQL="$PSQL" bash "$RUN" --apply-role edge_apply --only 0097_selftest_lower   --expect-db "$DB" --target-kind live-site --ack-target I_UNDERSTAND_LIVE_DARK_SITE_MIGRATION   --expect-sha256 "$LOWSHA" 2>&1)"; lrc=$?
lowmark="$(Q "SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='iam_v2' AND c.relname='edge_selftest_lower_marker'")"
highmark="$(marker)"
lowrow="$(Q "SELECT count(*) FROM public.schema_migrations WHERE version='0097_selftest_lower'")"
if [ "$lrc" = "0" ] && [ "$lowmark" = "1" ] && [ "$lowrow" = "1" ]; then
  ok "applying a NON-last migration created ITS object and recorded ITS ledger row"
else no "the runner did not apply the file it selected (rc=$lrc lower_marker=$lowmark ledger=$lowrow)" "$out"; fi
if [ "$highmark" = "0" ]; then
  ok "...and it did NOT run the last file in the directory"
else no "the runner executed 0099's body while applying 0097 -- the dynamic-scoping clobber is back" "$out"; fi
Q "DROP TABLE IF EXISTS iam_v2.edge_selftest_lower_marker" >/dev/null
rm -f "$MIGDIR/0097_selftest_lower.up.sql"
ledger_reset

echo "== 8. concurrent ledger mutation is serialised =="

# 8a. A HELD LEDGER LOCK BLOCKS AN APPLY. This is the property the whole fix rests on: if the ledger lock
# were not taken, or were taken on a different key, the apply would sail past a session holding it.
LKEY="$(Q "SELECT hashtextextended('stayconnect_edge_migrate:ledger', 0)")"
ledger_reset; mk_commerce; undo
# Hold the lock in a session that stays open, then try to apply with a short lock timeout.
docker exec -d "$C" psql -U postgres -d "$DB" -c   "SELECT pg_advisory_lock($LKEY); SELECT pg_sleep(25);" >/dev/null 2>&1
sleep 2
t0=$(date -u +%s)
out="$(EDGE_PSQL="$PSQL" bash "$RUN" --apply-role edge_apply --only 0099_selftest_noop   --expect-db "$DB" --target-kind live-site --ack-target I_UNDERSTAND_LIVE_DARK_SITE_MIGRATION   --expect-sha256 "$SHA" 2>&1)"; arc=$?
t1=$(date -u +%s)
if [ "$arc" != "0" ] && ! applied; then
  ok "an apply BLOCKS on the ledger lock another runner holds, and refuses rather than proceeding"
elif [ $((t1 - t0)) -ge 20 ]; then
  ok "an apply waited $((t1 - t0))s for the ledger lock before proceeding"
else
  no "an apply ignored a held ledger lock and completed in $((t1 - t0))s (rc=$arc)" "$out"
fi
# Let the holder finish before continuing, so the rest of the suite is not racing it.
sleep 26
undo

# 8b. BOTH DIRECTIONS TAKE THE SAME TWO LOCKS IN THE SAME ORDER. Two locks taken in two orders is a
# deadlock waiting for the first concurrent run, and a deadlock here would look exactly like a hung deploy.
# Line order in the runner: within each block the ledger key is locked first and unlocked last.
lock_order="$(grep -n 'pg_advisory_lock(%s)' "$RUNNER" | awk -F: '{ if (index($0,"lkey")) print $1" lkey"; else if (index($0,"key")) print $1" key" }')"
# The sequence must be exactly repeating (ledger, version) pairs -- one pair per block, ledger first.
# An earlier version of this check flagged the CORRECT order as broken: it looked for a key->lkey
# transition anywhere, which is precisely what the boundary between two blocks looks like.
lock_seq="$(echo "$lock_order" | awk 'NF {printf "%s%s", sep, $2; sep=","} END {print ""}')"
if echo "$lock_seq" | grep -Eq '^lkey,key(,lkey,key)*$'; then
  ok "the ledger lock is taken BEFORE the per-version lock, in both directions ($lock_seq)"
else
  no "the two locks are not taken in a fixed order; concurrent runs could deadlock" "$lock_seq"
fi

# 8c. THE UNDER-LOCK HEAD RECHECK EXISTS. The pre-lock check gives the operator a readable refusal; this one
# is what makes it true at the moment the rollback actually runs.
if grep -q "NOT_HEAD_AFTER_LOCK" "$RUNNER"; then
  ok "the down path re-establishes head-of-ledger UNDER the lock, not only before it"
else
  no "the down path only checks head before taking the lock" ""
fi

echo "============================================================"
if [ "$fail" = "0" ]; then echo "EDGE_MIGRATE_SELFTEST = PASS ($pass cases)"; exit 0; fi
echo "EDGE_MIGRATE_SELFTEST = FAIL"; exit 1
