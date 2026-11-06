DO $g$
BEGIN
  IF to_regprocedure('public.operator_password_reset_check(uuid,text)') IS NULL THEN
    RAISE NOTICE 'migration 0214 is not applied; operator password reset grants skipped';
    RETURN;
  END IF;
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
