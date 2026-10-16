-- REVERSES 0028_appliance_updates. The update agent was removed from the product (CLAUDE.md §0E); the table
-- may already be gone.

BEGIN;
DROP TABLE IF EXISTS appliance_update_assignments;
COMMIT;
