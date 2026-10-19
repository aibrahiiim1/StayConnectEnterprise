-- Disabled staff accounts are anonymised after a set period (migration 0138; GDPR audit 2026-10-19, B4).
-- Idempotent; included by gatep-grants.sql after its revoke, so a Gate-P reconcile reproduces exactly what the
-- migration granted.
--
--   svc_scd    runs the hourly staff retention through its definer function.
--   svc_edged  reads and changes the disabled-account period through its definers, and reads its change log.
--
-- GUARDED on the migration being present, like every file granting on 0116+ objects (audit B-20).
DO $g$
BEGIN
  IF to_regprocedure('public.run_staff_retention(uuid,uuid)') IS NULL THEN
    RAISE NOTICE 'migration 0138 is not applied; staff retention grants skipped';
    RETURN;
  END IF;
  GRANT EXECUTE ON FUNCTION public.run_staff_retention(uuid, uuid) TO svc_scd;
  GRANT EXECUTE ON FUNCTION public.operator_account_retention_get() TO svc_edged;
  GRANT EXECUTE ON FUNCTION public.operator_account_retention_set(integer,text,text) TO svc_edged;
  GRANT SELECT ON public.operator_account_retention_changes TO svc_edged;
END $g$;
