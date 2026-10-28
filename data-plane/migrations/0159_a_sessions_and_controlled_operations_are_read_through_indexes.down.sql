BEGIN;
DROP INDEX IF EXISTS iam_v2.controlled_operation_scope_opened_at;
DROP INDEX IF EXISTS iam_v2.sessions_site_started;
DROP INDEX IF EXISTS iam_v2.sessions_live_by_site;
DROP INDEX IF EXISTS iam_v2.sessions_open_by_device;
DROP INDEX IF EXISTS iam_v2.sessions_entitlement_id;
COMMIT;
