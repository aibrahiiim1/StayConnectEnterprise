-- 0058 -- THE FETCH-LOG PRUNE AND AN APPLIANCE'S HISTORY USE INDEXES (performance audit D9-M1, D9-L1)
--
-- 1. appliance_assignment_fetch_log is pruned hourly by `at < now() - 30 days`. Its only index leads with
--    appliance_id, so every prune read the whole table (51-77 s at 200 appliances, measured). An index on `at`
--    makes the prune read only the expired rows (23-68 ms, measured).
-- 2. The appliance detail page reads its history from audit_log by (target_type, target_id) or by
--    payload->>'appliance_id', newest first (internal/api/fleet.go applianceEvents, one UNION ALL branch each).
--    Without these indexes a quiet appliance's page walked the whole audit log backwards. audit_log is a
--    hypertable: each chunk gets its own copy of the index.
--
-- Additive: indexes only, no data changed. Building them takes a lock that blocks writes to the table for the
-- length of the build; on a large Central run it in a quiet period.

BEGIN;

CREATE INDEX IF NOT EXISTS appliance_assignment_fetch_log_at ON appliance_assignment_fetch_log (at);

CREATE INDEX IF NOT EXISTS audit_log_target_ts ON audit_log (target_type, target_id, ts DESC);
CREATE INDEX IF NOT EXISTS audit_log_payload_appliance_ts ON audit_log ((payload->>'appliance_id'), ts DESC);

COMMIT;
