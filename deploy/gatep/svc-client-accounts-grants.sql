-- Client accounts: Disable suspends the account's package and Enable resumes it (migration 0120, decision D-A).
-- Idempotent; included by gatep-grants.sql after its revoke, so a Gate-P reconcile reproduces exactly what the
-- migration granted.
--
--   svc_edged  aligns one account's packages with its enabled flag only through the definer function, which
--              moves status through apply_entitlement_transition. edged still holds no UPDATE on entitlement
--              status and no EXECUTE on the transition writer itself.
--
-- GUARDED on the migration being present (audit B-20), so a reconcile still runs on a database rolled back below
-- 0120.
DO $g0120$
BEGIN
  IF to_regprocedure('iam_v2.account_entitlements_follow_access(uuid,uuid,uuid)') IS NULL THEN
    RAISE NOTICE 'migration 0120 is not applied -- client-account grants skipped';
    RETURN;
  END IF;
  GRANT EXECUTE ON FUNCTION iam_v2.account_entitlements_follow_access(uuid,uuid,uuid) TO svc_edged;
END $g0120$;
