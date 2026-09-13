-- Reverse of 0076.
--
-- The connector falls back to its compiled reconnect bounds, which is what it used before this migration --
-- recovery is unaffected. What is lost is the operator's ability to change them without a deployment, and
-- the visibility of a persistent blocker: reconciliation would go on refusing a degraded feed correctly, and
-- silently.

BEGIN;

DROP FUNCTION IF EXISTS iam_v2.pms_integration_blockers(uuid,uuid);
DROP FUNCTION IF EXISTS iam_v2.pms_connection_settings_set(uuid,uuid,text,text,integer,integer,integer,integer,integer);
DROP FUNCTION IF EXISTS iam_v2.pms_connection_settings_get(uuid,uuid);
DROP TRIGGER IF EXISTS pms_connection_settings_changes_no_update ON iam_v2.pms_connection_settings_changes;
DROP TABLE IF EXISTS iam_v2.pms_connection_settings_changes;
DROP TABLE IF EXISTS iam_v2.pms_connection_settings;
DROP FUNCTION IF EXISTS iam_v2.pms_connection_settings_append_only();

COMMIT;
