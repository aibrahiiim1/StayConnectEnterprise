-- Reverse of 0071.
--
-- Dropping the mode table removes the absence-of-a-row semantics, so the code that reads it falls back to its
-- own compiled default -- which is also LICENSING_ONLY, deliberately: a rollback of the schema must not be a
-- quiet route back to sending telemetry.
--
-- No telemetry record is touched on either side. This migration never owned any.

BEGIN;

DROP FUNCTION IF EXISTS iam_v2.cloud_mode_set(uuid, uuid, text, text, text);
DROP FUNCTION IF EXISTS iam_v2.cloud_mode_get(uuid, uuid);
DROP TRIGGER IF EXISTS cloud_mode_changes_no_update ON iam_v2.cloud_mode_changes;
DROP TABLE IF EXISTS iam_v2.cloud_mode_changes;
DROP TABLE IF EXISTS iam_v2.site_cloud_mode;
DROP FUNCTION IF EXISTS iam_v2.cloud_mode_changes_append_only();

COMMIT;
