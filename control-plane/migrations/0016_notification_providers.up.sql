-- Phase 8.1 — per-tenant email/SMS provider config.
--
-- Mirrors the pms_providers shape (Tier-2 per-tenant config). At most one
-- enabled row per (tenant, channel); scd picks it on boot, falls back to
-- the in-process Stub when no row exists.

CREATE TABLE IF NOT EXISTS notification_providers (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    channel      text NOT NULL CHECK (channel IN ('email','sms')),
    kind         text NOT NULL CHECK (kind IN ('stub','sendgrid','ses','twilio')),
    enabled      boolean NOT NULL DEFAULT true,
    display_name text,

    -- Connection — populated per `kind`. Secrets (api_key, auth_token) are
    -- write-only on the API surface (never returned in GET responses).
    api_key       text,                       -- SendGrid bearer / Twilio auth_token
    api_user      text,                       -- Twilio account_sid (basic-auth user)
    from_address  text,                       -- "noreply@hotel.com" / E.164 sender
    from_name     text,                       -- display name on outgoing email
    region        text,                       -- SES region etc.
    extra         jsonb NOT NULL DEFAULT '{}'::jsonb,

    -- Health snapshot — written by scd's send loop so admins can see
    -- "last successful send" without spelunking logs.
    last_success_at timestamptz,
    last_error      text,
    last_error_at   timestamptz,

    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);

-- One enabled provider per (tenant, channel). Disabled rows can stack
-- (useful for "keep old creds around for rollback").
CREATE UNIQUE INDEX IF NOT EXISTS notification_providers_tenant_channel_enabled_idx
    ON notification_providers(tenant_id, channel)
 WHERE enabled = true;

INSERT INTO schema_migrations(version) VALUES ('0016_notification_providers') ON CONFLICT DO NOTHING;
