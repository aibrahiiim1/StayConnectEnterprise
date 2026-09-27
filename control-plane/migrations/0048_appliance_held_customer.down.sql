-- REVERSES 0048_appliance_held_customer. The column and what appliances reported in it are dropped; with it
-- goes Central's refusal to activate an appliance that still holds another customer's data.

BEGIN;

ALTER TABLE appliances DROP COLUMN IF EXISTS held_customer_id;

COMMIT;
