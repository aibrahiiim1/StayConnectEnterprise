-- Reverse of 0034: withdraw the policy entry points and restore 0033's grant shape.
--
-- ROLLING THIS BACK REINSTATES THE BYPASSES it closed: the runtime regains a release whose throttle it can
-- choose, and the operator role regains a direct unaudited write on the setting. Stated plainly for the same
-- reason 0032's down migration states its own: a rollback that quietly kept the fix would make the pair
-- untrustworthy in both directions, and the honest answer is to disable the capability before rolling back.
BEGIN;

REVOKE EXECUTE ON FUNCTION iam_v2.p6_guest_release_device_policy(uuid, uuid) FROM svc_scd;
DROP FUNCTION IF EXISTS iam_v2.p6_guest_release_device_policy(uuid, uuid);
GRANT EXECUTE ON FUNCTION iam_v2.p6_guest_release_device(uuid, uuid, int) TO svc_scd;

REVOKE EXECUTE ON FUNCTION iam_v2.p6_set_guest_device_self_service(uuid, uuid, uuid, boolean, uuid, text, text) FROM svc_edged;
DROP FUNCTION IF EXISTS iam_v2.p6_set_guest_device_self_service(uuid, uuid, uuid, boolean, uuid, text, text);
GRANT SELECT, INSERT, UPDATE ON iam_v2.appliance_product_settings TO svc_edged;
GRANT SELECT, INSERT ON iam_v2.appliance_product_setting_changes TO svc_edged;

GRANT SELECT, INSERT ON iam_v2.guest_device_actions TO svc_scd;
GRANT SELECT ON iam_v2.entitlement_device_authorizations TO svc_scd;

DELETE FROM public.schema_migrations WHERE version = '0034_phase6_policy_boundaries';

COMMIT;
