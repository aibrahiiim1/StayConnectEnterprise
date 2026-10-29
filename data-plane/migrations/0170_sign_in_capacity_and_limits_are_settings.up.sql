-- 0170 -- SIGN-IN LIMITS AND CAPACITY ARE SETTINGS (audit decisions AD-30, AD-31, AD-34, AD-51, AD-52, AD-58).
--
-- Operational values a hotel administrator may need to change are persisted, bounded, defaulted and audited
-- settings (CLAUDE.md 0C): one row per site in iam_v2.site_signin_capacity_settings, read and written only through
-- definer functions, with an append-only change log -- the 0118 shape.
--
--   AD-30  one-time code (OTP): lifetime, wrong-guess limit, codes per destination AND device per hour, a higher
--          destination-wide ceiling, codes per source address per hour.
--   AD-31  room sign-in pause: a device that signed in with a room recently is not paused with the network.
--   AD-34  device retention: devices not seen for N days, and referenced by nothing, are pruned.
--   AD-51  the appliance-wide sign-in ceiling per minute. NULL = follow the licence guest cap (never below 600).
--   AD-52  a LISTEN/NOTIFY channel so scd's short settings cache is cleared when a setting changes (the TTL stays as
--          the fallback), and a NOTIFY when a session becomes active so the last sign-in step need not poll.
--   AD-58  uploaded portal image limits: longest edge and compression quality.
--
-- Additive and idempotent. No existing row is changed. The notify triggers carry no data (only a channel name and,
-- for sessions, the session id).

BEGIN;

CREATE TABLE IF NOT EXISTS iam_v2.site_signin_capacity_settings (
  tenant_id                  uuid NOT NULL,
  site_id                    uuid NOT NULL,
  otp_code_lifetime_minutes  integer NOT NULL DEFAULT 10
    CONSTRAINT ssc_otp_lifetime CHECK (otp_code_lifetime_minutes BETWEEN 2 AND 30),
  otp_max_attempts           integer NOT NULL DEFAULT 5
    CONSTRAINT ssc_otp_attempts CHECK (otp_max_attempts BETWEEN 3 AND 10),
  otp_device_hourly_cap      integer NOT NULL DEFAULT 5
    CONSTRAINT ssc_otp_device_cap CHECK (otp_device_hourly_cap BETWEEN 1 AND 20),
  otp_destination_hourly_cap integer NOT NULL DEFAULT 15
    CONSTRAINT ssc_otp_destination_cap CHECK (otp_destination_hourly_cap BETWEEN 1 AND 100),
  otp_source_hourly_cap      integer NOT NULL DEFAULT 20
    CONSTRAINT ssc_otp_source_cap CHECK (otp_source_hourly_cap BETWEEN 5 AND 500),
  signin_ceiling_per_minute  integer
    CONSTRAINT ssc_ceiling CHECK (signin_ceiling_per_minute IS NULL OR signin_ceiling_per_minute BETWEEN 600 AND 100000),
  network_pause_exempt_hours integer NOT NULL DEFAULT 24
    CONSTRAINT ssc_pause_exempt CHECK (network_pause_exempt_hours BETWEEN 0 AND 168),
  device_retention_days      integer NOT NULL DEFAULT 90
    CONSTRAINT ssc_device_retention CHECK (device_retention_days BETWEEN 7 AND 3650),
  portal_image_max_edge_px   integer NOT NULL DEFAULT 2560
    CONSTRAINT ssc_image_edge CHECK (portal_image_max_edge_px BETWEEN 640 AND 4096),
  portal_image_quality       integer NOT NULL DEFAULT 82
    CONSTRAINT ssc_image_quality CHECK (portal_image_quality BETWEEN 50 AND 95),
  config_version             bigint NOT NULL DEFAULT 0 CONSTRAINT ssc_version CHECK (config_version >= 0),
  updated_at                 timestamptz NOT NULL DEFAULT now(),
  updated_by                 text,
  PRIMARY KEY (tenant_id, site_id),
  CONSTRAINT ssc_destination_over_device CHECK (otp_destination_hourly_cap >= otp_device_hourly_cap)
);
COMMENT ON TABLE iam_v2.site_signin_capacity_settings IS
  'Per-site sign-in limits and capacity (0170): one-time-code lifetime and caps, the appliance-wide sign-in ceiling '
  '(NULL follows the licence guest cap, never below 600 a minute), the room sign-in pause exemption, device '
  'retention and the uploaded-image limits. A site with no row reads the defaults. Written only through '
  'iam_v2.signin_capacity_settings_set.';

