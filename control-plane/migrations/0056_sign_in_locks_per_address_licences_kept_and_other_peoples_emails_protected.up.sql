-- 0056 -- A WRONG-PASSWORD LOCK HOLDS ONE ADDRESS, NOT THE ACCOUNT
-- (Review of the GDPR remediation branch, 2026-10-20; Product-Owner decision D-R1: "Email+IP, plus unlock".)
--
-- 1. SIGN-IN LOCKS PER (EMAIL, ADDRESS). 0053 counted wrong passwords per email alone, so anyone who knew an
--    administrator's address could keep that administrator locked out for ever, from anywhere, and there was no
--    way to lift the lock. Now:
--
--      key_hash   an HMAC over the lower-cased email AND the client address (internal/auth/signin_throttle.go),
--                 so failures from one address lock the account only for that address. Unknown names are still
--                 counted exactly like real ones; the per-address request limit (20 a minute) still applies.
--      name_hash  an HMAC over the email alone, so every lock on one account can be found and cleared without
--                 the table holding the email: by `ctrlapi unlock-signin --email E --reason R` on the host, and by
--                 an administrator's "Reset two-step" in the console.
--
--    operator_signin_throttle_fail(text) becomes operator_signin_throttle_fail(text, text): the same counting,
--    recording name_hash. The counters 0053 wrote are keyed on the email alone and cannot be read under the new
--    key, so they are removed: running lockouts end, which is what an unlock would do anyway.
--
-- Additive apart from that. Nothing is granted: ctrlapi owns these objects, as for 0053.

BEGIN;

DELETE FROM operator_signin_throttle;
ALTER TABLE operator_signin_throttle
  ADD COLUMN IF NOT EXISTS name_hash text NOT NULL CHECK (name_hash ~ '^[0-9a-f]{64}$');
CREATE INDEX IF NOT EXISTS operator_signin_throttle_name ON operator_signin_throttle (name_hash);
COMMENT ON TABLE operator_signin_throttle IS
  'Wrong passwords per sign-in name AND client address (0053, 0056). key_hash is an HMAC over the lower-cased '
  'email and the address, name_hash one over the email alone (to clear every lock on an account); neither holds '
  'the address or the email, and unknown names are treated like real ones. signin_lock_failures within '
  'signin_lock_minutes lock that name at that address for signin_lock_minutes (central_security_settings).';

DROP FUNCTION IF EXISTS operator_signin_throttle_fail(text);

-- One wrong password for p_key (name + address). Returns the count in the current window and, when this failure
-- started a lockout, when it ends. A failure during a running lockout does not extend it.
CREATE OR REPLACE FUNCTION operator_signin_throttle_fail(p_key text, p_name text, OUT failures int, OUT newly_locked_until timestamptz)
    LANGUAGE plpgsql AS $$
DECLARE v_max int; v_mins int; v_until timestamptz;
BEGIN
  SELECT s.signin_lock_failures, s.signin_lock_minutes INTO v_max, v_mins FROM central_security_settings s WHERE s.id;
  v_max := COALESCE(v_max, 5); v_mins := COALESCE(v_mins, 15);
  PERFORM pg_advisory_xact_lock(hashtext('operator_signin_throttle'), hashtext(p_key));
  INSERT INTO operator_signin_throttle AS t (key_hash, name_hash, failures, window_started_at, last_failure_at)
  VALUES (p_key, p_name, 1, now(), now())
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
