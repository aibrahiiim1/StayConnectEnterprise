-- 0140 down -- staff notes fall outside data retention again (0134's seven periods). The column is dropped with
-- every site's choice of it; notes already removed stay removed, as with any retention.

BEGIN;

DROP FUNCTION IF EXISTS iam_v2.run_staff_note_retention(uuid, uuid, integer);

CREATE OR REPLACE FUNCTION iam_v2.pms_case_resolutions_append_only() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'iam_v2.pms_case_resolutions is append-only: % refused', TG_OP
        USING ERRCODE = 'restrict_violation';
END;
$$;

CREATE OR REPLACE FUNCTION iam_v2.device_slot_actions_append_only() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'iam_v2.device_slot_actions is append-only: % refused', TG_OP USING ERRCODE = 'restrict_violation';
END $$;

DELETE FROM iam_v2.data_retention_runs WHERE category = 'staff_notes';
ALTER TABLE iam_v2.data_retention_runs DROP CONSTRAINT IF EXISTS drr_category;
ALTER TABLE iam_v2.data_retention_runs ADD CONSTRAINT drr_category CHECK (category = ANY (ARRAY[
  'pms_stays', 'client_activity', 'inactive_identities', 'expired_records', 'audit_log']));

DROP FUNCTION IF EXISTS iam_v2.data_retention_defaults();
CREATE FUNCTION iam_v2.data_retention_defaults(
    OUT pms_stay_days integer, OUT client_activity_days integer, OUT inactive_identity_days integer,
    OUT expired_record_days integer, OUT audit_log_days integer, OUT sign_in_attempt_days integer,
    OUT log_days integer)
    LANGUAGE sql IMMUTABLE
    AS $$ SELECT 30, 365, 365, 7, 730, 30, 14 $$;
COMMENT ON FUNCTION iam_v2.data_retention_defaults() IS
  'The Product Owner''s approved retention defaults, in days (D-C1 2026-10-17, D-F5 2026-10-19): PMS stay data 30 '
  'after checkout, sessions/devices 365, inactive guest identities 365, expired single-use records 7, audit log '
  '730, sign-in attempts 30, system logs 14. The one source of the defaults: a site with no row reads these.';

DROP FUNCTION IF EXISTS iam_v2.data_retention_get(uuid, uuid);
CREATE FUNCTION iam_v2.data_retention_get(p_tenant uuid, p_site uuid)
    RETURNS TABLE(pms_stay_days integer, client_activity_days integer, inactive_identity_days integer,
                  expired_record_days integer, audit_log_days integer, sign_in_attempt_days integer,
                  log_days integer, config_version bigint, updated_at timestamptz, updated_by text)
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
  SELECT COALESCE(s.pms_stay_days, d.pms_stay_days), COALESCE(s.client_activity_days, d.client_activity_days),
         COALESCE(s.inactive_identity_days, d.inactive_identity_days),
         COALESCE(s.expired_record_days, d.expired_record_days), COALESCE(s.audit_log_days, d.audit_log_days),
         COALESCE(s.sign_in_attempt_days, d.sign_in_attempt_days), COALESCE(s.log_days, d.log_days),
         COALESCE(s.config_version, 0), s.updated_at, s.updated_by
    FROM iam_v2.data_retention_defaults() d
    LEFT JOIN iam_v2.site_data_retention s ON s.tenant_id = p_tenant AND s.site_id = p_site;
$$;

DROP FUNCTION IF EXISTS iam_v2.data_retention_set(uuid,uuid,integer,integer,integer,integer,integer,integer,integer,integer,text,text);
CREATE FUNCTION iam_v2.data_retention_set(
    p_tenant uuid, p_site uuid, p_pms_stay_days integer, p_client_activity_days integer,
    p_inactive_identity_days integer, p_expired_record_days integer, p_audit_log_days integer,
    p_sign_in_attempt_days integer, p_log_days integer, p_operator text, p_reason text) RETURNS bigint
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE v_version bigint; v_old jsonb; v_new jsonb;
BEGIN
  IF p_operator IS NULL OR btrim(p_operator) = '' THEN
    RAISE EXCEPTION 'an operator label is required' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  IF p_reason IS NULL OR length(btrim(p_reason)) < 4 OR length(btrim(p_reason)) > 500 THEN
    RAISE EXCEPTION 'a reason of 4-500 characters is required to change data retention'
      USING ERRCODE = 'invalid_parameter_value';
  END IF;
  IF num_nulls(p_pms_stay_days, p_client_activity_days, p_inactive_identity_days, p_expired_record_days,
               p_audit_log_days, p_sign_in_attempt_days, p_log_days) > 0 THEN
    RAISE EXCEPTION 'every retention period is required' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  PERFORM pg_advisory_xact_lock(hashtext('data_retention'), hashtext(p_site::text));
  SELECT to_jsonb(s) - 'tenant_id' - 'site_id' - 'config_version' - 'updated_at' - 'updated_by' INTO v_old
    FROM iam_v2.site_data_retention s WHERE s.tenant_id = p_tenant AND s.site_id = p_site FOR UPDATE;
  INSERT INTO iam_v2.site_data_retention AS s
    (tenant_id, site_id, pms_stay_days, client_activity_days, inactive_identity_days, expired_record_days,
     audit_log_days, sign_in_attempt_days, log_days, updated_by)
  VALUES (p_tenant, p_site, p_pms_stay_days, p_client_activity_days, p_inactive_identity_days,
          p_expired_record_days, p_audit_log_days, p_sign_in_attempt_days, p_log_days, btrim(p_operator))
  ON CONFLICT (tenant_id, site_id) DO UPDATE
     SET pms_stay_days = EXCLUDED.pms_stay_days, client_activity_days = EXCLUDED.client_activity_days,
         inactive_identity_days = EXCLUDED.inactive_identity_days,
         expired_record_days = EXCLUDED.expired_record_days, audit_log_days = EXCLUDED.audit_log_days,
         sign_in_attempt_days = EXCLUDED.sign_in_attempt_days, log_days = EXCLUDED.log_days,
         updated_by = EXCLUDED.updated_by, config_version = s.config_version + 1, updated_at = now()
  RETURNING s.config_version, to_jsonb(s) - 'tenant_id' - 'site_id' - 'config_version' - 'updated_at' - 'updated_by'
    INTO v_version, v_new;
  INSERT INTO iam_v2.data_retention_changes
    (tenant_id, site_id, changed_by, change_reason, old_settings, new_settings, new_config_version)
  VALUES (p_tenant, p_site, btrim(p_operator), btrim(p_reason), v_old, v_new, v_version);
  RETURN v_version;
END $$;

DO $own$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
    EXECUTE 'ALTER FUNCTION iam_v2.data_retention_defaults() OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.data_retention_get(uuid,uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.data_retention_set(uuid,uuid,integer,integer,integer,integer,integer,integer,integer,text,text) OWNER TO iam_v2_owner';
  END IF;
END $own$;
REVOKE ALL ON FUNCTION iam_v2.data_retention_get(uuid,uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.data_retention_set(uuid,uuid,integer,integer,integer,integer,integer,integer,integer,text,text) FROM PUBLIC;
DO $g$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_scd') THEN
    GRANT EXECUTE ON FUNCTION iam_v2.data_retention_get(uuid,uuid) TO svc_scd;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
    GRANT EXECUTE ON FUNCTION iam_v2.data_retention_get(uuid,uuid) TO svc_edged;
    GRANT EXECUTE ON FUNCTION iam_v2.data_retention_set(uuid,uuid,integer,integer,integer,integer,integer,integer,integer,text,text) TO svc_edged;
  END IF;
END $g$;

ALTER TABLE iam_v2.site_data_retention DROP COLUMN IF EXISTS staff_note_days;

COMMIT;
