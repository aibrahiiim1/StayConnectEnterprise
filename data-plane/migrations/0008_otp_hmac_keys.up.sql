-- 0008 — keyed-HMAC OTP support (D7). Additive, public-schema.
-- Records OTP HMAC key-generation LIFECYCLE metadata (NO secret material — key bytes live in
-- protected appliance-local files). Adds a generation-pinning column to auth_otps so every newly
-- issued OTP records which key generation verified it; legacy salt:sha256 rows keep NULL and are
-- verified via the time-bounded compatibility path until they expire.

CREATE TABLE IF NOT EXISTS public.otp_hmac_key_generations (
    generation  integer     NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    retired_at  timestamptz,                    -- set when superseded; key retained until pinned OTPs expire
    active      boolean     NOT NULL DEFAULT true,
    note        text,
    CONSTRAINT otp_hmac_key_generations_pk PRIMARY KEY (generation),
    CONSTRAINT otp_hmac_key_generations_gen_chk CHECK (generation >= 1)
);
-- exactly one active generation at a time
CREATE UNIQUE INDEX IF NOT EXISTS otp_hmac_key_generations_one_active
    ON public.otp_hmac_key_generations ((active)) WHERE active;

COMMENT ON TABLE public.otp_hmac_key_generations IS
    'OTP HMAC key-generation lifecycle (D7). No secret material — key bytes are appliance-local, 0600.';

-- Pin the key generation used for each new OTP (NULL = legacy salt:sha256, compatibility window only).
ALTER TABLE public.auth_otps ADD COLUMN IF NOT EXISTS otp_key_generation integer
    REFERENCES public.otp_hmac_key_generations(generation);

INSERT INTO public.schema_migrations (version) VALUES ('0008_otp_hmac_keys')
    ON CONFLICT (version) DO NOTHING;
