-- 0058 down -- the three indexes go; the prune and the appliance history scan again.

BEGIN;

DROP INDEX IF EXISTS audit_log_payload_appliance_ts;
DROP INDEX IF EXISTS audit_log_target_ts;
DROP INDEX IF EXISTS appliance_assignment_fetch_log_at;

COMMIT;
