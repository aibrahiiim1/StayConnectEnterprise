-- 0041_license_per_appliance
--
-- Correct the License ownership invariant to PER-APPLIANCE.
--
-- The simple model binds one License to exactly one Appliance. Site is
-- organizational only and must NOT limit License count: multiple Appliances
-- under the same Site each hold their own current, Active License (with their
-- own concurrent limits, validity and grace).
--
-- 0040 mistakenly enforced one-current-per-SITE. This migration replaces the
-- per-site uniqueness with per-appliance uniqueness, keyed on the bound
-- appliance (appliance_ids[1] — the model issues exactly one appliance per
-- license). Legacy unbound (site-wide) licenses with empty appliance_ids are
-- left unconstrained by these indexes.

BEGIN;

-- Drop the wrong per-site invariants.
DROP INDEX IF EXISTS licenses_one_current_per_site;
DROP INDEX IF EXISTS licenses_site_version_unique;

-- Exactly one CURRENT (active/suspended) license per APPLIANCE.
CREATE UNIQUE INDEX IF NOT EXISTS licenses_one_current_per_appliance
    ON licenses ((appliance_ids[1]))
    WHERE status IN ('active','suspended') AND cardinality(appliance_ids) > 0;

-- Monotonic license_version is unique within an appliance's history.
CREATE UNIQUE INDEX IF NOT EXISTS licenses_appliance_version_unique
    ON licenses ((appliance_ids[1]), license_version)
    WHERE cardinality(appliance_ids) > 0;

INSERT INTO schema_migrations(version) VALUES ('0041_license_per_appliance') ON CONFLICT DO NOTHING;

COMMIT;
