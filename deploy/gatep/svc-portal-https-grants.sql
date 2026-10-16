-- HTTPS Client Portal and sealed social-login secrets (migration 0116; contract
-- docs/architecture/ONEGATE_HTTPS_PORTAL_AND_SOCIAL_LOGIN.md). Idempotent; included by gatep-grants.sql after its
-- revoke, so a Gate-P reconcile reproduces exactly what the migration granted.
--
--   svc_scd    reads the portal settings; owns the portal TLS state and the installed-certificate records;
--              seals and opens social-provider secrets and clears the plaintext column once sealed.
--   svc_edged  reads and writes the operator setting and appends its history; reads the TLS state.
--   svc_netd   reads the portal address and the TLS state (option 114, unbound, the portal VIP).

-- A cross-tenant transition purges the previous tenant's sealed notification secrets (cmd/scd/tenant_transition.go).
-- That table predates 0116 (0107), so this grant is outside the guard below.
GRANT DELETE ON iam_v2.notification_provider_secret_generations TO svc_scd;

-- GUARDED on the migration being present (audit B-20): every other object here is created by 0116, and a reconcile
-- run on a database rolled back below 0116 must skip these grants rather than fail on them.
DO $g0116$
BEGIN
  IF to_regclass('public.portal_https_settings') IS NULL
     OR to_regclass('iam_v2.social_provider_secret_generations') IS NULL THEN
    RAISE NOTICE 'migration 0116 is not applied -- HTTPS portal grants skipped';
    RETURN;
  END IF;
  GRANT SELECT                         ON public.portal_https_settings          TO svc_scd;
  GRANT SELECT, INSERT, UPDATE         ON public.portal_tls_state               TO svc_scd;
  GRANT SELECT, INSERT, UPDATE, DELETE ON public.portal_certificates            TO svc_scd;
  GRANT UPDATE (client_secret)         ON public.social_oauth_providers         TO svc_scd;
  GRANT SELECT, INSERT, UPDATE         ON iam_v2.social_provider_secret_generations TO svc_scd;
  -- a cross-tenant transition purges the previous tenant's sealed social secrets (cmd/scd/tenant_transition.go)
  GRANT DELETE ON iam_v2.social_provider_secret_generations TO svc_scd;

  GRANT SELECT, INSERT, UPDATE         ON public.portal_https_settings          TO svc_edged;
  GRANT SELECT, INSERT                 ON public.portal_https_settings_changes  TO svc_edged;
  GRANT USAGE                          ON SEQUENCE public.portal_https_settings_changes_id_seq TO svc_edged;
  GRANT SELECT                         ON public.portal_tls_state, public.portal_certificates TO svc_edged;

  GRANT SELECT                         ON public.portal_https_settings, public.portal_tls_state TO svc_netd;
END $g0116$;
