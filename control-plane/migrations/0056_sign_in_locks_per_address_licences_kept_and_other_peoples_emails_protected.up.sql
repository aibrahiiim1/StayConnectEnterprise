-- 0056 -- SIGN-IN LOCKS PER ADDRESS, LICENCES KEPT FOR LICENSING, OTHER PEOPLE'S EMAILS PROTECTED
-- (Review of the GDPR remediation branch, 2026-10-20; Product-Owner decision D-R1 for 1: "Email+IP, plus unlock".)
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
-- 3. A USER EXPORT NO LONGER HANDS OVER OTHER PEOPLE'S EMAILS. central_user_data() (0055) copied audit payloads
--    as they are, and an entry like user.created carries the email of the person created. Any email address in
--    the exported entries that is not the exported user's own now reads "[other user]"
--    (central_mask_other_emails).
--
-- 4. CORRECTING AN EMAIL REWRITES ONLY THAT USER'S HISTORY. updateUser called central_forget_actor(old, new),
--    which replaces every matching JSON string in the WHOLE audit log -- reachable by a customer's own
--    administrator, who could thereby rewrite platform-wide history wherever that string appeared.
--    central_correct_actor_email(id, old, new) changes the who-acted columns that hold the old address and only
--    the audit entries made by or about that user (actor_id / target_id). Removing a user still forgets their
--    email everywhere (central_forget_actor, unchanged).
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

-- 3. What a user export shows of other people.
-- Every email address in s except own (compared case-insensitively) becomes [other user]. An address is the
-- whole run of address characters around an @, so a shorter address inside a longer one is never matched.
CREATE OR REPLACE FUNCTION central_mask_other_emails_text(s text, own text) RETURNS text
LANGUAGE plpgsql IMMUTABLE AS $$
DECLARE m text; o text := lower(btrim(COALESCE(own, '')));
BEGIN
  IF s IS NULL OR position('@' IN s) = 0 THEN
    RETURN s;
  END IF;
  FOR m IN SELECT DISTINCT x[1] FROM regexp_matches(s, '([A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)+)', 'g') AS x LOOP
    IF lower(m) <> o THEN
      s := regexp_replace(s, '(?<![A-Za-z0-9._%+-])' || regexp_replace(m, '([.+])', '\\\1', 'g') || '(?![A-Za-z0-9-]|\.[A-Za-z0-9])',
                          '[other user]', 'g');
    END IF;
  END LOOP;
  RETURN s;
END $$;

CREATE OR REPLACE FUNCTION central_mask_other_emails(doc jsonb, own text) RETURNS jsonb
LANGUAGE sql IMMUTABLE AS $$ SELECT central_mask_other_emails_text(doc::text, own)::jsonb $$;
COMMENT ON FUNCTION central_mask_other_emails(jsonb, text) IS
  'doc with every email address other than own replaced by [other user] (0056): what a user export shows of '
  'other people named in that user''s history.';

CREATE OR REPLACE FUNCTION central_user_data(p_operator uuid) RETURNS jsonb
LANGUAGE plpgsql STABLE AS $$
DECLARE o operators; e text;
BEGIN
  SELECT * INTO o FROM operators WHERE id = p_operator;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'user not found' USING ERRCODE = 'no_data_found';
  END IF;
  e := lower(o.email);
  RETURN jsonb_build_object(
    'format', 'onegate.central-user-export/1', 'exported_at', now(),
    'account', jsonb_build_object(
      'email', o.email, 'display_name', o.display_name, 'status', o.status, 'created_at', o.created_at,
      'last_sign_in_at', o.last_login_at, 'disabled_at', o.disabled_at,
      'two_step_sign_in', EXISTS (SELECT 1 FROM operator_mfa m WHERE m.operator_id = o.id AND m.confirmed_at IS NOT NULL)),
    'roles', (SELECT COALESCE(jsonb_agg(jsonb_build_object('role', r.role, 'customer', t.name) ORDER BY r.role), '[]'::jsonb)
                FROM operator_roles r LEFT JOIN tenants t ON t.id = r.tenant_id WHERE r.operator_id = o.id),
    'activity', (SELECT COALESCE(jsonb_agg(jsonb_build_object(
                    'at', l.ts, 'action', l.action, 'on', l.target_type, 'target', l.target_id, 'from_ip', l.ip,
                    'browser', l.user_agent, 'details', central_mask_other_emails(l.payload, e)) ORDER BY l.ts), '[]'::jsonb)
                   FROM audit_log l WHERE l.actor_id = o.id::text),
    'about_this_account', (SELECT COALESCE(jsonb_agg(jsonb_build_object(
                    'at', l.ts, 'action', l.action, 'details', central_mask_other_emails(l.payload, e)) ORDER BY l.ts), '[]'::jsonb)
                   FROM audit_log l WHERE l.target_id = o.id::text AND l.actor_id IS DISTINCT FROM o.id::text),
    'appliance_events', (SELECT COALESCE(jsonb_agg(jsonb_build_object(
                    'at', x.created_at, 'from', x.from_state, 'to', x.to_state, 'from_ip', x.source_ip,
                    'reason', central_mask_other_emails_text(x.reason, e))
                    ORDER BY x.created_at), '[]'::jsonb)
                   FROM appliance_lifecycle_events x WHERE lower(x.actor) = e),
    'certificate_events', (SELECT COALESCE(jsonb_agg(jsonb_build_object(
                    'at', x.created_at, 'event', x.event, 'from_ip', x.source_ip) ORDER BY x.created_at), '[]'::jsonb)
                   FROM appliance_certificate_events x WHERE lower(x.actor) = e),
    'portal_certificate_events', (SELECT COALESCE(jsonb_agg(jsonb_build_object(
                    'at', x.at, 'kind', x.kind, 'hostname', x.hostname) ORDER BY x.at), '[]'::jsonb)
                   FROM portal_tls_events x WHERE lower(x.actor) = e),
    'licences_issued', (SELECT count(*) FROM licenses x WHERE x.created_by = o.id),
    'assignments_made', (SELECT count(*) FROM appliance_assignments x WHERE x.operator_id = o.id));
