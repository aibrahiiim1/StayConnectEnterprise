-- 0109 -- A SESSION SAYS HOW THE CLIENT SIGNED IN.
--
-- The auth context records the METHOD -- which authority decided (OTP, SOCIAL, VOUCHER, ACCOUNT, PMS, OPEN) --
-- and the session copied a coarser word still (PRINCIPAL for every email, phone or social sign-in). The
-- question the Admin Console's sessions screen is asked is "how did this client sign in?", and for a Client
-- the honest answers are "an emailed code", "a WhatsApp code", "Google": the channel or provider, which was
-- recorded nowhere. Found on PRE-LIVE by the Product Owner, 2026-10-11.
--
-- Two nullable label columns, written by scd and read back by scd when it opens the session; edged keeps its
-- column-level grant on auth_contexts (0083) and reads the label from the SESSION it already reads, never
-- from here. A label is a short token (iamv2/credential.go), never a secret and never an identity factor.
--
-- Additive: no row is touched; a context minted before this migration simply has no credential, and the
-- session opened from it falls back to the authority's name exactly as before.

BEGIN;

ALTER TABLE iam_v2.auth_contexts ADD COLUMN IF NOT EXISTS credential text;
ALTER TABLE iam_v2.auth_contexts DROP CONSTRAINT IF EXISTS ac_credential_label;
ALTER TABLE iam_v2.auth_contexts ADD CONSTRAINT ac_credential_label
  CHECK (credential IS NULL OR credential ~ '^[A-Z0-9_]{1,32}$');
COMMENT ON COLUMN iam_v2.auth_contexts.credential IS
  'How the client proved themselves for THIS context, as a label the operator reads: EMAIL_OTP, SMS_OTP, '
  'WHATSAPP_OTP, SOCIAL_<provider>, VOUCHER, ACCOUNT, OPEN. Copied onto sessions.credential_method at '
  'activation. NULL = minted before 0109.';

ALTER TABLE iam_v2.principal_device_credentials ADD COLUMN IF NOT EXISTS credential text;
ALTER TABLE iam_v2.principal_device_credentials DROP CONSTRAINT IF EXISTS principal_device_credentials_label;
ALTER TABLE iam_v2.principal_device_credentials ADD CONSTRAINT principal_device_credentials_label
  CHECK (credential IS NULL OR credential ~ '^[A-Z0-9_]{1,32}$');
COMMENT ON COLUMN iam_v2.principal_device_credentials.credential IS
  'The credential the device proved when it was remembered (EMAIL_OTP, SOCIAL_GOOGLE, ...), so a resumed '
  'sign-in is reported as what it originally was. NULL = issued before 0109.';

COMMIT;