CREATE TABLE IF NOT EXISTS iam_v2.signin_capacity_settings_changes (
  id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id          uuid NOT NULL,
  site_id            uuid NOT NULL,
  changed_by         text NOT NULL CONSTRAINT scsc_actor CHECK (length(btrim(changed_by)) > 0),
  change_reason      text NOT NULL CONSTRAINT scsc_reason CHECK (length(btrim(change_reason)) > 0),
  old_values         jsonb,
  new_values         jsonb NOT NULL,
  new_config_version bigint NOT NULL,
  changed_at         timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS scsc_lookup ON iam_v2.signin_capacity_settings_changes (tenant_id, site_id, changed_at DESC);

CREATE OR REPLACE FUNCTION iam_v2.signin_capacity_settings_changes_append_only() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'iam_v2.signin_capacity_settings_changes is append-only: % refused', TG_OP
    USING ERRCODE = 'restrict_violation';
END $$;
REVOKE EXECUTE ON FUNCTION iam_v2.signin_capacity_settings_changes_append_only() FROM PUBLIC;
DROP TRIGGER IF EXISTS signin_capacity_settings_changes_append_only ON iam_v2.signin_capacity_settings_changes;
CREATE TRIGGER signin_capacity_settings_changes_append_only
  BEFORE UPDATE OR DELETE ON iam_v2.signin_capacity_settings_changes
  FOR EACH ROW EXECUTE FUNCTION iam_v2.signin_capacity_settings_changes_append_only();

-- The effective values, defaults applied.
CREATE OR REPLACE FUNCTION iam_v2.signin_capacity_settings_get(p_tenant uuid, p_site uuid)
  RETURNS TABLE (otp_code_lifetime_minutes integer, otp_max_attempts integer, otp_device_hourly_cap integer,
                 otp_destination_hourly_cap integer, otp_source_hourly_cap integer,
                 signin_ceiling_per_minute integer, network_pause_exempt_hours integer,
                 device_retention_days integer, portal_image_max_edge_px integer, portal_image_quality integer,
                 config_version bigint, updated_at timestamptz, updated_by text)
  LANGUAGE sql STABLE SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
  SELECT COALESCE(s.otp_code_lifetime_minutes, 10), COALESCE(s.otp_max_attempts, 5),
         COALESCE(s.otp_device_hourly_cap, 5), COALESCE(s.otp_destination_hourly_cap, 15),
         COALESCE(s.otp_source_hourly_cap, 20), s.signin_ceiling_per_minute,
         COALESCE(s.network_pause_exempt_hours, 24), COALESCE(s.device_retention_days, 90),
         COALESCE(s.portal_image_max_edge_px, 2560), COALESCE(s.portal_image_quality, 82),
         COALESCE(s.config_version, 0), s.updated_at, s.updated_by
    FROM (SELECT 1) one
    LEFT JOIN iam_v2.site_signin_capacity_settings s ON s.tenant_id = p_tenant AND s.site_id = p_site;
$fn$;
REVOKE EXECUTE ON FUNCTION iam_v2.signin_capacity_settings_get(uuid,uuid) FROM PUBLIC;

-- p_values is a JSON object of only the fields to change; a field it does not name keeps its value. The CHECK
-- constraints are the validation, so the bounds live in one place. signin_ceiling_per_minute may be JSON null,
-- which means "follow the licence".
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
                 'device_retention_days','portal_image_max_edge_px','portal_image_quality') THEN
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

-- ---------------------------------------------------------------------------------------------------------
-- AD-52: change notifications. They carry no data; a listener only clears a cache or wakes a waiter.
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION iam_v2.notify_settings_changed() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  PERFORM pg_notify('onegate_settings', TG_TABLE_NAME);
  RETURN NULL;
