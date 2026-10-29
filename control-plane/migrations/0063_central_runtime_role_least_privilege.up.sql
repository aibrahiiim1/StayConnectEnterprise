-- 0063 -- CENTRAL'S APPLICATION NO LONGER CONNECTS AS THE DATABASE SUPERUSER (audit P3 M1) AND FORGETTING A
-- REMOVED USER NO LONGER SCANS THE AUDIT LOG INSIDE THE DELETE (audit P3 L2)
--
-- Until now ctrlapi connected as `stayconnect`, the superuser that owns every table: any SQL-injection or
-- code-execution bug in ctrlapi was full control of the database (rewrite the audit trail, alter triggers, COPY ...
-- PROGRAM). This migration adds a runtime role and moves the sanctioned "rewrite history" paths into SECURITY DEFINER
-- functions, so that the runtime role cannot do those things itself.
--
-- ROLES. The migration owner (the role that runs this file: `stayconnect`) keeps owning every object and keeps
-- running migrations, pg_dump/pg_restore and `ctrlapi seed-admin` (central-install.sh). The new role `ctrlapi_rt` is
-- what CTRLAPI_DB_URL names. It is created with NO password here (a migration is committed; a password never is): the
-- installer and central-deploy.sh set it with ALTER ROLE ... PASSWORD from a generated 0600 secret file.
--
-- WHAT ctrlapi_rt MAY DO (all re-asserted idempotently by central_apply_runtime_grants(), which this migration calls
-- and the scripts call again after every migrate/restore):
--   * CONNECT to this database; USAGE on schema public; NO CREATE on schema public (revoked from PUBLIC as well);
--   * SELECT/INSERT/UPDATE/DELETE on the application tables, USAGE/SELECT/UPDATE on sequences;
--   * SELECT and INSERT ONLY (no UPDATE, no DELETE) on the append-only/audit tables the code never changes:
--       audit_log, portal_tls_events, appliance_lifecycle_events, appliance_certificate_events,
--       appliance_certificate_revocations, appliance_assignments, appliance_assignment_history,
--       appliance_history_archive, retired_appliance_identities, appliance_ca_versions;
--   * SELECT ONLY on schema_migrations (the runner, as owner, writes it); NOTHING on central_forget_pending;
--   * never TRUNCATE, TRIGGER, REFERENCES; owns nothing; no role/database/extension creation; no superuser.
--   * EXECUTE on the SECURITY DEFINER functions below. Those, owned by the migration owner, are the ONLY way the
--     runtime role changes append-only history: retention (central_retention_apply), forgetting a removed user
--     (central_forget_actor, central_forget_audit_apply), correcting an email (central_correct_actor_email).
--
-- FUTURE OBJECTS. ALTER DEFAULT PRIVILEGES (in central_apply_runtime_grants(), with no FOR ROLE, so it applies to
-- objects created by the role that runs the migrations, the migration owner) gives ctrlapi_rt SELECT/INSERT/UPDATE/
-- DELETE on every table and USAGE/SELECT/UPDATE on every sequence a later migration creates in schema public. A later
-- migration that creates an APPEND-ONLY table must list it in central_apply_runtime_grants() (CREATE OR REPLACE) and
-- REVOKE UPDATE, DELETE on it from ctrlapi_rt.
--
-- THE GUARD. The portal_tls_events trigger (0050/0054) was lifted by `SET central.retention = 'on'`, which any session
-- can set; audit_log had no guard at all. Both guards now allow a change only when central.retention is 'on' AND the
-- current user is the table's owner, which inside a SECURITY DEFINER function is the definer, never ctrlapi_rt.
--
-- L2. central_forget_actor matched audit payloads with a case-sensitive LIKE and scanned the whole audit_log inside
-- the operator DELETE transaction. It now (1) rewrites only the small history columns synchronously, and (2) queues
-- the email in central_forget_pending; central_forget_audit_apply() scrubs audit_log week by week, case-insensitively,
-- outside that transaction (ctrlapi calls it right after the removal commits and again each housekeeping pass), and
-- deletes the queue row. Its return value no longer counts audit rows. The queue row holds the email only until then.
--
-- Additive and idempotent; the down migration restores the 0054/0055/0056 function bodies and drops the role.

