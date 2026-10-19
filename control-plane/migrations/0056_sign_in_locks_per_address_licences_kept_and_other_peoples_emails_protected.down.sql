-- 0056 down -- wrong-password locks are per email again (0053). The per-address counters cannot be read under the
-- email-only key and are removed, which ends running lockouts.

BEGIN;

DROP FUNCTION IF EXISTS operator_signin_throttle_fail(text, text);
DELETE FROM operator_signin_throttle;
DROP INDEX IF EXISTS operator_signin_throttle_name;
ALTER TABLE operator_signin_throttle DROP COLUMN IF EXISTS name_hash;
COMMENT ON TABLE operator_signin_throttle IS
  'Wrong passwords per sign-in name (0053), keyed by SHA-256 of the lower-cased email so it holds no address and '
  'treats unknown names like real ones. signin_lock_failures within signin_lock_minutes lock it for '
  'signin_lock_minutes (central_security_settings).';

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
