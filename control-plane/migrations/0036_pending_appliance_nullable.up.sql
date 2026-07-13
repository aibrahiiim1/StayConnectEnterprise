-- Token-less registration: a factory-clean appliance self-registers and appears
-- as Pending Activation BEFORE any customer assignment, so tenant/site are not
-- yet known. Allow them to be NULL until the operator activates (assign sets
-- them). name likewise defaults to the serial and may be unset at registration.
ALTER TABLE appliances
  ALTER COLUMN tenant_id DROP NOT NULL,
  ALTER COLUMN site_id DROP NOT NULL,
  ALTER COLUMN name DROP NOT NULL;
