-- edge-migrate: platform

BEGIN;

CREATE TABLE IF NOT EXISTS public.operator_password_reset_policy (
  id                    boolean PRIMARY KEY DEFAULT true CONSTRAINT oprp_singleton CHECK (id),
  enabled               boolean NOT NULL DEFAULT false,
  code_minutes          integer NOT NULL DEFAULT 15 CONSTRAINT oprp_code_minutes CHECK (code_minutes BETWEEN 5 AND 60),
  max_attempts          integer NOT NULL DEFAULT 5 CONSTRAINT oprp_max_attempts CHECK (max_attempts BETWEEN 3 AND 10),
  account_per_hour      integer NOT NULL DEFAULT 5 CONSTRAINT oprp_account_per_hour CHECK (account_per_hour BETWEEN 1 AND 20),
  address_per_hour      integer NOT NULL DEFAULT 20 CONSTRAINT oprp_address_per_hour CHECK (address_per_hour BETWEEN 5 AND 200),
  allow_last_site_admin boolean NOT NULL DEFAULT false,
  config_version        bigint NOT NULL DEFAULT 1 CONSTRAINT oprp_version CHECK (config_version >= 1),
  updated_at            timestamptz NOT NULL DEFAULT now(),
  updated_by            text
);
COMMENT ON TABLE public.operator_password_reset_policy IS
  'Admin Console password reset by email (0214): off by default; a code lasts code_minutes and allows max_attempts '
  'tries; requests are limited per email address and per network address each hour; the last active site admin is '
  'excluded unless allow_last_site_admin. One row for the appliance; no row means the defaults. Written only through '
  'public.operator_password_reset_policy_set.';

