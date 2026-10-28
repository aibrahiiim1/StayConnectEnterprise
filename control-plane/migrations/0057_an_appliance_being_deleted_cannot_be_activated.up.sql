-- 0057 -- AN APPLIANCE BEING DELETED CANNOT BE ACTIVATED (audit Phase 1 M8)
--
-- Deleting an appliance revokes its certificate and licence and releases its portal hostname (DNS and ACME
-- calls) before the row is deleted, and those steps cannot all run in one transaction. deletion_started_at is
-- set, under the row lock, before the first of them; activation refuses an appliance that carries it, and a
-- failed release clears it again. Additive: one nullable column, no data changed.

BEGIN;

ALTER TABLE appliances ADD COLUMN IF NOT EXISTS deletion_started_at timestamptz;
COMMENT ON COLUMN appliances.deletion_started_at IS
  'Set while DELETE /cloud/v1/appliances/{id} is releasing the appliance (0057); activation refuses it. NULL otherwise.';

COMMIT;
