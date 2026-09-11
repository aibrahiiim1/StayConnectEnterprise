-- Roll back 0067: drop the guest sign-in attempts record.
--
-- WHAT ROLLING BACK COSTS. Every recorded attempt goes with the table, and there is no second copy: this is
-- the only place the appliance holds what a guest typed beside what the mirror would have accepted. An
-- operator who is mid-way through investigating a complaint loses the evidence for it. Nothing else breaks —
-- scd's recorder is best-effort by construction and a guest is admitted or refused identically without it,
-- and edged answers the attempts API with an empty list rather than an error.
--
-- Dropping the rows is also the only correct way to roll this back. They hold sealed guest credential
-- material whose retention is justified by the feature; with the feature gone the justification goes with it,
-- so leaving the table orphaned "just in case" would be keeping guest data for no stated purpose.
--
-- This exists for completeness of the migration chain, not as an operational step.

BEGIN;

DROP FUNCTION IF EXISTS iam_v2.p3_guest_network_mirror_state(uuid,uuid,uuid);
DROP FUNCTION IF EXISTS iam_v2.complete_sign_in_attempt(uuid,uuid,uuid,text,uuid,uuid);
DROP INDEX IF EXISTS iam_v2.sign_in_attempts_request;
DROP INDEX IF EXISTS iam_v2.sign_in_attempts_expiry;
DROP INDEX IF EXISTS iam_v2.sign_in_attempts_site_room;
DROP INDEX IF EXISTS iam_v2.sign_in_attempts_site_recent;
DROP TABLE IF EXISTS iam_v2.sign_in_attempts;

COMMIT;
