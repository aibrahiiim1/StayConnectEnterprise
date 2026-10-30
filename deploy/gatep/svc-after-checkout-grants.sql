-- After Check-out Access (migration 0193). Idempotent; included by gatep-grants.sql after its revoke, so a Gate-P reconcile
-- reproduces what the migration granted (the grant-durability rule: a grant only a migration makes does not survive a
-- factory-clean install).
--   svc_pmsd  reads the enable switch inside the checkout conversion (the configuration row is already locked by p3_lock_grace_config).
--   svc_edged reads the switch and changes it through the audited definer (operator, reason code and expected version required).
-- GUARDED on the migration being present.
DO $g0193$
BEGIN
  IF to_regprocedure('iam_v2.p3_after_checkout_enabled(uuid,uuid)') IS NULL THEN
    RAISE NOTICE 'migration 0193 is not applied; After Check-out Access grants skipped';
    RETURN;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_pmsd') THEN
    GRANT EXECUTE ON FUNCTION iam_v2.p3_after_checkout_enabled(uuid,uuid) TO svc_pmsd;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
    GRANT EXECUTE ON FUNCTION iam_v2.p3_after_checkout_enabled(uuid,uuid) TO svc_edged;
    GRANT EXECUTE ON FUNCTION iam_v2.set_after_checkout_enabled(uuid,uuid,boolean,integer,uuid,text) TO svc_edged;
  END IF;
END $g0193$;
