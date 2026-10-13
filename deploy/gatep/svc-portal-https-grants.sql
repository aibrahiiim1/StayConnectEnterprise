-- HTTPS Client Portal and sealed social-login secrets (migration 0116; contract
-- docs/architecture/ONEGATE_HTTPS_PORTAL_AND_SOCIAL_LOGIN.md). Idempotent; included by gatep-grants.sql after its
-- revoke, so a Gate-P reconcile reproduces exactly what the migration granted.
--
--   svc_scd    reads the portal settings; owns the portal TLS state and the installed-certificate records;
--              seals and opens social-provider secrets and clears the plaintext column once sealed.
--   svc_edged  reads and writes the operator setting and appends its history; reads the TLS state.
--   svc_netd   reads the portal address and the TLS state (option 114, unbound, the portal VIP).
GRANT SELECT                         ON public.portal_https_settings          TO svc_scd;
GRANT SELECT, INSERT, UPDATE         ON public.portal_tls_state               TO svc_scd;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.portal_certificates            TO svc_scd;
GRANT UPDATE (client_secret)         ON public.social_oauth_providers         TO svc_scd;
GRANT SELECT, INSERT, UPDATE         ON iam_v2.social_provider_secret_generations TO svc_scd;

GRANT SELECT, INSERT, UPDATE         ON public.portal_https_settings          TO svc_edged;
GRANT SELECT, INSERT                 ON public.portal_https_settings_changes  TO svc_edged;
GRANT USAGE                          ON SEQUENCE public.portal_https_settings_changes_id_seq TO svc_edged;
GRANT SELECT                         ON public.portal_tls_state, public.portal_certificates TO svc_edged;

GRANT SELECT                         ON public.portal_https_settings, public.portal_tls_state TO svc_netd;
