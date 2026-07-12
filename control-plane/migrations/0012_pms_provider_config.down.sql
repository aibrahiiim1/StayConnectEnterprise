DROP INDEX IF EXISTS pms_providers_tenant_enabled_idx;
DROP TABLE IF EXISTS pms_providers;
DELETE FROM schema_migrations WHERE version = '0012_pms_provider_config';
