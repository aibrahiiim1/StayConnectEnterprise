-- 0089 — THE TABLES A MIGRATION CREATES BELONG TO THE OWNER, NOT TO WHOEVER APPLIED IT.
--
-- deploy/gatep/gatep-iam-roles.sql states the rule and the reason: "The migrator may SET ROLE to the owner,
-- so every object it creates is owned by the owner rather than by whoever happened to run the install. This
-- is what makes ownership reproducible instead of incidental." And: "Ownership here is load-bearing, not
-- cosmetic. The IAM-v2 boundary functions are SECURITY DEFINER and execute as their OWNER ... Objects
-- created by the wrong role produce a schema that passes a table count and fails the security model."
--
-- THREE TABLES ON PRE-LIVE DID NOT FOLLOW IT, and a rollback drill is what found them. Measured: 92 iam_v2
-- tables owned by iam_v2_owner, and three owned by iam_v2_migrator --
--
--     iam_v2.site_voucher_code_settings        (0085)
--     iam_v2.voucher_code_settings_changes     (0085)
--     iam_v2.voucher_code_reveals              (0087)
--
-- -- because those migrations declare no owner of their own and were applied with
-- `--apply-role iam_v2_migrator`. Every earlier table either was created by the owner or, like
-- iam_v2.sign_in_attempts in 0067, says whose it is in the migration itself.
--
-- HOW IT SURFACED, which is worth recording because a table count would never have shown it. Rolling 0087
-- back through the guarded down runner as iam_v2_rollback failed with "must be owner of relation
-- voucher_code_reveals". iam_v2_rollback is a member of iam_v2_owner, so it can act as owner for the 92 --
-- and it is not a member of iam_v2_migrator, so for these three it could do nothing. A rollback path that
-- works for most of the schema and not for the newest part of it is worse than one that does not exist,
-- because it will be trusted.
--
-- WHY A NEW MIGRATION RATHER THAN AN EDIT. 0085 and 0087 are already applied on the appliance. Editing them
-- would leave a file that no longer describes what ran, and public.schema_migrations stores no checksum, so
-- nothing would ever detect the difference. The correction goes forward.
--
-- AND THE GENERAL FIX IS NOT THIS FILE: it is that a migration creating a table says whose it is, and that
-- iam_v2 migrations are applied with --apply-role iam_v2_owner. Both are recorded with this delivery.

BEGIN;

DO $own$
DECLARE t text;
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
    RAISE NOTICE 'iam_v2_owner does not exist here; nothing to reassign';
    RETURN;
  END IF;
  -- Named one by one rather than "everything in the schema": a blanket REASSIGN OWNED would also move
  -- anything a future delivery deliberately owns elsewhere, and this migration is a correction of three
  -- specific rows in pg_class, not a policy engine.
  FOREACH t IN ARRAY ARRAY[
    'iam_v2.site_voucher_code_settings',
    'iam_v2.voucher_code_settings_changes',
    'iam_v2.voucher_code_reveals'
  ] LOOP
    IF to_regclass(t) IS NULL THEN
      RAISE NOTICE '% does not exist here; skipped', t;
      CONTINUE;
    END IF;
    -- Idempotent: already-correct ownership makes this a no-op rather than an error.
    EXECUTE format('ALTER TABLE %s OWNER TO iam_v2_owner', t);
  END LOOP;
END $own$;

-- ASSERTED, not assumed. The whole point of this migration is an ownership fact, so it fails if the fact is
-- not true afterwards.
DO $chk$
DECLARE bad text;
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN RETURN; END IF;
  SELECT string_agg(c.relname || ' (owned by ' || pg_get_userbyid(c.relowner) || ')', ', ')
    INTO bad
    FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
   WHERE n.nspname = 'iam_v2'
     AND c.relname IN ('site_voucher_code_settings','voucher_code_settings_changes','voucher_code_reveals')
     AND pg_get_userbyid(c.relowner) <> 'iam_v2_owner';
  IF bad IS NOT NULL THEN
    RAISE EXCEPTION 'ownership correction did not hold: %', bad;
  END IF;
END $chk$;

COMMIT;
