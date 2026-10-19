-- 0053 down -- accounts are no longer locked after repeated wrong passwords; the counters and the two settings go.

BEGIN;

DROP FUNCTION IF EXISTS operator_signin_throttle_fail(text);
DROP TABLE IF EXISTS operator_signin_throttle;
ALTER TABLE central_security_settings DROP COLUMN IF EXISTS signin_lock_minutes;
ALTER TABLE central_security_settings DROP COLUMN IF EXISTS signin_lock_failures;

COMMIT;
