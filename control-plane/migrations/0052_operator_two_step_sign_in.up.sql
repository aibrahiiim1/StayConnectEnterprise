-- 0052 -- CENTRAL CONSOLE SIGN-IN NEEDS AN AUTHENTICATOR CODE, AND SESSIONS END WHEN IDLE OR OLD
-- (GDPR remediation plan C10; Product-Owner decision D-C10, 2026-10-17: an authenticator app (TOTP) as the second
--  step for every Central account -- platform operators and customer users alike -- set up at the next sign-in,
--  ten single-use recovery codes, an administrator can reset a colleague's second step, idle logout after 30
--  minutes and a session limit of 12 hours, both settings; NIST SP 800-63B / OWASP ASVS. The appliance's half is
--  data-plane migration 0132.)
--
--   operator_mfa                 one authenticator per operator: the TOTP secret sealed by ctrlapi (AES-256-GCM
--                                under CTRLAPI_OPERATOR_MFA_KEY, bound to the operator), never in the clear;
--                                last_used_step stops a code being accepted twice; failed_codes / locked_until
--                                throttle wrong codes (five, then fifteen minutes).
--   operator_mfa_recovery_codes  ten single-use codes, stored as HMAC-SHA256 under the same key.
--   operator_signin_pending      the five minutes between a correct password and the code: a SHA-256 of the
--                                token in the operator's cookie, never the token.
--   central_security_settings    gains operator_idle_minutes (5-240, default 30). The absolute limit's default
--                                becomes 12 hours, and a value nobody ever changed (still the 24 of 0051) moves
--                                to 12; one an operator chose is kept.
--
-- Additive otherwise. Removing an authenticator (a reset) deletes its rows: they are credentials, not history;
-- the history is audit_log (user.mfa_reset).

BEGIN;

CREATE TABLE IF NOT EXISTS operator_mfa (
  operator_id    uuid PRIMARY KEY REFERENCES operators (id) ON DELETE CASCADE,
  key_id         text NOT NULL CHECK (length(key_id) BETWEEN 1 AND 64),
  nonce          bytea NOT NULL,
  ciphertext     bytea NOT NULL,
  cipher_version int NOT NULL CHECK (cipher_version = 1),
  created_at     timestamptz NOT NULL DEFAULT now(),
  confirmed_at   timestamptz,
  last_used_step bigint,
  failed_codes   int NOT NULL DEFAULT 0 CHECK (failed_codes >= 0),
  locked_until   timestamptz
);
COMMENT ON TABLE operator_mfa IS
  'A Central console operator''s authenticator (0052, GDPR plan C10): the TOTP secret sealed by ctrlapi, never in '
  'the clear. confirmed_at is set once proven with a code; last_used_step stops a code being reused; failed_codes '
  'and locked_until throttle wrong codes.';

CREATE TABLE IF NOT EXISTS operator_mfa_recovery_codes (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  operator_id uuid NOT NULL REFERENCES operators (id) ON DELETE CASCADE,
  code_mac    text NOT NULL CHECK (code_mac ~ '^[0-9a-f]{64}$'),
  created_at  timestamptz NOT NULL DEFAULT now(),
  used_at     timestamptz,
  UNIQUE (operator_id, code_mac)
);
COMMENT ON TABLE operator_mfa_recovery_codes IS
  'Single-use recovery codes (0052, GDPR plan C10): HMAC-SHA256 of the code under ctrlapi''s key, never the code.';

CREATE TABLE IF NOT EXISTS operator_signin_pending (
  token_hash  text PRIMARY KEY CHECK (token_hash ~ '^[0-9a-f]{64}$'),
  operator_id uuid NOT NULL REFERENCES operators (id) ON DELETE CASCADE,
  enrol       boolean NOT NULL,
  expires_at  timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS operator_signin_pending_expiry ON operator_signin_pending (expires_at);
COMMENT ON TABLE operator_signin_pending IS
  'A correct password waiting for its authenticator code (0052): at most five minutes, SHA-256 of the cookie token.';

ALTER TABLE central_security_settings
  ADD COLUMN IF NOT EXISTS operator_idle_minutes int NOT NULL DEFAULT 30
    CHECK (operator_idle_minutes BETWEEN 5 AND 240);
ALTER TABLE central_security_settings ALTER COLUMN operator_session_max_hours SET DEFAULT 12;
UPDATE central_security_settings
   SET operator_session_max_hours = 12, updated_at = now(), updated_by = 'migration 0052 (GDPR plan C10)'
 WHERE id AND operator_session_max_hours = 24 AND updated_by IS NULL;
COMMENT ON TABLE central_security_settings IS
  'Central operator-security settings (one row). operator_session_max_hours: absolute console session lifetime in '
  'hours since sign-in (default 12, 1..168). operator_idle_minutes: a session with no activity for this long ends '
  '(default 30, 5..240; 0052). Changes are audited (audit_log action settings.security_updated).';

COMMIT;
