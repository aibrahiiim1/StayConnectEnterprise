-- 0090 — EVERY iam_v2 TABLE HAS ONE OWNER, AND THAT IS NOW ASSERTED RATHER THAN HOPED FOR.
--
-- 0089 corrected three tables that a migration created under the applying role instead of the owner. The
-- ownership audit it prompted found one more, with a different cause:
--
--     iam_v2.backfill_0066_identity_text  ->  stayconnect
--
-- That is migration 0066's own audit table, owned by the cluster's administrative role because 0066 was
-- applied BY HAND as the superuser rather than through scripts/edge-migrate.sh. The same event left 0066
-- with no ledger row at all, which this delivery also had to repair -- two symptoms, one cause, and neither
-- visible to anything that counted tables.
--
-- WHY IT MATTERS RATHER THAN BEING TIDY. deploy/gatep/gatep-iam-roles.sql: "Ownership here is load-bearing,
-- not cosmetic. The IAM-v2 boundary functions are SECURITY DEFINER and execute as their OWNER ... Objects
-- created by the wrong role produce a schema that passes a table count and fails the security model." And
-- concretely: iam_v2_rollback is a member of iam_v2_owner and of nothing else, so a table owned by anybody
-- else cannot be dropped by the guarded rollback path. A rollback that works for most of the schema is the
-- kind of tool that gets trusted and then does not work.
--
-- THE ASSERTION IS THE POINT. Reassigning one table fixes one table; asserting the invariant is what makes
-- the next one impossible to land quietly. The same assertion is added to deploy/gatep/gatep-grants.sql so
-- it is re-checked on every reconcile, not only once here.

BEGIN;

-- THIS MIGRATION ONLY ASSERTS. It does not reassign, and the reason is a privilege fact rather than a
-- preference: ALTER TABLE ... OWNER TO requires the executing role to be a member of BOTH the current owner
-- and the new one. backfill_0066_identity_text belongs to the cluster's administrative role, and no
-- least-privilege migration role is a member of that -- measured, when an earlier version of this file was
-- refused with "must be owner of table backfill_0066_identity_text".
--
-- So the reassignment is an owner-level deployment action and lives in deploy/gatep/gatep-iam-roles.sql,
-- beside the other privileges only the administrative role can confer. The same split this delivery already
-- made for REFERENCES on public.operators, for the same reason. Migrations assert the invariant; Gate-P
-- establishes it.

DO $chk$
DECLARE bad text;
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN RETURN; END IF;
  SELECT string_agg(c.relname || ' (' || pg_get_userbyid(c.relowner) || ')', ', ' ORDER BY c.relname)
    INTO bad
    FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
   WHERE n.nspname = 'iam_v2' AND c.relkind = 'r'
     AND pg_get_userbyid(c.relowner) <> 'iam_v2_owner';
  IF bad IS NOT NULL THEN
    RAISE EXCEPTION 'iam_v2 tables not owned by iam_v2_owner: %', bad
      USING HINT = 'apply iam_v2 migrations with --apply-role iam_v2_owner, or have the migration declare '
                   'its own ALTER TABLE ... OWNER TO iam_v2_owner';
  END IF;
END $chk$;

COMMIT;
