-- Phase 9.1 — per-tenant social OAuth provider config.
--
-- One enabled row per (tenant, provider). When no row exists for a
-- provider (e.g. "google"), scd registers the in-process Stub so dev
-- environments work without real OAuth credentials.

CREATE TABLE IF NOT EXISTS social_oauth_providers (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    provider      text NOT NULL CHECK (provider IN ('google','apple','facebook','microsoft')),
    enabled       boolean NOT NULL DEFAULT true,
    display_name  text,

    -- OAuth client config. Secret is write-only on the API surface.
    client_id     text NOT NULL,
    client_secret text NOT NULL,

    -- Authorize/redirect — the public URL the provider sends the browser
    -- back to (typically https://portal.<site>/auth/social/callback). The
    -- portald handler accepts the redirect and forwards the code to scd.
    redirect_uri  text NOT NULL,

    -- Optional scope override; empty = provider's default minimal set.
    scopes        text,

    extra         jsonb NOT NULL DEFAULT '{}'::jsonb,

    -- Health snapshot (mirrors notification_providers shape).
    last_success_at timestamptz,
    last_error      text,
    last_error_at   timestamptz,

    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS social_oauth_providers_tenant_provider_enabled_idx
    ON social_oauth_providers(tenant_id, provider)
 WHERE enabled = true;

INSERT INTO schema_migrations(version) VALUES ('0017_social_oauth_providers') ON CONFLICT DO NOTHING;
