-- Phase 5.6 — per-site PMS provider overrides.
--
-- A chain tenant often runs one logical "main-pms" across many sites but
-- with site-specific connection details (each hotel talks to its own
-- HGBU/FIAS endpoint). Rather than forcing distinct names per site, we
-- allow a row to scope to a specific site. At resolution time scd at
-- that site picks the site-scoped row over the tenant-wide one.

ALTER TABLE pms_providers
    ADD COLUMN IF NOT EXISTS site_id uuid REFERENCES sites(id) ON DELETE CASCADE;

-- Replace the old UNIQUE (tenant_id, name) with two partial uniques:
--   one tenant-wide row per name (site_id IS NULL)
--   one per-site row per name (site_id IS NOT NULL)
-- That way "main-pms" can exist both globally AND at a specific site.

ALTER TABLE pms_providers
    DROP CONSTRAINT IF EXISTS pms_providers_tenant_id_name_key;

CREATE UNIQUE INDEX IF NOT EXISTS pms_providers_tenant_name_global_idx
    ON pms_providers(tenant_id, name)
 WHERE site_id IS NULL;

CREATE UNIQUE INDEX IF NOT EXISTS pms_providers_tenant_site_name_idx
    ON pms_providers(tenant_id, site_id, name)
 WHERE site_id IS NOT NULL;

-- Index to make the "pick overrides for this site" query fast.
CREATE INDEX IF NOT EXISTS pms_providers_tenant_site_enabled_idx
    ON pms_providers(tenant_id, site_id) WHERE enabled = true;

INSERT INTO schema_migrations(version) VALUES ('0014_pms_per_site_overrides') ON CONFLICT DO NOTHING;
