-- Phase 4.1 — Email OTP (and groundwork for SMS in 4.2 / social in 4.3).
--
-- Design notes:
--   * tenants.auth_methods is a jsonb keyed by method name. Each value is
--     {enabled: bool, template_id: uuid|null}. Plans gate which methods may
--     be enabled via feature.auth.* limit keys.
--   * guests gain optional contact identifiers; we keep MAC as the primary
--     correlator for now (option (a) per architectural decision).
--   * auth_otps is a per-attempt audit + verification record. Codes are
--     stored as argon2id hashes; plaintext is never persisted.

-- ---- 1. auth_methods on tenants -------------------------------------------

ALTER TABLE tenants
    ADD COLUMN IF NOT EXISTS auth_methods jsonb NOT NULL DEFAULT
        '{"voucher":{"enabled":true,"template_id":null}}'::jsonb;

-- ---- 2. guest contact identifiers -----------------------------------------

ALTER TABLE guests
    ADD COLUMN IF NOT EXISTS email             text,
    ADD COLUMN IF NOT EXISTS phone             text,
    ADD COLUMN IF NOT EXISTS email_verified_at timestamptz,
    ADD COLUMN IF NOT EXISTS phone_verified_at timestamptz;

CREATE INDEX IF NOT EXISTS guests_email_idx ON guests(tenant_id, lower(email)) WHERE email IS NOT NULL;
CREATE INDEX IF NOT EXISTS guests_phone_idx ON guests(tenant_id, phone)        WHERE phone IS NOT NULL;

-- ---- 3. auth_otps ---------------------------------------------------------

CREATE TABLE IF NOT EXISTS auth_otps (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    appliance_id uuid REFERENCES appliances(id) ON DELETE SET NULL,
    template_id  uuid REFERENCES ticket_templates(id) ON DELETE SET NULL,
    channel      text NOT NULL CHECK (channel IN ('email','sms')),
    destination  text NOT NULL,
    code_hash    text NOT NULL,
    issued_at    timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz NOT NULL,
    attempts     int NOT NULL DEFAULT 0,
    max_attempts int NOT NULL DEFAULT 5,
    consumed_at  timestamptz,
    ip           inet,
    user_agent   text
);

CREATE INDEX IF NOT EXISTS auth_otps_dest_idx
    ON auth_otps(tenant_id, channel, lower(destination), issued_at DESC);

-- For rate-limit lookups (recent issues for a destination)
CREATE INDEX IF NOT EXISTS auth_otps_recent_idx
    ON auth_otps(tenant_id, channel, lower(destination), issued_at)
    WHERE consumed_at IS NULL;

-- ---- 4. plan_limits — auth method gates -----------------------------------

WITH targets AS (
    SELECT id, code, regexp_replace(code, '-(monthly|yearly)$', '') AS tier FROM plans
)
INSERT INTO plan_limits (plan_id, key, value_type, bool_value)
SELECT t.id, x.key, 'bool', x.allowed
FROM targets t
CROSS JOIN LATERAL (
    VALUES
        ('starter',    'feature.auth.voucher',   true),
        ('starter',    'feature.auth.email_otp', true),
        ('starter',    'feature.auth.sms_otp',   false),
        ('starter',    'feature.auth.social',    false),
        ('starter',    'feature.auth.saml',      false),

        ('pro',        'feature.auth.voucher',   true),
        ('pro',        'feature.auth.email_otp', true),
        ('pro',        'feature.auth.sms_otp',   true),
        ('pro',        'feature.auth.social',    true),
        ('pro',        'feature.auth.saml',      false),

        ('enterprise', 'feature.auth.voucher',   true),
        ('enterprise', 'feature.auth.email_otp', true),
        ('enterprise', 'feature.auth.sms_otp',   true),
        ('enterprise', 'feature.auth.social',    true),
        ('enterprise', 'feature.auth.saml',      true)
) AS x(tier, key, allowed)
WHERE t.tier = x.tier
ON CONFLICT (plan_id, key) DO NOTHING;

INSERT INTO schema_migrations(version) VALUES ('0008_email_otp') ON CONFLICT DO NOTHING;
