BEGIN;
ALTER TABLE appliance_signed_assignments DROP CONSTRAINT IF EXISTS appliance_signed_assignments_state_check;
ALTER TABLE appliance_signed_assignments ADD CONSTRAINT appliance_signed_assignments_state_check
    CHECK (state = ANY (ARRAY['assigned','unassigned','revoked']));
DELETE FROM schema_migrations WHERE version = '0038_signed_assignment_decommissioned_state';
COMMIT;
