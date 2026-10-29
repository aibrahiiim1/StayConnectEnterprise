-- 0166 -- THE ENFORCEMENT POLICY THE HOTEL MAY TUNE IS A PERSISTED, AUDITED SETTING.
--
-- One singleton row, public.enforcement_policy, carries the operational values data-plane enforcement reads
-- (docs/OPERATIONAL_SETTINGS_POLICY.md: default, unit, explanation, scope, permission, validation, audit):
--
--   private_deny_enabled          AD-22  deny a signed-in guest every RFC 1918 / 100.64.0.0/10 / 169.254.0.0/16 destination
--                                        (default ON). OFF restores the previous behaviour (only the management CIDRs).
--   private_allow_cidrs           AD-22  IPv4 networks (/8../32) a guest may still reach. Empty by default.
--   allow_upstream_infrastructure AD-22  netd adds the appliance's own WAN gateway and DNS resolvers as /32 exceptions, so the
--                                        hotel's configured upstream is never cut by the default deny (default ON).
--   mac_binding_enabled           AD-23  the Phase-3 authorization is keyed (bridge, IP, MAC from the DHCP lease). OFF is the
--                                        emergency fallback to the previous IP-only key. Default OFF until the MAC-bound ruleset is proven on the test appliance; turn it ON with enforcement_policy_set.
--   cutoff_seconds                AD-47  how soon a used-up allowance cuts a guest off: acctd's enforcement pass period
--                                        (default 5 s, 1..60).
--
-- Every change goes through enforcement_policy_set(), which validates, bumps config_version and appends a row to
-- the append-only enforcement_policy_changes in the same transaction.
-- Existing sites: the row is created with the defaults; nothing about the configured guest networks, walled garden,
-- custom DNS or upstream is removed -- those stay ahead of the private drop in the rendered ruleset.

BEGIN;

CREATE TABLE IF NOT EXISTS public.enforcement_policy (
  id                            boolean PRIMARY KEY DEFAULT true CHECK (id),
  private_deny_enabled          boolean NOT NULL DEFAULT true,
  private_allow_cidrs           text[]  NOT NULL DEFAULT '{}',
  allow_upstream_infrastructure boolean NOT NULL DEFAULT true,
  mac_binding_enabled           boolean NOT NULL DEFAULT false,
  cutoff_seconds                integer NOT NULL DEFAULT 5 CHECK (cutoff_seconds BETWEEN 1 AND 60),
  config_version                bigint  NOT NULL DEFAULT 1 CHECK (config_version >= 1),
  updated_at                    timestamptz NOT NULL DEFAULT now(),
  updated_by                    text,
  CONSTRAINT enforcement_policy_allow_len CHECK (cardinality(private_allow_cidrs) <= 64)
);

COMMENT ON TABLE public.enforcement_policy IS
  'Singleton data-plane enforcement policy (0166). Written only through enforcement_policy_set(); read by netd and acctd.';

INSERT INTO public.enforcement_policy (id, updated_by) VALUES (true, 'migration 0166') ON CONFLICT (id) DO NOTHING;

CREATE TABLE IF NOT EXISTS public.enforcement_policy_changes (
  id             bigserial PRIMARY KEY,
  changed_at     timestamptz NOT NULL DEFAULT now(),
  changed_by     text NOT NULL CHECK (length(btrim(changed_by)) > 0),
  reason         text NOT NULL CHECK (length(btrim(reason)) > 0),
  field          text NOT NULL,
  old_value      text,
  new_value      text,
  config_version bigint NOT NULL
);

CREATE OR REPLACE FUNCTION public.enforcement_policy_changes_append_only() RETURNS trigger
LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'enforcement_policy_changes is append-only' USING ERRCODE = 'restrict_violation'; END $$;
REVOKE EXECUTE ON FUNCTION public.enforcement_policy_changes_append_only() FROM PUBLIC;
DROP TRIGGER IF EXISTS enforcement_policy_changes_append_only ON public.enforcement_policy_changes;
CREATE TRIGGER enforcement_policy_changes_append_only BEFORE UPDATE OR DELETE
  ON public.enforcement_policy_changes FOR EACH ROW EXECUTE FUNCTION public.enforcement_policy_changes_append_only();

INSERT INTO public.enforcement_policy_changes (changed_by, reason, field, old_value, new_value, config_version)
SELECT 'migration 0166', 'defaults created', 'all', NULL, 'defaults', 1
 WHERE NOT EXISTS (SELECT 1 FROM public.enforcement_policy_changes);

