-- Reverses 0167. A row parked in HELD_RECOVERY by repeated refusals stays HELD_RECOVERY (it is still held work an
-- operator must decide about); only the backoff bookkeeping goes.
DELETE FROM iam_v2.scrub_registry WHERE table_name IN ('iam_v2.posting_lane_settings_changes','iam_v2.posting_outbox_park_events');
DROP FUNCTION IF EXISTS iam_v2.posting_outbox_requeue_parked(uuid,uuid,uuid,text,text);
DROP FUNCTION IF EXISTS iam_v2.posting_outbox_defer(uuid,text,text);
DROP FUNCTION IF EXISTS iam_v2.posting_lane_settings_set(uuid,uuid,bigint,integer,text,text);
DROP FUNCTION IF EXISTS iam_v2.posting_lane_settings_get(uuid,uuid);
DROP TABLE IF EXISTS iam_v2.posting_outbox_park_events;
DROP TABLE IF EXISTS iam_v2.posting_lane_settings_changes;
DROP TABLE IF EXISTS iam_v2.posting_lane_settings;
DROP FUNCTION IF EXISTS iam_v2.posting_lane_append_only();
DROP INDEX IF EXISTS iam_v2.outbox_queued_by_lane;
ALTER TABLE iam_v2.posting_outbox DROP CONSTRAINT IF EXISTS posting_outbox_counts_nonnegative;
ALTER TABLE iam_v2.posting_outbox
    DROP COLUMN IF EXISTS parked_reason,
    DROP COLUMN IF EXISTS decline_count,
    DROP COLUMN IF EXISTS not_sent_count,
    DROP COLUMN IF EXISTS not_before;
