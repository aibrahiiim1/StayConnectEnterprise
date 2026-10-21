-- 0053 -- REPEATED WRONG PASSWORDS LOCK A CENTRAL ACCOUNT FOR A WHILE
-- (GDPR remediation plan, gap found by the F3 review: Central limited sign-in only per address, 20 a minute, so one
--  account could be guessed at slowly from many addresses. Hotel Admin has locked accounts since appliance
--  migration 0119.)
--
--   operator_signin_throttle   one row per sign-in name tried: a SHA-256 of the lower-cased email, never the email,
--                              so a name that has no account is counted exactly like one that has, and the lock
--                              reveals nothing about which accounts exist. Wrong passwords at sign-in and at the
--                              password confirmation both count. The lock runs out by itself.
--   central_security_settings  gains signin_lock_failures (3-20, default 5) and signin_lock_minutes (1-1440,
--                              default 15): how many wrong passwords within that many minutes lock the account,
--                              and for how long. Settings, not constants (docs/OPERATIONAL_SETTINGS_POLICY.md),
--                              changed under System > Team > Sign-in limits with a reason, audited. The
--                              authenticator-code lockout of 0052 follows the same two values.
--
-- Additive. Counters untouched for two days are removed as failures are recorded.

BEGIN;

ALTER TABLE central_security_settings
  ADD COLUMN IF NOT EXISTS signin_lock_failures int NOT NULL DEFAULT 5 CHECK (signin_lock_failures BETWEEN 3 AND 20),
  ADD COLUMN IF NOT EXISTS signin_lock_minutes int NOT NULL DEFAULT 15 CHECK (signin_lock_minutes BETWEEN 1 AND 1440);

CREATE TABLE IF NOT EXISTS operator_signin_throttle (
  key_hash          text PRIMARY KEY CHECK (key_hash ~ '^[0-9a-f]{64}$'),
  failures          int NOT NULL CHECK (failures >= 0),
  window_started_at timestamptz NOT NULL,
  last_failure_at   timestamptz NOT NULL,
  locked_until      timestamptz
);
COMMENT ON TABLE operator_signin_throttle IS
  'Wrong passwords per sign-in name (0053), keyed by SHA-256 of the lower-cased email so it holds no address and '
  'treats unknown names like real ones. signin_lock_failures within signin_lock_minutes lock it for '
  'signin_lock_minutes (central_security_settings).';

-- One wrong password for p_key. Returns the count in the current window and, when this failure started a lockout,
-- when it ends. A failure during a running lockout does not extend it.
CREATE OR REPLACE FUNCTION operator_signin_throttle_fail(p_key text, OUT failures int, OUT newly_locked_until timestamptz)
    LANGUAGE plpgsql AS $$
DECLARE v_max int; v_mins int; v_until timestamptz; v_was timestamptz;
BEGIN
  SELECT s.signin_lock_failures, s.signin_lock_minutes INTO v_max, v_mins FROM central_security_settings s WHERE s.id;
  v_max := COALESCE(v_max, 5); v_mins := COALESCE(v_mins, 15);
  PERFORM pg_advisory_xact_lock(hashtext('operator_signin_throttle'), hashtext(p_key));
  SELECT t.locked_until INTO v_was FROM operator_signin_throttle t WHERE t.key_hash = p_key;
  INSERT INTO operator_signin_throttle AS t (key_hash, failures, window_started_at, last_failure_at)
  VALUES (p_key, 1, now(), now())
  ON CONFLICT (key_hash) DO UPDATE SET
    failures = CASE WHEN t.window_started_at < now() - make_interval(mins => v_mins)
                         AND (t.locked_until IS NULL OR t.locked_until <= now()) THEN 1 ELSE t.failures + 1 END,
    window_started_at = CASE WHEN t.window_started_at < now() - make_interval(mins => v_mins)
                                  AND (t.locked_until IS NULL OR t.locked_until <= now()) THEN now() ELSE t.window_started_at END,
    last_failure_at = now()
  RETURNING t.failures, t.locked_until INTO failures, v_until;
  IF (v_until IS NULL OR v_until <= now()) AND failures >= v_max THEN
    newly_locked_until := now() + make_interval(mins => v_mins);
    UPDATE operator_signin_throttle t SET locked_until = newly_locked_until WHERE t.key_hash = p_key;
  END IF;
  IF random() < 0.05 THEN
    DELETE FROM operator_signin_throttle d
     WHERE d.last_failure_at < now() - interval '2 days' AND (d.locked_until IS NULL OR d.locked_until < now());
  END IF;
END $$;

COMMIT;
