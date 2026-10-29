-- 0060 -- A CAP ON WAITING APPLIANCES (AD-20)
--
-- Token-less self-registration lets anyone who can reach Central create a WAITING appliance row. Registration now
-- refuses a NEW row with 503 once this many appliances are waiting for activation, and raises one alert
-- (kind registration_cap_reached). Rows already waiting, and a known appliance re-registering, are never affected.
-- Nothing is pruned: deleting waiting rows is a separate Product-Owner choice.
--
-- A setting (System > Settings > Registration; OPERATIONAL_SETTINGS_POLICY): default 200 appliances, 10-5000,
-- changed with a reason and audited. Additive and idempotent. Nothing is granted: ctrlapi owns these objects.

BEGIN;

CREATE TABLE IF NOT EXISTS central_registration_settings (
  id                     boolean PRIMARY KEY DEFAULT true CHECK (id),
  max_waiting_appliances int NOT NULL DEFAULT 200 CHECK (max_waiting_appliances BETWEEN 10 AND 5000),
  updated_at             timestamptz NOT NULL DEFAULT now(),
  updated_by             text
);
INSERT INTO central_registration_settings (id) VALUES (true) ON CONFLICT DO NOTHING;
COMMENT ON TABLE central_registration_settings IS
  'Limits on unauthenticated appliance registration (0060): the most appliances that may wait for activation at once.';

COMMIT;
