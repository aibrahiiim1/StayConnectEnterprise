-- A SITE CHOOSES WHICH OF ITS LICENSED MODULES IT USES.
--
-- OneGate has four separate gates for an optional module (docs/architecture/ONEGATE_MODULES_AND_ACQUISITION.md):
-- the deployment ceiling (can this code run here), the signed licence (may this site use it), LOCAL ENABLEMENT
-- (has the site chosen to use it) and runtime readiness. This migration adds the third, and nothing else.
--
--   1. iam_v2.site_module_settings  -- one row per (site, module): enabled or not. Typed, CHECK-bounded.
--   2. iam_v2.site_module_changes   -- append-only history of every change, written by the setter only.
--   3. site_module_get / site_module_set -- the whole interface. No runtime role holds UPDATE on the table,
--      so the change log is mandatory by privilege, not by convention.
--   4. (Site Type is read from the signed assignment; no column -- see the end of this file.)
--      It authorises and enables NOTHING; it is shown and used for defaults only.
--
-- ABSENCE OF A ROW MEANS "NOT ENABLED". This is a product choice, not an operational value: a licensed module
-- that nobody switched on stays off. The identity modules (sms_otp, email_otp, social_login) are NOT stored
-- here: their local switch is, and stays, the Sign-in methods screen (one source of truth). ha and
-- white_label have no local switch.
--
-- PRESERVING ACCEPTED BEHAVIOUR. A site that already runs PMS interfaces is using the Hotel module today. It
-- is recorded as enabled, attributed to this migration (not to an operator), so the accepted Hotel section and
-- Room sign-in do not change. No financial module is seeded for anyone.

BEGIN;

CREATE TABLE IF NOT EXISTS iam_v2.site_module_settings (
  tenant_id      uuid NOT NULL,
  site_id        uuid NOT NULL,
  module_id      text NOT NULL
    CONSTRAINT site_module_settings_known CHECK (module_id IN ('hospitality','paid_access','card_payment','room_charge')),
  enabled        boolean NOT NULL,
  config_version bigint  NOT NULL DEFAULT 1 CONSTRAINT site_module_settings_version CHECK (config_version >= 1),
  updated_at     timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, site_id, module_id)
);

COMMENT ON TABLE iam_v2.site_module_settings IS
  'Local enablement of licensed optional modules, per site. Absence of a row means not enabled. The licence '
  'authorises; this row chooses; readiness decides whether anything can execute now. Written only by '
  'iam_v2.site_module_set, which records every change in iam_v2.site_module_changes.';

CREATE TABLE IF NOT EXISTS iam_v2.site_module_changes (
  id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id     uuid NOT NULL,
  site_id       uuid NOT NULL,
  module_id     text NOT NULL,
  changed_at    timestamptz NOT NULL DEFAULT now(),
  changed_by    text NOT NULL CONSTRAINT site_module_changes_actor CHECK (length(btrim(changed_by)) > 0),
  change_reason text,
  old_enabled   boolean,          -- NULL when no row existed (never enabled)
  new_enabled   boolean NOT NULL,
  new_config_version bigint NOT NULL
);
CREATE INDEX IF NOT EXISTS site_module_changes_lookup
  ON iam_v2.site_module_changes (tenant_id, site_id, changed_at DESC);

CREATE OR REPLACE FUNCTION iam_v2.site_module_changes_append_only() RETURNS trigger
  LANGUAGE plpgsql AS $fn$
BEGIN
  RAISE EXCEPTION 'iam_v2.site_module_changes is append-only: % refused', TG_OP
    USING ERRCODE = 'restrict_violation';
END $fn$;
REVOKE EXECUTE ON FUNCTION iam_v2.site_module_changes_append_only() FROM PUBLIC;
DROP TRIGGER IF EXISTS site_module_changes_append_only ON iam_v2.site_module_changes;
CREATE TRIGGER site_module_changes_append_only
  BEFORE UPDATE OR DELETE ON iam_v2.site_module_changes
  FOR EACH ROW EXECUTE FUNCTION iam_v2.site_module_changes_append_only();

-- The effective local state of every switchable module: the stored row, or "not enabled".
CREATE OR REPLACE FUNCTION iam_v2.site_module_get(p_tenant uuid, p_site uuid)
  RETURNS TABLE (module_id text, enabled boolean, config_version bigint, updated_at timestamptz)
  LANGUAGE sql STABLE SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
  SELECT m.id, COALESCE(s.enabled, false), COALESCE(s.config_version, 0), s.updated_at
    FROM unnest(ARRAY['hospitality','paid_access','card_payment','room_charge']) AS m(id)
    LEFT JOIN iam_v2.site_module_settings s
      ON s.tenant_id = p_tenant AND s.site_id = p_site AND s.module_id = m.id
   ORDER BY m.id;
