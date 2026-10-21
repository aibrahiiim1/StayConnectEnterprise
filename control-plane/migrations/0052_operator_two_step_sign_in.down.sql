-- 0052 down -- the Central console signs in with a password alone again.
--
-- The authenticator, recovery-code and pending-sign-in tables are DROPPED: they are credentials, and a rollback
-- that kept them would leave sealed secrets nobody uses. Every operator sets up a new authenticator if 0052 is
-- applied again. The idle setting is removed; the absolute limit keeps its current value (12 or what an operator
-- chose) and only its default returns to 0051's 24.

BEGIN;

DROP TABLE IF EXISTS operator_signin_pending;
DROP TABLE IF EXISTS operator_mfa_recovery_codes;
DROP TABLE IF EXISTS operator_mfa;
ALTER TABLE central_security_settings DROP COLUMN IF EXISTS operator_idle_minutes;
ALTER TABLE central_security_settings ALTER COLUMN operator_session_max_hours SET DEFAULT 24;

COMMIT;