-- p_field: private_deny_enabled | allow_upstream_infrastructure | mac_binding_enabled (p_value 'true'/'false'),
--          cutoff_seconds (1..60), private_allow_cidrs (comma-separated IPv4 CIDRs, each /8../32, may be empty).
CREATE OR REPLACE FUNCTION public.enforcement_policy_set(p_field text, p_value text, p_operator text, p_reason text)
  RETURNS bigint
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = public, pg_temp AS $fn$
DECLARE v_old text; v_new text; v_version bigint; v_cidrs text[]; v_c text; v_n cidr;
BEGIN
  IF p_operator IS NULL OR btrim(p_operator) = '' THEN
    RAISE EXCEPTION 'an operator label is required' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  IF p_reason IS NULL OR length(btrim(p_reason)) < 4 OR length(btrim(p_reason)) > 500 THEN
    RAISE EXCEPTION 'a reason of 4-500 characters is required to change the enforcement policy' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  PERFORM pg_advisory_xact_lock(hashtext('enforcement_policy'));
  IF NOT EXISTS (SELECT 1 FROM public.enforcement_policy) THEN
    INSERT INTO public.enforcement_policy (id, updated_by) VALUES (true, 'enforcement_policy_set');
  END IF;

  IF p_field IN ('private_deny_enabled', 'allow_upstream_infrastructure', 'mac_binding_enabled') THEN
    IF p_value NOT IN ('true', 'false') THEN
      RAISE EXCEPTION '% must be true or false', p_field USING ERRCODE = 'invalid_parameter_value';
    END IF;
    EXECUTE format('SELECT %I::text FROM public.enforcement_policy', p_field) INTO v_old;
    EXECUTE format('UPDATE public.enforcement_policy SET %I = $1::boolean, updated_by = $2, updated_at = now(), config_version = config_version + 1', p_field)
      USING p_value, btrim(p_operator);
    v_new := p_value;
  ELSIF p_field = 'cutoff_seconds' THEN
    IF p_value !~ '^[0-9]{1,2}$' OR p_value::int < 1 OR p_value::int > 60 THEN
      RAISE EXCEPTION 'cutoff_seconds must be between 1 and 60' USING ERRCODE = 'invalid_parameter_value';
    END IF;
    SELECT cutoff_seconds::text INTO v_old FROM public.enforcement_policy;
    UPDATE public.enforcement_policy SET cutoff_seconds = p_value::int, updated_by = btrim(p_operator), updated_at = now(), config_version = config_version + 1;
    v_new := p_value;
  ELSIF p_field = 'private_allow_cidrs' THEN
    v_cidrs := ARRAY[]::text[];
    FOREACH v_c IN ARRAY string_to_array(COALESCE(p_value, ''), ',') LOOP
      v_c := btrim(v_c);
      CONTINUE WHEN v_c = '';
      BEGIN v_n := v_c::cidr; EXCEPTION WHEN OTHERS THEN
        RAISE EXCEPTION '% is not an IPv4 network in CIDR form', v_c USING ERRCODE = 'invalid_parameter_value';
      END;
      IF family(v_n) <> 4 OR masklen(v_n) < 8 THEN
        RAISE EXCEPTION '% must be an IPv4 network no wider than /8', v_c USING ERRCODE = 'invalid_parameter_value';
      END IF;
      IF NOT (v_n::text = ANY (v_cidrs)) THEN v_cidrs := v_cidrs || v_n::text; END IF;
    END LOOP;
    IF cardinality(v_cidrs) > 64 THEN
      RAISE EXCEPTION 'at most 64 networks may be allowed' USING ERRCODE = 'invalid_parameter_value';
    END IF;
    SELECT array_to_string(private_allow_cidrs, ',') INTO v_old FROM public.enforcement_policy;
    UPDATE public.enforcement_policy SET private_allow_cidrs = v_cidrs, updated_by = btrim(p_operator), updated_at = now(), config_version = config_version + 1;
    v_new := array_to_string(v_cidrs, ',');
  ELSE
    RAISE EXCEPTION 'unknown enforcement policy field %', p_field USING ERRCODE = 'invalid_parameter_value';
  END IF;

  SELECT config_version INTO v_version FROM public.enforcement_policy;
  INSERT INTO public.enforcement_policy_changes (changed_by, reason, field, old_value, new_value, config_version)
  VALUES (btrim(p_operator), btrim(p_reason), p_field, v_old, v_new, v_version);
  RETURN v_version;
END $fn$;

REVOKE EXECUTE ON FUNCTION public.enforcement_policy_set(text,text,text,text) FROM PUBLIC;

-- Mirrored in deploy/gatep/svc-enforcement-policy-grants.sql, which survives a Gate-P reconcile.
DO $g$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_netd') THEN
    GRANT SELECT ON public.enforcement_policy TO svc_netd;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_acctd') THEN
    GRANT SELECT ON public.enforcement_policy TO svc_acctd;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
    GRANT SELECT ON public.enforcement_policy, public.enforcement_policy_changes TO svc_edged;
    GRANT EXECUTE ON FUNCTION public.enforcement_policy_set(text,text,text,text) TO svc_edged;
  END IF;
END $g$;

COMMIT;
