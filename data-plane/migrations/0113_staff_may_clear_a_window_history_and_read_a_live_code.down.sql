-- 0113 down -- drops the sealed code copies and the clearing columns. Cleared rows count again towards the
-- limits; scd built before 0113 never reads either.
BEGIN;
DROP TABLE IF EXISTS iam_v2.otp_code_escrow;
ALTER TABLE iam_v2.verification_access_grants
  DROP CONSTRAINT IF EXISTS verification_access_cleared_only_when_ended,
  DROP CONSTRAINT IF EXISTS verification_access_cleared_by_bounded,
  DROP CONSTRAINT IF EXISTS verification_access_cleared_pair,
  DROP COLUMN IF EXISTS cleared_by,
  DROP COLUMN IF EXISTS cleared_at;
COMMIT;
