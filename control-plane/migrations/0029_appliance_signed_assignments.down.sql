-- REVERSES 0029_appliance_signed_assignments. DESTROYS every signed assignment and its history: appliances keep
-- the documents they hold, but Central loses the version sequence they are checked against. Disposable
-- databases only.

BEGIN;
DROP TABLE IF EXISTS appliance_assignment_history;
DROP TABLE IF EXISTS appliance_signed_assignments;
COMMIT;
