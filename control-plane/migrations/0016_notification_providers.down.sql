DROP INDEX IF EXISTS notification_providers_tenant_channel_enabled_idx;
DROP TABLE IF EXISTS notification_providers;
DELETE FROM schema_migrations WHERE version = '0016_notification_providers';
