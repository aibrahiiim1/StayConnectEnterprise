-- 0008 rollback — remove keyed-HMAC OTP support (additive; safe).
ALTER TABLE public.auth_otps DROP COLUMN IF EXISTS otp_key_generation;
DROP TABLE IF EXISTS public.otp_hmac_key_generations;
DELETE FROM public.schema_migrations WHERE version = '0008_otp_hmac_keys';
