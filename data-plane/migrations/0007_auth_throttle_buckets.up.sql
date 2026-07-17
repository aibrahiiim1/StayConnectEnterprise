-- 0007 — durable, local-first authentication throttle (D4).
-- Additive, public-schema. Fixed-window attempt buckets keyed by a NON-REVERSIBLE HMAC of the scope
-- value, so no raw identity / MAC / IP / username / OTP / credential is ever stored. Atomic increment
-- via INSERT ... ON CONFLICT DO UPDATE. Local Postgres only (no Redis / no Central dependency).

CREATE TABLE IF NOT EXISTS public.auth_throttle_buckets (
    scope_kind    text        NOT NULL,   -- endpoint | identity | ip | device | method
    scope_key     text        NOT NULL,   -- hex HMAC-SHA256(local_key, scope_kind|value); irreversible
    window_start  timestamptz NOT NULL,   -- fixed-window start (truncated to window length)
    window_len_s  integer     NOT NULL,   -- window length in seconds
    attempt_count integer     NOT NULL DEFAULT 0,
    blocked_until timestamptz,            -- hard-block expiry (set once a bucket exceeds its cap)
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT auth_throttle_buckets_pk PRIMARY KEY (scope_kind, scope_key, window_start),
    CONSTRAINT auth_throttle_buckets_scope_kind_chk
        CHECK (scope_kind IN ('endpoint','identity','ip','device','method')),
    CONSTRAINT auth_throttle_buckets_window_len_chk CHECK (window_len_s > 0),
    CONSTRAINT auth_throttle_buckets_count_chk CHECK (attempt_count >= 0),
    CONSTRAINT auth_throttle_buckets_scope_key_hex_chk CHECK (scope_key ~ '^[0-9a-f]{64}$')
);

-- Bounded-retention cleanup driver (delete buckets whose window + a grace has fully expired).
CREATE INDEX IF NOT EXISTS auth_throttle_buckets_expiry
    ON public.auth_throttle_buckets (window_start);
-- Fast active-block lookup.
CREATE INDEX IF NOT EXISTS auth_throttle_buckets_blocked
    ON public.auth_throttle_buckets (blocked_until)
    WHERE blocked_until IS NOT NULL;

COMMENT ON TABLE public.auth_throttle_buckets IS
    'Durable local-first auth throttle (D4). scope_key is an irreversible HMAC; no raw identity/IP/MAC/OTP.';

INSERT INTO public.schema_migrations (version) VALUES ('0007_auth_throttle_buckets')
    ON CONFLICT (version) DO NOTHING;
