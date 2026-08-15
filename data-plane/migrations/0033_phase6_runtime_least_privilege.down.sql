-- Reverse of 0033: withdraw the Phase-6 runtime grants and return the release to SECURITY INVOKER.
--
-- The ROLES are not dropped. Gate P owns their lifecycle and they carry grants from earlier phases; dropping
-- them here would remove privileges 0033 never granted. Revoking exactly what was granted is the whole job.
BEGIN;

REVOKE EXECUTE ON FUNCTION iam_v2.p6_guest_release_device(uuid, uuid, int) FROM svc_scd;
REVOKE SELECT ON iam_v2.entitlement_devices, iam_v2.devices,
                 iam_v2.entitlement_device_authorizations FROM svc_scd;
REVOKE SELECT, INSERT ON iam_v2.sessions FROM svc_scd;
REVOKE SELECT, INSERT ON iam_v2.guest_device_actions FROM svc_scd;
REVOKE SELECT ON iam_v2.appliance_product_settings FROM svc_scd;

REVOKE SELECT, INSERT, UPDATE ON iam_v2.appliance_product_settings FROM svc_edged;
REVOKE SELECT, INSERT ON iam_v2.appliance_product_setting_changes FROM svc_edged;

ALTER FUNCTION iam_v2.p6_guest_release_device(uuid, uuid, int) SECURITY INVOKER;

DELETE FROM public.schema_migrations WHERE version = '0033_phase6_runtime_least_privilege';

COMMIT;
