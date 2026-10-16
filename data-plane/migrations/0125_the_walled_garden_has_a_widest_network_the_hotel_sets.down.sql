-- Removing the walled-garden policy setting.
--
-- WHAT THIS COSTS. edged can no longer read the widest-network policy and refuses to create or edit a CIDR rule
-- (it requires the reader rather than reverting to a constant); address and domain rules are unaffected. The
-- policy's change history is dropped with its table -- copy it first if it is wanted. No garden rule is touched.

BEGIN;

DROP FUNCTION IF EXISTS iam_v2.walled_garden_settings_set(uuid,uuid,integer,text,text);
DROP FUNCTION IF EXISTS iam_v2.walled_garden_settings_get(uuid,uuid);
DROP TRIGGER IF EXISTS walled_garden_settings_changes_append_only ON iam_v2.walled_garden_settings_changes;
DROP TABLE IF EXISTS iam_v2.walled_garden_settings_changes;
DROP FUNCTION IF EXISTS iam_v2.walled_garden_settings_changes_append_only();
DROP TABLE IF EXISTS iam_v2.site_walled_garden_settings;

COMMIT;