END $$;
REVOKE EXECUTE ON FUNCTION iam_v2.notify_settings_changed() FROM PUBLIC;

DROP TRIGGER IF EXISTS notify_settings_changed ON iam_v2.site_signin_capacity_settings;
CREATE TRIGGER notify_settings_changed
  AFTER INSERT OR UPDATE ON iam_v2.site_signin_capacity_settings
  FOR EACH STATEMENT EXECUTE FUNCTION iam_v2.notify_settings_changed();

-- The sign-in method switches live on the tenant row (tenants.auth_methods): an Admin Console "off" must reach
-- scd's cache at once, not at the end of its TTL.
DROP TRIGGER IF EXISTS notify_settings_changed ON public.tenants;
CREATE TRIGGER notify_settings_changed
  AFTER UPDATE OF auth_methods ON public.tenants
  FOR EACH STATEMENT EXECUTE FUNCTION iam_v2.notify_settings_changed();

-- A session becoming active is what scd's last sign-in step waits for (it polled every 100 ms). The id is not guest data.
CREATE OR REPLACE FUNCTION iam_v2.notify_session_active() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  PERFORM pg_notify('onegate_session_active', NEW.id::text);
  RETURN NULL;
END $$;
REVOKE EXECUTE ON FUNCTION iam_v2.notify_session_active() FROM PUBLIC;

DROP TRIGGER IF EXISTS notify_session_active ON iam_v2.sessions;
CREATE TRIGGER notify_session_active
  AFTER UPDATE OF state ON iam_v2.sessions
  FOR EACH ROW WHEN (NEW.state = 'active' AND OLD.state IS DISTINCT FROM 'active')
  EXECUTE FUNCTION iam_v2.notify_session_active();

-- ---------------------------------------------------------------------------------------------------------
-- AD-34: device retention. A device seen last longer ago than the setting, and referenced by nothing at all, is
-- removed. "Referenced" is read from the catalogue, so a table added later that points at devices protects its
-- rows without this function being edited. Batched; returns how many were removed.
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION iam_v2.prune_idle_devices(p_tenant uuid, p_site uuid, p_batch integer)
  RETURNS integer
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE v_days integer; v_sql text; v_not text := ''; r record; v_n integer;
BEGIN
  IF p_batch IS NULL OR p_batch < 1 OR p_batch > 5000 THEN
    RAISE EXCEPTION 'batch must be 1-5000' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  SELECT g.device_retention_days INTO v_days FROM iam_v2.signin_capacity_settings_get(p_tenant, p_site) g;
  FOR r IN
    SELECT c.conrelid::regclass::text AS tbl, a.attname AS col
      FROM pg_constraint c
      JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = c.conkey[1]
     WHERE c.contype = 'f' AND c.confrelid = 'iam_v2.devices'::regclass AND array_length(c.conkey, 1) = 1
  LOOP
    v_not := v_not || format(' AND NOT EXISTS (SELECT 1 FROM %s x WHERE x.%I = d.id)', r.tbl, r.col);
  END LOOP;
  v_sql := format($q$
    WITH doomed AS (
      SELECT d.id FROM iam_v2.devices d
       WHERE d.tenant_id = %L AND d.site_id = %L
         AND COALESCE(d.last_seen, d.first_seen) < now() - make_interval(days => %s)
         %s
       ORDER BY COALESCE(d.last_seen, d.first_seen) LIMIT %s)
    DELETE FROM iam_v2.devices d USING doomed WHERE d.id = doomed.id$q$,
    p_tenant, p_site, v_days, v_not, p_batch);
  EXECUTE v_sql;
  GET DIAGNOSTICS v_n = ROW_COUNT;
  RETURN v_n;
END $fn$;
REVOKE EXECUTE ON FUNCTION iam_v2.prune_idle_devices(uuid,uuid,integer) FROM PUBLIC;
COMMENT ON FUNCTION iam_v2.prune_idle_devices(uuid,uuid,integer) IS
  'AD-34: removes devices unseen for device_retention_days that nothing references (every foreign key to '
  'iam_v2.devices is consulted from the catalogue). Never removes a device with a session, a slot, a credential or any other dependent row.';

