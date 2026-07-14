-- Tears down the entire site-local schema. Only sane on a scratch DB —
-- a production appliance drops the whole database instead.
BEGIN;
DROP TABLE IF EXISTS backup_records, sync_checkpoints, sync_outbox,
  tenant_effective_limits, payments, stripe_events, stripe_accounts,
  social_oauth_providers, notification_providers, pms_providers, pms_attempts,
  social_oauth_states, auth_otps, accounting_records, sessions, guests,
  vouchers, voucher_batches, ticket_templates, walled_garden_rules,
  operator_roles, operators, appliances, sites, audit_log, tenants,
  schema_migrations CASCADE;
COMMIT;
