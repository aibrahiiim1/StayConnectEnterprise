-- 0138 down. Accounts already anonymised stay anonymised: nothing can bring their email back.
BEGIN;

DROP FUNCTION IF EXISTS public.run_staff_retention(uuid, uuid);
CREATE OR REPLACE FUNCTION public.portal_https_settings_changes_append_only() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'portal_https_settings_changes is append-only';
END $$;
DROP FUNCTION IF EXISTS public.operator_account_retention_set(integer, text, text);
DROP FUNCTION IF EXISTS public.operator_account_retention_get();
DROP TABLE IF EXISTS public.operator_account_retention_changes;
DROP FUNCTION IF EXISTS public.operator_account_retention_changes_append_only();
DROP TABLE IF EXISTS public.operator_account_retention;
DROP TRIGGER IF EXISTS operators_track_disabled ON public.operators;
DROP FUNCTION IF EXISTS public.operators_track_disabled();
ALTER TABLE public.operators DROP COLUMN IF EXISTS anonymised_at;
ALTER TABLE public.operators DROP COLUMN IF EXISTS disabled_at;

COMMIT;
