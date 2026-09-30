-- Roll back 0094: remove local module enablement.
-- Rolling back makes the resolver see every switchable module as "not enabled".

BEGIN;
DROP FUNCTION IF EXISTS iam_v2.site_module_set(uuid,uuid,text,boolean,text,text);
DROP FUNCTION IF EXISTS iam_v2.site_module_get(uuid,uuid);
DROP TRIGGER IF EXISTS site_module_changes_append_only ON iam_v2.site_module_changes;
DROP TABLE IF EXISTS iam_v2.site_module_changes;
DROP FUNCTION IF EXISTS iam_v2.site_module_changes_append_only();
DROP TABLE IF EXISTS iam_v2.site_module_settings;
COMMIT;
