-- ADD THE WHATSAPP CHANNEL TO public.notification_providers AND public.auth_otps ON A LIVE APPLIANCE.
--
-- WhatsApp one-time code is its own identity channel (licence module whatsapp_otp). Migration 0099 widens the
-- CHECK constraints that name the channel only when the applying role owns the tables, because the live-site
-- migration runner applies as iam_v2_owner and may not change public-schema structure. On a live appliance the
-- tables are owned by the database owner, so the same change is applied here, by that owner, as one explicit
-- step.
--
-- Run as the table owner, after 0099 is applied:
--   docker exec -i stayconnect-pg psql -X -v ON_ERROR_STOP=1 -U stayconnect -d stayconnect_site \
--     < deploy/scripts/extend-notification-channels-whatsapp.sql
--
-- Idempotent: running it again leaves exactly the same constraints. It touches no row and no privilege.
\set ON_ERROR_STOP 1
BEGIN;
DO $extend$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM public.schema_migrations WHERE version = '0099_a_client_can_get_a_code_on_whatsapp') THEN
    RAISE EXCEPTION 'EXTEND_REFUSED: migration 0099 is not applied; the WhatsApp channel software is not in place';
  END IF;

  EXECUTE 'ALTER TABLE public.notification_providers DROP CONSTRAINT IF EXISTS notification_providers_channel_check';
  EXECUTE $c$ALTER TABLE public.notification_providers ADD CONSTRAINT notification_providers_channel_check
    CHECK (channel = ANY (ARRAY['email'::text, 'sms'::text, 'whatsapp'::text]))$c$;
  EXECUTE 'ALTER TABLE public.notification_providers DROP CONSTRAINT IF EXISTS notification_providers_kind_check';
  EXECUTE $c$ALTER TABLE public.notification_providers ADD CONSTRAINT notification_providers_kind_check
    CHECK (kind = ANY (ARRAY['stub'::text, 'sendgrid'::text, 'ses'::text, 'twilio'::text,
                             'meta_whatsapp'::text, 'twilio_whatsapp'::text]))$c$;
  EXECUTE 'ALTER TABLE public.notification_providers DROP CONSTRAINT IF EXISTS notification_providers_whatsapp_kind_check';
  EXECUTE $c$ALTER TABLE public.notification_providers ADD CONSTRAINT notification_providers_whatsapp_kind_check
    CHECK ((channel = 'whatsapp') = (kind = ANY (ARRAY['meta_whatsapp'::text, 'twilio_whatsapp'::text]))
           OR kind = 'stub')$c$;

  EXECUTE 'ALTER TABLE public.auth_otps DROP CONSTRAINT IF EXISTS auth_otps_channel_check';
  EXECUTE $c$ALTER TABLE public.auth_otps ADD CONSTRAINT auth_otps_channel_check
    CHECK (channel = ANY (ARRAY['email'::text, 'sms'::text, 'whatsapp'::text]))$c$;

  RAISE NOTICE 'WhatsApp channel enabled on public.notification_providers and public.auth_otps';
END $extend$;
COMMIT;
