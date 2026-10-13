-- 0113 — staff may clear a device's temporary-internet history, and read a live code to help a client
-- (Product-Owner request, 2026-10-12; contract §6.6, §6.7).
--
--   1. verification_access_grants gains cleared_at / cleared_by. A cleared row stays in the ledger (it is the
--      record of what the device used) but no longer counts towards the per-device / per-address limits or the
--      wait before another window. Only an ENDED row may be cleared: a live window is not touched.
--   2. otp_code_escrow holds a SEALED copy of each live code that was sent (AES-256-GCM, sealed and opened by scd
--      alone under its local key, domain "otp-code-escrow:v1", bound to tenant, site and challenge). It exists so
--      an operator holding the code-reveal permission can read a code to a client whose message did not arrive.
--      scd deletes the row when the code is used, and once the code has expired. No other role may read it.
--
-- Additive. The .down drops the table and the two columns.
BEGIN;

ALTER TABLE iam_v2.verification_access_grants
  ADD COLUMN IF NOT EXISTS cleared_at timestamptz,
  ADD COLUMN IF NOT EXISTS cleared_by text;

DO $c$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'verification_access_cleared_pair') THEN
    ALTER TABLE iam_v2.verification_access_grants
      ADD CONSTRAINT verification_access_cleared_pair CHECK ((cleared_at IS NULL) = (cleared_by IS NULL)),
      ADD CONSTRAINT verification_access_cleared_by_bounded CHECK (cleared_by IS NULL OR length(cleared_by) BETWEEN 1 AND 200),
      ADD CONSTRAINT verification_access_cleared_only_when_ended CHECK (cleared_at IS NULL OR ended_at IS NOT NULL);
  END IF;
END $c$;

CREATE TABLE IF NOT EXISTS iam_v2.otp_code_escrow (
  challenge_id   uuid PRIMARY KEY,
  tenant_id      uuid NOT NULL,
  site_id        uuid NOT NULL,
  key_id         text NOT NULL CHECK (key_id ~ '^[0-9a-f]{16}$'),
  nonce          bytea NOT NULL,
  ciphertext     bytea NOT NULL,
  cipher_version integer NOT NULL CHECK (cipher_version > 0),
  expires_at     timestamptz NOT NULL,
  created_at     timestamptz NOT NULL DEFAULT now(),
  CHECK (expires_at > created_at)
);
CREATE INDEX IF NOT EXISTS otp_code_escrow_expiry ON iam_v2.otp_code_escrow (expires_at);
COMMENT ON TABLE iam_v2.otp_code_escrow IS
  'A sealed copy of each live, sent one-time code, readable only by scd for an operator holding the code-reveal '
  'permission (contract §6.7). Deleted when the code is used or expires.';

DO $g$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_scd') THEN
    GRANT SELECT, INSERT, DELETE ON iam_v2.otp_code_escrow TO svc_scd;
  END IF;
END $g$;

COMMIT;
