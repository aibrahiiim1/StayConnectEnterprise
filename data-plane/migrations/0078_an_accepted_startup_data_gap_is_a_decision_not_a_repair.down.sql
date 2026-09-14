-- Reverse of 0078.
--
-- Any acceptance already recorded is REMOVED, because the disposition value it uses ceases to be expressible
-- -- and a row whose disposition violates its own CHECK is worse than no row. The stay_events it referred to
-- are untouched throughout and become outstanding again, which is the honest outcome: undoing the ability to
-- accept a gap should undo the acceptance, not leave a decision recorded in a vocabulary that no longer
-- contains it.

BEGIN;

DROP FUNCTION IF EXISTS iam_v2.pms_accept_startup_data_gap(uuid,uuid,uuid,text,text);

DELETE FROM iam_v2.pms_case_resolutions WHERE disposition = 'ACCEPTED_STARTUP_DATA_GAP';

ALTER TABLE iam_v2.pms_case_resolutions DROP CONSTRAINT IF EXISTS pms_case_resolutions_disposition_check;
ALTER TABLE iam_v2.pms_case_resolutions ADD CONSTRAINT pms_case_resolutions_disposition_check
    CHECK (disposition IN ('DEPARTED_CONFIRMED_BY_ROSTER','NOT_A_DEPARTURE_ROSTER_SNAPSHOT',
                           'ALREADY_CLOSED','NEEDS_PMS_EVIDENCE'));

ALTER TABLE iam_v2.pms_case_resolutions DROP CONSTRAINT IF EXISTS pms_case_resolutions_evidence_kind_check;
ALTER TABLE iam_v2.pms_case_resolutions ADD CONSTRAINT pms_case_resolutions_evidence_kind_check
    CHECK (evidence_kind IN ('PUBLISHED_ROSTER_GENERATION','ADMISSION_KIND','STAY_STATE','NONE'));

DROP VIEW IF EXISTS iam_v2.pms_unanswered_review_events;

COMMIT;
