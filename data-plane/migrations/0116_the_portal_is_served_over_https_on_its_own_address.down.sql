-- 0116 down. A secret sealed by scd cannot be restored to plaintext here: re-enter it after a rollback.
BEGIN;
DROP TABLE IF EXISTS iam_v2.social_provider_secret_generations;
ALTER TABLE public.social_oauth_states DROP COLUMN IF EXISTS nonce;
ALTER TABLE public.social_oauth_states DROP COLUMN IF EXISTS code_verifier;
ALTER TABLE public.social_oauth_providers DROP COLUMN IF EXISTS callback_hosts;
DROP TABLE IF EXISTS public.portal_certificates;
DROP TABLE IF EXISTS public.portal_tls_state;
DROP TRIGGER IF EXISTS portal_https_settings_changes_append_only ON public.portal_https_settings_changes;
DROP TABLE IF EXISTS public.portal_https_settings_changes;
DROP FUNCTION IF EXISTS public.portal_https_settings_changes_append_only();
DROP TABLE IF EXISTS public.portal_https_settings;
COMMIT;
