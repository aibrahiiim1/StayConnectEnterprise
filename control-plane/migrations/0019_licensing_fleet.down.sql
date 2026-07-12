BEGIN;
DELETE FROM plan_limits WHERE key IN ('feature.paid_wifi','max_guest_access_plans');
DROP TABLE IF EXISTS fleet_telemetry_dedupe;
DROP TABLE IF EXISTS fleet_telemetry;
DROP TABLE IF EXISTS licenses;
DROP VIEW IF EXISTS commercial_plans;
COMMENT ON TABLE plans IS NULL;
DELETE FROM schema_migrations WHERE version = '0019_licensing_fleet';
COMMIT;
