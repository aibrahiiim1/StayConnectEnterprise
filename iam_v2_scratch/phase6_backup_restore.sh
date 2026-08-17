#!/usr/bin/env bash
# PHASE-6 M4 — BACKUP AND A REAL RESTORE.
#
# Not a simulated one. The database is dumped with pg_dump, DROPPED, recreated and restored with pg_restore,
# and then everything that matters is asserted against the restored copy. A restore test that keeps the
# original database around is a test of pg_dump's exit code.
#
# WHAT A RESTORE CAN QUIETLY LOSE, and therefore what is checked:
#
#   * FUNCTIONS. Phase 6 is mostly controlled writers; a dump that restored tables and not functions would
#     leave a schema that looks complete and cannot do anything.
#   * PRIVILEGES. Roles live at CLUSTER level, so a database-level dump carries the GRANTs but not the roles.
#     A restore into a cluster whose roles are missing silently drops those grants -- and the failure would
#     appear later, as a service that cannot work, not as a restore error.
#   * The SETTING and its audit, which are the appliance's own product state.
#
# Disposable database only. It contacts no appliance and no Production database.
set -uo pipefail
export PATH="$PATH:/c/Program Files/Docker/Docker/resources/bin"
C="${PHASE6_CONTAINER:-iamv2-p6}"
DB="${PHASE6_DB:-iam_scratch}"
DUMP="/var/lib/postgresql/p6_backup.dump"

pass=0; fail=0
ok(){ printf '  [PASS] %s\n' "$1"; pass=$((pass+1)); }
no(){ printf '  [FAIL] %s :: %s\n' "$1" "${2:-}"; fail=$((fail+1)); }
q(){ docker exec -i "$C" psql -U postgres -d "$DB" -tAqc "$1" 2>&1; }
eqv(){ [ "$2" = "$3" ] && ok "$1" || no "$1" "expected '$3', got '$2'"; }

echo "== Phase-6 backup and real restore =="

# ---- a fact of our own to look for on the other side ------------------------------------------------------
marker="$(q "SELECT gen_random_uuid()")"
q "INSERT INTO iam_v2.appliance_product_setting_changes
     (tenant_id, site_id, appliance_id, setting_key, old_value, new_value, changed_by_operator_id,
      changed_by, change_reason)
   VALUES ('11111111-1111-1111-1111-111111111111','22222222-2222-2222-2222-222222222222',
           '44444444-4444-4444-4444-444444444444','guest_device_self_service', false, true,
           '55555555-5555-5555-5555-555555555555','Restore Rehearsal','$marker')" >/dev/null
before_tables="$(q "SELECT count(*) FROM information_schema.tables WHERE table_schema='iam_v2'")"
before_funcs="$(q "SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='iam_v2'")"

# ---- PRECONDITION: THE SOURCE DATA MUST SATISFY ITS OWN CONSTRAINTS ----------------------------------------
#
# pg_restore re-validates every foreign key at the end of a restore, so a database holding rows that violate
# one CANNOT be restored from its own dump. Worth stating plainly, because it is exactly what happened: several
# Phase-6 gates seed entitlements under `SET LOCAL session_replication_role = replica` -- deliberately, to skip
# building the whole parent chain above sessions -- and the rows they leave reference package revisions that do
# not exist. All 21 of them. pg_restore then failed on a constraint nobody had touched, and it read as a defect
# in backup and restore.
#
# It is not. It is a statement about the DATA this gate was pointed at. Checking it BEFORE the dump, generically
# over every validated foreign key in iam_v2, means a real restore defect and a fixture polluted by a seeding
# shortcut can never again arrive as the same message. The check only SELECTs.
badfk="$(docker exec -i "$C" psql -U postgres -d "$DB" -tAq </dev/null 2>&1 <<'SQL' | tail -1
CREATE TEMP TABLE _viol(conname text, n bigint);
DO $probe$
DECLARE
  r record; child text; parent text; cond text; nulls text; cnt bigint; i int;
BEGIN
  FOR r IN
    SELECT c.conname,
           cn.nspname || '.' || ct.relname AS child,
           pn.nspname || '.' || pt.relname AS parent,
           c.conkey, c.confkey, c.conrelid, c.confrelid
      FROM pg_constraint c
      JOIN pg_class ct     ON ct.oid = c.conrelid
      JOIN pg_namespace cn ON cn.oid = ct.relnamespace
      JOIN pg_class pt     ON pt.oid = c.confrelid
      JOIN pg_namespace pn ON pn.oid = pt.relnamespace
     WHERE c.contype = 'f' AND c.convalidated AND cn.nspname = 'iam_v2'
  LOOP
    cond := ''; nulls := '';
    FOR i IN 1 .. array_length(r.conkey, 1) LOOP
      IF i > 1 THEN cond := cond || ' AND '; nulls := nulls || ' AND '; END IF;
      cond := cond || format('p.%I = c.%I',
                (SELECT attname FROM pg_attribute WHERE attrelid = r.confrelid AND attnum = r.confkey[i]),
                (SELECT attname FROM pg_attribute WHERE attrelid = r.conrelid  AND attnum = r.conkey[i]));
      nulls := nulls || format('c.%I IS NOT NULL',
                (SELECT attname FROM pg_attribute WHERE attrelid = r.conrelid AND attnum = r.conkey[i]));
    END LOOP;
    EXECUTE format('SELECT count(*) FROM %s c WHERE (%s) AND NOT EXISTS (SELECT 1 FROM %s p WHERE %s)',
                   r.child, nulls, r.parent, cond) INTO cnt;
    IF cnt > 0 THEN INSERT INTO _viol VALUES (r.conname, cnt); END IF;
  END LOOP;
