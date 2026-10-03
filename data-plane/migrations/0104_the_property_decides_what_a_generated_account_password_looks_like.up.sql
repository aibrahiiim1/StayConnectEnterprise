-- 0104 -- THE PROPERTY DECIDES WHAT A GENERATED CLIENT-ACCOUNT PASSWORD LOOKS LIKE, AND A NEW ACCOUNT KNOWS
-- WHEN IT WAS CREATED.
--
-- 1. THE GENERATED-PASSWORD FORMAT
-- --------------------------------
-- Client (guest) accounts can be given a server-generated password ("Generate a strong password instead"),
-- on create and on set-password. cmd/edged/resources_guest_accounts.go produced it from ONE hardcoded
-- alphabet at ONE hardcoded length:
--
--     const genPasswordAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZabcdefghijkmnpqrstuvwxyz23456789"
--     const n = 14
--
-- Reception reads these passwords aloud and clients type them on phones, so whether a property wants mixed
-- case, uppercase-only, lowercase-only or digits-only -- and how long -- is a property decision. Under the
-- standing operational-settings rule (docs/OPERATIONAL_SETTINGS_POLICY.md) that makes it a persisted,
-- bounded, defaulted, audited setting, in the same shape as the voucher code format (0085).
--
-- THE STYLES. Every alphabet leaves out the characters people misread (0/O, 1/I/l, and lowercase o), the
-- same exclusion the hardcoded alphabet already applied:
--
--     mixed         ABCDEFGHJKMNPQRSTUVWXYZ abcdefghijkmnpqrstuvwxyz 23456789   55 symbols
--     upper_digits  ABCDEFGHJKMNPQRSTUVWXYZ 23456789                            31 symbols
--     lower_digits  abcdefghijkmnpqrstuvwxyz 23456789                           32 symbols
--     digits        23456789                                                    8 symbols
--
-- Digits-only uses 2-9: 0 and 1 are dropped for the same reason they are dropped from the other three
-- styles, so a password read off a printed slip means the same thing in every style.
--
-- THE SECURITY FLOOR IS NOT THE VOUCHER'S. A voucher is a single-use card limited to 6-8 characters by a
-- Product-Owner requirement; an account password is a reusable credential. Each style must reach at least
-- 40 bits of estimated entropy, so its minimum length is ceil(40 / log2(alphabet size)):
--
--     mixed         ceil(40 / 5.781) =  7
--     upper_digits  ceil(40 / 4.954) =  9
--     lower_digits  ceil(40 / 5.000) =  8
--     digits        ceil(40 / 3.000) = 14
--
-- and every style is capped at 32. The floor is enforced in edged, in the setter below AND in the table
-- CHECK, so a value that bypassed the first two still cannot be stored.
--
-- THE DEFAULT IS TODAY'S BEHAVIOUR: mixed, 14. A site that never opens the screen generates exactly what it
-- generated before this migration.
--
-- WHAT THIS DOES NOT CHANGE. No existing account or credential is touched: the setting is read only when a
-- password is GENERATED. Operator-typed passwords keep their existing rules (1-128 characters, no control
-- characters); this migration neither weakens nor tightens them.
--
-- 2. guest_access_accounts.created_at
-- -----------------------------------
-- The Client accounts list shows the newest accounts first, which needs a creation time the table never
-- recorded. The column is added WITHOUT a default first, so every existing row stays NULL -- inventing a
-- creation time for an account nobody recorded one for would be a fabricated fact -- and the default is set
-- afterwards so every new insert records now(). The list orders created_at DESC NULLS LAST.

BEGIN;

-- ---------------------------------------------------------------------------------------------------------
-- 1. The setting. One row per site, CHECK-bounded, versioned.
-- ---------------------------------------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS iam_v2.site_account_password_settings (
  tenant_id       uuid NOT NULL,
  site_id         uuid NOT NULL,
  password_style  text    NOT NULL DEFAULT 'mixed',
  password_length integer NOT NULL DEFAULT 14,
  config_version  bigint  NOT NULL DEFAULT 1,
  updated_at      timestamptz NOT NULL DEFAULT now(),
  updated_by      text,
  PRIMARY KEY (tenant_id, site_id),
  CONSTRAINT account_password_settings_style
    CHECK (password_style IN ('mixed','upper_digits','lower_digits','digits')),
  -- The 40-bit floor per style, and the common ceiling. The style CHECK above is what makes the CASE total:
  -- an unknown style is refused there rather than yielding NULL here.
  CONSTRAINT account_password_settings_length
    CHECK (password_length <= 32 AND password_length >= CASE password_style
                                                           WHEN 'mixed'        THEN 7
                                                           WHEN 'upper_digits' THEN 9
                                                           WHEN 'lower_digits' THEN 8
                                                           WHEN 'digits'       THEN 14
                                                         END),
  CONSTRAINT account_password_settings_version CHECK (config_version >= 1)
);

