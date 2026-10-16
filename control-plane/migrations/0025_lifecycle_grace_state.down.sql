-- REVERSES 0025_lifecycle_grace_state: the lifecycle CHECK goes back to the 0022 list (no 'grace'). Refused
-- while an appliance is in the 'grace' state, which the older list cannot represent.

BEGIN;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM appliances WHERE lifecycle_state = 'grace') THEN
    RAISE EXCEPTION '0025 down: an appliance has lifecycle_state=grace, which the 0022 constraint does not allow';
  END IF;
END $$;

ALTER TABLE appliances DROP CONSTRAINT IF EXISTS appliances_lifecycle_state_check;
ALTER TABLE appliances ADD CONSTRAINT appliances_lifecycle_state_check
  CHECK (lifecycle_state IN ('manufactured','installed_unenrolled','pending_enrollment','pending_approval',
    'claimed','assigned','licensed','online','offline','suspended','license_expired','revoked','decommissioned'));

COMMIT;
