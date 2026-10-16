-- 0125 -- THE WALLED GARDEN HAS A WIDEST NETWORK, AND THE HOTEL SETS IT.
--
-- The walled garden is what a client may reach BEFORE signing in. Its rules accepted any IPv4 CIDR, including
-- 0.0.0.0/0 -- a single rule that opens the whole internet without a sign-in, i.e. switches the captive portal
-- off without saying so (audit N-7). A garden rule is meant to name a sign-in or payment service, so the widest
-- network a rule may name is a property decision with a safe default: a /24 (256 addresses). Under the standing
-- operational-settings rule (docs/OPERATIONAL_SETTINGS_POLICY.md) it is a persisted, bounded, defaulted, audited
-- setting, in the shape of 0104.
--
-- Bounds: /16 (65,536 addresses, a large CDN block) to /32 (one address). Default /24. edged enforces the
-- setting on every create and edit; the enforcement path (internal/nft ValidGardenElem) independently refuses
-- anything wider than a /8, 0.0.0.0, loopback and multicast, so a rule stored before this migration -- or
-- written behind the console's back -- cannot open the internet either.
--
-- No existing rule is changed or removed. A rule wider than the setting stays listed (the console marks it),
-- and stops taking effect only if it is wider than the enforcement floor.

BEGIN;

CREATE TABLE IF NOT EXISTS iam_v2.site_walled_garden_settings (
  tenant_id          uuid NOT NULL,
  site_id            uuid NOT NULL,
  widest_prefix_len  integer NOT NULL DEFAULT 24,
  config_version     bigint  NOT NULL DEFAULT 1,
  updated_at         timestamptz NOT NULL DEFAULT now(),
  updated_by         text,
  PRIMARY KEY (tenant_id, site_id),
  CONSTRAINT walled_garden_settings_prefix CHECK (widest_prefix_len BETWEEN 16 AND 32),
  CONSTRAINT walled_garden_settings_version CHECK (config_version >= 1)
);

COMMENT ON TABLE iam_v2.site_walled_garden_settings IS
  'Per-site walled-garden policy: the widest IPv4 network (smallest prefix length) a rule may name. Read by edged '
  'on every rule create and edit.';

