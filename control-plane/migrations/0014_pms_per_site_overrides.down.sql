DROP INDEX IF EXISTS pms_providers_tenant_site_enabled_idx;
DROP INDEX IF EXISTS pms_providers_tenant_site_name_idx;
DROP INDEX IF EXISTS pms_providers_tenant_name_global_idx;
ALTER TABLE pms_providers
    ADD CONSTRAINT pms_providers_tenant_id_name_key UNIQUE (tenant_id, name);
ALTER TABLE pms_providers DROP COLUMN IF EXISTS site_id;
DELETE FROM schema_migrations WHERE version = '0014_pms_per_site_overrides';
