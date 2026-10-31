-- The Cross-PMS Transfer (migration 0201). Idempotent; included by gatep-grants.sql after its revoke, so a Gate-P reconcile
-- reproduces what the migration granted.
--
-- The Hotel Admin login (svc_edged) performs the transfer by EXECUTING one definer function and nothing else: it holds no EXECUTE on
-- the entitlement state writer, no INSERT on purchases, entitlements, entitlement_devices, entitlement_device_authorizations,
-- entitlement_transfers or stay_links, and (withdrawn with this migration) no UPDATE of sessions.entitlement_id. The function
-- re-checks every precondition under the stay and entitlement locks and moves live sessions through rebind_session_entitlement.
--
-- GUARDED on the migration being present (audit B-20).
DO $g$
BEGIN
  IF to_regprocedure('iam_v2.cross_pms_transfer(uuid,uuid,uuid,uuid,uuid,text,integer)') IS NULL THEN
    RAISE NOTICE 'migration 0201 is not applied; cross-PMS transfer grants skipped';
    RETURN;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
    GRANT EXECUTE ON FUNCTION iam_v2.cross_pms_transfer(uuid,uuid,uuid,uuid,uuid,text,integer) TO svc_edged;
  END IF;
END $g$;
