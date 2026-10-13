-- 0111 down -- drops the verification-window ledger. Any window still open in the kernel ends at its own
-- timeout; scd built before 0111 never reads the table.
BEGIN;
DROP TABLE IF EXISTS iam_v2.verification_access_grants;
COMMIT;
