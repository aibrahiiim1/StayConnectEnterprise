-- Reverse of 0072.
--
-- Dropping the reconciliation machinery removes the only supported way to close a stay from roster evidence.
-- It does NOT reopen any stay this system already closed: those stays are CHECKED_OUT because a complete
-- roster said so, and that remains true whether or not the function that read the roster still exists.
--
-- The disposition and run ledgers go with it. They are records of decisions made by this machinery, so a
-- rollback that removes the machinery removes its bookkeeping too -- but note that no stay_event was ever
-- modified to create them, so the underlying evidence of what the PMS said is untouched here, as it was
-- untouched there.

BEGIN;

DROP FUNCTION IF EXISTS iam_v2.pms_dispose_snapshot_cases(uuid, uuid, uuid, text, text);
DROP FUNCTION IF EXISTS iam_v2.pms_roster_reconcile(uuid, uuid, uuid, bigint, text, boolean, text);
DROP FUNCTION IF EXISTS iam_v2.pms_roster_of_generation(uuid, uuid, uuid, bigint);
DROP FUNCTION IF EXISTS iam_v2.pms_reconciliation_settings_set(uuid, uuid, integer, integer, text, text, integer, integer);
DROP FUNCTION IF EXISTS iam_v2.pms_reconciliation_settings_get(uuid, uuid);

DROP TRIGGER IF EXISTS pms_case_resolutions_no_update ON iam_v2.pms_case_resolutions;
DROP TABLE IF EXISTS iam_v2.pms_case_resolutions;
DROP TABLE IF EXISTS iam_v2.pms_roster_reconciliation_runs;

DROP TRIGGER IF EXISTS pms_reconciliation_settings_changes_no_update
    ON iam_v2.pms_reconciliation_settings_changes;
DROP TABLE IF EXISTS iam_v2.pms_reconciliation_settings_changes;
DROP TABLE IF EXISTS iam_v2.pms_reconciliation_settings;

DROP FUNCTION IF EXISTS iam_v2.pms_case_resolutions_append_only();
DROP FUNCTION IF EXISTS iam_v2.pms_reconciliation_settings_append_only();

COMMIT;
