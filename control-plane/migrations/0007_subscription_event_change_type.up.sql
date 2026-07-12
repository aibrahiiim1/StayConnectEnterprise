-- Add change_type to subscription_events. Only plan_changed events populate
-- it; other event types leave it NULL. Values:
--   upgrade    — moving to a higher tier (or higher monthly-equivalent price)
--   downgrade  — moving to a lower tier / lower monthly-equivalent price
--   lateral    — same tier (e.g. monthly ↔ yearly of same product)

ALTER TABLE subscription_events
    ADD COLUMN IF NOT EXISTS change_type text
        CHECK (change_type IS NULL OR change_type IN ('upgrade','downgrade','lateral'));

CREATE INDEX IF NOT EXISTS subscription_events_change_type_idx
    ON subscription_events(tenant_id, change_type)
 WHERE change_type IS NOT NULL;

INSERT INTO schema_migrations(version) VALUES ('0007_subscription_event_change_type') ON CONFLICT DO NOTHING;
