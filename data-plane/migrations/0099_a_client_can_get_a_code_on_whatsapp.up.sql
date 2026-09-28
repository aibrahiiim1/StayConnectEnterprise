-- A CLIENT CAN GET A CODE ON WHATSAPP.
--
-- WhatsApp one-time code is its own identity channel (licence module whatsapp_otp, Sign-in methods switch
-- `whatsapp`). It is not SMS: it has its own provider row and its own provider kinds, and the code travels as a
-- parameter of a provider-approved AUTHENTICATION template, never as free text.
--
-- Two public tables carry the channel name under a CHECK, and both are widened here:
--
--   1. public.notification_providers -- channel 'whatsapp', kinds 'meta_whatsapp' (WhatsApp Business Cloud API)
--      and 'twilio_whatsapp' (Twilio WhatsApp sender). A pairing check keeps WhatsApp kinds on the WhatsApp
--      channel and keeps SMS/email kinds off it ('stub' stays valid on every channel).
--   2. public.auth_otps -- channel 'whatsapp', so a WhatsApp challenge can be stored beside email and SMS ones.
--
-- A MIGRATION ON A LIVE SITE MAY NOT CHANGE PUBLIC-SCHEMA STRUCTURE: scripts/edge-migrate.sh applies it as
-- iam_v2_owner, which owns nothing in public. So each table is changed here only when the applying role owns
-- it -- a factory-clean build and the baseline generator -- and a live appliance applies the same change with
-- the owner-run deploy/scripts/extend-notification-channels-whatsapp.sql. Both paths end with the same
-- constraints. No row is touched and no privilege changes: edged and scd already hold what they use.

BEGIN;

DO $wa_providers$
DECLARE v_owner oid;
BEGIN
  SELECT relowner INTO v_owner FROM pg_class WHERE oid = 'public.notification_providers'::regclass;
  IF NOT pg_has_role(current_user, v_owner, 'USAGE') THEN
    RAISE NOTICE 'public.notification_providers is not owned by %; extend it with deploy/scripts/extend-notification-channels-whatsapp.sql', current_user;
    RETURN;
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
END $wa_providers$;

DO $wa_otps$
DECLARE v_owner oid;
BEGIN
  SELECT relowner INTO v_owner FROM pg_class WHERE oid = 'public.auth_otps'::regclass;
  IF NOT pg_has_role(current_user, v_owner, 'USAGE') THEN
    RAISE NOTICE 'public.auth_otps is not owned by %; extend it with deploy/scripts/extend-notification-channels-whatsapp.sql', current_user;
    RETURN;
  END IF;
  EXECUTE 'ALTER TABLE public.auth_otps DROP CONSTRAINT IF EXISTS auth_otps_channel_check';
  EXECUTE $c$ALTER TABLE public.auth_otps ADD CONSTRAINT auth_otps_channel_check
    CHECK (channel = ANY (ARRAY['email'::text, 'sms'::text, 'whatsapp'::text]))$c$;
END $wa_otps$;

COMMIT;
