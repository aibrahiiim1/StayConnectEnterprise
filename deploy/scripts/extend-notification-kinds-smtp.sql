-- ADD THE smtp EMAIL KIND TO public.notification_providers ON A LIVE APPLIANCE.
--
-- Migration 0107 widens the kind CHECK only when the applying role owns the table, because the live-site
-- migration runner applies as iam_v2_owner and may not change public-schema structure. On a live appliance the
-- table is owned by the database owner, so the same change is applied here, by that owner, as one explicit
-- step (the 0099 WhatsApp pattern).
--
-- Run as the table owner, after 0107 is applied:
--   docker exec -i stayconnect-pg psql -X -v ON_ERROR_STOP=1 -U stayconnect -d stayconnect_site \
--     < deploy/scripts/extend-notification-kinds-smtp.sql
--
-- Idempotent: running it again leaves exactly the same constraint. It touches no row and no privilege.
\set ON_ERROR_STOP 1
BEGIN;
DO $extend$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM public.schema_migrations
                  WHERE version = '0107_a_client_is_one_identity_with_groups_and_a_remembered_device') THEN
    RAISE EXCEPTION 'EXTEND_REFUSED: migration 0107 is not applied; the SMTP sender software is not in place';
  END IF;
  EXECUTE 'ALTER TABLE public.notification_providers DROP CONSTRAINT IF EXISTS notification_providers_kind_check';
  EXECUTE $c$ALTER TABLE public.notification_providers ADD CONSTRAINT notification_providers_kind_check
    CHECK (kind = ANY (ARRAY['stub'::text, 'sendgrid'::text, 'ses'::text, 'smtp'::text, 'twilio'::text,
                             'meta_whatsapp'::text, 'twilio_whatsapp'::text]))$c$;
  RAISE NOTICE 'smtp email kind enabled on public.notification_providers';
END $extend$;
COMMIT;
