-- 0090 — NO iam_v2 TABLE IS OWNED BY THE LEAST-PRIVILEGE MIGRATION ROLE.
--
-- 0089 corrected three tables that 0085 and 0087 had created under iam_v2_migrator, because they declared
-- no owner of their own and were applied with `--apply-role iam_v2_migrator`. A rollback drill found them:
-- iam_v2_rollback is a member of iam_v2_owner and of nothing else, so it could drop 92 tables and not those
-- three. This migration is the guard that stops that class of defect coming back.
--
-- WHAT IT ASSERTS, AND WHY IT IS NOT THE STRONGER STATEMENT IT FIRST MADE.
--
-- The first version of this file demanded that EVERY iam_v2 table be owned by iam_v2_owner. That is the
-- right end state, and it is the wrong thing for a MIGRATION to assert, because it is not true at the point
-- a migration runs on a factory-clean install. Measured by
-- scripts/clean-install-reconstruction.sh, which is the disaster-recovery path:
--
--     FAIL: 0090_every_iam_v2_table_has_one_owner -- CONTEXT: PL/pgSQL function inline_code_block line 11
--     FAIL: migration 0090 is not recorded in schema_migrations
--
-- On that path the numbered migrations run as the cluster's SUPERUSER -- the platform migrations create
-- extensions -- so every table they create is owned by `stayconnect`, and ownership is moved to
-- iam_v2_owner afterwards by deploy/gatep/gatep-iam-roles.sql. The old comment in this file said
-- "Migrations assert the invariant; Gate-P establishes it", which cannot work in that order: Gate-P runs
-- AFTER the migrations on both install paths. So the assertion failed the whole reconstruction, and an
-- assert-only migration that breaks disaster recovery is worse than no assertion at all.
--
-- THE INVARIANT THIS FILE CAN HONESTLY CARRY is the narrower and more useful one: no iam_v2 table is owned
-- by iam_v2_migrator. That is order-independent and true on both paths --
--
--     factory-clean install   tables owned by the superuser, later swept to iam_v2_owner by Gate-P
--     appliance upgrade       tables owned by iam_v2_owner, because --apply-role sets it
--
-- -- and it catches exactly the mistake that happened: a migration creating an object under the
-- least-privilege role that applied it, leaving an object the rollback role cannot touch.
--
-- The full "everything belongs to iam_v2_owner" invariant is still asserted, in the place where it is true:
-- a standing assertion at the end of deploy/gatep/gatep-grants.sql, which runs after both the migrations
-- and the ownership sweep.
--
-- WHY THIS FILE WAS EDITED RATHER THAN SUPERSEDED BY AN 0091. 0089's own down-migration argues against
-- editing an applied migration, and that argument holds for one that CHANGED STATE: the file would no
-- longer describe what ran, and public.schema_migrations stores no checksum to notice. This migration
-- changes nothing -- it is an assertion, and its ledger row means "this check ran". Adding 0091 would not
-- help either, because 0090 still executes on every future factory-clean install and would still fail
-- there. A broken check has to be fixed where it is.

BEGIN;

DO $chk$
DECLARE bad text;
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_migrator') THEN RETURN; END IF;
  SELECT string_agg(c.relname, ', ' ORDER BY c.relname)
    INTO bad
    FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
   WHERE n.nspname = 'iam_v2' AND c.relkind = 'r'
     AND pg_get_userbyid(c.relowner) = 'iam_v2_migrator';
  IF bad IS NOT NULL THEN
    RAISE EXCEPTION 'iam_v2 tables owned by the least-privilege migration role: %', bad
      USING HINT = 'a migration created these under the role that applied it. The rollback role is a member '
                   'of iam_v2_owner and NOT of iam_v2_migrator, so it cannot drop them: the down path works '
                   'for the rest of the schema and not for these, which is worse than no down path. Either '
                   'apply iam_v2 migrations with --apply-role iam_v2_owner, or have the migration declare '
                   'ALTER TABLE ... OWNER TO iam_v2_owner itself, as 0067 does for sign_in_attempts.';
  END IF;
END $chk$;

COMMIT;