END $$;
COMMENT ON FUNCTION central_user_data(uuid) IS
  'One Central user''s data as a JSON document (0055, GDPR audit B5, Art. 15/20): the account, roles, '
  'second-step status, their activity log entries and those about them, and the appliance, certificate and '
  'portal-certificate events they made. Any other person''s email in those entries reads [other user] (0056). '
  'Changes nothing.';

-- 4. Correcting one user's email rewrites only what is attributable to that user.
CREATE OR REPLACE FUNCTION central_correct_actor_email(p_operator uuid, p_old text, p_new text) RETURNS bigint
LANGUAGE plpgsql AS $$
DECLARE n bigint; total bigint := 0; was text := current_setting('central.retention', true);
        e text := lower(btrim(p_old)); nw text := lower(btrim(p_new));
BEGIN
  IF p_operator IS NULL OR e IS NULL OR e = '' OR nw IS NULL OR nw = '' OR e = nw THEN
    RETURN 0;
  END IF;
  -- Columns that hold who acted: the old address there IS this user.
  UPDATE appliance_lifecycle_events SET actor = nw WHERE lower(actor) = e;
  GET DIAGNOSTICS n = ROW_COUNT; total := total + n;
  UPDATE appliance_certificate_events SET actor = nw WHERE lower(actor) = e;
  GET DIAGNOSTICS n = ROW_COUNT; total := total + n;
  PERFORM set_config('central.retention', 'on', true);
  UPDATE portal_tls_events SET actor = nw WHERE lower(actor) = e;
  GET DIAGNOSTICS n = ROW_COUNT; total := total + n;
  PERFORM set_config('central.retention', COALESCE(was, ''), true);
  UPDATE central_retention_settings SET updated_by = nw WHERE lower(updated_by) = e;
  GET DIAGNOSTICS n = ROW_COUNT; total := total + n;
  UPDATE central_security_settings SET updated_by = nw WHERE lower(updated_by) = e;
  GET DIAGNOSTICS n = ROW_COUNT; total := total + n;
  UPDATE portal_tls_settings SET updated_by = nw WHERE lower(updated_by) = e;
  GET DIAGNOSTICS n = ROW_COUNT; total := total + n;
  -- Audit entries this user made or that are about this user; nobody else's.
  UPDATE audit_log SET payload = replace(payload::text, to_jsonb(e)::text, to_jsonb(nw)::text)::jsonb
   WHERE (actor_id = p_operator::text OR target_id = p_operator::text)
     AND payload::text LIKE '%' || replace(replace(to_jsonb(e)::text, '%', '\%'), '_', '\_') || '%';
  GET DIAGNOSTICS n = ROW_COUNT; total := total + n;
  RETURN total;
END $$;
COMMENT ON FUNCTION central_correct_actor_email(uuid, text, text) IS
  'Replaces a corrected email with the new one in the history columns that record who acted and in the audit '
  'entries made by or about that user only (0056). Returns how many records changed.';

COMMIT;
