-- 0110 -- A ONE-TIME CODE IS BOUND TO THE DEVICE THAT ASKED FOR IT.
--
-- A code was single-use but not device-bound: any device on the client network that obtained the code -- or
-- the email's tap-to-connect link, which carries the same code -- could verify it as that identity, and the
-- captive portal speaks plain HTTP on the client LAN. Reviewed 2026-10-11 at the Product Owner's request.
--
-- The challenge store (public.auth_otps) is a public-schema table owned by the cluster role, which the
-- site migration runner does not alter (the 0107 smtp-kind precedent). The binding therefore lives beside the
-- other device facts in iam_v2: one row per challenge, the MAC the kernel resolved for the requesting device,
-- written at issue by scd and compared at verify. A challenge with no row (issued before this migration)
-- verifies as before for the ten minutes it lives; after that every code has a row.
--
-- Additive: one table, no existing row touched.

BEGIN;

CREATE TABLE IF NOT EXISTS iam_v2.otp_device_bindings (
  challenge_id uuid PRIMARY KEY,
  tenant_id    uuid NOT NULL,
  device_mac   macaddr NOT NULL,
  issued_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS otp_device_bindings_issued ON iam_v2.otp_device_bindings (issued_at);
COMMENT ON TABLE iam_v2.otp_device_bindings IS
  'The device (kernel-resolved MAC) a one-time code was issued to. scd writes a row at issue and refuses a '
  'verification from any other device (internal/otp VerifyFrom). Rows are pruned a day after issue; the code '
  'itself lives ten minutes. MAC identifies the device only -- never a person.';

DO $g$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_scd') THEN
    GRANT SELECT, INSERT, DELETE ON iam_v2.otp_device_bindings TO svc_scd;
  END IF;
END $g$;

COMMIT;
