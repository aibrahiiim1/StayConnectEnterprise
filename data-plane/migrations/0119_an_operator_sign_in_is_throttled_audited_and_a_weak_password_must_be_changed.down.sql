-- 0119 down -- remove the sign-in throttle and the must-change flag; KEEP the record of every policy change.
--
-- The functions and the live counters go (they are state, not history), so edged returns to an unthrottled
-- login -- run this only together with a rollback of edged to a build that does not call them. The must-change
-- flag goes with them. public.operator_signin_protection and its append-only change log are kept: a rollback
-- must not erase who changed the policy and when, and re-applying the up migration (IF NOT EXISTS) continues
-- them. The failed sign-ins recorded in public.audit_log are untouched.

BEGIN;

DROP FUNCTION IF EXISTS public.operator_signin_throttle_clear(text,text);
DROP FUNCTION IF EXISTS public.operator_signin_throttle_fail(text,text);
DROP FUNCTION IF EXISTS public.operator_signin_throttle_state(text,text);
DROP FUNCTION IF EXISTS public.operator_signin_throttle_limits(text);
DROP FUNCTION IF EXISTS public.operator_signin_protection_set(integer,integer,integer,integer,integer,text,text);
DROP FUNCTION IF EXISTS public.operator_signin_protection_get();
DROP TABLE IF EXISTS public.operator_signin_throttle;

DO $g$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
    REVOKE SELECT ON public.operator_signin_protection_changes FROM svc_edged;
  END IF;
END $g$;

ALTER TABLE public.operators DROP COLUMN IF EXISTS must_change_password;

COMMIT;