$fn$;

CREATE OR REPLACE FUNCTION iam_v2.site_module_set(
    p_tenant uuid, p_site uuid, p_module text, p_enabled boolean, p_operator text, p_reason text DEFAULT NULL)
  RETURNS bigint
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE v_old boolean; v_version bigint;
BEGIN
  IF p_operator IS NULL OR btrim(p_operator) = '' THEN
    RAISE EXCEPTION 'an operator label is required' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  IF p_module IS NULL OR p_module NOT IN ('hospitality','paid_access','card_payment','room_charge') THEN
    RAISE EXCEPTION 'unknown switchable module %', p_module USING ERRCODE = 'invalid_parameter_value';
  END IF;
  IF p_enabled IS NULL THEN
    RAISE EXCEPTION 'enabled must be true or false' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  PERFORM pg_advisory_xact_lock(hashtext('site_module_settings'), hashtext(p_site::text));
  SELECT s.enabled INTO v_old FROM iam_v2.site_module_settings s
   WHERE s.tenant_id = p_tenant AND s.site_id = p_site AND s.module_id = p_module FOR UPDATE;
  INSERT INTO iam_v2.site_module_settings AS s (tenant_id, site_id, module_id, enabled)
  VALUES (p_tenant, p_site, p_module, p_enabled)
  ON CONFLICT (tenant_id, site_id, module_id) DO UPDATE
     SET enabled = EXCLUDED.enabled, config_version = s.config_version + 1, updated_at = now()
  RETURNING s.config_version INTO v_version;
  INSERT INTO iam_v2.site_module_changes
    (tenant_id, site_id, module_id, changed_by, change_reason, old_enabled, new_enabled, new_config_version)
  VALUES (p_tenant, p_site, p_module, btrim(p_operator), NULLIF(btrim(COALESCE(p_reason,'')),''),
          v_old, p_enabled, v_version);
  RETURN v_version;
END $fn$;

COMMENT ON FUNCTION iam_v2.site_module_set(uuid,uuid,text,boolean,text,text) IS
  'Switches a licensed module on or off for a site and records the change. Does not check the licence: '
  'enabling an unlicensed module stores the choice but the resolver still refuses it (the licence is a '
  'separate gate). Takes effect on the next resolver read; no restart.';

REVOKE ALL ON iam_v2.site_module_settings FROM PUBLIC;
REVOKE ALL ON iam_v2.site_module_changes FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.site_module_get(uuid,uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.site_module_set(uuid,uuid,text,boolean,text,text) FROM PUBLIC;

DO $grant$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_scd') THEN
    GRANT EXECUTE ON FUNCTION iam_v2.site_module_get(uuid,uuid) TO svc_scd;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
    GRANT SELECT ON iam_v2.site_module_changes TO svc_edged;
    GRANT EXECUTE ON FUNCTION iam_v2.site_module_get(uuid,uuid) TO svc_edged;
    GRANT EXECUTE ON FUNCTION iam_v2.site_module_set(uuid,uuid,text,boolean,text,text) TO svc_edged;
  END IF;
END
$grant$;

DO $own$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
    EXECUTE 'ALTER TABLE iam_v2.site_module_settings OWNER TO iam_v2_owner';
    EXECUTE 'ALTER TABLE iam_v2.site_module_changes OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.site_module_changes_append_only() OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.site_module_get(uuid,uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.site_module_set(uuid,uuid,text,boolean,text,text) OWNER TO iam_v2_owner';
  END IF;
END $own$;

-- Preserve accepted behaviour: a site already running PMS interfaces uses the Hotel module today.
INSERT INTO iam_v2.site_module_settings (tenant_id, site_id, module_id, enabled)
SELECT DISTINCT i.tenant_id, i.site_id, 'hospitality', true
  FROM iam_v2.pms_interfaces i
ON CONFLICT DO NOTHING;
INSERT INTO iam_v2.site_module_changes
  (tenant_id, site_id, module_id, changed_by, change_reason, old_enabled, new_enabled, new_config_version)
SELECT s.tenant_id, s.site_id, s.module_id, 'migration 0094',
       'site already operated PMS interfaces; Hotel module kept in use', NULL, true, 1
  FROM iam_v2.site_module_settings s
 WHERE s.module_id = 'hospitality'
   AND NOT EXISTS (SELECT 1 FROM iam_v2.site_module_changes c
                    WHERE c.tenant_id = s.tenant_id AND c.site_id = s.site_id AND c.module_id = 'hospitality');

-- Site Type has NO column here. It is descriptive metadata carried by the signed assignment, and the appliance
-- reads it from the verified assignment it already persists; a database copy would be a second source that
-- could disagree with the signed one, and a migration may not change public-schema structure in any case.

COMMIT;
