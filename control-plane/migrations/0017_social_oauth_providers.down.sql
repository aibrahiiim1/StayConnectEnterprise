DROP INDEX IF EXISTS social_oauth_providers_tenant_provider_enabled_idx;
DROP TABLE IF EXISTS social_oauth_providers;
DELETE FROM schema_migrations WHERE version = '0017_social_oauth_providers';
