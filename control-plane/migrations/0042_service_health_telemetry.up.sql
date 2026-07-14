-- 0042_service_health_telemetry
--
-- Appliance service-health telemetry: a new fleet_telemetry kind carrying the
-- appliance health supervisor's sanitized per-service state. The kind allowlist
-- is enforced in application code (fleet.allowedKinds), so drop the brittle
-- table-level CHECK that would otherwise reject every new kind and require a
-- migration each time. Also index the latest-service_health lookup used by the
-- Fleet list and add a dedicated alert kind convention (appliance_security_alerts
-- has a free-form kind, so no schema change is needed there).

BEGIN;

DO $$
DECLARE cname text;
BEGIN
  SELECT conname INTO cname FROM pg_constraint
   WHERE conrelid = 'fleet_telemetry'::regclass AND contype = 'c'
     AND pg_get_constraintdef(oid) ILIKE '%kind%';
  IF cname IS NOT NULL THEN
    EXECUTE format('ALTER TABLE fleet_telemetry DROP CONSTRAINT %I', cname);
  END IF;
END $$;

INSERT INTO schema_migrations(version) VALUES ('0042_service_health_telemetry')
ON CONFLICT DO NOTHING;

COMMIT;