END $probe$;
SELECT COALESCE(string_agg(conname || ' (' || n || ' rows)', ', ' ORDER BY conname), '') FROM _viol;
SQL
)"
if [ -n "$badfk" ]; then
  no "the source database satisfies its own foreign keys BEFORE the dump" \
     "$badfk -- seeded under session_replication_role=replica; such a database cannot be restored from its own dump, and that is a property of the DATA, not of backup and restore"
else
  ok "every validated foreign key in iam_v2 is satisfied, so a restore can revalidate them"
fi

# ---- backup -----------------------------------------------------------------------------------------------
out="$(docker exec "$C" sh -c "pg_dump -U postgres -d $DB -Fc > $DUMP && ls -la $DUMP" 2>&1)"
case "$out" in *"$DUMP"*) ok "pg_dump produced a custom-format backup";; *) no "backup" "$out";; esac

# ---- DROP the database, and restore into a fresh one ------------------------------------------------------
out="$(docker exec "$C" sh -c "psql -U postgres -d postgres -qAt -c 'DROP DATABASE $DB WITH (FORCE)' && \
  psql -U postgres -d postgres -qAt -c 'CREATE DATABASE $DB'" 2>&1)"
case "$out" in *ERROR*) no "drop and recreate the database" "$out";; *) ok "the database was genuinely dropped and recreated";; esac
# pg_restore's own outcome, checked rather than announced. It was reported as a PASS unconditionally, which
# would have called a completely failed restore a success -- and every assertion after it would then have been
# measuring an empty database against expectations taken from the same empty database.
restore_out="$(docker exec "$C" sh -c "pg_restore -U postgres -d $DB --no-owner $DUMP" 2>&1)"
restore_rc=$?
restore_errs="$(printf '%s\n' "$restore_out" | grep -c 'error:')"
if [ "$restore_rc" -eq 0 ] && [ "$restore_errs" -eq 0 ]; then
  ok "pg_restore completed with no errors"
else
  no "pg_restore" "exit $restore_rc, $restore_errs error line(s): $(printf '%s\n' "$restore_out" | grep 'error:' | head -2)"
fi

# ---- what survived ----------------------------------------------------------------------------------------
eqv "every iam_v2 table is back" "$(q "SELECT count(*) FROM information_schema.tables WHERE table_schema='iam_v2'")" "$before_tables"
eqv "every iam_v2 function is back" "$(q "SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='iam_v2'")" "$before_funcs"
eqv "the Phase-6 controlled writers are present" \
   "$(q "SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='iam_v2' AND p.proname IN ('p6_expire_entitlement','p6_tick_online_time','p6_guest_release_device_policy','p6_set_guest_device_self_service','p6_exhaustion_instant','p6_data_crossing','p6_due_terminal')")" "7"
eqv "the audit row written before the backup is on the other side" \
   "$(q "SELECT count(*) FROM iam_v2.appliance_product_setting_changes WHERE change_reason='$marker'")" "1"

# ---- PRIVILEGES, which are the part a restore most easily loses -------------------------------------------
eqv "svc_acctd can still execute the sanctioned expiry writer" \
   "$(q "SELECT has_function_privilege('svc_acctd','iam_v2.p6_expire_entitlement(uuid)','EXECUTE')")" "t"
eqv "svc_scd can still read the per-appliance setting" \
   "$(q "SELECT has_table_privilege('svc_scd','iam_v2.appliance_product_settings','SELECT')")" "t"
eqv "svc_acctd still holds NO write anywhere in iam_v2" \
   "$(q "SELECT count(*) FROM information_schema.role_table_grants WHERE table_schema='iam_v2' AND grantee='svc_acctd' AND privilege_type <> 'SELECT'")" "0"
eqv "PUBLIC still cannot execute the expiry writer" \
   "$(q "SELECT has_function_privilege('public','iam_v2.p6_expire_entitlement(uuid)','EXECUTE')")" "f"
eqv "PUBLIC still cannot execute the accrual tick" \
   "$(q "SELECT has_function_privilege('public','iam_v2.p6_tick_online_time(uuid,uuid,timestamptz,int,uuid[],timestamptz[])','EXECUTE')")" "f"

# ---- the guards, which are triggers rather than tables ----------------------------------------------------
for trg in p6_session_requires_authorized_binding p6_guest_device_actions_append_only \
           p6_setting_changes_append_only p6_skipped_intervals_append_only; do
  eqv "the $trg guard survived the restore" "$(q "SELECT count(*) FROM pg_trigger WHERE tgname='$trg'")" "1"
done

# ---- leave the database as we found it --------------------------------------------------------------------
#
# The marker row is written into the FIXTURE appliance's audit history, which other suites assert on -- and it
# made one of them fail by being the second row where exactly one was expected. A gate that leaves state
# behind breaks its neighbours, so it removes its own row. The table is append-only by trigger, which is
# exactly why this is done deliberately and only for the row this script created.
q "ALTER TABLE iam_v2.appliance_product_setting_changes DISABLE TRIGGER p6_setting_changes_append_only" >/dev/null
q "DELETE FROM iam_v2.appliance_product_setting_changes WHERE change_reason='$marker'" >/dev/null
q "ALTER TABLE iam_v2.appliance_product_setting_changes ENABLE TRIGGER p6_setting_changes_append_only" >/dev/null
eqv "the gate removed its own marker row"    "$(q "SELECT count(*) FROM iam_v2.appliance_product_setting_changes WHERE change_reason='$marker'")" "0"
eqv "...and the append-only guard is armed again"    "$(q "SELECT tgenabled FROM pg_trigger WHERE tgname='p6_setting_changes_append_only'")" "O"

echo "------------------------------------------------------------"
printf 'PHASE6_BACKUP_RESTORE pass=%d fail=%d\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
