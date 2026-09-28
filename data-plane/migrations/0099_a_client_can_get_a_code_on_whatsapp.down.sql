-- Roll back 0099: the WhatsApp channel leaves public.notification_providers and public.auth_otps.
--
-- Refused while a WhatsApp provider or a WhatsApp challenge exists: restoring the original CHECKs would reject
-- those rows, and deleting a configured provider or a challenge is the operator's decision, not a rollback's.
-- Like the up migration, each table is changed only when the applying role owns it; otherwise a NOTICE names
-- what the owner must do.

BEGIN;

DO $wa_providers$
DECLARE v_owner oid;
BEGIN
  SELECT relowner INTO v_owner FROM pg_class WHERE oid = 'public.notification_providers'::regclass;
  IF NOT pg_has_role(current_user, v_owner, 'USAGE') THEN
    RAISE NOTICE 'public.notification_providers is not owned by %; its WhatsApp constraints stay until the table owner restores them', current_user;
    RETURN;
  END IF;
  IF EXISTS (SELECT 1 FROM public.notification_providers
              WHERE channel = 'whatsapp' OR kind IN ('meta_whatsapp', 'twilio_whatsapp')) THEN
    RAISE EXCEPTION 'WHATSAPP_PROVIDERS_EXIST: public.notification_providers still holds a WhatsApp provider; delete it deliberately first'
      USING ERRCODE = 'check_violation';
  END IF;
  EXECUTE 'ALTER TABLE public.notification_providers DROP CONSTRAINT IF EXISTS notification_providers_whatsapp_kind_check';
  EXECUTE 'ALTER TABLE public.notification_providers DROP CONSTRAINT IF EXISTS notification_providers_channel_check';
  EXECUTE $c$ALTER TABLE public.notification_providers ADD CONSTRAINT notification_providers_channel_check
    CHECK (channel = ANY (ARRAY['email'::text, 'sms'::text]))$c$;
  EXECUTE 'ALTER TABLE public.notification_providers DROP CONSTRAINT IF EXISTS notification_providers_kind_check';
  EXECUTE $c$ALTER TABLE public.notification_providers ADD CONSTRAINT notification_providers_kind_check
    CHECK (kind = ANY (ARRAY['stub'::text, 'sendgrid'::text, 'ses'::text, 'twilio'::text]))$c$;
END $wa_providers$;

DO $wa_otps$
DECLARE v_owner oid;
BEGIN
  SELECT relowner INTO v_owner FROM pg_class WHERE oid = 'public.auth_otps'::regclass;
  IF NOT pg_has_role(current_user, v_owner, 'USAGE') THEN
    RAISE NOTICE 'public.auth_otps is not owned by %; its WhatsApp channel stays until the table owner restores it', current_user;
    RETURN;
  END IF;
  IF EXISTS (SELECT 1 FROM public.auth_otps WHERE channel = 'whatsapp') THEN
    RAISE EXCEPTION 'WHATSAPP_CHALLENGES_EXIST: public.auth_otps still holds a WhatsApp challenge; they must be removed deliberately first'
      USING ERRCODE = 'check_violation';
  END IF;
  EXECUTE 'ALTER TABLE public.auth_otps DROP CONSTRAINT IF EXISTS auth_otps_channel_check';
  EXECUTE $c$ALTER TABLE public.auth_otps ADD CONSTRAINT auth_otps_channel_check
    CHECK (channel = ANY (ARRAY['email'::text, 'sms'::text]))$c$;
END $wa_otps$;

COMMIT;