BEGIN;

-- ---------------------------------------------------------------- the queue behind the audit scrub (owner only)
CREATE TABLE IF NOT EXISTS central_forget_pending (
  id        bigserial PRIMARY KEY,
  email     text NOT NULL,
  label     text NOT NULL,
  queued_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (email, label)
);
COMMENT ON TABLE central_forget_pending IS
  'Emails waiting to be replaced by a removed-user label in audit_log payloads (0063, audit P3 L2). Emptied by central_forget_audit_apply(); no runtime-role privileges.';

-- ---------------------------------------------------------------- the two append-only guards
CREATE OR REPLACE FUNCTION central_history_guard() RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, pg_temp AS $$
BEGIN
  IF current_setting('central.retention', true) = 'on'
     AND current_user = (SELECT pg_get_userbyid(c.relowner) FROM pg_class c WHERE c.oid = TG_RELID) THEN
    IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
    RETURN NEW;
  END IF;
  RAISE EXCEPTION '% is append-only', TG_TABLE_NAME USING ERRCODE = 'insufficient_privilege';
END $$;

CREATE OR REPLACE FUNCTION portal_tls_events_append_only() RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, pg_temp AS $$
BEGIN
  IF current_setting('central.retention', true) = 'on'
     AND current_user = (SELECT pg_get_userbyid(c.relowner) FROM pg_class c WHERE c.oid = TG_RELID) THEN
    IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
    RETURN NEW;
  END IF;
  RAISE EXCEPTION 'portal_tls_events is append-only' USING ERRCODE = 'insufficient_privilege';
END $$;

DROP TRIGGER IF EXISTS audit_log_append_only ON audit_log;
CREATE TRIGGER audit_log_append_only BEFORE UPDATE OR DELETE ON audit_log
  FOR EACH ROW EXECUTE FUNCTION central_history_guard();

-- ---------------------------------------------------------------- the sanctioned history-rewriting functions
-- Retention (the 0056 body, unchanged except SECURITY DEFINER and a pinned search_path).
CREATE OR REPLACE FUNCTION central_retention_apply() RETURNS jsonb LANGUAGE plpgsql SECURITY DEFINER SET search_path = public, pg_temp AS $$
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

-- Forgetting a removed user: the history COLUMNS now, the audit payloads through the queue (L2).
CREATE OR REPLACE FUNCTION central_forget_actor(p_email text, p_label text) RETURNS bigint
LANGUAGE plpgsql SECURITY DEFINER SET search_path = public, pg_temp AS $$
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
  INSERT INTO central_forget_pending (email, label) VALUES (e, p_label) ON CONFLICT DO NOTHING;
  RETURN total;
END $$;
COMMENT ON FUNCTION central_forget_actor(text, text) IS
  'Replaces one user''s email with a removed-user label in the history columns that keep it as text, and queues the '
  'audit-payload scrub for central_forget_audit_apply() (0063, audit P3 L2; 0055 GDPR B5). Returns how many column records changed.';

-- The audit payload scrub: case-insensitive, one week of the hypertable at a time, never inside the operator DELETE.
CREATE OR REPLACE FUNCTION central_forget_audit_apply(p_limit int DEFAULT 100) RETURNS bigint
LANGUAGE plpgsql SECURITY DEFINER SET search_path = public, pg_temp AS $$
DECLARE r record; lo timestamptz; hi timestamptz; w timestamptz; n bigint; total bigint := 0;
        was text := current_setting('central.retention', true); lit text; pat text; rx text; rep text;
