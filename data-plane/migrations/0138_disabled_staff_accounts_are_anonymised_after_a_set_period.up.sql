-- 0138 -- DISABLED STAFF ACCOUNTS ARE ANONYMISED AFTER A SET PERIOD
-- (GDPR audit 2026-10-19, B4; docs/privacy/GDPR_AUDIT_2026-10-19.md, docs/OPERATIONAL_SETTINGS_POLICY.md).
--
-- "Remove" in Hotel Admin only disables a staff account, so the email and name stayed for ever. Now:
--
--   1. operators.disabled_at records when an account was disabled (a trigger sets it; existing disabled
--      accounts take their last update time), and operators.anonymised_at when it was anonymised. An
--      anonymised account cannot be enabled again.
--   2. public.operator_account_retention -- how long a disabled account is kept, an audited appliance setting:
--      default 730 days (24 months, the period the Product Owner chose for Central accounts), 30 to 3650.
--   3. public.run_staff_retention -- run hourly by scd beside the guest retention. It anonymises accounts
--      disabled for longer than the setting: the email becomes removed-<id>@staff.invalid, the name, password,
--      single sign-on subject and authenticator are removed, and the email is taken out of the activity-log
--      entries about the account. The account row and its id stay, so the activity log still says which
--      (anonymous) account did what.
--   4. Staff IP addresses kept outside the activity log -- system_network_audit.source_ip and
--      portal_https_settings_changes.source_ip -- are cleared after the site's activity-log period, like the
--      activity log's own.
--
-- What is NOT changed: the label a staff member's email left in change-history columns (changed_by,
-- released_by and the like). Those records are append-only accountability evidence and keep it for their own
-- life; the record of processing says so.

BEGIN;

-- ---------------------------------------------------------------------------------------------------------
-- 1. When an account was disabled, and when it was anonymised.
-- ---------------------------------------------------------------------------------------------------------
ALTER TABLE public.operators ADD COLUMN IF NOT EXISTS disabled_at timestamptz;
ALTER TABLE public.operators ADD COLUMN IF NOT EXISTS anonymised_at timestamptz;
UPDATE public.operators SET disabled_at = updated_at WHERE status = 'disabled' AND disabled_at IS NULL;

CREATE OR REPLACE FUNCTION public.operators_track_disabled() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
  IF OLD.anonymised_at IS NOT NULL AND NEW.status <> 'disabled' THEN
    RAISE EXCEPTION 'an anonymised staff account cannot be enabled again' USING ERRCODE = 'restrict_violation';
  END IF;
  IF NEW.status = 'disabled' AND OLD.status IS DISTINCT FROM 'disabled' THEN
    NEW.disabled_at := COALESCE(NEW.disabled_at, now());
  ELSIF NEW.status <> 'disabled' THEN
    NEW.disabled_at := NULL;
  END IF;
  RETURN NEW;
END $$;
REVOKE EXECUTE ON FUNCTION public.operators_track_disabled() FROM PUBLIC;
DROP TRIGGER IF EXISTS operators_track_disabled ON public.operators;
CREATE TRIGGER operators_track_disabled BEFORE UPDATE OF status ON public.operators
  FOR EACH ROW EXECUTE FUNCTION public.operators_track_disabled();

-- ---------------------------------------------------------------------------------------------------------
-- 2. The setting.
-- ---------------------------------------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.operator_account_retention (
  id                    boolean PRIMARY KEY DEFAULT true CONSTRAINT oacr_singleton CHECK (id),
  disabled_account_days integer NOT NULL DEFAULT 730
    CONSTRAINT oacr_days CHECK (disabled_account_days BETWEEN 30 AND 3650),
  config_version        bigint  NOT NULL DEFAULT 1 CONSTRAINT oacr_version CHECK (config_version >= 1),
  updated_at            timestamptz NOT NULL DEFAULT now(),
  updated_by            text
);
COMMENT ON TABLE public.operator_account_retention IS
  'How long a disabled staff account is kept before it is anonymised (0138, GDPR audit B4). One row for the '
  'appliance; no row means the default, 730 days. Written only through public.operator_account_retention_set.';

