-- Reverse 0036. It removes exactly what 0036 created and nothing else.
--
-- ROLLBACK CONSEQUENCE, STATED RATHER THAN HIDDEN: dropping online_time_skipped_intervals discards the record
-- of which intervals were deliberately not charged. That evidence cannot be reconstructed afterwards, because
-- it describes observations nobody made. Before rolling this back, the aggregate time mode must be OFF and
-- no entitlement may be in AGGREGATE_ONLINE_TIME mode -- the same disable-then-roll-back ordering 0032
-- already requires, and for the same reason: a faithful down migration must not be run while the thing it
-- reverses is in use.
BEGIN;

DROP FUNCTION IF EXISTS iam_v2.p6_tick_online_time(uuid, uuid, timestamptz, int);

ALTER TABLE iam_v2.entitlements DROP COLUMN IF EXISTS online_time_exhausted_at;

DROP TRIGGER IF EXISTS p6_skipped_intervals_append_only ON iam_v2.online_time_skipped_intervals;
DROP FUNCTION IF EXISTS iam_v2.p6_skipped_intervals_append_only();
DROP TABLE IF EXISTS iam_v2.online_time_skipped_intervals;

COMMIT;
