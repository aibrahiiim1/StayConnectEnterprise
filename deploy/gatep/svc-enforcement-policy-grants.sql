-- Enforcement policy (migration 0190): netd and acctd read it, edged reads it and changes it only through
-- enforcement_policy_set(). Idempotent; included by gatep-grants.sql after its revoke.
DO $g0190$
BEGIN
  IF to_regclass('public.enforcement_policy') IS NULL THEN
    RAISE NOTICE 'migration 0190 is not applied -- enforcement policy grants skipped';
    RETURN;
  END IF;
  GRANT SELECT ON public.enforcement_policy TO svc_netd;
  GRANT SELECT ON public.enforcement_policy TO svc_acctd;
  GRANT SELECT ON public.enforcement_policy, public.enforcement_policy_changes TO svc_edged;
  GRANT EXECUTE ON FUNCTION public.enforcement_policy_set(text,text,text,text) TO svc_edged;
END $g0190$;
