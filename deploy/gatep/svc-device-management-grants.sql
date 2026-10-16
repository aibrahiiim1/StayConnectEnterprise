-- Device management: client device replacement and release, staff slot release and fresh sign-in (migration
-- 0118; contract docs/architecture/ONEGATE_DEVICE_MANAGEMENT.md). Idempotent; included by gatep-grants.sql after
-- its revoke, so a Gate-P reconcile reproduces exactly what the migration granted.
--
--   svc_scd    moves a slot for a client only through client_device_action (proof: a fresh sign-in on the new
--              device, or a live session on the package); reads the setting through the getter.
--   svc_edged  releases a slot or forgets a device's remembered sign-in only through the staff functions (operator
--              and reason required); reads and writes the setting through the definer functions; reads the
--              action history, and whether a device has a remembered sign-in -- never its secret.
--
-- deauthorize_entitlement_device stays unreachable by every runtime role (iam_v2_scratch/phase6_least_privilege.sh).
--
-- GUARDED on the migration being present (audit B-20), so a reconcile still runs on a database rolled back below
-- 0118.
DO $g0118$
BEGIN
  IF to_regclass('iam_v2.device_slot_actions') IS NULL
     OR to_regprocedure('iam_v2.client_device_action(uuid,uuid,text,uuid,uuid,uuid)') IS NULL THEN
    RAISE NOTICE 'migration 0118 is not applied -- device-management grants skipped';
    RETURN;
  END IF;
  GRANT EXECUTE ON FUNCTION iam_v2.client_device_action(uuid,uuid,text,uuid,uuid,uuid) TO svc_scd;
  GRANT EXECUTE ON FUNCTION iam_v2.device_settings_get(uuid,uuid) TO svc_scd;
  GRANT SELECT (entitlement_id, actor, outcome, acted_at) ON iam_v2.device_slot_actions TO svc_scd;

  GRANT EXECUTE ON FUNCTION iam_v2.device_settings_get(uuid,uuid) TO svc_edged;
  GRANT EXECUTE ON FUNCTION iam_v2.device_settings_set(uuid,uuid,integer,text,text) TO svc_edged;
  GRANT EXECUTE ON FUNCTION iam_v2.staff_release_device_slot(uuid,uuid,uuid,uuid,uuid,text,text) TO svc_edged;
  GRANT EXECUTE ON FUNCTION iam_v2.staff_require_device_signin(uuid,uuid,uuid,uuid,text,text) TO svc_edged;
  GRANT SELECT ON iam_v2.device_slot_actions, iam_v2.device_settings_changes TO svc_edged;
  GRANT SELECT (id, tenant_id, site_id, device_id, method, created_at, expires_at, last_used_at, revoked_at)
    ON iam_v2.principal_device_credentials TO svc_edged;
END $g0118$;
