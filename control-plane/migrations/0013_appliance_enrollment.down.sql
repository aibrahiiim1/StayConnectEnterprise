ALTER TABLE appliances DROP COLUMN IF EXISTS identity_verified_at;
DROP INDEX IF EXISTS appliance_bootstrap_tokens_tenant_idx;
DROP TABLE IF EXISTS appliance_bootstrap_tokens;
DELETE FROM schema_migrations WHERE version = '0013_appliance_enrollment';
