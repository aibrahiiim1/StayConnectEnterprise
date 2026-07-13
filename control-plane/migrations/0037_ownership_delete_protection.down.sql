-- Revert ownership RESTRICT edges back to CASCADE (pre-0037 behaviour).
BEGIN;

ALTER TABLE sites DROP CONSTRAINT IF EXISTS sites_tenant_id_fkey;
ALTER TABLE sites ADD CONSTRAINT sites_tenant_id_fkey
    FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE;

ALTER TABLE appliances DROP CONSTRAINT IF EXISTS appliances_tenant_id_fkey;
ALTER TABLE appliances ADD CONSTRAINT appliances_tenant_id_fkey
    FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE;

ALTER TABLE appliances DROP CONSTRAINT IF EXISTS appliances_site_id_fkey;
ALTER TABLE appliances ADD CONSTRAINT appliances_site_id_fkey
    FOREIGN KEY (site_id) REFERENCES sites(id) ON DELETE CASCADE;

ALTER TABLE licenses DROP CONSTRAINT IF EXISTS licenses_tenant_id_fkey;
ALTER TABLE licenses ADD CONSTRAINT licenses_tenant_id_fkey
    FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE;

ALTER TABLE licenses DROP CONSTRAINT IF EXISTS licenses_site_id_fkey;
ALTER TABLE licenses ADD CONSTRAINT licenses_site_id_fkey
    FOREIGN KEY (site_id) REFERENCES sites(id) ON DELETE CASCADE;

ALTER TABLE tenant_subscriptions DROP CONSTRAINT IF EXISTS tenant_subscriptions_tenant_id_fkey;
ALTER TABLE tenant_subscriptions ADD CONSTRAINT tenant_subscriptions_tenant_id_fkey
    FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE;

ALTER TABLE appliance_bootstrap_tokens DROP CONSTRAINT IF EXISTS appliance_bootstrap_tokens_tenant_id_fkey;
ALTER TABLE appliance_bootstrap_tokens ADD CONSTRAINT appliance_bootstrap_tokens_tenant_id_fkey
    FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE;

ALTER TABLE appliance_bootstrap_tokens DROP CONSTRAINT IF EXISTS appliance_bootstrap_tokens_site_id_fkey;
ALTER TABLE appliance_bootstrap_tokens ADD CONSTRAINT appliance_bootstrap_tokens_site_id_fkey
    FOREIGN KEY (site_id) REFERENCES sites(id) ON DELETE CASCADE;

DROP INDEX IF EXISTS sites_status_idx;
ALTER TABLE sites DROP COLUMN IF EXISTS status;

DELETE FROM schema_migrations WHERE version = '0037_ownership_delete_protection';

COMMIT;
