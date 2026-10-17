-- 0132 -- ADMIN CONSOLE SIGN-IN NEEDS AN AUTHENTICATOR CODE, AND SESSIONS END WHEN IDLE OR OLD
-- (GDPR remediation plan C10; Product-Owner decision D-C10, 2026-10-17: an authenticator app (TOTP) as the
--  second step for every Admin Console account, set up at the next sign-in, 10 single-use recovery codes, a site
--  administrator can reset a colleague's second step, idle logout after 30 minutes and a session limit of 12
--  hours, both settings; NIST SP 800-63B / OWASP ASVS; docs/privacy/GDPR_REMEDIATION_PLAN.md section 4.)
--
--   public.operator_mfa                  one authenticator per operator. The secret is sealed by scd (AES-256-GCM,
--                                        operator_totp_dek.key, bound to the operator) -- this table never holds it
--                                        in the clear, and edged, which serves the console, cannot open it.
--                                        confirmed_at NULL: set up but not yet proven with a code.
--                                        last_used_step: the newest 30-second step accepted, so a code is never
--                                        accepted twice (RFC 6238 section 5.2).
--   public.operator_mfa_recovery_codes   ten single-use codes per operator, stored as HMAC-SHA256 under the same
--                                        key, so a copy of the database cannot be searched for them.
--   public.operator_session_policy       idle and absolute session limits (defaults 30 minutes and 12 hours), an
--                                        audited appliance setting like the sign-in protection (0119): one row for
--                                        the appliance, no row means the defaults.
--   operator_signin_throttle             gains the 'mfa' scope: wrong codes are counted per operator with the
--                                        account limits, exactly like wrong passwords.
--
-- Additive. Removing an authenticator (a reset) deletes its rows: they are credentials, not history; the history
-- is public.audit_log (operator.mfa_reset).

BEGIN;

-- ---------------------------------------------------------------------------------------------------------
-- 1. The authenticator and the recovery codes. Written by scd only.
-- ---------------------------------------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.operator_mfa (
  operator_id    uuid PRIMARY KEY REFERENCES public.operators (id) ON DELETE CASCADE,
  key_id         text NOT NULL CONSTRAINT om_key_id CHECK (length(key_id) BETWEEN 1 AND 64),
  nonce          bytea NOT NULL,
  ciphertext     bytea NOT NULL,
  cipher_version integer NOT NULL CONSTRAINT om_cipher CHECK (cipher_version = 1),
  created_at     timestamptz NOT NULL DEFAULT now(),
  confirmed_at   timestamptz,
  last_used_step bigint
);
COMMENT ON TABLE public.operator_mfa IS
  'The Admin Console operator''s authenticator (0132, GDPR plan C10): the TOTP secret sealed by scd, never in the '
  'clear. confirmed_at is set once the operator proved it with a code; last_used_step stops a code being reused.';

CREATE TABLE IF NOT EXISTS public.operator_mfa_recovery_codes (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  operator_id uuid NOT NULL REFERENCES public.operators (id) ON DELETE CASCADE,
  code_mac    text NOT NULL CONSTRAINT omrc_mac CHECK (code_mac ~ '^[0-9a-f]{64}$'),
  created_at  timestamptz NOT NULL DEFAULT now(),
  used_at     timestamptz,
  CONSTRAINT omrc_unique UNIQUE (operator_id, code_mac)
);
COMMENT ON TABLE public.operator_mfa_recovery_codes IS
  'Single-use recovery codes for an operator who lost their authenticator (0132, GDPR plan C10): HMAC-SHA256 of '
  'the code under scd''s key, never the code. A new set replaces the old one.';

-- What edged may know: whether a second step is set up, and how many recovery codes are left. Nothing secret.
CREATE OR REPLACE FUNCTION public.operator_mfa_status(p_operator uuid)
  RETURNS TABLE (enrolled boolean, confirmed_at timestamptz, recovery_codes_left integer)
  LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public, pg_temp AS $fn$
  SELECT m.confirmed_at IS NOT NULL, m.confirmed_at,
         (SELECT count(*)::integer FROM public.operator_mfa_recovery_codes c
           WHERE c.operator_id = p_operator AND c.used_at IS NULL)
    FROM (SELECT 1) one
    LEFT JOIN public.operator_mfa m ON m.operator_id = p_operator;
