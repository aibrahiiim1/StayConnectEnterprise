-- Phase A: license revocation is NOT natural expiry. Add the distinct 'grace'
-- commercial state and widen the lifecycle_state CHECK. The five license-driven
-- states are now all representable and distinct:
--   licensed | grace | license_expired | suspended | revoked
ALTER TABLE appliances DROP CONSTRAINT IF EXISTS appliances_lifecycle_state_check;
ALTER TABLE appliances ADD CONSTRAINT appliances_lifecycle_state_check
  CHECK (lifecycle_state IN ('manufactured','installed_unenrolled','pending_enrollment','pending_approval',
    'claimed','assigned','licensed','grace','online','offline','suspended','license_expired','revoked','decommissioned'));
