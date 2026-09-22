-- THE HOTEL DECIDES WHAT A VOUCHER CODE LOOKS LIKE.
--
-- A Product-Owner requirement: a voucher may be issued as DIGITS ONLY, or as digits MIXED with letters, and
-- neither form may be longer than EIGHT characters. Reception reads these codes aloud and guests type them
-- on a phone keypad, so which of the two a property wants is a property decision, not ours -- a resort with
-- a numeric-keypad kiosk wants digits, a hotel printing cards wants the shorter mixed code.
--
-- Under the standing operational-settings rule (docs/OPERATIONAL_SETTINGS_POLICY.md) that makes it a
-- persisted, bounded, defaulted, audited setting in Hotel Admin rather than a constant someone edits and
-- redeploys. This migration is that setting.
--
-- WHAT WAS THERE BEFORE, and why it was not enough. cmd/scd/voucher_issue_iamv2.go carried one hardcoded
-- alphabet and took a per-request length:
--
--     const voucherAlphabet = "ABCDEFGHJKMNPQRSTVWXYZ23456789"
--     if in.Length == 0 { in.Length = 8 }
--     if in.Length < 6 || in.Length > 24 { ... }
--
-- One fixed mode, no persistence, and a ceiling of 24 that the requirement lowers to 8. Meanwhile
-- internal/codegen already implemented exactly the configurable generator this needs -- modes, bounds,
-- ambiguity exclusion, an unbiased crypto/rand draw and a code-space guard -- and had been left generating
-- post-stay PINs after the legacy public.voucher_batches columns that used to configure it were dropped by
-- 0049. So the capability existed on one side of the house and the voucher path could not reach it. This
-- reconnects them and gives the choice a home.
--
-- THE BOUNDS, AND WHY THEY ARE THESE
-- ----------------------------------
--   code_length 6..8. The CEILING is the requirement: not more than eight, for either mode. The FLOOR is
--   the floor the shipped code already enforced -- voucher_issue_iamv2.go refused anything under 6 -- so it
--   is carried forward rather than invented. It matters: with ambiguous characters excluded, digits-only
--   leaves seven symbols, and 7^4 is 2 401 codes. Six is 117 649 and eight is 5 764 801, against which the
--   per-device sign-in protection policy from 0068 (five wrong submissions, then a refusal) is a real
--   defence. Four would not be.
--
--   code_mode numbers | mixed. Exactly the two the requirement names. internal/codegen also has "letters"
--   and "complex"; they are deliberately NOT offered here, because an operator setting whose values nobody
--   asked for is a surface nobody validated.
--
-- AMBIGUITY EXCLUSION IS NOT A SETTING, on purpose. The live alphabet already dropped the characters guests
-- misread from a printed card, and every code this appliance has issued has been free of them. Exposing it
-- as a choice would let a property turn a readability guarantee off, and no one asked for that. It stays on,
-- for both modes, in the generator.
--
-- WHAT THIS DOES NOT CHANGE. No voucher already issued is touched, re-encoded or re-indexed: the setting is
-- read at generation time and nowhere else, and codes are stored encrypted with a blind index that does not
-- depend on the alphabet. Redemption is unchanged -- the guest-facing path normalises to uppercase and
-- strips separators, which every code either mode produces already satisfies. A site with no row here
-- issues eight-character mixed codes, which is what the hardcoded path did.

BEGIN;

-- ---------------------------------------------------------------------------------------------------------
-- 1. The setting. One row per site, CHECK-bounded, versioned.
-- ---------------------------------------------------------------------------------------------------------
-- Bounded by CHECK and not by JSON, for the reason 0068 gives: a caller that skipped its own validation
-- must still be unable to store a format that would hurt guests or weaken a code.
CREATE TABLE IF NOT EXISTS iam_v2.site_voucher_code_settings (
  tenant_id      uuid NOT NULL,
  site_id        uuid NOT NULL,
  -- 'mixed' is the default because it is what the hardcoded generator produced: a site that has never
  -- opened the screen keeps the behaviour it already had.
  code_mode      text    NOT NULL DEFAULT 'mixed',
  code_length    integer NOT NULL DEFAULT 8,
  config_version bigint  NOT NULL DEFAULT 1,
  updated_at     timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, site_id),
  CONSTRAINT voucher_code_settings_mode CHECK (code_mode IN ('numbers','mixed')),
  CONSTRAINT voucher_code_settings_length CHECK (code_length BETWEEN 6 AND 8),
  CONSTRAINT voucher_code_settings_version CHECK (config_version >= 1)
);

