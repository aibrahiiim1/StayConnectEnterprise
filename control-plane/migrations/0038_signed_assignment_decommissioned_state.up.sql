-- 0038_signed_assignment_decommissioned_state
--
-- The assignment state model (internal/assignment) has always included
-- 'decommissioned' as a terminal Clears() state, but the CHECK constraint on
-- appliance_signed_assignments omitted it — so the /decommission lifecycle path
-- failed to persist its signed terminal assignment (503, license left active).
-- Align the constraint with the state model so decommission works end to end.

BEGIN;

ALTER TABLE appliance_signed_assignments DROP CONSTRAINT IF EXISTS appliance_signed_assignments_state_check;
ALTER TABLE appliance_signed_assignments ADD CONSTRAINT appliance_signed_assignments_state_check
    CHECK (state = ANY (ARRAY['assigned','unassigned','revoked','decommissioned']));

INSERT INTO schema_migrations(version) VALUES ('0038_signed_assignment_decommissioned_state') ON CONFLICT DO NOTHING;

COMMIT;
