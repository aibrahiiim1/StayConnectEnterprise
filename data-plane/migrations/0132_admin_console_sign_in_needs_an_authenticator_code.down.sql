-- 0132 down -- the Admin Console signs in with a password alone again, and sessions follow the old 12-hour
-- sliding rule in edged.
--
-- The authenticator and recovery-code tables are DROPPED: they are credentials, and a rollback that kept them
-- would leave sealed secrets nobody uses. Every operator sets up a new authenticator if 0132 is applied again.
-- The session-limit setting and its change log are KEPT, as history. 'mfa' throttle counters are removed with
-- the scope.

BEGIN;

DROP FUNCTION IF EXISTS public.operator_mfa_status(uuid);
DROP FUNCTION IF EXISTS public.operator_mfa_remove(uuid);
DROP TABLE IF EXISTS public.operator_mfa_recovery_codes;
DROP TABLE IF EXISTS public.operator_mfa;

DROP FUNCTION IF EXISTS public.operator_session_policy_set(integer,integer,text,text);
DROP FUNCTION IF EXISTS public.operator_session_policy_get();

DELETE FROM public.operator_signin_throttle WHERE scope = 'mfa';
ALTER TABLE public.operator_signin_throttle DROP CONSTRAINT IF EXISTS ost_scope;
ALTER TABLE public.operator_signin_throttle ADD CONSTRAINT ost_scope CHECK (scope IN ('account','ip','reauth'));

CREATE OR REPLACE FUNCTION public.operator_signin_throttle_fail(p_scope text, p_key_hash text)
  RETURNS TABLE (failures integer, locked_until timestamptz, newly_locked boolean)
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = public, pg_temp AS $fn$
DECLARE l record; v_fail integer; v_until timestamptz; v_was timestamptz;
BEGIN
  IF p_scope IS NULL OR p_scope NOT IN ('account','ip','reauth') THEN
    RAISE EXCEPTION 'unknown throttle scope %', p_scope USING ERRCODE = 'invalid_parameter_value';
  END IF;
  SELECT * INTO l FROM public.operator_signin_throttle_limits(p_scope);
  PERFORM pg_advisory_xact_lock(hashtext('operator_signin_throttle'), hashtext(p_scope || ':' || p_key_hash));
  SELECT t.locked_until INTO v_was FROM public.operator_signin_throttle t
   WHERE t.scope = p_scope AND t.key_hash = p_key_hash;
  INSERT INTO public.operator_signin_throttle AS t (scope, key_hash, failures, window_started_at, last_failure_at,
                                                    locked_until)
  VALUES (p_scope, p_key_hash, 1, now(), now(), NULL)
  ON CONFLICT (scope, key_hash) DO UPDATE SET
    failures = CASE WHEN t.window_started_at < now() - make_interval(secs => l.window_seconds)
                         AND (t.locked_until IS NULL OR t.locked_until <= now())
                    THEN 1 ELSE t.failures + 1 END,
    window_started_at = CASE WHEN t.window_started_at < now() - make_interval(secs => l.window_seconds)
                                  AND (t.locked_until IS NULL OR t.locked_until <= now())
                             THEN now() ELSE t.window_started_at END,
    last_failure_at = now(),
    locked_until = CASE WHEN t.locked_until > now() THEN t.locked_until ELSE NULL END
  RETURNING t.failures, t.locked_until INTO v_fail, v_until;
  IF (v_until IS NULL OR v_until <= now()) AND v_fail >= l.max_failures THEN
    v_until := now() + make_interval(secs => l.lockout_seconds);
    UPDATE public.operator_signin_throttle t SET locked_until = v_until
     WHERE t.scope = p_scope AND t.key_hash = p_key_hash;
  END IF;
  failures := v_fail;
  locked_until := CASE WHEN v_until > now() THEN v_until END;
  newly_locked := v_until > now() AND (v_was IS NULL OR v_was <= now());
  RETURN NEXT;
  IF random() < 0.05 THEN
    DELETE FROM public.operator_signin_throttle d
     WHERE d.last_failure_at < now() - interval '2 days'
       AND (d.locked_until IS NULL OR d.locked_until < now());
  END IF;
END $fn$;
REVOKE EXECUTE ON FUNCTION public.operator_signin_throttle_fail(text,text) FROM PUBLIC;

CREATE OR REPLACE FUNCTION public.operator_signin_throttle_clear(p_scope text, p_key_hash text)
  RETURNS void
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = public, pg_temp AS $fn$
BEGIN
  IF p_scope IS NULL OR p_scope NOT IN ('account','reauth') THEN
    RAISE EXCEPTION 'only an account or step-up counter can be cleared (got %)', p_scope
      USING ERRCODE = 'invalid_parameter_value';
  END IF;
  DELETE FROM public.operator_signin_throttle t WHERE t.scope = p_scope AND t.key_hash = p_key_hash;
END $fn$;
REVOKE EXECUTE ON FUNCTION public.operator_signin_throttle_clear(text,text) FROM PUBLIC;

COMMIT;
