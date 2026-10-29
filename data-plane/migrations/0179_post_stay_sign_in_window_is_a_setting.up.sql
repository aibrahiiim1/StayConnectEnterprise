-- 0179 -- HOW LONG AFTER CHECK-OUT A GUEST MAY STILL SIGN IN WITH THEIR ROOM AND RESERVATION IS A SETTING (AD-36).
--
-- Post-stay sign-in (0165) found the stay by room number and reservation number, but nothing limited how long after
-- check-out that stayed possible: a stay left months ago still opened a fresh 24-hour profile. The PIN it replaced was
-- valid for at most 30 days. The window is now a per-site, bounded, audited setting (docs/OPERATIONAL_SETTINGS_POLICY.md):
-- post_stay_signin_days, default 7, 1..30, measured from the stay's effective check-out. scd reads it through
-- iam_v2.signin_capacity_settings_get with the other sign-in limits; it is changed only through ..._set, which writes
-- the append-only change log.
--
-- Additive and idempotent. The two functions change their column list, so they are dropped and created again; their
-- owner and grants are restored below (and by the Gate-P reconcile).

BEGIN;

ALTER TABLE iam_v2.site_signin_capacity_settings
  ADD COLUMN IF NOT EXISTS post_stay_signin_days integer NOT NULL DEFAULT 7;
DO $c$ BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'ssc_post_stay_days'
                   AND conrelid = 'iam_v2.site_signin_capacity_settings'::regclass) THEN
    ALTER TABLE iam_v2.site_signin_capacity_settings
      ADD CONSTRAINT ssc_post_stay_days CHECK (post_stay_signin_days BETWEEN 1 AND 30);
  END IF;
END $c$;

DROP FUNCTION IF EXISTS iam_v2.signin_capacity_settings_set(uuid,uuid,jsonb,text,text);
DROP FUNCTION IF EXISTS iam_v2.signin_capacity_settings_get(uuid,uuid);

CREATE OR REPLACE FUNCTION iam_v2.signin_capacity_settings_get(p_tenant uuid, p_site uuid)
  RETURNS TABLE (otp_code_lifetime_minutes integer, otp_max_attempts integer, otp_device_hourly_cap integer,
                 otp_destination_hourly_cap integer, otp_source_hourly_cap integer,
                 signin_ceiling_per_minute integer, network_pause_exempt_hours integer,
                 device_retention_days integer, portal_image_max_edge_px integer, portal_image_quality integer,
                 post_stay_signin_days integer, config_version bigint, updated_at timestamptz, updated_by text)
  LANGUAGE sql STABLE SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
  SELECT COALESCE(s.otp_code_lifetime_minutes, 10), COALESCE(s.otp_max_attempts, 5),
         COALESCE(s.otp_device_hourly_cap, 5), COALESCE(s.otp_destination_hourly_cap, 15),
         COALESCE(s.otp_source_hourly_cap, 20), s.signin_ceiling_per_minute,
         COALESCE(s.network_pause_exempt_hours, 24), COALESCE(s.device_retention_days, 90),
         COALESCE(s.portal_image_max_edge_px, 2560), COALESCE(s.portal_image_quality, 82), COALESCE(s.post_stay_signin_days, 7),
         COALESCE(s.config_version, 0), s.updated_at, s.updated_by
    FROM (SELECT 1) one
    LEFT JOIN iam_v2.site_signin_capacity_settings s ON s.tenant_id = p_tenant AND s.site_id = p_site;
$fn$;
REVOKE EXECUTE ON FUNCTION iam_v2.signin_capacity_settings_get(uuid,uuid) FROM PUBLIC;

