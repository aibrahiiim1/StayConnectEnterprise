-- Re-add the original kind CHECK (without service_health) — for completeness.
BEGIN;
ALTER TABLE fleet_telemetry ADD CONSTRAINT fleet_telemetry_kind_check
  CHECK (kind IN ('heartbeat','health','usage','auth_counts','pms_health',
                  'license_ack','backup','sync','update_progress'));
DELETE FROM schema_migrations WHERE version = '0042_service_health_telemetry';
COMMIT;
