-- Reverse of 0073.
--
-- Restores the pre-0073 reconcile function, which measures completeness by counting rooms in admitted
-- stay_events. On a system running the connector fix that measurement is permanently short, so reconciliation
-- will refuse REFUSED_ROSTER_INCOMPLETE and close nothing. That is the correct behaviour for a rollback: it
-- fails safe rather than acting on a measure it can no longer take.

BEGIN;

DROP FUNCTION IF EXISTS iam_v2.pms_record_resync_coverage(uuid,uuid,uuid,bigint,integer,integer,integer);
DROP TABLE IF EXISTS iam_v2.pms_resync_coverage;

ALTER TABLE iam_v2.pms_roster_reconciliation_runs DROP CONSTRAINT IF EXISTS pms_roster_reconciliation_runs_outcome_check;
ALTER TABLE iam_v2.pms_roster_reconciliation_runs ADD CONSTRAINT pms_roster_reconciliation_runs_outcome_check
    CHECK (outcome IN ('COMPLETED','REFUSED_ROSTER_TOO_SMALL','REFUSED_GENERATION_UNPUBLISHED',
                       'REFUSED_CAP_EXCEEDED','REFUSED_GENERATION_NOT_LATEST','REFUSED_LINK_NOT_HEALTHY',
                       'REFUSED_ROSTER_INCOMPLETE','REFUSED_SCOPE_MISMATCH'));

COMMIT;