$fn$;
REVOKE EXECUTE ON FUNCTION public.operator_mfa_status(uuid) FROM PUBLIC;

-- Break-glass for the appliance's own command line (`edged reset-mfa --email`), when no other site administrator
-- is left to reset the last one. It only REMOVES an authenticator -- what scd's reset already lets edged do --
-- and opens nothing. Returns whether there was one.
CREATE OR REPLACE FUNCTION public.operator_mfa_remove(p_operator uuid) RETURNS boolean
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = public, pg_temp AS $fn$
DECLARE n integer;
BEGIN
  DELETE FROM public.operator_mfa_recovery_codes WHERE operator_id = p_operator;
  DELETE FROM public.operator_mfa WHERE operator_id = p_operator;
  GET DIAGNOSTICS n = ROW_COUNT;
  RETURN n > 0;
END $fn$;
REVOKE EXECUTE ON FUNCTION public.operator_mfa_remove(uuid) FROM PUBLIC;

-- ---------------------------------------------------------------------------------------------------------
-- 2. Wrong codes are throttled like wrong passwords: scope 'mfa', per operator, the account limits.
-- ---------------------------------------------------------------------------------------------------------
ALTER TABLE public.operator_signin_throttle DROP CONSTRAINT IF EXISTS ost_scope;
ALTER TABLE public.operator_signin_throttle
  ADD CONSTRAINT ost_scope CHECK (scope IN ('account','ip','reauth','mfa'));

CREATE OR REPLACE FUNCTION public.operator_signin_throttle_fail(p_scope text, p_key_hash text)
  RETURNS TABLE (failures integer, locked_until timestamptz, newly_locked boolean)
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = public, pg_temp AS $fn$
DECLARE l record; v_fail integer; v_until timestamptz; v_was timestamptz;
BEGIN
  IF p_scope IS NULL OR p_scope NOT IN ('account','ip','reauth','mfa') THEN
    RAISE EXCEPTION 'unknown throttle scope %', p_scope USING ERRCODE = 'invalid_parameter_value';
  END IF;
  SELECT * INTO l FROM public.operator_signin_throttle_limits(p_scope);
  -- Serialise failures on ONE key so "was a lockout already running" is read under the same lock the update
  -- takes; different keys never wait for each other.
  PERFORM pg_advisory_xact_lock(hashtext('operator_signin_throttle'), hashtext(p_scope || ':' || p_key_hash));
  SELECT t.locked_until INTO v_was FROM public.operator_signin_throttle t
   WHERE t.scope = p_scope AND t.key_hash = p_key_hash;
  INSERT INTO public.operator_signin_throttle AS t (scope, key_hash, failures, window_started_at, last_failure_at,
                                                    locked_until)
  VALUES (p_scope, p_key_hash, 1, now(), now(), NULL)
  ON CONFLICT (scope, key_hash) DO UPDATE SET
    -- A window that ran out with no lockout running starts again at one.
    failures = CASE WHEN t.window_started_at < now() - make_interval(secs => l.window_seconds)
                         AND (t.locked_until IS NULL OR t.locked_until <= now())
                    THEN 1 ELSE t.failures + 1 END,
    window_started_at = CASE WHEN t.window_started_at < now() - make_interval(secs => l.window_seconds)
                                  AND (t.locked_until IS NULL OR t.locked_until <= now())
                             THEN now() ELSE t.window_started_at END,
    last_failure_at = now(),
    locked_until = CASE WHEN t.locked_until > now() THEN t.locked_until ELSE NULL END
  RETURNING t.failures, t.locked_until INTO v_fail, v_until;
  -- The failure that reaches the threshold starts the lockout. A lockout already running keeps its expiry: a
  -- guess during a lockout does not extend it, so the owner's wait is predictable.
  IF (v_until IS NULL OR v_until <= now()) AND v_fail >= l.max_failures THEN
    v_until := now() + make_interval(secs => l.lockout_seconds);
    UPDATE public.operator_signin_throttle t SET locked_until = v_until
     WHERE t.scope = p_scope AND t.key_hash = p_key_hash;
  END IF;
  failures := v_fail;
  locked_until := CASE WHEN v_until > now() THEN v_until END;
  newly_locked := v_until > now() AND (v_was IS NULL OR v_was <= now());
  RETURN NEXT;
  -- Bounded retention: counters nobody has touched for two days are past every window and lockout the
  -- bounds allow (both at most 24 h). Occasional, so a burst of failures does not each pay for it.
  IF random() < 0.05 THEN
    DELETE FROM public.operator_signin_throttle d
     WHERE d.last_failure_at < now() - interval '2 days'
       AND (d.locked_until IS NULL OR d.locked_until < now());
  END IF;
