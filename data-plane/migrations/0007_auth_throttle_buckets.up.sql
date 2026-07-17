-- 0007 — durable, local-first authentication throttle (D4). Additive, public-schema.
-- Fixed-window attempt buckets keyed by a NON-REVERSIBLE HMAC of the scope value plus an explicit
-- normalized auth METHOD dimension, so unrelated methods (account/otp/voucher/social) never share a
-- counter for the same IP/device/identity. Atomic increment via INSERT ... ON CONFLICT DO UPDATE.
-- A hard block (blocked_until) applies across EVERY later window for that identity until it expires.
-- Local Postgres only (no Redis / no Central dependency). No raw identity/MAC/IP/username/OTP stored.

CREATE TABLE IF NOT EXISTS public.auth_throttle_buckets (
    scope_kind    text        NOT NULL,   -- endpoint | identity | ip | device | method
    scope_key     text        NOT NULL,   -- hex HMAC-SHA256(local_key, scope_kind|value); irreversible
    method        text        NOT NULL DEFAULT '*',  -- account|otp|voucher|social|pms|* (* = shared across methods)
    window_start  timestamptz NOT NULL,   -- fixed-window start (truncated to window length)
    window_len_s  integer     NOT NULL,   -- window length in seconds
    attempt_count integer     NOT NULL DEFAULT 0,
    blocked_until timestamptz,            -- hard-block expiry (applies across all later windows)
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT auth_throttle_buckets_pk PRIMARY KEY (scope_kind, scope_key, method, window_start),
    CONSTRAINT auth_throttle_buckets_scope_kind_chk
        CHECK (scope_kind IN ('endpoint','identity','ip','device','method')),
    CONSTRAINT auth_throttle_buckets_method_chk
        CHECK (method IN ('account','otp','voucher','social','pms','*')),
    CONSTRAINT auth_throttle_buckets_window_len_chk CHECK (window_len_s > 0),
    CONSTRAINT auth_throttle_buckets_count_chk CHECK (attempt_count >= 0),
    CONSTRAINT auth_throttle_buckets_scope_key_hex_chk CHECK (scope_key ~ '^[0-9a-f]{64}$')
);

-- Bounded-retention cleanup driver.
CREATE INDEX IF NOT EXISTS auth_throttle_buckets_expiry
    ON public.auth_throttle_buckets (window_start);
-- Cross-window active-block lookup for a throttle identity (scope_kind+scope_key+method).
CREATE INDEX IF NOT EXISTS auth_throttle_buckets_block
    ON public.auth_throttle_buckets (scope_kind, scope_key, method, blocked_until)
    WHERE blocked_until IS NOT NULL;

COMMENT ON TABLE public.auth_throttle_buckets IS
    'Durable local-first auth throttle (D4). scope_key is an irreversible HMAC; method isolates auth methods; blocked_until applies across windows. No raw identity/IP/MAC/OTP.';

INSERT INTO public.schema_migrations (version) VALUES ('0007_auth_throttle_buckets')
    ON CONFLICT (version) DO NOTHING;
