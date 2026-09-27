-- WHOSE CUSTOMER DATA A REGISTERING APPLIANCE STILL HOLDS.
--
-- Product-Owner rule: changing an appliance's customer is retire -> FACTORY-CLEAN -> new activation. An
-- identity reset keeps the site database but produces a new identity key, and a new key looks factory-clean.
-- So the appliance now reports, inside its signed registration (and its offline activation request), the
-- customer whose data it still holds: holds_customer_id. Central stores it here, overwriting it on every
-- registration and offline request import (NULL when the appliance reports none), and refuses to activate
-- the appliance for any other customer (409 holds_other_customer_data).
--
-- NO FOREIGN KEY, on purpose: the customer may since have been deleted from Central, and the appliance still
-- holds its data. A deleted customer is still "another customer".
--
-- No backfill: rows registered before this migration are NULL until the appliance registers again.

BEGIN;

ALTER TABLE appliances ADD COLUMN IF NOT EXISTS held_customer_id uuid NULL;

COMMENT ON COLUMN appliances.held_customer_id IS
  'Customer (tenant) whose data the appliance reported holding at its last registration or offline activation request; NULL = none. No FK: the customer may have been deleted. Activation for any other customer is refused.';

COMMIT;
