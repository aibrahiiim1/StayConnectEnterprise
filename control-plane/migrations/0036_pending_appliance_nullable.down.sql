-- REVERSES 0036_pending_appliance_nullable: tenant_id, site_id and name are required again. Refused while any
-- appliance lacks one of them -- every waiting (token-less registered) appliance does, so this down needs those
-- appliances activated or deleted first.

BEGIN;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM appliances WHERE tenant_id IS NULL OR site_id IS NULL OR name IS NULL) THEN
    RAISE EXCEPTION '0036 down: appliances without a customer, site or name exist (waiting appliances); activate or delete them first';
  END IF;
END $$;

ALTER TABLE appliances
  ALTER COLUMN tenant_id SET NOT NULL,
  ALTER COLUMN site_id SET NOT NULL,
  ALTER COLUMN name SET NOT NULL;

COMMIT;
