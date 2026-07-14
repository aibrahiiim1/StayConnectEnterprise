-- Revert to the (incorrect) per-site invariant from 0040.
BEGIN;
DROP INDEX IF EXISTS licenses_one_current_per_appliance;
DROP INDEX IF EXISTS licenses_appliance_version_unique;
CREATE UNIQUE INDEX IF NOT EXISTS licenses_one_current_per_site
    ON licenses (site_id) WHERE status IN ('active','suspended');
CREATE UNIQUE INDEX IF NOT EXISTS licenses_site_version_unique
    ON licenses (site_id, license_version);
DELETE FROM schema_migrations WHERE version = '0041_license_per_appliance';
COMMIT;
