-- REVERSES 0033_terminal_delivery_and_registry. DESTROYS the retirement delivery records, the assignment
-- fetch log and the signed trust registry history. Disposable databases only.

BEGIN;

DROP TABLE IF EXISTS assignment_registry;
DROP TABLE IF EXISTS appliance_assignment_fetch_log;
ALTER TABLE appliance_signed_assignments DROP COLUMN IF EXISTS last_acked_version;
DROP TABLE IF EXISTS appliance_terminal_delivery;

COMMIT;
