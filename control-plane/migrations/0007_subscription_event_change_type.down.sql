DROP INDEX IF EXISTS subscription_events_change_type_idx;
ALTER TABLE subscription_events DROP COLUMN IF EXISTS change_type;
DELETE FROM schema_migrations WHERE version = '0007_subscription_event_change_type';
