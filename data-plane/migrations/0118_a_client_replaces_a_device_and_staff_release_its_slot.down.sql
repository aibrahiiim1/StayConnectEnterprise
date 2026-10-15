-- 0118 down -- remove the ways a slot can move; KEEP the record of every move that happened.
--
-- The functions go, so no client or staff action can move a slot any more (the appliance returns to "a device
-- keeps its slot; only the dark Phase-6 release frees one"). iam_v2.device_slot_actions and the settings with
-- their change log are kept: a rollback must not erase the audit of what was done to clients' packages, and
-- re-applying the up migration (IF NOT EXISTS) continues them. Slots already moved stay moved -- the intervals
-- they closed are history, as every other closed interval is.

BEGIN;

DROP FUNCTION IF EXISTS iam_v2.client_device_action(uuid,uuid,text,uuid,uuid,uuid);
DROP FUNCTION IF EXISTS iam_v2.staff_release_device_slot(uuid,uuid,uuid,uuid,uuid,text,text);
DROP FUNCTION IF EXISTS iam_v2.staff_require_device_signin(uuid,uuid,uuid,uuid,text,text);
DROP FUNCTION IF EXISTS iam_v2.device_settings_set(uuid,uuid,integer,text,text);
DROP FUNCTION IF EXISTS iam_v2.device_settings_get(uuid,uuid);

DO $g$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_scd') THEN
    REVOKE SELECT (entitlement_id, actor, outcome, acted_at) ON iam_v2.device_slot_actions FROM svc_scd;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
    REVOKE SELECT ON iam_v2.device_slot_actions, iam_v2.device_settings_changes FROM svc_edged;
    REVOKE SELECT (id, tenant_id, site_id, device_id, method, created_at, expires_at, last_used_at, revoked_at)
      ON iam_v2.principal_device_credentials FROM svc_edged;
  END IF;
END $g$;

COMMIT;
