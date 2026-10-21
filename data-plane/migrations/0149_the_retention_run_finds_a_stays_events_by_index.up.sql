-- 0149 -- THE RETENTION RUN FINDS A STAY'S EVENTS BY INDEX
-- (found on PRE-LIVE on 2026-10-21, the first time migration 0127's hourly run met a real database).
--
-- iam_v2.run_data_retention (0127) works out when each finished stay last had a PMS event, and iam_v2.anonymise_stay blanks the
-- events of one stay. Both look the events up by stay_id, and iam_v2.stay_events had no index on it: every lookup read the whole
-- table. With 374,000 events a batch of 100 stays took 75 seconds and a batch of 500 took 160, and the appliance's own role is
-- held to 30 seconds per statement, so the run failed every hour and never anonymised a single stay. In the integration suites
-- the table holds a handful of rows, which is why nothing before a real database could show it.
--
-- One partial index (events that resolved to a stay; the unresolved ones carry no stay_id and are found another way) makes the
-- same batch of 500 stays take seven seconds, and the next ones under half a second. Nothing else changes: no column, no
-- function, no privilege, no row.

BEGIN;

CREATE INDEX IF NOT EXISTS stay_events_by_stay
    ON iam_v2.stay_events (stay_id, received_at)
 WHERE stay_id IS NOT NULL;

COMMIT;
