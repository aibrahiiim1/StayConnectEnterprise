-- Revert simple-license-model schema additions. Data in the added columns is
-- dropped; plan retirement is NOT auto-reverted (operator decision).
BEGIN;
DROP INDEX IF EXISTS licenses_one_current_per_site;
DROP INDEX IF EXISTS licenses_site_version_unique;
ALTER TABLE licenses DROP COLUMN IF EXISTS license_version;
ALTER TABLE licenses DROP COLUMN IF EXISTS max_concurrent_online_guests;
ALTER TABLE licenses DROP COLUMN IF EXISTS grace_period_days;
ALTER TABLE licenses DROP COLUMN IF EXISTS supersedes_license_id;
ALTER TABLE licenses DROP COLUMN IF EXISTS valid_from;
DELETE FROM schema_migrations WHERE version = '0040_simple_license_model';
COMMIT;
