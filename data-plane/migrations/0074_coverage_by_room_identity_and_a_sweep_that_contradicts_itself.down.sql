-- Reverse of 0074.
--
-- Returns completeness to a COUNT and removes conflict detection. A sweep that names the right NUMBER of the
-- wrong rooms, or that reports a room both occupied and empty, would once again be treated as usable
-- evidence for closing a stay. That is a real weakening, so the rollback exists for schema recovery, not as
-- an operating mode.

BEGIN;

DROP FUNCTION IF EXISTS iam_v2.pms_roster_reconcile(uuid, uuid, uuid, bigint, text, boolean, text);
DROP FUNCTION IF EXISTS iam_v2.pms_known_room_inventory(uuid,uuid,uuid,bigint,integer);
DROP FUNCTION IF EXISTS iam_v2.pms_record_resync_coverage(uuid,uuid,uuid,bigint,text[],integer,integer,integer);

ALTER TABLE iam_v2.pms_resync_coverage DROP CONSTRAINT IF EXISTS prc_conflicts_sane;
ALTER TABLE iam_v2.pms_resync_coverage DROP COLUMN IF EXISTS rooms;
ALTER TABLE iam_v2.pms_resync_coverage DROP COLUMN IF EXISTS conflicting_rooms;

ALTER TABLE iam_v2.pms_roster_reconciliation_runs DROP CONSTRAINT IF EXISTS pms_roster_reconciliation_runs_outcome_check;
ALTER TABLE iam_v2.pms_roster_reconciliation_runs ADD CONSTRAINT pms_roster_reconciliation_runs_outcome_check
    CHECK (outcome IN ('COMPLETED','REFUSED_ROSTER_TOO_SMALL','REFUSED_GENERATION_UNPUBLISHED',
                       'REFUSED_CAP_EXCEEDED','REFUSED_GENERATION_NOT_LATEST','REFUSED_LINK_NOT_HEALTHY',
                       'REFUSED_ROSTER_INCOMPLETE','REFUSED_SCOPE_MISMATCH','REFUSED_NO_COVERAGE_EVIDENCE'));

COMMIT;