BEGIN
  PERFORM pg_advisory_xact_lock(hashtext('central_forget_audit_apply'));
  SELECT min(ts), max(ts) INTO lo, hi FROM audit_log;
  PERFORM set_config('central.retention', 'on', true);
  FOR r IN SELECT id, email, label FROM central_forget_pending ORDER BY id LIMIT GREATEST(p_limit, 1) LOOP
    IF lo IS NOT NULL THEN
      -- a whole JSON string equal to the email, wherever it sits in a payload ("email", "username", "by" ...)
      lit := to_jsonb(r.email)::text;
      pat := '%' || replace(replace(replace(lower(lit), '\', '\\'), '%', '\%'), '_', '\_') || '%';
      rx  := regexp_replace(lit, '([.^$*+?(){}|\[\]\\])', '\\\1', 'g');
      rep := replace(to_jsonb(r.label)::text, '\', '\\');
      w := date_trunc('week', lo);
      WHILE w <= hi LOOP
        UPDATE audit_log SET payload = regexp_replace(payload::text, rx, rep, 'gi')::jsonb
         WHERE ts >= w AND ts < w + interval '7 days' AND lower(payload::text) LIKE pat;
        GET DIAGNOSTICS n = ROW_COUNT; total := total + n;
        w := w + interval '7 days';
      END LOOP;
    END IF;
    DELETE FROM central_forget_pending WHERE id = r.id;
  END LOOP;
  PERFORM set_config('central.retention', COALESCE(was, ''), true);
  RETURN total;
END $$;
COMMENT ON FUNCTION central_forget_audit_apply(int) IS
  'Replaces queued removed-user emails (central_forget_pending) in audit_log payloads, case-insensitively, one week at a time; returns the rows changed (0063, audit P3 L2).';

-- Correcting an email (the 0056 body; the audit update now runs with the retention flag the guard requires).
CREATE OR REPLACE FUNCTION central_correct_actor_email(p_operator uuid, p_old text, p_new text) RETURNS bigint
LANGUAGE plpgsql SECURITY DEFINER SET search_path = public, pg_temp AS $$
DECLARE n bigint; total bigint := 0; was text := current_setting('central.retention', true);
        e text := lower(btrim(p_old)); nw text := lower(btrim(p_new));
BEGIN
  IF p_operator IS NULL OR e IS NULL OR e = '' OR nw IS NULL OR nw = '' OR e = nw THEN
    RETURN 0;
  END IF;
  UPDATE appliance_lifecycle_events SET actor = nw WHERE lower(actor) = e;
  GET DIAGNOSTICS n = ROW_COUNT; total := total + n;
  UPDATE appliance_certificate_events SET actor = nw WHERE lower(actor) = e;
  GET DIAGNOSTICS n = ROW_COUNT; total := total + n;
  PERFORM set_config('central.retention', 'on', true);
  UPDATE portal_tls_events SET actor = nw WHERE lower(actor) = e;
  GET DIAGNOSTICS n = ROW_COUNT; total := total + n;
  UPDATE audit_log SET payload = replace(payload::text, to_jsonb(e)::text, to_jsonb(nw)::text)::jsonb
   WHERE (actor_id = p_operator::text OR target_id = p_operator::text)
     AND payload::text LIKE '%' || replace(replace(to_jsonb(e)::text, '%', '\%'), '_', '\_') || '%';
  GET DIAGNOSTICS n = ROW_COUNT; total := total + n;
  PERFORM set_config('central.retention', COALESCE(was, ''), true);
  UPDATE central_retention_settings SET updated_by = nw WHERE lower(updated_by) = e;
  GET DIAGNOSTICS n = ROW_COUNT; total := total + n;
  UPDATE central_security_settings SET updated_by = nw WHERE lower(updated_by) = e;
  GET DIAGNOSTICS n = ROW_COUNT; total := total + n;
  UPDATE portal_tls_settings SET updated_by = nw WHERE lower(updated_by) = e;
  GET DIAGNOSTICS n = ROW_COUNT; total := total + n;
  RETURN total;
END $$;
COMMENT ON FUNCTION central_correct_actor_email(uuid, text, text) IS
  'Replaces a corrected email with the new one in the history columns that record who acted and in the audit '
  'entries made by or about that user only (0056, runs as the table owner since 0063). Returns how many records changed.';

-- ---------------------------------------------------------------- the runtime role and its privileges, one function
-- Run as the migration owner. Idempotent: converges the role to exactly this model, so it is safe to call again after a
-- restore (pg_restore --no-acl) or a migration that added a table. It never sets a password.
CREATE OR REPLACE FUNCTION central_apply_runtime_grants() RETURNS void
LANGUAGE plpgsql SET search_path = public, pg_temp AS $$
DECLARE
  rt   constant text := 'ctrlapi_rt';
  ro   constant text[] := ARRAY['audit_log','portal_tls_events','appliance_lifecycle_events','appliance_certificate_events',
                                'appliance_certificate_revocations','appliance_assignments','appliance_assignment_history',
                                'appliance_history_archive','retired_appliance_identities','appliance_ca_versions'];
  sel  constant text[] := ARRAY['schema_migrations'];
  none constant text[] := ARRAY['central_forget_pending'];
  t record;
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = rt) THEN
    EXECUTE format('CREATE ROLE %I LOGIN', rt);
  END IF;
  EXECUTE format('ALTER ROLE %I LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS INHERIT', rt);
  EXECUTE format('ALTER ROLE %I SET search_path = public', rt);

  EXECUTE format('GRANT CONNECT ON DATABASE %I TO %I', current_database(), rt);
  EXECUTE format('REVOKE CREATE ON DATABASE %I FROM %I', current_database(), rt);
  REVOKE CREATE ON SCHEMA public FROM PUBLIC;
  EXECUTE format('REVOKE ALL ON SCHEMA public FROM %I', rt);
  EXECUTE format('GRANT USAGE ON SCHEMA public TO %I', rt);

  EXECUTE format('REVOKE ALL ON ALL TABLES IN SCHEMA public FROM %I', rt);
  FOR t IN SELECT c.relname, c.relkind FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
            WHERE n.nspname = 'public' AND c.relkind IN ('r','p','v','m') AND c.relispartition = false
              AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.objid = c.oid AND d.deptype = 'e') LOOP
    IF t.relname = ANY (none) THEN
      CONTINUE;
    ELSIF t.relname = ANY (sel) OR t.relkind IN ('v','m') THEN
      EXECUTE format('GRANT SELECT ON public.%I TO %I', t.relname, rt);
    ELSIF t.relname = ANY (ro) THEN
      EXECUTE format('GRANT SELECT, INSERT ON public.%I TO %I', t.relname, rt);
    ELSE
      EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON public.%I TO %I', t.relname, rt);
    END IF;
  END LOOP;
  EXECUTE format('REVOKE ALL ON ALL SEQUENCES IN SCHEMA public FROM %I', rt);
  EXECUTE format('GRANT USAGE, SELECT, UPDATE ON ALL SEQUENCES IN SCHEMA public TO %I', rt);
  EXECUTE format('REVOKE ALL ON SEQUENCE public.central_forget_pending_id_seq FROM %I', rt);

  -- Tables and sequences a later migration creates (as the role running this function) follow the same rule.
  EXECUTE format('ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO %I', rt);
  EXECUTE format('ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT USAGE, SELECT, UPDATE ON SEQUENCES TO %I', rt);

  -- The functions that change append-only history are callable by the runtime role and by nobody else.
  REVOKE ALL ON FUNCTION central_retention_apply() FROM PUBLIC;
  REVOKE ALL ON FUNCTION central_forget_actor(text, text) FROM PUBLIC;
  REVOKE ALL ON FUNCTION central_forget_audit_apply(int) FROM PUBLIC;
  REVOKE ALL ON FUNCTION central_correct_actor_email(uuid, text, text) FROM PUBLIC;
  REVOKE ALL ON FUNCTION central_apply_runtime_grants() FROM PUBLIC;
  EXECUTE format('GRANT EXECUTE ON FUNCTION central_retention_apply() TO %I', rt);
  EXECUTE format('GRANT EXECUTE ON FUNCTION central_forget_actor(text, text) TO %I', rt);
  EXECUTE format('GRANT EXECUTE ON FUNCTION central_forget_audit_apply(int) TO %I', rt);
  EXECUTE format('GRANT EXECUTE ON FUNCTION central_correct_actor_email(uuid, text, text) TO %I', rt);
END $$;
COMMENT ON FUNCTION central_apply_runtime_grants() IS
  'Converges the ctrlapi_rt role to the Central least-privilege model (0063). Run by the migration owner; sets no password.';

SELECT central_apply_runtime_grants();

COMMIT;
