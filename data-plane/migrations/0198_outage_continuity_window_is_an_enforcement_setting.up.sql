-- 0198 -- HOW LONG GUESTS STAY ONLINE WHEN ONEGATE ITSELF FAILS IS A PERSISTED, AUDITED SETTING.
-- edge-migrate: platform
--
-- public.enforcement_policy gains outage_continuity_minutes: how long, after plans from acctd stop arriving (acctd hung or
-- crashed, the database unavailable), netd keeps the kernel authorization of the sessions the last healthy plan covered.
-- It is NOT a longer lease: netd renews only authorizations that are still present, only until the earliest of the window,
-- the session's hard end and the entitlement's byte budget, and a revocation ends them at once
-- (docs/architecture/ONEGATE_OUTAGE_CONTINUITY.md).
--
--   default 60 minutes, 0..120, 0 = strict (the 90-second lease only: the behaviour before this migration).
--
-- Every change goes through enforcement_policy_set(), as for the other fields: validated, version bumped, appended to the
-- append-only enforcement_policy_changes in the same transaction. The grants on the table and the function are unchanged
-- (CREATE OR REPLACE keeps them).
--
-- DEPLOYMENT NOTE: a hotel that has not been told about the new default gets 60 minutes of continuity on upgrade. That is the
-- Product Owner's stated priority; set the value to 0 to restore the previous behaviour exactly.

BEGIN;

ALTER TABLE public.enforcement_policy
  ADD COLUMN IF NOT EXISTS outage_continuity_minutes integer NOT NULL DEFAULT 60;

DO $c$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'enforcement_policy_outage_continuity_range'
                   AND conrelid = 'public.enforcement_policy'::regclass) THEN
    ALTER TABLE public.enforcement_policy
      ADD CONSTRAINT enforcement_policy_outage_continuity_range CHECK (outage_continuity_minutes BETWEEN 0 AND 120);
  END IF;
END $c$;

COMMENT ON COLUMN public.enforcement_policy.outage_continuity_minutes IS
  'Minutes (0..120, default 60) that sessions the last healthy plan covered stay authorized after plans stop arriving; 0 = strict 90 s lease (0198).';

INSERT INTO public.enforcement_policy_changes (changed_by, reason, field, old_value, new_value, config_version)
SELECT 'migration 0198', 'outage continuity setting created (default 60 minutes)', 'outage_continuity_minutes', NULL, '60',
       (SELECT config_version FROM public.enforcement_policy LIMIT 1)
 WHERE EXISTS (SELECT 1 FROM public.enforcement_policy);

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
  ELSIF p_field = 'outage_continuity_minutes' THEN
    IF p_value !~ '^[0-9]{1,3}$' OR p_value::int > 120 THEN
      RAISE EXCEPTION 'outage_continuity_minutes must be between 0 and 120' USING ERRCODE = 'invalid_parameter_value';
    END IF;
    SELECT outage_continuity_minutes::text INTO v_old FROM public.enforcement_policy;
    UPDATE public.enforcement_policy SET outage_continuity_minutes = p_value::int, updated_by = btrim(p_operator), updated_at = now(), config_version = config_version + 1;
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

COMMIT;