-- ---------------------------------------------------------------------------------------------------------
-- AD-31: the network-wide room sign-in pause does not stop a device that signed in with a room recently, and its
-- default is raised from 30 to 60 wrong sign-ins in the window. A device already paused by its own restriction
-- (0068) is unaffected: this is the NETWORK gate only.
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION iam_v2.guest_signin_network_policy_get(p_tenant uuid, p_site uuid)
  RETURNS TABLE (network_max_failures integer, network_window_seconds integer, network_pause_seconds integer,
                 config_version bigint, updated_at timestamptz, is_default boolean)
  LANGUAGE sql STABLE SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
  SELECT COALESCE(s.network_max_failures, 60), COALESCE(s.network_window_seconds, 600),
         COALESCE(s.network_pause_seconds, 900), COALESCE(s.config_version, 1), s.updated_at, (s.tenant_id IS NULL)
    FROM (SELECT 1) one
    LEFT JOIN iam_v2.site_guest_signin_network_policy s ON s.tenant_id = p_tenant AND s.site_id = p_site;
$fn$;

CREATE OR REPLACE FUNCTION iam_v2.guest_signin_network_gate(p_tenant uuid, p_site uuid, p_network uuid, p_mac macaddr)
  RETURNS TABLE (paused boolean, pause_id uuid, remaining_seconds integer)
  LANGUAGE sql STABLE SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
  SELECT (p.id IS NOT NULL
          AND NOT EXISTS (SELECT 1 FROM iam_v2.guest_signin_device_passes d
                           WHERE d.pause_id = p.id AND d.device_mac = p_mac)
          AND NOT EXISTS (SELECT 1 FROM iam_v2.sign_in_attempts a, iam_v2.signin_capacity_settings_get(p_tenant, p_site) g
                           WHERE g.network_pause_exempt_hours > 0
                             AND a.tenant_id = p_tenant AND a.site_id = p_site AND a.device_mac = p_mac
                             AND a.result = 'VERIFIED'
                             AND a.occurred_at > now() - make_interval(hours => g.network_pause_exempt_hours))),
         p.id,
         CASE WHEN p.id IS NOT NULL THEN GREATEST(1, CEIL(EXTRACT(EPOCH FROM (p.expires_at - now())))::int) END
    FROM (SELECT 1) one
    LEFT JOIN LATERAL (
      SELECT x.id, x.expires_at FROM iam_v2.guest_signin_network_pauses x
       WHERE x.tenant_id = p_tenant AND x.site_id = p_site AND x.guest_network_id = p_network
         AND x.ended_at IS NULL AND x.expires_at > now()
       ORDER BY x.expires_at DESC LIMIT 1) p ON true;
$fn$;

-- ---------------------------------------------------------------------------------------------------------
-- Ownership and least privilege (mirrored in deploy/gatep/svc-signin-capacity-grants.sql).
-- ---------------------------------------------------------------------------------------------------------
DO $own$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
    EXECUTE 'ALTER TABLE iam_v2.site_signin_capacity_settings OWNER TO iam_v2_owner';
    EXECUTE 'ALTER TABLE iam_v2.signin_capacity_settings_changes OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.signin_capacity_settings_changes_append_only() OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.signin_capacity_settings_get(uuid,uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.signin_capacity_settings_set(uuid,uuid,jsonb,text,text) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.notify_settings_changed() OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.notify_session_active() OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.prune_idle_devices(uuid,uuid,integer) OWNER TO iam_v2_owner';
  END IF;
END $own$;

DO $g$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_scd') THEN
    GRANT EXECUTE ON FUNCTION iam_v2.signin_capacity_settings_get(uuid,uuid) TO svc_scd;
    GRANT EXECUTE ON FUNCTION iam_v2.prune_idle_devices(uuid,uuid,integer) TO svc_scd;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
    GRANT EXECUTE ON FUNCTION iam_v2.signin_capacity_settings_get(uuid,uuid) TO svc_edged;
    GRANT EXECUTE ON FUNCTION iam_v2.signin_capacity_settings_set(uuid,uuid,jsonb,text,text) TO svc_edged;
    GRANT SELECT ON iam_v2.signin_capacity_settings_changes TO svc_edged;
  END IF;
END $g$;

COMMIT;