CREATE TABLE IF NOT EXISTS iam_v2.walled_garden_settings_changes (
  id                     uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id              uuid NOT NULL,
  site_id                uuid NOT NULL,
  changed_by             text NOT NULL
    CONSTRAINT walled_garden_settings_changes_actor CHECK (length(btrim(changed_by)) > 0),
  change_reason          text NOT NULL
    CONSTRAINT walled_garden_settings_changes_reason CHECK (length(btrim(change_reason)) > 0),
  old_widest_prefix_len  integer,
  new_widest_prefix_len  integer NOT NULL,
  new_config_version     bigint  NOT NULL,
  changed_at             timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS walled_garden_settings_changes_lookup
  ON iam_v2.walled_garden_settings_changes (tenant_id, site_id, changed_at DESC);

CREATE OR REPLACE FUNCTION iam_v2.walled_garden_settings_changes_append_only() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'iam_v2.walled_garden_settings_changes is append-only: % refused', TG_OP
    USING ERRCODE = 'restrict_violation';
END $$;

REVOKE EXECUTE ON FUNCTION iam_v2.walled_garden_settings_changes_append_only() FROM PUBLIC;

DROP TRIGGER IF EXISTS walled_garden_settings_changes_append_only ON iam_v2.walled_garden_settings_changes;
CREATE TRIGGER walled_garden_settings_changes_append_only
  BEFORE UPDATE OR DELETE ON iam_v2.walled_garden_settings_changes
  FOR EACH ROW EXECUTE FUNCTION iam_v2.walled_garden_settings_changes_append_only();

CREATE OR REPLACE FUNCTION iam_v2.walled_garden_settings_get(p_tenant uuid, p_site uuid)
  RETURNS TABLE (widest_prefix_len integer, config_version bigint, updated_at timestamptz, updated_by text)
  LANGUAGE sql STABLE SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
  SELECT COALESCE(s.widest_prefix_len, 24), COALESCE(s.config_version, 0), s.updated_at, s.updated_by
    FROM (SELECT 1) one
    LEFT JOIN iam_v2.site_walled_garden_settings s ON s.tenant_id = p_tenant AND s.site_id = p_site;
$fn$;

COMMENT ON FUNCTION iam_v2.walled_garden_settings_get(uuid,uuid) IS
  'The walled-garden policy in force for a site. config_version 0 = no row; the default (/24) is in force.';

CREATE OR REPLACE FUNCTION iam_v2.walled_garden_settings_set(
    p_tenant uuid, p_site uuid, p_widest_prefix_len integer, p_operator text, p_reason text)
  RETURNS bigint
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE v_version bigint; v_old int;
BEGIN
  IF p_operator IS NULL OR btrim(p_operator) = '' THEN
    RAISE EXCEPTION 'an operator label is required' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  IF p_reason IS NULL OR length(btrim(p_reason)) < 4 OR length(btrim(p_reason)) > 500 THEN
    RAISE EXCEPTION 'a reason of 4-500 characters is required to change the walled-garden policy'
      USING ERRCODE = 'invalid_parameter_value';
  END IF;
  IF p_widest_prefix_len IS NULL OR p_widest_prefix_len < 16 OR p_widest_prefix_len > 32 THEN
    RAISE EXCEPTION 'the widest walled-garden network must be between /16 and /32 (got %)', p_widest_prefix_len
      USING ERRCODE = 'invalid_parameter_value';
  END IF;

  PERFORM pg_advisory_xact_lock(hashtext('walled_garden_settings'), hashtext(p_site::text));

  SELECT s.widest_prefix_len INTO v_old
    FROM iam_v2.site_walled_garden_settings s
   WHERE s.tenant_id = p_tenant AND s.site_id = p_site
     FOR UPDATE;

  INSERT INTO iam_v2.site_walled_garden_settings AS s (tenant_id, site_id, widest_prefix_len, updated_by)
  VALUES (p_tenant, p_site, p_widest_prefix_len, btrim(p_operator))
  ON CONFLICT (tenant_id, site_id) DO UPDATE
     SET widest_prefix_len = EXCLUDED.widest_prefix_len,
         updated_by        = EXCLUDED.updated_by,
         config_version    = s.config_version + 1,
         updated_at        = now()
  RETURNING s.config_version INTO v_version;

  INSERT INTO iam_v2.walled_garden_settings_changes
    (tenant_id, site_id, changed_by, change_reason, old_widest_prefix_len, new_widest_prefix_len, new_config_version)
  VALUES (p_tenant, p_site, btrim(p_operator), btrim(p_reason), v_old, p_widest_prefix_len, v_version);

  RETURN v_version;
END $fn$;

COMMENT ON FUNCTION iam_v2.walled_garden_settings_set(uuid,uuid,integer,text,text) IS
  'Stores a validated walled-garden policy and its change row in one transaction. Applies to the next rule '
  'created or edited; existing rules are not changed.';

DO $own$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
    EXECUTE 'ALTER TABLE iam_v2.site_walled_garden_settings OWNER TO iam_v2_owner';
    EXECUTE 'ALTER TABLE iam_v2.walled_garden_settings_changes OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.walled_garden_settings_changes_append_only() OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.walled_garden_settings_get(uuid,uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.walled_garden_settings_set(uuid,uuid,integer,text,text) OWNER TO iam_v2_owner';
  END IF;
END $own$;

-- Least privilege, mirrored in deploy/gatep/svc-edged-phase345-admin-grants.sql. edged alone reads and changes
-- the policy, only through the functions; no role is granted the settings table.
DO $g$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
    GRANT EXECUTE ON FUNCTION iam_v2.walled_garden_settings_get(uuid,uuid) TO svc_edged;
    GRANT EXECUTE ON FUNCTION iam_v2.walled_garden_settings_set(uuid,uuid,integer,text,text) TO svc_edged;
    GRANT SELECT ON iam_v2.walled_garden_settings_changes TO svc_edged;
  END IF;
END;
$g$;

REVOKE EXECUTE ON FUNCTION iam_v2.walled_garden_settings_get(uuid,uuid) FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION iam_v2.walled_garden_settings_set(uuid,uuid,integer,text,text) FROM PUBLIC;

COMMIT;
