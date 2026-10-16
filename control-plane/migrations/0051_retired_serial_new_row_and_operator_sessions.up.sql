-- A RETIRED APPLIANCE'S SERIAL MAY COME BACK AS A NEW APPLIANCE; OPERATOR SESSIONS END WHEN ACCESS DOES.
--
-- 1. appliances.serial is unique only among appliances that are NOT retired. Re-registering a retired
--    (revoked / decommissioned) box after a factory reset now creates a FRESH appliance row: the previous
--    customer's licence history, audit trail and security alerts stay with the old, retired row and are never
--    shown to the next customer. A serial still in use (waiting or activated) stays unique.
--
-- 2. operators.sessions_not_before: every console session created before it is dead. Disabling, demoting,
--    removing an operator or resetting their password moves it to now(), so their open sessions stop at the
--    next request instead of living out the 12-hour sliding lifetime. operators.last_login_at is the last
--    successful console sign-in (the Team page showed "Never" for everyone).
--
-- 3. central_security_settings: the ABSOLUTE operator session lifetime (hours since sign-in, regardless of
--    activity). Default 24, bounded 1..168. Changed through the console; every change is audited.
--
-- 4. licenses.revocation_delivered_at: when the appliance's authenticated licence fetch last carried this
--    revoked licence id. The appliance persists every revocation it is told about, so after that moment the
--    row is no longer needed for the revocation to hold. Deleting a site is refused while a revocation of a
--    still-registered appliance has not been delivered.
--
-- 5. appliance_certificates.superseded_at: when a rotation replaced the certificate. A superseded certificate
--    is accepted on the appliance mTLS listener only for a short overlap; then it is recorded as revoked.

BEGIN;

ALTER TABLE appliances DROP CONSTRAINT IF EXISTS appliances_serial_key;
CREATE UNIQUE INDEX IF NOT EXISTS appliances_serial_live_uniq
  ON appliances (serial) WHERE lifecycle_state NOT IN ('revoked','decommissioned');
CREATE INDEX IF NOT EXISTS appliances_serial_idx ON appliances (serial);
COMMENT ON INDEX appliances_serial_live_uniq IS
  'One live (waiting or activated) appliance per serial. Retired rows keep their serial as history; a re-registered retired box gets a new row.';

ALTER TABLE operators ADD COLUMN IF NOT EXISTS sessions_not_before timestamptz;
ALTER TABLE operators ADD COLUMN IF NOT EXISTS last_login_at timestamptz;
COMMENT ON COLUMN operators.sessions_not_before IS
  'Console sessions created before this instant are refused. Set to now() when the operator is disabled, demoted, removed or has the password reset.';
COMMENT ON COLUMN operators.last_login_at IS 'Last successful console sign-in.';

CREATE TABLE IF NOT EXISTS central_security_settings (
  id                          boolean PRIMARY KEY DEFAULT true CHECK (id),
  operator_session_max_hours  int NOT NULL DEFAULT 24
                                CHECK (operator_session_max_hours BETWEEN 1 AND 168),
  updated_at                  timestamptz NOT NULL DEFAULT now(),
  updated_by                  text
);
INSERT INTO central_security_settings DEFAULT VALUES ON CONFLICT DO NOTHING;
COMMENT ON TABLE central_security_settings IS
  'Central operator-security settings (one row). operator_session_max_hours: absolute console session lifetime in hours since sign-in (default 24, 1..168). Changes are audited (audit_log action settings.security_updated).';

ALTER TABLE licenses ADD COLUMN IF NOT EXISTS revocation_delivered_at timestamptz;
COMMENT ON COLUMN licenses.revocation_delivered_at IS
  'When an authenticated licence fetch by the appliance last carried this revoked licence id (the appliance persists revocations).';

ALTER TABLE appliance_certificates ADD COLUMN IF NOT EXISTS superseded_at timestamptz;
COMMENT ON COLUMN appliance_certificates.superseded_at IS
  'When a rotation superseded this certificate. Accepted on the mTLS listener only during the rotation overlap.';

COMMIT;