COMMENT ON TABLE iam_v2.site_voucher_code_settings IS
  'Per-site voucher code format: digits only or digits mixed with letters, and how many characters (6-8). '
  'Read at issuance by scd and nowhere else. This is the single source the issuance path, the operator API '
  'and the UI all read; it affects codes generated AFTER a change and never a code already printed.';
COMMENT ON COLUMN iam_v2.site_voucher_code_settings.code_mode IS
  'numbers = digits only, for keypad entry. mixed = uppercase letters and digits, which reaches the same '
  'code space in fewer characters. Characters guests misread from a card are excluded from both.';
COMMENT ON COLUMN iam_v2.site_voucher_code_settings.code_length IS
  'Number of characters in the code, 6 to 8. Eight is the Product-Owner ceiling for both modes; six is the '
  'floor the issuance path already enforced. Shorter is easier to read aloud and easier to guess.';

-- ---------------------------------------------------------------------------------------------------------
-- 2. Append-only audit of format changes
-- ---------------------------------------------------------------------------------------------------------
-- A format change decides what every code printed afterwards looks like, so who changed it and to what is
-- evidence a property may need months later, when a card from a previous format will not scan.
CREATE TABLE IF NOT EXISTS iam_v2.voucher_code_settings_changes (
  id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id          uuid NOT NULL,
  site_id            uuid NOT NULL,
  -- The identity the server resolved from the session, never a name the caller supplied. The CHECK only
  -- catches an empty write; the API is what resolves it.
  changed_by         text NOT NULL
    CONSTRAINT voucher_code_settings_changes_actor CHECK (length(btrim(changed_by)) > 0),
  change_reason      text,
  old_code_mode      text,
  old_code_length    integer,
  new_code_mode      text    NOT NULL,
  new_code_length    integer NOT NULL,
  new_config_version bigint  NOT NULL,
  changed_at         timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS voucher_code_settings_changes_lookup
  ON iam_v2.voucher_code_settings_changes (tenant_id, site_id, changed_at DESC);

-- Append-only, enforced rather than promised.
CREATE OR REPLACE FUNCTION iam_v2.voucher_code_settings_changes_append_only() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'iam_v2.voucher_code_settings_changes is append-only: % refused', TG_OP
    USING ERRCODE = 'restrict_violation';
END $$;

REVOKE EXECUTE ON FUNCTION iam_v2.voucher_code_settings_changes_append_only() FROM PUBLIC;

DROP TRIGGER IF EXISTS voucher_code_settings_changes_append_only
  ON iam_v2.voucher_code_settings_changes;
CREATE TRIGGER voucher_code_settings_changes_append_only
  BEFORE UPDATE OR DELETE ON iam_v2.voucher_code_settings_changes
  FOR EACH ROW EXECUTE FUNCTION iam_v2.voucher_code_settings_changes_append_only();

-- ---------------------------------------------------------------------------------------------------------
-- 3. Reading it. A site with no row reads the defaults rather than nothing.
-- ---------------------------------------------------------------------------------------------------------
-- The issuance path must always get an answer: an appliance that cannot read the format must not fall back
-- to a second hardcoded constant, which is how two sources of truth start. Defaults live in ONE place --
-- the column defaults above -- and this function reproduces them for the no-row case.
CREATE OR REPLACE FUNCTION iam_v2.voucher_code_settings_get(p_tenant uuid, p_site uuid)
  RETURNS TABLE (code_mode text, code_length integer, config_version bigint, updated_at timestamptz)
  LANGUAGE sql STABLE SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
  SELECT COALESCE(s.code_mode, 'mixed'),
         COALESCE(s.code_length, 8),
         COALESCE(s.config_version, 0),
         s.updated_at
    FROM (SELECT 1) one
    LEFT JOIN iam_v2.site_voucher_code_settings s
      ON s.tenant_id = p_tenant AND s.site_id = p_site;
$fn$;

COMMENT ON FUNCTION iam_v2.voucher_code_settings_get(uuid,uuid) IS
  'The voucher code format in force for a site. config_version 0 with a null updated_at means no row exists '
  'and the answer is the defaults -- distinguishable from a site that deliberately saved the same values.';

-- ---------------------------------------------------------------------------------------------------------
-- 4. Changing it. Bounds re-checked here AND by the table CHECK.
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION iam_v2.voucher_code_settings_set(
    p_tenant uuid, p_site uuid, p_mode text, p_length integer,
    p_operator text, p_reason text DEFAULT NULL)
  RETURNS bigint
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE v_version bigint; v_old_mode text; v_old_length int;
BEGIN
  IF p_operator IS NULL OR btrim(p_operator) = '' THEN
    RAISE EXCEPTION 'an operator label is required: "somebody changed it" is not an audit record'
      USING ERRCODE = 'invalid_parameter_value';
  END IF;
  IF p_mode IS NULL OR p_mode NOT IN ('numbers','mixed') THEN
    RAISE EXCEPTION 'voucher code mode must be numbers or mixed (got %)', coalesce(p_mode,'null')
      USING ERRCODE = 'invalid_parameter_value';
  END IF;
  IF p_length IS NULL OR p_length < 6 OR p_length > 8 THEN
    RAISE EXCEPTION 'voucher code length must be between 6 and 8 characters (got %)', p_length
      USING ERRCODE = 'invalid_parameter_value';
  END IF;

  -- Serialise concurrent edits on this site, so two operators saving at once produce two ordered change
  -- rows rather than one silently overwriting the other's audit.
  PERFORM pg_advisory_xact_lock(hashtext('voucher_code_settings'), hashtext(p_site::text));

  SELECT s.code_mode, s.code_length INTO v_old_mode, v_old_length
    FROM iam_v2.site_voucher_code_settings s
   WHERE s.tenant_id = p_tenant AND s.site_id = p_site
     FOR UPDATE;

  INSERT INTO iam_v2.site_voucher_code_settings AS s
    (tenant_id, site_id, code_mode, code_length)
  VALUES (p_tenant, p_site, p_mode, p_length)
  ON CONFLICT (tenant_id, site_id) DO UPDATE
     SET code_mode      = EXCLUDED.code_mode,
         code_length    = EXCLUDED.code_length,
         config_version = s.config_version + 1,
         updated_at     = now()
  RETURNING s.config_version INTO v_version;

  INSERT INTO iam_v2.voucher_code_settings_changes
    (tenant_id, site_id, changed_by, change_reason,
     old_code_mode, old_code_length, new_code_mode, new_code_length, new_config_version)
  VALUES (p_tenant, p_site, btrim(p_operator), NULLIF(btrim(COALESCE(p_reason,'')),''),
          v_old_mode, v_old_length, p_mode, p_length, v_version);

  RETURN v_version;
END $fn$;

COMMENT ON FUNCTION iam_v2.voucher_code_settings_set(uuid,uuid,text,integer,text,text) IS
  'Stores a validated voucher code format and returns its new config_version. Takes effect on the NEXT '
  'issuance -- the issuance path reads this table every time, so no restart, rebuild or deployment is '
  'involved. Codes already issued keep the format they were printed with; they remain redeemable, because '
  'redemption matches a stored blind index and never re-derives the format.';

-- ---------------------------------------------------------------------------------------------------------
-- 5. Least privilege
-- ---------------------------------------------------------------------------------------------------------
-- Neither service role touches the tables. scd reads the format through the reader; edged reads and writes
-- through the two functions and reads the change history directly, because a history screen is a plain
-- SELECT and wrapping it in a function would buy nothing.
DO $g$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_scd') THEN
    GRANT EXECUTE ON FUNCTION iam_v2.voucher_code_settings_get(uuid,uuid) TO svc_scd;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
    GRANT EXECUTE ON FUNCTION iam_v2.voucher_code_settings_get(uuid,uuid) TO svc_edged;
    GRANT EXECUTE ON FUNCTION iam_v2.voucher_code_settings_set(uuid,uuid,text,integer,text,text) TO svc_edged;
    GRANT SELECT ON iam_v2.voucher_code_settings_changes TO svc_edged;
  END IF;
END;
$g$;

-- Not to PUBLIC, and not the tables themselves. A role that could write site_voucher_code_settings directly
-- could change the format without leaving a change row, which is the one thing the audit exists to prevent.
REVOKE EXECUTE ON FUNCTION iam_v2.voucher_code_settings_get(uuid,uuid) FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION iam_v2.voucher_code_settings_set(uuid,uuid,text,integer,text,text) FROM PUBLIC;

COMMIT;
