-- 0070 down -- second-step locks are account-wide only again (0052). Running per-address locks end.
BEGIN;
DROP TABLE IF EXISTS operator_mfa_address_locks;
COMMIT;
