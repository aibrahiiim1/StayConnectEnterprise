-- 0060 -- TWO-STEP SIGN-IN LOCKS PER (OPERATOR, ADDRESS), WITH AN ACCOUNT-WIDE BACKSTOP (AD-6)
--
-- 0052 counted wrong second-step codes in operator_mfa.failed_codes alone, so anyone who knew an administrator's
-- password could lock that administrator's second step from anywhere for up to signin_lock_minutes (up to 24 h).
-- Now a wrong code is counted for the operator AND the client address it came from (this table); signin_lock_failures
-- of them lock the second step for that address only. The account-wide counter stays as a backstop and locks the
-- whole account only at four times that number across all addresses (internal/auth/mfa.go).
--
-- key_hash is an HMAC over the operator id and the address (as for operator_signin_throttle, 0056), so the table
-- holds no address. Additive: nothing existing is changed. Removing an operator removes its rows.

BEGIN;

CREATE TABLE IF NOT EXISTS operator_mfa_address_locks (
  operator_id     uuid NOT NULL REFERENCES operators(id) ON DELETE CASCADE,
  key_hash        text NOT NULL CHECK (key_hash ~ '^[0-9a-f]{64}$'),
  failures        int  NOT NULL DEFAULT 0 CHECK (failures >= 0),
  locked_until    timestamptz,
  last_failure_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (operator_id, key_hash)
);
CREATE INDEX IF NOT EXISTS operator_mfa_address_locks_last ON operator_mfa_address_locks (last_failure_at);
COMMENT ON TABLE operator_mfa_address_locks IS
  'Wrong second-step codes per operator AND client address (0060, AD-6). key_hash is an HMAC of both, so no address '
  'is held. signin_lock_failures of them lock that address for signin_lock_minutes; operator_mfa.failed_codes is the '
  'account-wide backstop at four times that.';

COMMIT;
