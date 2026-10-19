-- 0055 -- A CENTRAL USER'S DATA CAN BE EXPORTED, AND THEIR EMAIL IS FORGOTTEN WHEN THEY GO
-- (GDPR audit 2026-10-19, B5; docs/privacy/GDPR_AUDIT_2026-10-19.md, docs/privacy/CENTRAL_PRIVACY_NOTICE.md).
--
-- Removing a Central user deleted the row but left their email in the history: the actor of appliance and
-- certificate events, the actor of portal certificate events, the updated_by of the settings, and the
-- user.created / user.removed audit payloads. Anonymising a disabled account (0054) left it in the same places.
--
--   1. central_forget_actor(email, label) replaces that email everywhere it is kept as text with a
--      removed-user label. What happened, when and to what stays; who did it becomes "a removed user".
--   2. Two triggers call it: BEFORE DELETE on operators (every removal path), and AFTER UPDATE when an account
--      is anonymised (the 0054 retention run).
--   3. central_user_data(id) is one user's data as a JSON document (Art. 15/20), for the platform to hand over:
--      the account, roles and second-step status, and what the history records about them.
--
-- The audit log keeps the user's id as actor_id: it is a random identifier that names nobody once the account
-- row is gone or anonymised.

BEGIN;

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
  -- A whole JSON string equal to the email, wherever it sits in a payload ("email", "username", "by" ...).
  UPDATE audit_log SET payload = replace(payload::text, to_jsonb(e)::text, to_jsonb(p_label)::text)::jsonb
   WHERE payload::text LIKE '%' || replace(replace(to_jsonb(e)::text, '%', '\%'), '_', '\_') || '%';
  GET DIAGNOSTICS n = ROW_COUNT; total := total + n;
  RETURN total;
END $$;
COMMENT ON FUNCTION central_forget_actor(text, text) IS
  'Replaces one user''s email with a removed-user label in every history column and audit payload that keeps it '
  'as text (0055, GDPR audit B5). Returns how many records changed.';

CREATE OR REPLACE FUNCTION operators_forget_on_delete() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  PERFORM central_forget_actor(OLD.email, 'removed-user-' || left(OLD.id::text, 8));
  RETURN OLD;
END $$;
DROP TRIGGER IF EXISTS operators_forget_on_delete ON operators;
CREATE TRIGGER operators_forget_on_delete BEFORE DELETE ON operators
  FOR EACH ROW EXECUTE FUNCTION operators_forget_on_delete();

CREATE OR REPLACE FUNCTION operators_forget_on_anonymise() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  PERFORM central_forget_actor(OLD.email, 'removed-user-' || left(OLD.id::text, 8));
  RETURN NULL;
END $$;
DROP TRIGGER IF EXISTS operators_forget_on_anonymise ON operators;
CREATE TRIGGER operators_forget_on_anonymise AFTER UPDATE OF anonymised_at ON operators
  FOR EACH ROW WHEN (OLD.anonymised_at IS NULL AND NEW.anonymised_at IS NOT NULL)
  EXECUTE FUNCTION operators_forget_on_anonymise();

-- The accounts 0054 already anonymised took their email with them, so nothing is left to find for those.

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
                    'browser', l.user_agent, 'details', l.payload) ORDER BY l.ts), '[]'::jsonb)
                   FROM audit_log l WHERE l.actor_id = o.id::text),
    'about_this_account', (SELECT COALESCE(jsonb_agg(jsonb_build_object(
                    'at', l.ts, 'action', l.action, 'details', l.payload) ORDER BY l.ts), '[]'::jsonb)
                   FROM audit_log l WHERE l.target_id = o.id::text AND l.actor_id IS DISTINCT FROM o.id::text),
    'appliance_events', (SELECT COALESCE(jsonb_agg(jsonb_build_object(
                    'at', x.created_at, 'from', x.from_state, 'to', x.to_state, 'from_ip', x.source_ip, 'reason', x.reason)
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
  'portal-certificate events they made. Changes nothing.';

COMMIT;
