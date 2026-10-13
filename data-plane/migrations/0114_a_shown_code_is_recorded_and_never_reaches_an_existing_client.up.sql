-- 0114 — a code shown by staff is recorded, so the sign-in it completes is known to be staff-assisted
-- (Product-Owner decision 2026-10-12; contract §6.7).
--
-- One insert-only row per challenge whose code an operator revealed. scd writes it BEFORE returning the code, and
-- reads it when the code is verified: a staff-assisted verification may create a NEW Client (its factor marked
-- staff_assisted, no email-domain group, no remembered device) but never signs in to an existing Client. The row
-- names the operator; edged's audit record carries the reason. scd prunes rows a day after the reveal, as it
-- does the device bindings (0110).
--
-- Additive. The .down drops the table.
BEGIN;

CREATE TABLE IF NOT EXISTS iam_v2.otp_code_reveals (
  challenge_id uuid PRIMARY KEY,
  tenant_id    uuid NOT NULL,
  site_id      uuid NOT NULL,
  revealed_at  timestamptz NOT NULL DEFAULT now(),
  revealed_by  text NOT NULL CHECK (length(revealed_by) BETWEEN 1 AND 200)
);
CREATE INDEX IF NOT EXISTS otp_code_reveals_age ON iam_v2.otp_code_reveals (revealed_at);
COMMENT ON TABLE iam_v2.otp_code_reveals IS
  'Challenges whose live code an operator revealed (contract §6.7). A verification of such a code is '
  'staff-assisted: it may create a new Client marked staff_assisted, never sign in to an existing one.';

DO $g$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_scd') THEN
    GRANT SELECT, INSERT, DELETE ON iam_v2.otp_code_reveals TO svc_scd;
  END IF;
END $g$;

COMMIT;