END $fn$;
REVOKE EXECUTE ON FUNCTION public.operator_signin_throttle_fail(text,text) FROM PUBLIC;

-- A correct code clears the operator's code counter, as a correct password clears the account's.
CREATE OR REPLACE FUNCTION public.operator_signin_throttle_clear(p_scope text, p_key_hash text)
  RETURNS void
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = public, pg_temp AS $fn$
BEGIN
  IF p_scope IS NULL OR p_scope NOT IN ('account','reauth','mfa') THEN
    RAISE EXCEPTION 'only an account, step-up or code counter can be cleared (got %)', p_scope
      USING ERRCODE = 'invalid_parameter_value';
  END IF;
  DELETE FROM public.operator_signin_throttle t WHERE t.scope = p_scope AND t.key_hash = p_key_hash;
END $fn$;
REVOKE EXECUTE ON FUNCTION public.operator_signin_throttle_clear(text,text) FROM PUBLIC;

-- ---------------------------------------------------------------------------------------------------------
-- 3. The session limits: an audited appliance setting (docs/OPERATIONAL_SETTINGS_POLICY.md).
-- ---------------------------------------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.operator_session_policy (
  id                   boolean PRIMARY KEY DEFAULT true CONSTRAINT osessp_singleton CHECK (id),
  idle_timeout_minutes integer NOT NULL DEFAULT 30
    CONSTRAINT osessp_idle CHECK (idle_timeout_minutes BETWEEN 5 AND 240),
  max_session_hours    integer NOT NULL DEFAULT 12
    CONSTRAINT osessp_max CHECK (max_session_hours BETWEEN 1 AND 24),
  config_version       bigint  NOT NULL DEFAULT 1 CONSTRAINT osessp_version CHECK (config_version >= 1),
  updated_at           timestamptz NOT NULL DEFAULT now(),
  updated_by           text
);
COMMENT ON TABLE public.operator_session_policy IS
  'Admin Console session limits (0132, GDPR plan C10): signed out after idle_timeout_minutes without activity and '
  'max_session_hours after signing in, whatever the activity. One row for the appliance; no row means the '
  'defaults (30 minutes, 12 hours). Written only through public.operator_session_policy_set.';

