-- 0119 -- AN OPERATOR SIGN-IN IS THROTTLED AND AUDITED, AND A WEAK PASSWORD MUST BE CHANGED
-- (audit remediation stream 1: N-1, N-8, N-24; docs/ROLE_AND_SCOPE_MATRIX.md, docs/user-guide/hotel-admin-reference.md).
--
-- Until now the Admin Console login (edged POST /edge/v1/auth/login) and the password step-up every high-risk
-- action asks for accepted unlimited guesses: 20 wrong passwords in 2.6 s, then the right one signed in, and none
-- of the failures left a record. This migration adds the three things the fix needs, in the public schema next
-- to public.operators, and nothing else:
--
--   1. public.operator_signin_protection -- the policy, as an audited setting (docs/OPERATIONAL_SETTINGS_POLICY.md):
--        how many failed passwords per account and per source address within how long, and how long a lockout
--        lasts. Defaults 5 per account / 15 min, 20 per address / 15 min, lockout 15 min. One row for the
--        appliance (operators belong to the appliance, and the login must be protected before the appliance is
--        assigned to a site). Bounds in a CHECK; written ONLY through operator_signin_protection_set, which also
--        appends the change row, so no runtime role can change the policy without leaving the record.
--   2. public.operator_signin_throttle -- the live counters, durable so a restart or parallel requests do not
--        reset them. Keyed by a SHA-256 of the account name / source address / operator id, never the raw value
--        (a username field sometimes receives a password). Written ONLY through the definer functions.
--   3. public.operators.must_change_password -- an operator with this set can do nothing in the console but
--        change their password. Set by `edged seed-admin --allow-weak`, by an administrator who resets or
--        creates an account with "must change at next sign-in", and by edged when an operator signs in with a
--        password shorter than the policy.
--
-- The failures themselves are recorded in the existing append-only public.audit_log by edged
-- (operator.login_failed / operator.reauth_failed / operator.login_locked), with the reason and source address
-- and never the password. Additive and idempotent. No existing row is changed.

BEGIN;

-- ---------------------------------------------------------------------------------------------------------
-- 1. The policy.
-- ---------------------------------------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.operator_signin_protection (
  id                     boolean PRIMARY KEY DEFAULT true CONSTRAINT osp_singleton CHECK (id),
  account_max_failures   integer NOT NULL DEFAULT 5
    CONSTRAINT osp_account_max CHECK (account_max_failures BETWEEN 3 AND 20),
  account_window_seconds integer NOT NULL DEFAULT 900
    CONSTRAINT osp_account_window CHECK (account_window_seconds BETWEEN 60 AND 86400),
  ip_max_failures        integer NOT NULL DEFAULT 20
    CONSTRAINT osp_ip_max CHECK (ip_max_failures BETWEEN 5 AND 500),
  ip_window_seconds      integer NOT NULL DEFAULT 900
    CONSTRAINT osp_ip_window CHECK (ip_window_seconds BETWEEN 60 AND 86400),
  lockout_seconds        integer NOT NULL DEFAULT 900
    CONSTRAINT osp_lockout CHECK (lockout_seconds BETWEEN 60 AND 86400),
  config_version         bigint  NOT NULL DEFAULT 1 CONSTRAINT osp_version CHECK (config_version >= 1),
  updated_at             timestamptz NOT NULL DEFAULT now(),
  updated_by             text
);
COMMENT ON TABLE public.operator_signin_protection IS
  'Admin Console sign-in protection (0119): failed passwords allowed per account and per source address within '
  'a window, and the lockout that follows. One row for the appliance; no row means the defaults (5/900s, '
  '20/900s, lockout 900s). Written only through public.operator_signin_protection_set.';

