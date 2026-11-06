BEGIN;

DROP FUNCTION IF EXISTS public.operator_password_reset_discard(uuid);
DROP FUNCTION IF EXISTS public.operator_password_reset_consume(uuid,text);
DROP FUNCTION IF EXISTS public.operator_password_reset_fail(uuid);
DROP FUNCTION IF EXISTS public.operator_password_reset_check(uuid,text);
DROP FUNCTION IF EXISTS public.operator_password_reset_issue(uuid,text);
DROP FUNCTION IF EXISTS public.operator_password_reset_request_allowed(text,text);
DROP FUNCTION IF EXISTS public.operator_password_reset_policy_set(boolean,integer,integer,integer,integer,boolean,bigint,text,text);
DROP FUNCTION IF EXISTS public.operator_password_reset_policy_get();
DROP TABLE IF EXISTS public.operator_password_resets;
DROP TABLE IF EXISTS public.operator_password_reset_requests;

DO $g$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
    REVOKE SELECT ON public.operator_password_reset_policy_changes FROM svc_edged;
  END IF;
END $g$;

COMMIT;
