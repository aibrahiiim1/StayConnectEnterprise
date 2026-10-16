-- REVERSES 0026_appliance_commands. The command channel was removed from the product (CLAUDE.md §0E); the
-- table may already be gone.

BEGIN;
DROP TABLE IF EXISTS appliance_commands;
COMMIT;
