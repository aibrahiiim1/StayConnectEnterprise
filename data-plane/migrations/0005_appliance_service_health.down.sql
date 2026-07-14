BEGIN;
DROP TABLE IF EXISTS appliance_boot_convergence;
DROP TABLE IF EXISTS appliance_recovery_events;
DROP TABLE IF EXISTS appliance_service_health;
DELETE FROM schema_migrations WHERE version = '0005_appliance_service_health';
COMMIT;