CREATE TABLE IF NOT EXISTS public.operator_password_reset_policy_changes (
  id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  changed_by         text NOT NULL CONSTRAINT oprpc_actor CHECK (length(btrim(changed_by)) > 0),
  change_reason      text NOT NULL CONSTRAINT oprpc_reason CHECK (length(btrim(change_reason)) > 0),
  old_values         jsonb,
  new_values         jsonb NOT NULL,
  new_config_version bigint NOT NULL,
  changed_at         timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS operator_password_reset_policy_changes_at
  ON public.operator_password_reset_policy_changes (changed_at DESC);
COMMENT ON TABLE public.operator_password_reset_policy_changes IS
  'Append-only history of public.operator_password_reset_policy (0214).';

CREATE OR REPLACE FUNCTION public.operator_password_reset_policy_changes_append_only() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'public.operator_password_reset_policy_changes is append-only: % refused', TG_OP
    USING ERRCODE = 'restrict_violation';
END $$;
REVOKE EXECUTE ON FUNCTION public.operator_password_reset_policy_changes_append_only() FROM PUBLIC;
DROP TRIGGER IF EXISTS operator_password_reset_policy_changes_append_only ON public.operator_password_reset_policy_changes;
CREATE TRIGGER operator_password_reset_policy_changes_append_only
  BEFORE UPDATE OR DELETE ON public.operator_password_reset_policy_changes
  FOR EACH ROW EXECUTE FUNCTION public.operator_password_reset_policy_changes_append_only();

CREATE OR REPLACE FUNCTION public.operator_password_reset_policy_get()
  RETURNS TABLE (enabled boolean, code_minutes integer, max_attempts integer, account_per_hour integer,
                 address_per_hour integer, allow_last_site_admin boolean, config_version bigint,
                 updated_at timestamptz, updated_by text)
  LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public, pg_temp AS $fn$
  SELECT COALESCE(p.enabled, false), COALESCE(p.code_minutes, 15), COALESCE(p.max_attempts, 5),
         COALESCE(p.account_per_hour, 5), COALESCE(p.address_per_hour, 20), COALESCE(p.allow_last_site_admin, false),
         COALESCE(p.config_version, 0::bigint), p.updated_at, p.updated_by
    FROM (SELECT 1) one
    LEFT JOIN public.operator_password_reset_policy p ON p.id;
$fn$;
REVOKE EXECUTE ON FUNCTION public.operator_password_reset_policy_get() FROM PUBLIC;

CREATE OR REPLACE FUNCTION public.operator_password_reset_policy_set(
    p_enabled boolean, p_code_minutes integer, p_max_attempts integer, p_account_per_hour integer,
    p_address_per_hour integer, p_allow_last_site_admin boolean, p_expected_version bigint,
    p_operator text, p_reason text)
  RETURNS bigint
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = public, pg_temp AS $fn$
DECLARE v_version bigint; v_current bigint; v_old jsonb; v_new jsonb;
BEGIN
  IF p_operator IS NULL OR btrim(p_operator) = '' THEN
    RAISE EXCEPTION 'an operator label is required' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  IF p_reason IS NULL OR length(btrim(p_reason)) < 4 OR length(btrim(p_reason)) > 500 THEN
    RAISE EXCEPTION 'a reason of 4-500 characters is required to change the password reset settings'
      USING ERRCODE = 'invalid_parameter_value';
  END IF;
  IF p_enabled IS NULL OR p_allow_last_site_admin IS NULL THEN
    RAISE EXCEPTION 'every password reset setting is required' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  PERFORM pg_advisory_xact_lock(hashtext('operator_password_reset_policy'));
  SELECT p.config_version,
         jsonb_build_object('enabled', p.enabled, 'code_minutes', p.code_minutes, 'max_attempts', p.max_attempts,
                            'account_per_hour', p.account_per_hour, 'address_per_hour', p.address_per_hour,
                            'allow_last_site_admin', p.allow_last_site_admin)
    INTO v_current, v_old FROM public.operator_password_reset_policy p WHERE p.id FOR UPDATE;
  IF p_expected_version IS NOT NULL AND p_expected_version <> COALESCE(v_current, 0) THEN
    RAISE EXCEPTION 'the password reset settings changed since they were read'
      USING ERRCODE = 'serialization_failure';
  END IF;
  INSERT INTO public.operator_password_reset_policy AS p
    (id, enabled, code_minutes, max_attempts, account_per_hour, address_per_hour, allow_last_site_admin, updated_by)
  VALUES (true, p_enabled, p_code_minutes, p_max_attempts, p_account_per_hour, p_address_per_hour,
          p_allow_last_site_admin, btrim(p_operator))
  ON CONFLICT (id) DO UPDATE
     SET enabled = EXCLUDED.enabled, code_minutes = EXCLUDED.code_minutes, max_attempts = EXCLUDED.max_attempts,
         account_per_hour = EXCLUDED.account_per_hour, address_per_hour = EXCLUDED.address_per_hour,
         allow_last_site_admin = EXCLUDED.allow_last_site_admin, updated_by = EXCLUDED.updated_by,
         config_version = p.config_version + 1, updated_at = now()
  RETURNING p.config_version INTO v_version;
  v_new := jsonb_build_object('enabled', p_enabled, 'code_minutes', p_code_minutes, 'max_attempts', p_max_attempts,
                              'account_per_hour', p_account_per_hour, 'address_per_hour', p_address_per_hour,
                              'allow_last_site_admin', p_allow_last_site_admin);
  INSERT INTO public.operator_password_reset_policy_changes
    (changed_by, change_reason, old_values, new_values, new_config_version)
  VALUES (btrim(p_operator), btrim(p_reason), v_old, v_new, v_version);
  RETURN v_version;
END $fn$;
REVOKE EXECUTE ON FUNCTION public.operator_password_reset_policy_set(boolean,integer,integer,integer,integer,boolean,bigint,text,text)
  FROM PUBLIC;

CREATE TABLE IF NOT EXISTS public.operator_password_reset_requests (
  scope        text NOT NULL CONSTRAINT oprr_scope CHECK (scope IN ('account','address')),
  key_hash     text NOT NULL CONSTRAINT oprr_key_hex CHECK (key_hash ~ '^[0-9a-f]{64}$'),
  requested_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS operator_password_reset_requests_key
  ON public.operator_password_reset_requests (scope, key_hash, requested_at DESC);
CREATE INDEX IF NOT EXISTS operator_password_reset_requests_at
  ON public.operator_password_reset_requests (requested_at);
COMMENT ON TABLE public.operator_password_reset_requests IS
  'Admin Console password reset requests (0214), counted per email address and per network address so the hourly '
  'limits hold across restarts. key_hash is SHA-256 of the value, never the value; rows older than a day are removed '
  'by public.operator_password_reset_request_allowed.';

CREATE OR REPLACE FUNCTION public.operator_password_reset_request_allowed(p_account_hash text, p_address_hash text)
  RETURNS TABLE (account_allowed boolean, address_allowed boolean)
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = public, pg_temp AS $fn$
DECLARE l record; v_account integer; v_address integer; v_last timestamptz;
BEGIN
  IF p_account_hash IS NULL OR p_account_hash !~ '^[0-9a-f]{64}$'
     OR p_address_hash IS NULL OR p_address_hash !~ '^[0-9a-f]{64}$' THEN
    RAISE EXCEPTION 'a hashed email address and network address are required' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  SELECT * INTO l FROM public.operator_password_reset_policy_get();
  PERFORM pg_advisory_xact_lock(hashtext('operator_password_reset_requests'), hashtext(p_account_hash));
  DELETE FROM public.operator_password_reset_requests d WHERE d.requested_at < now() - interval '1 day';
  SELECT count(*), max(r.requested_at) INTO v_account, v_last FROM public.operator_password_reset_requests r
   WHERE r.scope = 'account' AND r.key_hash = p_account_hash AND r.requested_at > now() - interval '1 hour';
  SELECT count(*) INTO v_address FROM public.operator_password_reset_requests r
   WHERE r.scope = 'address' AND r.key_hash = p_address_hash AND r.requested_at > now() - interval '1 hour';
  address_allowed := v_address < l.address_per_hour;
  account_allowed := address_allowed AND v_account < l.account_per_hour
                     AND (v_last IS NULL OR v_last < now() - interval '60 seconds');
  IF address_allowed THEN
    INSERT INTO public.operator_password_reset_requests (scope, key_hash) VALUES ('address', p_address_hash);
  END IF;
  IF account_allowed THEN
    INSERT INTO public.operator_password_reset_requests (scope, key_hash) VALUES ('account', p_account_hash);
  END IF;
  RETURN NEXT;
END $fn$;
REVOKE EXECUTE ON FUNCTION public.operator_password_reset_request_allowed(text,text) FROM PUBLIC;

CREATE TABLE IF NOT EXISTS public.operator_password_resets (
  operator_id  uuid PRIMARY KEY REFERENCES public.operators(id) ON DELETE CASCADE,
  code_hash    text NOT NULL CONSTRAINT opr_code_hex CHECK (code_hash ~ '^[0-9a-f]{64}$'),
  created_at   timestamptz NOT NULL DEFAULT now(),
  expires_at   timestamptz NOT NULL,
  attempts     integer NOT NULL DEFAULT 0 CONSTRAINT opr_attempts CHECK (attempts >= 0)
);
CREATE INDEX IF NOT EXISTS operator_password_resets_expires ON public.operator_password_resets (expires_at);
COMMENT ON TABLE public.operator_password_resets IS
  'The one live password reset code per operator (0214), stored as SHA-256, never the code. A new request replaces '
  'it; a correct use, too many wrong tries or a day past expiry removes it.';

CREATE OR REPLACE FUNCTION public.operator_password_reset_issue(p_operator uuid, p_code_hash text)
  RETURNS timestamptz
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = public, pg_temp AS $fn$
DECLARE l record; v_expires timestamptz;
BEGIN
  IF p_operator IS NULL OR p_code_hash IS NULL OR p_code_hash !~ '^[0-9a-f]{64}$' THEN
    RAISE EXCEPTION 'an operator and a hashed code are required' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  SELECT * INTO l FROM public.operator_password_reset_policy_get();
  DELETE FROM public.operator_password_resets d WHERE d.expires_at < now() - interval '1 day';
  v_expires := now() + make_interval(mins => l.code_minutes);
  INSERT INTO public.operator_password_resets AS r (operator_id, code_hash, created_at, expires_at, attempts)
  VALUES (p_operator, p_code_hash, now(), v_expires, 0)
  ON CONFLICT (operator_id) DO UPDATE
     SET code_hash = EXCLUDED.code_hash, created_at = EXCLUDED.created_at, expires_at = EXCLUDED.expires_at, attempts = 0;
  RETURN v_expires;
END $fn$;
REVOKE EXECUTE ON FUNCTION public.operator_password_reset_issue(uuid,text) FROM PUBLIC;

CREATE OR REPLACE FUNCTION public.operator_password_reset_check(p_operator uuid, p_code_hash text)
  RETURNS text
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = public, pg_temp AS $fn$
DECLARE l record; r public.operator_password_resets%ROWTYPE;
BEGIN
  IF p_operator IS NULL OR p_code_hash IS NULL THEN
    RETURN 'none';
  END IF;
  SELECT * INTO l FROM public.operator_password_reset_policy_get();
  SELECT * INTO r FROM public.operator_password_resets x WHERE x.operator_id = p_operator FOR UPDATE;
  IF NOT FOUND THEN
    RETURN 'none';
  END IF;
  IF r.expires_at <= now() THEN
    DELETE FROM public.operator_password_resets x WHERE x.operator_id = p_operator;
    RETURN 'expired';
  END IF;
  IF r.attempts >= l.max_attempts THEN
    DELETE FROM public.operator_password_resets x WHERE x.operator_id = p_operator;
    RETURN 'exhausted';
  END IF;
  IF r.code_hash = p_code_hash THEN
    RETURN 'ok';
  END IF;
  UPDATE public.operator_password_resets x SET attempts = x.attempts + 1 WHERE x.operator_id = p_operator;
  IF r.attempts + 1 >= l.max_attempts THEN
    DELETE FROM public.operator_password_resets x WHERE x.operator_id = p_operator;
    RETURN 'exhausted';
  END IF;
  RETURN 'wrong';
END $fn$;
REVOKE EXECUTE ON FUNCTION public.operator_password_reset_check(uuid,text) FROM PUBLIC;

CREATE OR REPLACE FUNCTION public.operator_password_reset_fail(p_operator uuid)
  RETURNS text
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = public, pg_temp AS $fn$
DECLARE l record; v_attempts integer;
BEGIN
  SELECT * INTO l FROM public.operator_password_reset_policy_get();
  UPDATE public.operator_password_resets x SET attempts = x.attempts + 1
   WHERE x.operator_id = p_operator RETURNING x.attempts INTO v_attempts;
  IF v_attempts IS NULL THEN
    RETURN 'none';
  END IF;
  IF v_attempts >= l.max_attempts THEN
    DELETE FROM public.operator_password_resets x WHERE x.operator_id = p_operator;
    RETURN 'exhausted';
  END IF;
  RETURN 'wrong';
END $fn$;
REVOKE EXECUTE ON FUNCTION public.operator_password_reset_fail(uuid) FROM PUBLIC;

CREATE OR REPLACE FUNCTION public.operator_password_reset_consume(p_operator uuid, p_code_hash text)
  RETURNS boolean
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = public, pg_temp AS $fn$
DECLARE v_found boolean;
BEGIN
  DELETE FROM public.operator_password_resets x
   WHERE x.operator_id = p_operator AND x.code_hash = p_code_hash AND x.expires_at > now()
  RETURNING true INTO v_found;
  RETURN COALESCE(v_found, false);
END $fn$;
REVOKE EXECUTE ON FUNCTION public.operator_password_reset_consume(uuid,text) FROM PUBLIC;

CREATE OR REPLACE FUNCTION public.operator_password_reset_discard(p_operator uuid)
  RETURNS void
  LANGUAGE sql SECURITY DEFINER SET search_path = public, pg_temp AS $fn$
  DELETE FROM public.operator_password_resets x WHERE x.operator_id = p_operator;
$fn$;
REVOKE EXECUTE ON FUNCTION public.operator_password_reset_discard(uuid) FROM PUBLIC;

DO $g$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
    GRANT EXECUTE ON FUNCTION public.operator_password_reset_policy_get() TO svc_edged;
    GRANT EXECUTE ON FUNCTION public.operator_password_reset_policy_set(boolean,integer,integer,integer,integer,boolean,bigint,text,text) TO svc_edged;
    GRANT EXECUTE ON FUNCTION public.operator_password_reset_request_allowed(text,text) TO svc_edged;
    GRANT EXECUTE ON FUNCTION public.operator_password_reset_issue(uuid,text) TO svc_edged;
    GRANT EXECUTE ON FUNCTION public.operator_password_reset_check(uuid,text) TO svc_edged;
    GRANT EXECUTE ON FUNCTION public.operator_password_reset_fail(uuid) TO svc_edged;
    GRANT EXECUTE ON FUNCTION public.operator_password_reset_consume(uuid,text) TO svc_edged;
    GRANT EXECUTE ON FUNCTION public.operator_password_reset_discard(uuid) TO svc_edged;
    GRANT SELECT ON public.operator_password_reset_policy_changes TO svc_edged;
  END IF;
END $g$;

COMMIT;
