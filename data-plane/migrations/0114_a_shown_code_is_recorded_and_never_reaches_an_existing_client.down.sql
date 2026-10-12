-- 0114 down -- drops the reveal record. scd built before 0114 never reads it; a verification of a shown code is
-- then no longer known to be staff-assisted.
BEGIN;
DROP TABLE IF EXISTS iam_v2.otp_code_reveals;
COMMIT;
