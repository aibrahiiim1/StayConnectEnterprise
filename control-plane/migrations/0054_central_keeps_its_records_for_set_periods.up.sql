-- 0054 -- CENTRAL KEEPS ITS RECORDS FOR SET PERIODS
-- (GDPR remediation plan, Central retention; Product-Owner decision 2026-10-19: the audit log 24 months; alerts
--  and appliance history as long as GDPR allows, which is 6 years for the history with the people in it removed
--  after 24 months; disabled accounts as long as GDPR allows, which is 24 months; retired appliances 6 years.)
--
-- Nothing in Central was ever removed. Four periods now apply, each a setting (System > Team > Data retention,
-- with a reason, audited; docs/OPERATIONAL_SETTINGS_POLICY.md), applied hourly by ctrlapi through
-- central_retention_apply():
--
--   audit_log_months         (24, 6-120)  audit log entries older than this are deleted.
--   history_personal_months  (24, 6-120)  in security alerts and appliance, certificate and portal-certificate
--                                         history, the people are removed: the operator's email, the source IP
--                                         address and who acknowledged an alert. The events themselves stay.
--   history_years            (6, 1-10)    those events are deleted (open alerts are kept until closed); ended
--                                         licences are deleted; a retired appliance's MAC addresses, host name
--                                         and last public IP are cleared. The licence is a contract record, and
--                                         6 years is the UK limitation period for a claim under it.
--   disabled_account_months  (24, 1-120)  an account disabled this long is anonymised: email replaced, name,
--                                         password, authenticator and roles removed. Its audit entries are gone
--                                         by then under the default periods, so nothing needs the identity.
--
-- Retired appliance records are cleared, not deleted: deleting one revokes certificates and releases DNS names
-- (appliance_ops.go), which a background job must not do. retired_appliance_identities holds only keys and
-- serials, no personal data, and is kept so a retired key stays refused.
--
-- operators gains disabled_at (set by trigger) and anonymised_at; an anonymised account cannot be re-enabled.
-- portal_tls_events stays append-only except to this function.

BEGIN;

CREATE TABLE IF NOT EXISTS central_retention_settings (
  id                      boolean PRIMARY KEY DEFAULT true CHECK (id),
  audit_log_months        int NOT NULL DEFAULT 24 CHECK (audit_log_months BETWEEN 6 AND 120),
  history_personal_months int NOT NULL DEFAULT 24 CHECK (history_personal_months BETWEEN 6 AND 120),
  history_years           int NOT NULL DEFAULT 6 CHECK (history_years BETWEEN 1 AND 10),
  disabled_account_months int NOT NULL DEFAULT 24 CHECK (disabled_account_months BETWEEN 1 AND 120),
  updated_at              timestamptz NOT NULL DEFAULT now(),
  updated_by              text
);
INSERT INTO central_retention_settings (id) VALUES (true) ON CONFLICT DO NOTHING;
COMMENT ON TABLE central_retention_settings IS
  'How long Central keeps its records (0054): audit log, the people in alerts and appliance history, the history '
  'itself with ended licences and retired appliance details, and disabled accounts. Applied hourly by '
  'central_retention_apply().';

ALTER TABLE operators
  ADD COLUMN IF NOT EXISTS disabled_at   timestamptz,
  ADD COLUMN IF NOT EXISTS anonymised_at timestamptz;
UPDATE operators SET disabled_at = updated_at WHERE status = 'disabled' AND disabled_at IS NULL;

CREATE OR REPLACE FUNCTION operators_track_disabled() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP = 'UPDATE' AND OLD.anonymised_at IS NOT NULL AND NEW.status <> 'disabled' THEN
    RAISE EXCEPTION 'an anonymised account cannot be enabled again' USING ERRCODE = 'check_violation';
  END IF;
  IF NEW.status = 'disabled' THEN
    IF TG_OP = 'INSERT' OR OLD.status <> 'disabled' THEN
      NEW.disabled_at := now();
    ELSE
      NEW.disabled_at := COALESCE(NEW.disabled_at, OLD.disabled_at, now());
    END IF;
  ELSE
    NEW.disabled_at := NULL;
  END IF;
  RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS operators_track_disabled ON operators;
CREATE TRIGGER operators_track_disabled BEFORE INSERT OR UPDATE ON operators
  FOR EACH ROW EXECUTE FUNCTION operators_track_disabled();

CREATE OR REPLACE FUNCTION portal_tls_events_append_only() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF current_setting('central.retention', true) = 'on' THEN
    IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
    RETURN NEW;
  END IF;
  RAISE EXCEPTION 'portal_tls_events is append-only';
END $$;

-- Applies the periods once and returns what it did, by record kind. Idempotent: a second run finds nothing.
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
  UPDATE portal_tls_events SET actor = 'removed' WHERE at < people_cut AND actor LIKE '%@%';
  GET DIAGNOSTICS n = ROW_COUNT; out := out || jsonb_build_object('portal_tls_events_anonymised', n); total := total + n;

  -- Ended licences: revoked, superseded by a newer one, or expired, before the history period. A licence with
  -- none of those ends at infinity and is never removed.
  DELETE FROM licenses l
   WHERE LEAST(COALESCE(l.revoked_at, 'infinity'), COALESCE(l.valid_until, 'infinity'),
               COALESCE((SELECT min(n2.created_at) FROM licenses n2 WHERE n2.supersedes_license_id = l.id), 'infinity'))
         < history_cut;
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

  PERFORM set_config('central.retention', 'off', true);
  IF total > 0 THEN
    INSERT INTO audit_log (actor_type, actor_id, action, target_type, target_id, payload)
    VALUES ('system', 'retention', 'retention.applied', 'settings', 'retention', out);
  END IF;
  RETURN out;
END $$;

COMMIT;
