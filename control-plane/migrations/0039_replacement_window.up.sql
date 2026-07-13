-- 0039_replacement_window
--
-- Bounded replacement overlap window. When an appliance is put into replacement,
-- it may stay licensed only until this deadline; the outgoing appliance is
-- auto-terminated when its replacement becomes Active, or an operational alert is
-- raised if the window elapses first (requiring an explicit operator decision).
-- (replacement_of / replaced_by / replacement_pending already exist since 0024.)

ALTER TABLE appliances ADD COLUMN IF NOT EXISTS replacement_deadline timestamptz;

INSERT INTO schema_migrations(version) VALUES ('0039_replacement_window') ON CONFLICT DO NOTHING;
