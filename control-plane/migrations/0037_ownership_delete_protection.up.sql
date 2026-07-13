-- 0037_ownership_delete_protection
--
-- Enforce the ownership tree at the DATABASE level so an accidental SQL DELETE
-- or a direct API call can NEVER wipe a whole customer/site subtree. The
-- structural edges become ON DELETE RESTRICT: a parent cannot be deleted while
-- any child of these types exists. Application handlers surface a friendly
-- 409 first; these constraints are the last line of defence.
--
-- RESTRICT edges (parent delete blocked while children exist):
--   sites               -> tenants        (Customer cannot be deleted while Sites exist)
--   appliances          -> tenants        (Customer cannot be deleted while Appliances exist)
--   appliances          -> sites          (Site cannot be deleted while Appliances assigned)
--   licenses            -> tenants        (Licenses are retained commercial records)
--   licenses            -> sites          (Site cannot be deleted while Licenses exist)
--   tenant_subscriptions-> tenants        (Customer cannot be deleted while a Subscription exists)
--   appliance_bootstrap_tokens -> tenants (Enrollment tokens must be cleared first)
--   appliance_bootstrap_tokens -> sites
--
-- CASCADE is retained ONLY for tightly-owned technical leaf records beneath an
-- Appliance (see 0037 comment block at end) — those have no meaning without
-- their appliance and are cleaned up automatically when the appliance is deleted.

BEGIN;

-- sites -> tenants
ALTER TABLE sites DROP CONSTRAINT IF EXISTS sites_tenant_id_fkey;
ALTER TABLE sites ADD CONSTRAINT sites_tenant_id_fkey
    FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE RESTRICT;

-- appliances -> tenants
ALTER TABLE appliances DROP CONSTRAINT IF EXISTS appliances_tenant_id_fkey;
ALTER TABLE appliances ADD CONSTRAINT appliances_tenant_id_fkey
    FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE RESTRICT;

-- appliances -> sites
ALTER TABLE appliances DROP CONSTRAINT IF EXISTS appliances_site_id_fkey;
ALTER TABLE appliances ADD CONSTRAINT appliances_site_id_fkey
    FOREIGN KEY (site_id) REFERENCES sites(id) ON DELETE RESTRICT;

-- licenses -> tenants
ALTER TABLE licenses DROP CONSTRAINT IF EXISTS licenses_tenant_id_fkey;
ALTER TABLE licenses ADD CONSTRAINT licenses_tenant_id_fkey
    FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE RESTRICT;

-- licenses -> sites
ALTER TABLE licenses DROP CONSTRAINT IF EXISTS licenses_site_id_fkey;
ALTER TABLE licenses ADD CONSTRAINT licenses_site_id_fkey
    FOREIGN KEY (site_id) REFERENCES sites(id) ON DELETE RESTRICT;

-- tenant_subscriptions -> tenants
ALTER TABLE tenant_subscriptions DROP CONSTRAINT IF EXISTS tenant_subscriptions_tenant_id_fkey;
ALTER TABLE tenant_subscriptions ADD CONSTRAINT tenant_subscriptions_tenant_id_fkey
    FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE RESTRICT;

-- appliance_bootstrap_tokens -> tenants
ALTER TABLE appliance_bootstrap_tokens DROP CONSTRAINT IF EXISTS appliance_bootstrap_tokens_tenant_id_fkey;
ALTER TABLE appliance_bootstrap_tokens ADD CONSTRAINT appliance_bootstrap_tokens_tenant_id_fkey
    FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE RESTRICT;

-- appliance_bootstrap_tokens -> sites
ALTER TABLE appliance_bootstrap_tokens DROP CONSTRAINT IF EXISTS appliance_bootstrap_tokens_site_id_fkey;
ALTER TABLE appliance_bootstrap_tokens ADD CONSTRAINT appliance_bootstrap_tokens_site_id_fkey
    FOREIGN KEY (site_id) REFERENCES sites(id) ON DELETE RESTRICT;

-- Sites gain an archive lifecycle (mirrors tenants.status).
ALTER TABLE sites ADD COLUMN IF NOT EXISTS status text NOT NULL DEFAULT 'active';
CREATE INDEX IF NOT EXISTS sites_status_idx ON sites(tenant_id, status);

INSERT INTO schema_migrations(version) VALUES ('0037_ownership_delete_protection') ON CONFLICT DO NOTHING;

COMMIT;

-- Documented remaining CASCADE relationships (intentional — tightly-owned leaves):
--   Beneath an Appliance (deleted with it):
--     appliance_assignments, appliance_signed_assignments, appliance_certificates,
--     appliance_certificate_requests, appliance_commands, appliance_lifecycle_events,
--     appliance_terminal_delivery, appliance_update_assignments,
--     offline_activation_packages, networks
--     (appliance_security_alerts, appliance_bootstrap_tokens.consumed_by_appliance,
--      auth_otps, pms_attempts, social_oauth_states -> SET NULL, so alert/history rows survive)
--   Beneath a Tenant, only reached by a PERMANENT delete of an already-empty
--   customer (no sites/appliances/licenses/subscription/tokens), cascade cleans
--   tenant-scoped configuration + spent history that is meaningless without the
--   tenant: operators, operator_roles, notification_providers, idp_providers,
--   social_oauth_providers, stripe_accounts, tenant_limit_overrides,
--   subscription_events, invoices, guests/sessions/vouchers/voucher_batches/
--   ticket_templates (all zero for an empty customer). The immutable audit_log
--   has NO foreign key and therefore survives permanent deletion.
