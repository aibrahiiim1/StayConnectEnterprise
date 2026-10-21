-- 0149 down -- the index on stay_events (stay_id, received_at) is dropped.
--
-- Kept: nothing is stored in it that is not in the table. Without it the hourly retention run is slow again on a large site (see 0149).

BEGIN;

DROP INDEX IF EXISTS iam_v2.stay_events_by_stay;

COMMIT;
