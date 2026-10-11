-- 0110 down -- drops the device binding table. A code issued meanwhile verifies from any device again, as it
-- did before 0110, for the ten minutes it lives.
BEGIN;
DROP TABLE IF EXISTS iam_v2.otp_device_bindings;
COMMIT;
