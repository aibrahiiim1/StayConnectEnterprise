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
-- 2. RETENTION NO LONGER BREAKS LICENSING. central_retention_apply() (0054) deleted a revoked licence once the
--    revocation was older than history_years, whether or not the appliance had heard of it and whether or not the
--    licence was still valid -- and with it the only record that tells an appliance which licence to stop
--    honouring. It could also delete every licence an appliance ever had, after which the next one was issued as
--    version 1 and refused by the appliance as a rollback. Now a revoked licence is kept until its validity has
--    ended as well, and the newest licence of every appliance is kept for good. The function is replaced; nothing
--    else in it changes.
--
-- Additive apart from the counters. Nothing is granted: ctrlapi owns these objects, as for 0053.

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

-- Applies the periods once and returns what it did, by record kind. Idempotent: a second run finds nothing.
-- As in 0054, except for the licences (above).
CREATE OR REPLACE FUNCTION central_retention_apply() RETURNS jsonb LANGUAGE plpgsql AS $$
DECLARE
  s central_retention_settings;
  audit_cut timestamptz; people_cut timestamptz; history_cut timestamptz; account_cut timestamptz;
  out jsonb := '{}'::jsonb; n bigint; total bigint := 0;
BEGIN
  SELECT * INTO s FROM central_retention_settings WHERE id;
  IF NOT FOUND THEN
    RETURN out;
  END IF;
  audit_cut   := now() - make_interval(months => s.audit_log_months);
  people_cut  := now() - make_interval(months => s.history_personal_months);
  history_cut := now() - make_interval(years => s.history_years);
  account_cut := now() - make_interval(months => s.disabled_account_months);
  PERFORM set_config('central.retention', 'on', true);

  DELETE FROM audit_log WHERE ts < audit_cut;
  GET DIAGNOSTICS n = ROW_COUNT; out := out || jsonb_build_object('audit_deleted', n); total := total + n;

  -- History older than the history period goes; open alerts stay until someone closes them.
  DELETE FROM appliance_security_alerts
   WHERE created_at < history_cut AND (resolved OR status IN ('resolved', 'false_positive'));
  GET DIAGNOSTICS n = ROW_COUNT; out := out || jsonb_build_object('alerts_deleted', n); total := total + n;
  DELETE FROM appliance_lifecycle_events WHERE created_at < history_cut;
  GET DIAGNOSTICS n = ROW_COUNT; out := out || jsonb_build_object('lifecycle_events_deleted', n); total := total + n;
  DELETE FROM appliance_certificate_events WHERE created_at < history_cut;
  GET DIAGNOSTICS n = ROW_COUNT; out := out || jsonb_build_object('certificate_events_deleted', n); total := total + n;
  DELETE FROM portal_tls_events WHERE at < history_cut;
  GET DIAGNOSTICS n = ROW_COUNT; out := out || jsonb_build_object('portal_tls_events_deleted', n); total := total + n;
  DELETE FROM appliance_assignments WHERE created_at < history_cut;
  GET DIAGNOSTICS n = ROW_COUNT; out := out || jsonb_build_object('assignment_moves_deleted', n); total := total + n;

  -- The people in what is left, after the shorter period. An actor that is an email becomes 'removed';
  -- 'system' and appliance actors are kept.
  UPDATE appliance_security_alerts
     SET source_ip = NULL, acknowledged_by = NULL, detail = detail - 'last_source_ip'
   WHERE created_at < people_cut
     AND (source_ip IS NOT NULL OR acknowledged_by IS NOT NULL OR detail ? 'last_source_ip');
  GET DIAGNOSTICS n = ROW_COUNT; out := out || jsonb_build_object('alerts_anonymised', n); total := total + n;
  UPDATE appliance_lifecycle_events
     SET source_ip = NULL, actor = CASE WHEN actor LIKE '%@%' THEN 'removed' ELSE actor END
   WHERE created_at < people_cut AND (source_ip IS NOT NULL OR actor LIKE '%@%');
  GET DIAGNOSTICS n = ROW_COUNT; out := out || jsonb_build_object('lifecycle_events_anonymised', n); total := total + n;
  UPDATE appliance_certificate_events
     SET source_ip = NULL, actor = CASE WHEN actor LIKE '%@%' THEN 'removed' ELSE actor END
   WHERE created_at < people_cut AND (source_ip IS NOT NULL OR actor LIKE '%@%');
  GET DIAGNOSTICS n = ROW_COUNT; out := out || jsonb_build_object('certificate_events_anonymised', n); total := total + n;
  -- A settings change records the certificate contact email it changed (portaltls/admin.go): it goes too.
  UPDATE portal_tls_events
     SET actor = CASE WHEN actor LIKE '%@%' THEN 'removed' ELSE actor END, detail = detail - 'contact_email'
   WHERE at < people_cut AND (actor LIKE '%@%' OR detail ? 'contact_email');
  GET DIAGNOSTICS n = ROW_COUNT; out := out || jsonb_build_object('portal_tls_events_anonymised', n); total := total + n;
  -- Who moved an appliance between customers, and from where (0022).
  UPDATE appliance_assignments SET operator_id = NULL, source_ip = NULL
   WHERE created_at < people_cut AND (operator_id IS NOT NULL OR source_ip IS NOT NULL);
  GET DIAGNOSTICS n = ROW_COUNT; out := out || jsonb_build_object('assignment_moves_anonymised', n); total := total + n;
  -- The address an appliance asked for its certificate from (0023).
  UPDATE appliance_certificate_requests SET source_ip = NULL
   WHERE requested_at < people_cut AND source_ip IS NOT NULL;
  GET DIAGNOSTICS n = ROW_COUNT; out := out || jsonb_build_object('certificate_requests_anonymised', n); total := total + n;

  -- Ended licences: revoked, superseded by a newer one, or expired, before the history period. A licence with
  -- none of those ends at infinity and is never removed. Two rules keep licensing whole (0056):
  --   * A REVOKED licence stays until its validity (valid_until plus its grace period) has ended AS WELL AS the
  --     history period since the revocation. The revocation list an appliance fetches (/v1/appliance/license) is
  --     built from these rows, so deleting one an offline appliance never heard about would un-revoke it there.
  --   * The NEWEST licence of every appliance that still exists is never removed. Issuance numbers the next licence
  --     from the highest version left, and an appliance refuses a version lower than one it has seen (rollback
  --     protection): removing them all would make every later licence look like a rollback.
  DELETE FROM licenses l
   WHERE CASE WHEN l.revoked_at IS NOT NULL OR l.status = 'revoked' THEN
                COALESCE(l.revoked_at, 'infinity') < history_cut
            AND l.valid_until + make_interval(days => l.grace_period_days) < now()
          ELSE
                LEAST(l.valid_until,
                      COALESCE((SELECT min(n2.created_at) FROM licenses n2 WHERE n2.supersedes_license_id = l.id), 'infinity'))
                < history_cut
         END
     AND NOT EXISTS (
           SELECT 1 FROM unnest(l.appliance_ids) AS a(id)
            WHERE EXISTS (SELECT 1 FROM appliances ap WHERE ap.id = a.id)
              AND l.license_version >= (SELECT max(x.license_version) FROM licenses x WHERE a.id = ANY(x.appliance_ids)));
  GET DIAGNOSTICS n = ROW_COUNT; out := out || jsonb_build_object('licences_deleted', n); total := total + n;

  -- Retired appliances: the network details that could point to a person are cleared.
  UPDATE appliances a
     SET wan_mac = NULL, lan_mac = NULL, hostname = NULL, last_public_ip = NULL
   WHERE a.lifecycle_state IN ('revoked', 'decommissioned')
     AND (a.wan_mac IS NOT NULL OR a.lan_mac IS NOT NULL OR a.hostname IS NOT NULL OR a.last_public_ip IS NOT NULL)
     AND COALESCE((SELECT max(e.created_at) FROM appliance_lifecycle_events e
                    WHERE e.appliance_id = a.id AND e.to_state = a.lifecycle_state), a.updated_at) < history_cut;
  GET DIAGNOSTICS n = ROW_COUNT; out := out || jsonb_build_object('retired_appliances_cleared', n); total := total + n;

  -- Disabled accounts.
  WITH gone AS (
    UPDATE operators
       SET email = 'removed-' || id || '@removed.invalid', display_name = NULL, password_hash = NULL,
           last_login_at = NULL, sessions_not_before = now(), anonymised_at = now(), updated_at = now()
     WHERE status = 'disabled' AND anonymised_at IS NULL AND disabled_at < account_cut
    RETURNING id)
  SELECT count(*) INTO n FROM gone;
  out := out || jsonb_build_object('accounts_anonymised', n); total := total + n;
  IF n > 0 THEN
    DELETE FROM operator_mfa m USING operators o WHERE m.operator_id = o.id AND o.anonymised_at IS NOT NULL;
    DELETE FROM operator_mfa_recovery_codes c USING operators o WHERE c.operator_id = o.id AND o.anonymised_at IS NOT NULL;
    DELETE FROM operator_signin_pending p USING operators o WHERE p.operator_id = o.id AND o.anonymised_at IS NOT NULL;
    DELETE FROM operator_roles r USING operators o WHERE r.operator_id = o.id AND o.anonymised_at IS NOT NULL;
  END IF;

  -- Wrong-password counters untouched for two days (0053 removed them only, by chance, on a later failure). A
  -- routine sweep, so it is reported but does not by itself make the run an audited event.
  DELETE FROM operator_signin_throttle
   WHERE last_failure_at < now() - interval '2 days' AND (locked_until IS NULL OR locked_until < now());
  GET DIAGNOSTICS n = ROW_COUNT; out := out || jsonb_build_object('signin_counters_deleted', n);

  PERFORM set_config('central.retention', 'off', true);
  IF total > 0 THEN
    INSERT INTO audit_log (actor_type, actor_id, action, target_type, target_id, payload)
    VALUES ('system', 'retention', 'retention.applied', 'settings', 'retention', out);
  END IF;
  RETURN out;
END $$;

COMMIT;