COMMENT ON TABLE iam_v2.site_account_password_settings IS
  'Per-site format of GENERATED client-account passwords: which characters, and how many. Read by edged '
  'only when it generates a password; operator-typed passwords and existing credentials are unaffected.';
COMMENT ON COLUMN iam_v2.site_account_password_settings.password_style IS
  'mixed = upper, lower and digits; upper_digits; lower_digits; digits (2-9). Easily misread characters '
  '(0/O, 1/I/l, o) are excluded from every style.';
COMMENT ON COLUMN iam_v2.site_account_password_settings.password_length IS
  'Characters in a generated password. At least 40 bits of entropy per style (mixed 7, upper_digits 9, '
  'lower_digits 8, digits 14) and at most 32.';

-- ---------------------------------------------------------------------------------------------------------
-- 2. Append-only audit of format changes
-- ---------------------------------------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS iam_v2.account_password_settings_changes (
  id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id           uuid NOT NULL,
  site_id             uuid NOT NULL,
  changed_by          text NOT NULL
    CONSTRAINT account_password_settings_changes_actor CHECK (length(btrim(changed_by)) > 0),
  -- Required here, unlike 0085: a weaker password format is a security decision and must say why.
  change_reason       text NOT NULL
    CONSTRAINT account_password_settings_changes_reason CHECK (length(btrim(change_reason)) > 0),
  old_password_style  text,
  old_password_length integer,
  new_password_style  text    NOT NULL,
  new_password_length integer NOT NULL,
  new_config_version  bigint  NOT NULL,
  changed_at          timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS account_password_settings_changes_lookup
  ON iam_v2.account_password_settings_changes (tenant_id, site_id, changed_at DESC);

CREATE OR REPLACE FUNCTION iam_v2.account_password_settings_changes_append_only() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'iam_v2.account_password_settings_changes is append-only: % refused', TG_OP
    USING ERRCODE = 'restrict_violation';
END $$;

REVOKE EXECUTE ON FUNCTION iam_v2.account_password_settings_changes_append_only() FROM PUBLIC;

DROP TRIGGER IF EXISTS account_password_settings_changes_append_only
  ON iam_v2.account_password_settings_changes;
CREATE TRIGGER account_password_settings_changes_append_only
  BEFORE UPDATE OR DELETE ON iam_v2.account_password_settings_changes
  FOR EACH ROW EXECUTE FUNCTION iam_v2.account_password_settings_changes_append_only();

-- ---------------------------------------------------------------------------------------------------------
-- 3. Reading it. A site with no row reads the defaults (config_version 0, updated_at NULL).
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION iam_v2.account_password_settings_get(p_tenant uuid, p_site uuid)
  RETURNS TABLE (password_style text, password_length integer, config_version bigint,
                 updated_at timestamptz, updated_by text)
  LANGUAGE sql STABLE SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
  SELECT COALESCE(s.password_style, 'mixed'),
         COALESCE(s.password_length, 14),
         COALESCE(s.config_version, 0),
         s.updated_at,
         s.updated_by
    FROM (SELECT 1) one
    LEFT JOIN iam_v2.site_account_password_settings s
      ON s.tenant_id = p_tenant AND s.site_id = p_site;
$fn$;

COMMENT ON FUNCTION iam_v2.account_password_settings_get(uuid,uuid) IS
  'The generated-password format in force for a site. config_version 0 with a null updated_at means no row '
  'exists and the answer is the defaults (mixed, 14) -- what the hardcoded generator produced.';

-- ---------------------------------------------------------------------------------------------------------
-- 4. Changing it. Bounds re-checked here AND by the table CHECK; a reason is required.
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION iam_v2.account_password_settings_set(
    p_tenant uuid, p_site uuid, p_style text, p_length integer,
    p_operator text, p_reason text)
  RETURNS bigint
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE v_version bigint; v_old_style text; v_old_length int; v_min int;
BEGIN
  IF p_operator IS NULL OR btrim(p_operator) = '' THEN
    RAISE EXCEPTION 'an operator label is required: "somebody changed it" is not an audit record'
      USING ERRCODE = 'invalid_parameter_value';
  END IF;
  IF p_reason IS NULL OR length(btrim(p_reason)) < 4 OR length(btrim(p_reason)) > 500 THEN
    RAISE EXCEPTION 'a reason of 4-500 characters is required to change the password format'
      USING ERRCODE = 'invalid_parameter_value';
  END IF;
  v_min := CASE p_style
             WHEN 'mixed'        THEN 7
             WHEN 'upper_digits' THEN 9
             WHEN 'lower_digits' THEN 8
             WHEN 'digits'       THEN 14
           END;
  IF v_min IS NULL THEN
    RAISE EXCEPTION 'password style must be mixed, upper_digits, lower_digits or digits (got %)',
      coalesce(p_style,'null') USING ERRCODE = 'invalid_parameter_value';
  END IF;
  IF p_length IS NULL OR p_length < v_min OR p_length > 32 THEN
    RAISE EXCEPTION 'a % password must be between % and 32 characters (got %)', p_style, v_min, p_length
      USING ERRCODE = 'invalid_parameter_value';
  END IF;

  PERFORM pg_advisory_xact_lock(hashtext('account_password_settings'), hashtext(p_site::text));

  SELECT s.password_style, s.password_length INTO v_old_style, v_old_length
    FROM iam_v2.site_account_password_settings s
   WHERE s.tenant_id = p_tenant AND s.site_id = p_site
     FOR UPDATE;

  INSERT INTO iam_v2.site_account_password_settings AS s
    (tenant_id, site_id, password_style, password_length, updated_by)
  VALUES (p_tenant, p_site, p_style, p_length, btrim(p_operator))
  ON CONFLICT (tenant_id, site_id) DO UPDATE
     SET password_style  = EXCLUDED.password_style,
         password_length = EXCLUDED.password_length,
         updated_by      = EXCLUDED.updated_by,
         config_version  = s.config_version + 1,
         updated_at      = now()
  RETURNING s.config_version INTO v_version;

  INSERT INTO iam_v2.account_password_settings_changes
    (tenant_id, site_id, changed_by, change_reason,
     old_password_style, old_password_length, new_password_style, new_password_length, new_config_version)
  VALUES (p_tenant, p_site, btrim(p_operator), btrim(p_reason),
          v_old_style, v_old_length, p_style, p_length, v_version);

  RETURN v_version;
END $fn$;

COMMENT ON FUNCTION iam_v2.account_password_settings_set(uuid,uuid,text,integer,text,text) IS
  'Stores a validated generated-password format and returns its new config_version. Takes effect on the '
  'NEXT generated password; no existing credential changes.';

-- ---------------------------------------------------------------------------------------------------------
-- 5. guest_access_accounts.created_at -- NULL for existing rows, now() for new ones.
-- ---------------------------------------------------------------------------------------------------------
ALTER TABLE iam_v2.guest_access_accounts ADD COLUMN IF NOT EXISTS created_at timestamptz;
ALTER TABLE iam_v2.guest_access_accounts ALTER COLUMN created_at SET DEFAULT now();

COMMENT ON COLUMN iam_v2.guest_access_accounts.created_at IS
  'When the account was created. NULL for accounts created before migration 0104, which recorded no '
  'creation time; never back-filled with an invented one.';

CREATE INDEX IF NOT EXISTS gaa_site_created
  ON iam_v2.guest_access_accounts (tenant_id, site_id, created_at DESC NULLS LAST);

-- ---------------------------------------------------------------------------------------------------------
-- 6. Ownership, declared rather than inherited from whoever applied this file (see 0089).
-- ---------------------------------------------------------------------------------------------------------
DO $own$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
    EXECUTE 'ALTER TABLE iam_v2.site_account_password_settings OWNER TO iam_v2_owner';
    EXECUTE 'ALTER TABLE iam_v2.account_password_settings_changes OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.account_password_settings_changes_append_only() OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.account_password_settings_get(uuid,uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.account_password_settings_set(uuid,uuid,text,integer,text,text) OWNER TO iam_v2_owner';
  END IF;
END $own$;

-- ---------------------------------------------------------------------------------------------------------
-- 7. Least privilege. Mirrored in deploy/gatep/svc-edged-phase345-admin-grants.sql so it survives Gate-P.
-- ---------------------------------------------------------------------------------------------------------
-- edged generates the password, so edged alone reads the format; it writes it only through the setter, and
-- reads the history directly. Neither table is granted: a role that could write the settings table directly
-- could change the format without leaving a change row. scd generates no account password and gets nothing.
DO $g$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
    GRANT EXECUTE ON FUNCTION iam_v2.account_password_settings_get(uuid,uuid) TO svc_edged;
    GRANT EXECUTE ON FUNCTION iam_v2.account_password_settings_set(uuid,uuid,text,integer,text,text) TO svc_edged;
    GRANT SELECT ON iam_v2.account_password_settings_changes TO svc_edged;
  END IF;
END;
$g$;

REVOKE EXECUTE ON FUNCTION iam_v2.account_password_settings_get(uuid,uuid) FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION iam_v2.account_password_settings_set(uuid,uuid,text,integer,text,text) FROM PUBLIC;

COMMIT;