CREATE TABLE IF NOT EXISTS public.operator_signin_protection_changes (
  id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  changed_by         text NOT NULL CONSTRAINT ospc_actor CHECK (length(btrim(changed_by)) > 0),
  change_reason      text NOT NULL CONSTRAINT ospc_reason CHECK (length(btrim(change_reason)) > 0),
  old_values         jsonb,
  new_values         jsonb NOT NULL,
  new_config_version bigint NOT NULL,
  changed_at         timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS operator_signin_protection_changes_at
  ON public.operator_signin_protection_changes (changed_at DESC);
COMMENT ON TABLE public.operator_signin_protection_changes IS
  'Append-only history of public.operator_signin_protection (0119).';

CREATE OR REPLACE FUNCTION public.operator_signin_protection_changes_append_only() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'public.operator_signin_protection_changes is append-only: % refused', TG_OP
    USING ERRCODE = 'restrict_violation';
END $$;
REVOKE EXECUTE ON FUNCTION public.operator_signin_protection_changes_append_only() FROM PUBLIC;
DROP TRIGGER IF EXISTS operator_signin_protection_changes_append_only ON public.operator_signin_protection_changes;
CREATE TRIGGER operator_signin_protection_changes_append_only
  BEFORE UPDATE OR DELETE ON public.operator_signin_protection_changes
  FOR EACH ROW EXECUTE FUNCTION public.operator_signin_protection_changes_append_only();

CREATE OR REPLACE FUNCTION public.operator_signin_protection_get()
  RETURNS TABLE (account_max_failures integer, account_window_seconds integer, ip_max_failures integer,
                 ip_window_seconds integer, lockout_seconds integer, config_version bigint,
                 updated_at timestamptz, updated_by text)
  LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public, pg_temp AS $fn$
  SELECT COALESCE(p.account_max_failures, 5), COALESCE(p.account_window_seconds, 900),
         COALESCE(p.ip_max_failures, 20), COALESCE(p.ip_window_seconds, 900),
         COALESCE(p.lockout_seconds, 900), COALESCE(p.config_version, 0::bigint), p.updated_at, p.updated_by
    FROM (SELECT 1) one
    LEFT JOIN public.operator_signin_protection p ON p.id;
$fn$;
REVOKE EXECUTE ON FUNCTION public.operator_signin_protection_get() FROM PUBLIC;

CREATE OR REPLACE FUNCTION public.operator_signin_protection_set(
    p_account_max_failures integer, p_account_window_seconds integer, p_ip_max_failures integer,
    p_ip_window_seconds integer, p_lockout_seconds integer, p_operator text, p_reason text)
  RETURNS bigint
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = public, pg_temp AS $fn$
DECLARE v_version bigint; v_old jsonb; v_new jsonb;
BEGIN
  IF p_operator IS NULL OR btrim(p_operator) = '' THEN
    RAISE EXCEPTION 'an operator label is required' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  IF p_reason IS NULL OR length(btrim(p_reason)) < 4 OR length(btrim(p_reason)) > 500 THEN
    RAISE EXCEPTION 'a reason of 4-500 characters is required to change the sign-in protection'
      USING ERRCODE = 'invalid_parameter_value';
  END IF;
  -- The bounds are the table CHECKs; the INSERT below refuses anything outside them with check_violation.
  PERFORM pg_advisory_xact_lock(hashtext('operator_signin_protection'));
  SELECT jsonb_build_object('account_max_failures', p.account_max_failures,
                            'account_window_seconds', p.account_window_seconds,
                            'ip_max_failures', p.ip_max_failures, 'ip_window_seconds', p.ip_window_seconds,
                            'lockout_seconds', p.lockout_seconds)
    INTO v_old FROM public.operator_signin_protection p WHERE p.id FOR UPDATE;
  INSERT INTO public.operator_signin_protection AS p
    (id, account_max_failures, account_window_seconds, ip_max_failures, ip_window_seconds, lockout_seconds,
     updated_by)
  VALUES (true, p_account_max_failures, p_account_window_seconds, p_ip_max_failures, p_ip_window_seconds,
          p_lockout_seconds, btrim(p_operator))
  ON CONFLICT (id) DO UPDATE
     SET account_max_failures = EXCLUDED.account_max_failures,
         account_window_seconds = EXCLUDED.account_window_seconds,
         ip_max_failures = EXCLUDED.ip_max_failures, ip_window_seconds = EXCLUDED.ip_window_seconds,
         lockout_seconds = EXCLUDED.lockout_seconds, updated_by = EXCLUDED.updated_by,
         config_version = p.config_version + 1, updated_at = now()
  RETURNING p.config_version INTO v_version;
  v_new := jsonb_build_object('account_max_failures', p_account_max_failures,
                              'account_window_seconds', p_account_window_seconds,
                              'ip_max_failures', p_ip_max_failures, 'ip_window_seconds', p_ip_window_seconds,
                              'lockout_seconds', p_lockout_seconds);
  INSERT INTO public.operator_signin_protection_changes
    (changed_by, change_reason, old_values, new_values, new_config_version)
  VALUES (btrim(p_operator), btrim(p_reason), v_old, v_new, v_version);
  RETURN v_version;
END $fn$;
REVOKE EXECUTE ON FUNCTION public.operator_signin_protection_set(integer,integer,integer,integer,integer,text,text)
  FROM PUBLIC;

-- ---------------------------------------------------------------------------------------------------------
-- 2. The live counters.
-- ---------------------------------------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.operator_signin_throttle (
  scope             text NOT NULL CONSTRAINT ost_scope CHECK (scope IN ('account','ip','reauth')),
  key_hash          text NOT NULL CONSTRAINT ost_key_hex CHECK (key_hash ~ '^[0-9a-f]{64}$'),
  failures          integer NOT NULL DEFAULT 0 CONSTRAINT ost_failures CHECK (failures >= 0),
  window_started_at timestamptz NOT NULL DEFAULT now(),
  last_failure_at   timestamptz NOT NULL DEFAULT now(),
  locked_until      timestamptz,
  PRIMARY KEY (scope, key_hash)
);
CREATE INDEX IF NOT EXISTS operator_signin_throttle_last_failure
  ON public.operator_signin_throttle (last_failure_at);
COMMENT ON TABLE public.operator_signin_throttle IS
  'Admin Console sign-in throttle counters (0119): per account (login), per operator (password step-up) and per '
  'source address. key_hash is SHA-256 of the value, never the value. Live state, not history: the history is '
  'public.audit_log. Written only through operator_signin_throttle_fail / _clear.';

-- The window and threshold a scope uses. 'reauth' (the password step-up of a signed-in operator) uses the
-- account values: the same person, the same password, a different door.
CREATE OR REPLACE FUNCTION public.operator_signin_throttle_limits(p_scope text,
    OUT max_failures integer, OUT window_seconds integer, OUT lockout_seconds integer)
  LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public, pg_temp AS $fn$
  SELECT CASE WHEN p_scope = 'ip' THEN g.ip_max_failures ELSE g.account_max_failures END,
         CASE WHEN p_scope = 'ip' THEN g.ip_window_seconds ELSE g.account_window_seconds END,
         g.lockout_seconds
    FROM public.operator_signin_protection_get() g;
$fn$;
REVOKE EXECUTE ON FUNCTION public.operator_signin_throttle_limits(text) FROM PUBLIC;

-- The state a decision is made on. A window that has run out, with no lockout still running, reads as zero.
CREATE OR REPLACE FUNCTION public.operator_signin_throttle_state(p_scope text, p_key_hash text)
  RETURNS TABLE (failures integer, last_failure_at timestamptz, locked_until timestamptz, max_failures integer)
  LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public, pg_temp AS $fn$
  SELECT CASE WHEN t.scope IS NULL
                OR (t.window_started_at < now() - make_interval(secs => l.window_seconds)
                    AND (t.locked_until IS NULL OR t.locked_until <= now()))
              THEN 0 ELSE t.failures END,
         t.last_failure_at,
         CASE WHEN t.locked_until > now() THEN t.locked_until END,
         l.max_failures
    FROM public.operator_signin_throttle_limits(p_scope) l
    LEFT JOIN public.operator_signin_throttle t ON t.scope = p_scope AND t.key_hash = p_key_hash;
$fn$;
REVOKE EXECUTE ON FUNCTION public.operator_signin_throttle_state(text,text) FROM PUBLIC;

-- One failed password. Atomic (one INSERT .. ON CONFLICT under the row lock), so parallel requests cannot
-- under-count. Returns the count, the lockout if one is running, and whether THIS failure started it.
CREATE OR REPLACE FUNCTION public.operator_signin_throttle_fail(p_scope text, p_key_hash text)
  RETURNS TABLE (failures integer, locked_until timestamptz, newly_locked boolean)
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = public, pg_temp AS $fn$
DECLARE l record; v_fail integer; v_until timestamptz; v_was timestamptz;
BEGIN
  IF p_scope IS NULL OR p_scope NOT IN ('account','ip','reauth') THEN
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

-- A correct password clears the counter of the ACCOUNT (or the operator's step-up), never of the source
-- address: one right password must not buy an address another twenty guesses at other accounts.
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

-- ---------------------------------------------------------------------------------------------------------
-- 3. Must change the password.
-- ---------------------------------------------------------------------------------------------------------
ALTER TABLE public.operators ADD COLUMN IF NOT EXISTS must_change_password boolean NOT NULL DEFAULT false;
COMMENT ON COLUMN public.operators.must_change_password IS
  'When true the operator can only change their password in the Admin Console (0119). Set by seed-admin '
  '--allow-weak, by an administrator reset or create with "must change", and at sign-in with a password '
  'shorter than the policy; cleared when the operator sets a new password.';

-- ---------------------------------------------------------------------------------------------------------
-- 4. Least privilege. Mirrored in deploy/gatep/gatep-grants.sql (public schema) so it survives Gate-P.
--    svc_edged gets EXECUTE on the functions and SELECT on the change log -- no table write: the policy cannot
--    change without its change row, and a counter cannot be wiped except by a correct password.
--    public.operators already grants svc_edged SELECT, INSERT, UPDATE at table level; the new column follows.
-- ---------------------------------------------------------------------------------------------------------
DO $g$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
    GRANT EXECUTE ON FUNCTION public.operator_signin_protection_get() TO svc_edged;
    GRANT EXECUTE ON FUNCTION public.operator_signin_protection_set(integer,integer,integer,integer,integer,text,text)
      TO svc_edged;
    GRANT EXECUTE ON FUNCTION public.operator_signin_throttle_state(text,text) TO svc_edged;
    GRANT EXECUTE ON FUNCTION public.operator_signin_throttle_fail(text,text) TO svc_edged;
    GRANT EXECUTE ON FUNCTION public.operator_signin_throttle_clear(text,text) TO svc_edged;
    GRANT SELECT ON public.operator_signin_protection_changes TO svc_edged;
  END IF;
END $g$;

COMMIT;
