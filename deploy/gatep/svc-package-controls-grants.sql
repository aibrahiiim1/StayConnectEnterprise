-- Staff End Package / Reset Package (migration 0202). Idempotent; included by gatep-grants.sql after its revoke, so a Gate-P
-- reconcile reproduces what the migration granted.
--
-- The Hotel Admin login (svc_edged) ends or resets a package by EXECUTING definer functions and nothing else: it holds no EXECUTE
-- on the entitlement state writer, the end-access primitive or the session rebinder, and no INSERT on purchases, entitlements,
-- entitlement_devices or entitlement_device_authorizations. The functions re-check every precondition under the stay and
-- entitlement locks. The preview and history functions are read-only.
--
-- GUARDED on the migration being present (audit B-20).
DO $g$
BEGIN
  IF to_regprocedure('iam_v2.admin_reset_package(uuid,uuid,uuid,uuid,text,uuid)') IS NULL THEN
    RAISE NOTICE 'migration 0202 is not applied; package control grants skipped';
    RETURN;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
    GRANT EXECUTE ON FUNCTION iam_v2.package_control_preview(uuid,uuid,uuid) TO svc_edged;
    GRANT EXECUTE ON FUNCTION iam_v2.package_admin_history(uuid,uuid,uuid) TO svc_edged;
    GRANT EXECUTE ON FUNCTION iam_v2.package_control_list(uuid,uuid,text,uuid) TO svc_edged;
    GRANT EXECUTE ON FUNCTION iam_v2.admin_end_package(uuid,uuid,uuid,uuid,text,uuid) TO svc_edged;
    GRANT EXECUTE ON FUNCTION iam_v2.admin_reset_package(uuid,uuid,uuid,uuid,text,uuid) TO svc_edged;
  END IF;
END $g$;