CREATE TABLE IF NOT EXISTS public.operator_session_policy_changes (
  id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  changed_by         text NOT NULL CONSTRAINT osesspc_actor CHECK (length(btrim(changed_by)) > 0),
  change_reason      text NOT NULL CONSTRAINT osesspc_reason CHECK (length(btrim(change_reason)) > 0),
  old_values         jsonb,
  new_values         jsonb NOT NULL,
  new_config_version bigint NOT NULL,
  changed_at         timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS operator_session_policy_changes_at ON public.operator_session_policy_changes (changed_at DESC);
COMMENT ON TABLE public.operator_session_policy_changes IS 'Append-only history of public.operator_session_policy (0132).';

CREATE OR REPLACE FUNCTION public.operator_session_policy_changes_append_only() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'public.operator_session_policy_changes is append-only: % refused', TG_OP
    USING ERRCODE = 'restrict_violation';
END $$;
REVOKE EXECUTE ON FUNCTION public.operator_session_policy_changes_append_only() FROM PUBLIC;
DROP TRIGGER IF EXISTS operator_session_policy_changes_append_only ON public.operator_session_policy_changes;
CREATE TRIGGER operator_session_policy_changes_append_only
  BEFORE UPDATE OR DELETE ON public.operator_session_policy_changes
  FOR EACH ROW EXECUTE FUNCTION public.operator_session_policy_changes_append_only();

CREATE OR REPLACE FUNCTION public.operator_session_policy_get()
  RETURNS TABLE (idle_timeout_minutes integer, max_session_hours integer, config_version bigint,
                 updated_at timestamptz, updated_by text)
  LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public, pg_temp AS $fn$
  SELECT COALESCE(p.idle_timeout_minutes, 30), COALESCE(p.max_session_hours, 12),
         COALESCE(p.config_version, 0::bigint), p.updated_at, p.updated_by
    FROM (SELECT 1) one
    LEFT JOIN public.operator_session_policy p ON p.id;
$fn$;
REVOKE EXECUTE ON FUNCTION public.operator_session_policy_get() FROM PUBLIC;

CREATE OR REPLACE FUNCTION public.operator_session_policy_set(
    p_idle_timeout_minutes integer, p_max_session_hours integer, p_operator text, p_reason text)
  RETURNS bigint
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = public, pg_temp AS $fn$
DECLARE v_version bigint; v_old jsonb; v_new jsonb;
BEGIN
  IF p_operator IS NULL OR btrim(p_operator) = '' THEN
    RAISE EXCEPTION 'an operator label is required' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  IF p_reason IS NULL OR length(btrim(p_reason)) < 4 OR length(btrim(p_reason)) > 500 THEN
    RAISE EXCEPTION 'a reason of 4-500 characters is required to change the session limits'
      USING ERRCODE = 'invalid_parameter_value';
  END IF;
  -- The bounds are the table CHECKs; the INSERT below refuses anything outside them with check_violation.
  PERFORM pg_advisory_xact_lock(hashtext('operator_session_policy'));
  SELECT jsonb_build_object('idle_timeout_minutes', p.idle_timeout_minutes, 'max_session_hours', p.max_session_hours)
    INTO v_old FROM public.operator_session_policy p WHERE p.id FOR UPDATE;
  INSERT INTO public.operator_session_policy AS p (id, idle_timeout_minutes, max_session_hours, updated_by)
  VALUES (true, p_idle_timeout_minutes, p_max_session_hours, btrim(p_operator))
  ON CONFLICT (id) DO UPDATE
     SET idle_timeout_minutes = EXCLUDED.idle_timeout_minutes, max_session_hours = EXCLUDED.max_session_hours,
         updated_by = EXCLUDED.updated_by, config_version = p.config_version + 1, updated_at = now()
  RETURNING p.config_version INTO v_version;
  v_new := jsonb_build_object('idle_timeout_minutes', p_idle_timeout_minutes, 'max_session_hours', p_max_session_hours);
  INSERT INTO public.operator_session_policy_changes (changed_by, change_reason, old_values, new_values, new_config_version)
  VALUES (btrim(p_operator), btrim(p_reason), v_old, v_new, v_version);
  RETURN v_version;
END $fn$;
REVOKE EXECUTE ON FUNCTION public.operator_session_policy_set(integer,integer,text,text) FROM PUBLIC;

-- ---------------------------------------------------------------------------------------------------------
-- 4. Least privilege, mirrored in deploy/gatep/svc-operator-mfa-grants.sql.
--    svc_scd   reads and writes the authenticator and recovery codes (it holds the key; nothing else does).
--    svc_edged knows only whether a second step is set up, reads and changes the session limits through their
--              definers and reads the change log. It has no privilege on the authenticator tables at all.
-- ---------------------------------------------------------------------------------------------------------
REVOKE ALL ON public.operator_mfa, public.operator_mfa_recovery_codes, public.operator_session_policy,
              public.operator_session_policy_changes FROM PUBLIC;
DO $g$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_scd') THEN
    GRANT SELECT, INSERT, UPDATE, DELETE ON public.operator_mfa, public.operator_mfa_recovery_codes TO svc_scd;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
    GRANT EXECUTE ON FUNCTION public.operator_mfa_status(uuid) TO svc_edged;
    GRANT EXECUTE ON FUNCTION public.operator_mfa_remove(uuid) TO svc_edged;
    GRANT EXECUTE ON FUNCTION public.operator_session_policy_get() TO svc_edged;
    GRANT EXECUTE ON FUNCTION public.operator_session_policy_set(integer,integer,text,text) TO svc_edged;
    GRANT SELECT ON public.operator_session_policy_changes TO svc_edged;
  END IF;
END $g$;

COMMIT;
