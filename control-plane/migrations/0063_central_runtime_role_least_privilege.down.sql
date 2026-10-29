-- 0063 down. Drops the runtime role and its helpers and restores the 0054/0055/0056 bodies of the functions it
-- replaced. STOP ctrlapi first and put CTRLAPI_DB_URL back on the owner role: a connected ctrlapi_rt session keeps
-- the role alive (DROP ROLE then only warns). The schema-level REVOKE CREATE ... FROM PUBLIC is not undone (PostgreSQL 15+
-- already ships without it). Audit payloads queued in central_forget_pending and not yet scrubbed are scrubbed first.
BEGIN;

DO $$
BEGIN
  IF to_regproc('central_forget_audit_apply(int)') IS NOT NULL THEN
    EXECUTE 'SELECT central_forget_audit_apply(1000000)';
  END IF;
END $$;

DROP TRIGGER IF EXISTS audit_log_append_only ON audit_log;
DROP FUNCTION IF EXISTS central_history_guard();

-- 0054 body of the portal_tls_events guard.
CREATE OR REPLACE FUNCTION portal_tls_events_append_only() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF current_setting('central.retention', true) = 'on' THEN
    IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
    RETURN NEW;
  END IF;
  RAISE EXCEPTION 'portal_tls_events is append-only';
END $$;

GRANT EXECUTE ON FUNCTION central_retention_apply() TO PUBLIC;
GRANT EXECUTE ON FUNCTION central_forget_actor(text, text) TO PUBLIC;
GRANT EXECUTE ON FUNCTION central_correct_actor_email(uuid, text, text) TO PUBLIC;

-- 0056 body of central_retention_apply.
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

-- 0055 body of central_forget_actor (it also scrubs audit_log inside the delete, case-sensitively).
CREATE OR REPLACE FUNCTION central_forget_actor(p_email text, p_label text) RETURNS bigint
LANGUAGE plpgsql AS $$
DECLARE n bigint; total bigint := 0; was text := current_setting('central.retention', true); e text := lower(btrim(p_email));
BEGIN
  IF e IS NULL OR e = '' OR e LIKE 'removed-%' THEN
    RETURN 0;
  END IF;
  UPDATE appliance_lifecycle_events SET actor = p_label WHERE lower(actor) = e;
  GET DIAGNOSTICS n = ROW_COUNT; total := total + n;
  UPDATE appliance_certificate_events SET actor = p_label WHERE lower(actor) = e;
  GET DIAGNOSTICS n = ROW_COUNT; total := total + n;
  PERFORM set_config('central.retention', 'on', true);
  UPDATE portal_tls_events SET actor = p_label WHERE lower(actor) = e;
  GET DIAGNOSTICS n = ROW_COUNT; total := total + n;
  PERFORM set_config('central.retention', COALESCE(was, ''), true);
  UPDATE central_retention_settings SET updated_by = p_label WHERE lower(updated_by) = e;
  GET DIAGNOSTICS n = ROW_COUNT; total := total + n;
  UPDATE central_security_settings SET updated_by = p_label WHERE lower(updated_by) = e;
  GET DIAGNOSTICS n = ROW_COUNT; total := total + n;
  UPDATE portal_tls_settings SET updated_by = p_label WHERE lower(updated_by) = e;
  GET DIAGNOSTICS n = ROW_COUNT; total := total + n;
  UPDATE audit_log SET payload = replace(payload::text, to_jsonb(e)::text, to_jsonb(p_label)::text)::jsonb
   WHERE payload::text LIKE '%' || replace(replace(to_jsonb(e)::text, '%', '\%'), '_', '\_') || '%';
  GET DIAGNOSTICS n = ROW_COUNT; total := total + n;
  RETURN total;
END $$;

-- 0056 body of central_correct_actor_email.
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

DROP FUNCTION IF EXISTS central_forget_audit_apply(int);
DROP FUNCTION IF EXISTS central_apply_runtime_grants();
DROP TABLE IF EXISTS central_forget_pending;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'ctrlapi_rt') THEN
    DROP OWNED BY ctrlapi_rt;
    BEGIN
      DROP ROLE ctrlapi_rt;
    EXCEPTION WHEN dependent_objects_still_exist OR object_in_use THEN
      RAISE NOTICE 'role ctrlapi_rt still has privileges in another database or a live session; drop it there/after stopping ctrlapi';
    END;
  END IF;
END $$;

COMMIT;