CREATE OR REPLACE FUNCTION iam_v2.signin_capacity_settings_set(
    p_tenant uuid, p_site uuid, p_values jsonb, p_operator text, p_reason text)
  RETURNS bigint
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE v_version bigint; v_old jsonb; v_new jsonb; k text;
BEGIN
  IF p_operator IS NULL OR btrim(p_operator) = '' THEN
    RAISE EXCEPTION 'an operator label is required' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  IF p_reason IS NULL OR length(btrim(p_reason)) < 4 OR length(btrim(p_reason)) > 500 THEN
    RAISE EXCEPTION 'a reason of 4-500 characters is required to change the sign-in limits'
      USING ERRCODE = 'invalid_parameter_value';
  END IF;
  IF p_values IS NULL OR jsonb_typeof(p_values) <> 'object' OR p_values = '{}'::jsonb THEN
    RAISE EXCEPTION 'no setting named' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  FOR k IN SELECT jsonb_object_keys(p_values) LOOP
    IF k NOT IN ('otp_code_lifetime_minutes','otp_max_attempts','otp_device_hourly_cap','otp_destination_hourly_cap',
                 'otp_source_hourly_cap','signin_ceiling_per_minute','network_pause_exempt_hours',
                 'device_retention_days','portal_image_max_edge_px','portal_image_quality','post_stay_signin_days') THEN
      RAISE EXCEPTION 'unknown setting %', k USING ERRCODE = 'invalid_parameter_value';
    END IF;
  END LOOP;
  PERFORM pg_advisory_xact_lock(hashtext('signin_capacity_settings'), hashtext(p_site::text));
  INSERT INTO iam_v2.site_signin_capacity_settings (tenant_id, site_id, updated_by, config_version)
  VALUES (p_tenant, p_site, btrim(p_operator), 0) ON CONFLICT (tenant_id, site_id) DO NOTHING;
  SELECT to_jsonb(g) - 'config_version' - 'updated_at' - 'updated_by' INTO v_old
    FROM iam_v2.signin_capacity_settings_get(p_tenant, p_site) g;
  UPDATE iam_v2.site_signin_capacity_settings s SET
      otp_code_lifetime_minutes  = COALESCE((p_values->>'otp_code_lifetime_minutes')::int, s.otp_code_lifetime_minutes),
      otp_max_attempts           = COALESCE((p_values->>'otp_max_attempts')::int, s.otp_max_attempts),
      otp_device_hourly_cap      = COALESCE((p_values->>'otp_device_hourly_cap')::int, s.otp_device_hourly_cap),
      otp_destination_hourly_cap = COALESCE((p_values->>'otp_destination_hourly_cap')::int, s.otp_destination_hourly_cap),
      otp_source_hourly_cap      = COALESCE((p_values->>'otp_source_hourly_cap')::int, s.otp_source_hourly_cap),
      signin_ceiling_per_minute  = CASE WHEN p_values ? 'signin_ceiling_per_minute'
                                        THEN (p_values->>'signin_ceiling_per_minute')::int
                                        ELSE s.signin_ceiling_per_minute END,
      network_pause_exempt_hours = COALESCE((p_values->>'network_pause_exempt_hours')::int, s.network_pause_exempt_hours),
      device_retention_days      = COALESCE((p_values->>'device_retention_days')::int, s.device_retention_days),
      portal_image_max_edge_px   = COALESCE((p_values->>'portal_image_max_edge_px')::int, s.portal_image_max_edge_px),
      portal_image_quality       = COALESCE((p_values->>'portal_image_quality')::int, s.portal_image_quality),
      post_stay_signin_days      = COALESCE((p_values->>'post_stay_signin_days')::int, s.post_stay_signin_days),
      updated_by = btrim(p_operator), updated_at = now(), config_version = s.config_version + 1
    WHERE s.tenant_id = p_tenant AND s.site_id = p_site
    RETURNING s.config_version INTO v_version;
  SELECT to_jsonb(g) - 'config_version' - 'updated_at' - 'updated_by' INTO v_new
    FROM iam_v2.signin_capacity_settings_get(p_tenant, p_site) g;
  INSERT INTO iam_v2.signin_capacity_settings_changes
    (tenant_id, site_id, changed_by, change_reason, old_values, new_values, new_config_version)
  VALUES (p_tenant, p_site, btrim(p_operator), btrim(p_reason), v_old, v_new, v_version);
  RETURN v_version;
END $fn$;
REVOKE EXECUTE ON FUNCTION iam_v2.signin_capacity_settings_set(uuid,uuid,jsonb,text,text) FROM PUBLIC;

DO $own$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
    EXECUTE 'ALTER FUNCTION iam_v2.signin_capacity_settings_get(uuid,uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.signin_capacity_settings_set(uuid,uuid,jsonb,text,text) OWNER TO iam_v2_owner';
  END IF;
END $own$;

DO $g$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_scd') THEN
    GRANT EXECUTE ON FUNCTION iam_v2.signin_capacity_settings_get(uuid,uuid) TO svc_scd;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
    GRANT EXECUTE ON FUNCTION iam_v2.signin_capacity_settings_get(uuid,uuid) TO svc_edged;
    GRANT EXECUTE ON FUNCTION iam_v2.signin_capacity_settings_set(uuid,uuid,jsonb,text,text) TO svc_edged;
  END IF;
END $g$;

COMMIT;
