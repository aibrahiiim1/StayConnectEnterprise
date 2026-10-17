-- Admin Console authenticator codes and session limits (migration 0132; GDPR remediation plan C10, Product-Owner
-- decision D-C10). Idempotent; included by gatep-grants.sql after its revoke, so a Gate-P reconcile reproduces
-- exactly what the migration granted.
--
--   svc_scd    holds the sealing key, so it alone reads and writes the authenticator and the recovery codes.
--   svc_edged  knows only whether a second step is set up (operator_mfa_status), may remove one for the
--              command-line break-glass (operator_mfa_remove, which opens nothing), and reads and changes the
--              session limits through their definers (operator and reason required, every change logged).
--              It has no privilege on the authenticator tables.
--
-- The throttle functions it already holds (0119) accept the new 'mfa' scope; no new grant is needed for them.
--
-- GUARDED on the migration being present, like every file granting on 0116+ objects (audit B-20).
DO $g$
BEGIN
  IF to_regclass('public.operator_mfa') IS NULL THEN
    RAISE NOTICE 'migration 0132 is not applied; operator-mfa grants skipped';
    RETURN;
  END IF;
  GRANT SELECT, INSERT, UPDATE, DELETE ON public.operator_mfa, public.operator_mfa_recovery_codes TO svc_scd;

  GRANT EXECUTE ON FUNCTION public.operator_mfa_status(uuid) TO svc_edged;
  GRANT EXECUTE ON FUNCTION public.operator_mfa_remove(uuid) TO svc_edged;
  GRANT EXECUTE ON FUNCTION public.operator_session_policy_get() TO svc_edged;
  GRANT EXECUTE ON FUNCTION public.operator_session_policy_set(integer,integer,text,text) TO svc_edged;
  GRANT SELECT ON public.operator_session_policy_changes TO svc_edged;
END $g$;
