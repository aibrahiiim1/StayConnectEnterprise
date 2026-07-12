DROP INDEX IF EXISTS stripe_events_received_at_idx;
DROP TABLE IF EXISTS stripe_events;
DROP INDEX IF EXISTS payments_tenant_status_idx;
DROP TABLE IF EXISTS payments;
DROP INDEX IF EXISTS stripe_accounts_tenant_enabled_idx;
DROP TABLE IF EXISTS stripe_accounts;
DELETE FROM schema_migrations WHERE version = '0018_stripe_payments';
