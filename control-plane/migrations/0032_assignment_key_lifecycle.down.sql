-- REVERSES 0032_assignment_key_lifecycle: back to the two-state (active | retired) signing-key model. Both
-- verify_only and revoked become 'retired' -- the older model cannot tell "stop signing" from "stop trusting",
-- which is exactly why 0032 exists.

BEGIN;

DROP VIEW IF EXISTS assignment_signer_usage;
ALTER TABLE assignment_signing_keys DROP CONSTRAINT IF EXISTS assignment_signing_keys_state_check;
UPDATE assignment_signing_keys SET state = 'retired', retired_at = COALESCE(retired_at, revoked_at, verify_only_at, now())
 WHERE state IN ('verify_only','revoked');
ALTER TABLE assignment_signing_keys ADD CONSTRAINT assignment_signing_keys_state_check
  CHECK (state IN ('active','retired'));
ALTER TABLE assignment_signing_keys DROP COLUMN IF EXISTS emergency;
ALTER TABLE assignment_signing_keys DROP COLUMN IF EXISTS revoked_at;
ALTER TABLE assignment_signing_keys DROP COLUMN IF EXISTS verify_only_at;

COMMIT;
