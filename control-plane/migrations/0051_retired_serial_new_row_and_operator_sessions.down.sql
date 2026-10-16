-- REVERSES 0051_retired_serial_new_row_and_operator_sessions.
--
-- The global UNIQUE on appliances.serial can only come back when no serial appears twice. After 0051 a
-- re-registered retired box has a second (retired) row with the same serial, so the down REFUSES rather than
-- deleting history: delete or merge the retired duplicates through the API first.

BEGIN;

DO $$
DECLARE dup int;
BEGIN
  SELECT count(*) INTO dup FROM (SELECT serial FROM appliances GROUP BY serial HAVING count(*) > 1) d;
  IF dup > 0 THEN
    RAISE EXCEPTION '0051 down: % serial(s) belong to more than one appliance row (a retired row and its re-registration); resolve them before restoring the global unique serial', dup;
  END IF;
END $$;

ALTER TABLE appliance_certificates DROP COLUMN IF EXISTS superseded_at;
ALTER TABLE licenses DROP COLUMN IF EXISTS revocation_delivered_at;
DROP TABLE IF EXISTS central_security_settings;
ALTER TABLE operators DROP COLUMN IF EXISTS last_login_at;
ALTER TABLE operators DROP COLUMN IF EXISTS sessions_not_before;

DROP INDEX IF EXISTS appliances_serial_idx;
DROP INDEX IF EXISTS appliances_serial_live_uniq;
ALTER TABLE appliances ADD CONSTRAINT appliances_serial_key UNIQUE (serial);

COMMIT;
