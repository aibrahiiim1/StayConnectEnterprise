-- Reverses 0169. The operator reasons stored in the column are dropped with it (they are staff notes with a retention
-- period, not evidence the system relies on).
DROP TRIGGER IF EXISTS zy_scrub_operator_reason ON iam_v2.entitlement_transfers;
DROP FUNCTION IF EXISTS iam_v2.entitlement_transfers_scrub_operator_reason();
DELETE FROM iam_v2.scrub_registry WHERE table_name = 'iam_v2.entitlement_transfers' AND column_name = 'operator_reason';
ALTER TABLE iam_v2.entitlement_transfers DROP CONSTRAINT IF EXISTS entitlement_transfers_operator_reason_bounded;
ALTER TABLE iam_v2.entitlement_transfers DROP COLUMN IF EXISTS operator_reason;