CREATE TABLE IF NOT EXISTS public.operator_account_retention_changes (
  id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  changed_by         text NOT NULL CONSTRAINT oacrc_actor CHECK (length(btrim(changed_by)) > 0),
  change_reason      text NOT NULL CONSTRAINT oacrc_reason CHECK (length(btrim(change_reason)) BETWEEN 4 AND 500),
  old_values         jsonb,
  new_values         jsonb NOT NULL,
  new_config_version bigint NOT NULL,
  changed_at         timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS operator_account_retention_changes_at ON public.operator_account_retention_changes (changed_at DESC);
COMMENT ON TABLE public.operator_account_retention_changes IS 'Append-only history of public.operator_account_retention (0138).';

CREATE OR REPLACE FUNCTION public.operator_account_retention_changes_append_only() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'public.operator_account_retention_changes is append-only: % refused', TG_OP
    USING ERRCODE = 'restrict_violation';
END $$;
REVOKE EXECUTE ON FUNCTION public.operator_account_retention_changes_append_only() FROM PUBLIC;
DROP TRIGGER IF EXISTS operator_account_retention_changes_append_only ON public.operator_account_retention_changes;
CREATE TRIGGER operator_account_retention_changes_append_only
  BEFORE UPDATE OR DELETE ON public.operator_account_retention_changes
  FOR EACH ROW EXECUTE FUNCTION public.operator_account_retention_changes_append_only();

CREATE OR REPLACE FUNCTION public.operator_account_retention_get()
  RETURNS TABLE (disabled_account_days integer, config_version bigint, updated_at timestamptz, updated_by text)
  LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public, pg_temp AS $fn$
  SELECT COALESCE(p.disabled_account_days, 730), COALESCE(p.config_version, 0::bigint), p.updated_at, p.updated_by
    FROM (SELECT 1) one
    LEFT JOIN public.operator_account_retention p ON p.id;
$fn$;
REVOKE EXECUTE ON FUNCTION public.operator_account_retention_get() FROM PUBLIC;

CREATE OR REPLACE FUNCTION public.operator_account_retention_set(p_disabled_account_days integer, p_operator text, p_reason text)
  RETURNS bigint
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = public, pg_temp AS $fn$
DECLARE v_version bigint; v_old jsonb;
BEGIN
  IF p_operator IS NULL OR btrim(p_operator) = '' THEN
    RAISE EXCEPTION 'an operator label is required' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  IF p_reason IS NULL OR length(btrim(p_reason)) < 4 OR length(btrim(p_reason)) > 500 THEN
    RAISE EXCEPTION 'a reason of 4-500 characters is required to change how long disabled accounts are kept'
      USING ERRCODE = 'invalid_parameter_value';
  END IF;
  PERFORM pg_advisory_xact_lock(hashtext('operator_account_retention'));
  SELECT jsonb_build_object('disabled_account_days', p.disabled_account_days)
    INTO v_old FROM public.operator_account_retention p WHERE p.id FOR UPDATE;
  INSERT INTO public.operator_account_retention AS p (id, disabled_account_days, updated_by)
  VALUES (true, p_disabled_account_days, btrim(p_operator))
  ON CONFLICT (id) DO UPDATE
     SET disabled_account_days = EXCLUDED.disabled_account_days, updated_by = EXCLUDED.updated_by,
         config_version = p.config_version + 1, updated_at = now()
  RETURNING p.config_version INTO v_version;
  INSERT INTO public.operator_account_retention_changes (changed_by, change_reason, old_values, new_values, new_config_version)
  VALUES (btrim(p_operator), btrim(p_reason), v_old,
          jsonb_build_object('disabled_account_days', p_disabled_account_days), v_version);
  RETURN v_version;
END $fn$;
REVOKE EXECUTE ON FUNCTION public.operator_account_retention_set(integer,text,text) FROM PUBLIC;

-- ---------------------------------------------------------------------------------------------------------
-- 3. The HTTPS-settings history may have its source address cleared by the retention run, and nothing else.
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION public.portal_https_settings_changes_append_only() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP = 'UPDATE' AND current_setting('public.staff_retention', true) = 'on'
     AND NEW.source_ip IS NULL
     AND (to_jsonb(NEW) - 'source_ip') = (to_jsonb(OLD) - 'source_ip') THEN
    RETURN NEW;
  END IF;
  RAISE EXCEPTION 'portal_https_settings_changes is append-only';
END $$;

-- ---------------------------------------------------------------------------------------------------------
-- 4. The run.
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION public.run_staff_retention(p_tenant uuid, p_site uuid)
  RETURNS jsonb
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = public, pg_temp AS $fn$
DECLARE v_days integer; v_audit_days integer; ids uuid[]; n_ops integer; n_audit integer; n_net integer; n_https integer;
BEGIN
  SELECT r.disabled_account_days INTO v_days FROM public.operator_account_retention_get() r;
  SELECT r.audit_log_days INTO v_audit_days FROM iam_v2.data_retention_get(p_tenant, p_site) r;

  SELECT COALESCE(array_agg(o.id), '{}') INTO ids FROM (
    SELECT o.id FROM public.operators o
     WHERE o.status = 'disabled' AND o.anonymised_at IS NULL
       AND o.disabled_at < now() - make_interval(days => v_days)
     ORDER BY o.disabled_at LIMIT 500 FOR UPDATE) o;
  UPDATE public.operators o
     SET email = 'removed-' || left(replace(o.id::text, '-', ''), 12) || '@staff.invalid',
         display_name = NULL, password_hash = NULL, oidc_sub = NULL, anonymised_at = now(), updated_at = now()
   WHERE o.id = ANY (ids);
  GET DIAGNOSTICS n_ops = ROW_COUNT;
  DELETE FROM public.operator_mfa_recovery_codes WHERE operator_id = ANY (ids);
  DELETE FROM public.operator_mfa WHERE operator_id = ANY (ids);
  UPDATE public.audit_log l SET payload = l.payload - 'email' - 'display_name' - 'username'
   WHERE l.target_type = 'operator' AND l.target_id = ANY (ids::text[])
     AND (l.payload ? 'email' OR l.payload ? 'display_name' OR l.payload ? 'username');
  GET DIAGNOSTICS n_audit = ROW_COUNT;

  UPDATE public.system_network_audit x SET source_ip = NULL
   WHERE x.source_ip IS NOT NULL AND x.created_at < now() - make_interval(days => v_audit_days);
  GET DIAGNOSTICS n_net = ROW_COUNT;
  PERFORM set_config('public.staff_retention', 'on', true);
  UPDATE public.portal_https_settings_changes x SET source_ip = NULL
   WHERE x.source_ip IS NOT NULL AND x.changed_at < now() - make_interval(days => v_audit_days);
  GET DIAGNOSTICS n_https = ROW_COUNT;
  PERFORM set_config('public.staff_retention', '', true);

  RETURN jsonb_build_object('staff_accounts_anonymised', n_ops, 'audit_entries', n_audit,
                            'staff_addresses_cleared', n_net + n_https);
END $fn$;
COMMENT ON FUNCTION public.run_staff_retention(uuid, uuid) IS
  'Hourly (scd, 0138, GDPR audit B4): anonymises staff accounts disabled for longer than '
  'public.operator_account_retention, and clears staff IP addresses kept outside the activity log once they are '
  'older than the site''s activity-log period. Returns what changed.';
REVOKE EXECUTE ON FUNCTION public.run_staff_retention(uuid, uuid) FROM PUBLIC;

-- ---------------------------------------------------------------------------------------------------------
-- 5. Least privilege, mirrored in deploy/gatep/svc-staff-retention-grants.sql.
-- ---------------------------------------------------------------------------------------------------------
REVOKE ALL ON public.operator_account_retention, public.operator_account_retention_changes FROM PUBLIC;
DO $g$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_scd') THEN
    GRANT EXECUTE ON FUNCTION public.run_staff_retention(uuid, uuid) TO svc_scd;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
    GRANT EXECUTE ON FUNCTION public.operator_account_retention_get() TO svc_edged;
    GRANT EXECUTE ON FUNCTION public.operator_account_retention_set(integer,text,text) TO svc_edged;
    GRANT SELECT ON public.operator_account_retention_changes TO svc_edged;
  END IF;
END $g$;

COMMIT;
