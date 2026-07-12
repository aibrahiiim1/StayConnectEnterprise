-- Assignment-signing key lifecycle: active -> verify_only -> revoked.
--
-- 'retired' collapsed two very different ideas ("stop signing with it" and "stop
-- trusting it") into one flag, which can STRAND an appliance: a box still holding
-- an assignment signed by the old key must be able to reboot and re-verify it
-- while the fleet migrates. verify_only is that safe middle state.
ALTER TABLE assignment_signing_keys DROP CONSTRAINT IF EXISTS assignment_signing_keys_state_check;

-- Migrate any legacy 'retired' rows to the safe middle state rather than to
-- 'revoked' (which would refuse verification and could strand appliances).
UPDATE assignment_signing_keys SET state = 'verify_only' WHERE state = 'retired';

ALTER TABLE assignment_signing_keys ADD CONSTRAINT assignment_signing_keys_state_check
    CHECK (state IN ('active','verify_only','revoked'));

ALTER TABLE assignment_signing_keys ADD COLUMN IF NOT EXISTS verify_only_at timestamptz;
ALTER TABLE assignment_signing_keys ADD COLUMN IF NOT EXISTS revoked_at     timestamptz;
ALTER TABLE assignment_signing_keys ADD COLUMN IF NOT EXISTS emergency      boolean NOT NULL DEFAULT false;

-- How many CURRENT assignments still depend on each signer. Revoking a key that
-- still has dependants would strand exactly those appliances, so the API refuses
-- it unless an explicit emergency compromise override is supplied.
CREATE OR REPLACE VIEW assignment_signer_usage AS
SELECT k.key_id,
       k.state,
       COUNT(a.appliance_id) AS current_assignments
  FROM assignment_signing_keys k
  LEFT JOIN appliance_signed_assignments a
         ON a.signed_doc->>'signer_key_id' = k.key_id
 GROUP BY k.key_id, k.state;
