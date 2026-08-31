-- Remove the speed-allocation mode.
--
-- Every revision reverts to the single meaning the column replaced: the configured rate, per device. A SHARED
-- plan sold before the rollback would silently become PER_DEVICE, so a property running one must not roll this
-- back while such a revision is live.

BEGIN;

ALTER TABLE iam_v2.service_plan_revisions DROP CONSTRAINT IF EXISTS spr_speed_allocation_check;
ALTER TABLE iam_v2.service_plan_revisions DROP COLUMN IF EXISTS speed_allocation;

COMMIT;
