-- Phase 4.4 — Operator SSO via OIDC.
--
-- Stores per-tenant IdP configuration, the per-flow CSRF/nonce state, and
-- extends operators with sub + auth_method so we can distinguish local vs
-- federated identities and honour future SLO.

-- ---- 1. idp_providers -----------------------------------------------------

CREATE TABLE IF NOT EXISTS idp_providers (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name            text NOT NULL,        -- short slug, e.g. "okta", "azure-ad", "stub"
    display_name    text NOT NULL,        -- shown on the login page
    kind            text NOT NULL CHECK (kind IN ('oidc','saml','stub')),
    enabled         boolean NOT NULL DEFAULT true,
    auto_provision  boolean NOT NULL DEFAULT true,

    -- OIDC config (NULL columns mean "use discovery").
    discovery_url   text,
    authorize_url   text,
    token_url       text,
    userinfo_url    text,
    issuer          text,
    jwks_url        text,
    client_id       text,
    client_secret   text,
    scopes          text[] NOT NULL DEFAULT ARRAY['openid','email','profile'],

    -- Claims mapping (which claim keys to read; defaults to OIDC standards).
    sub_claim       text NOT NULL DEFAULT 'sub',
    email_claim     text NOT NULL DEFAULT 'email',
    name_claim      text NOT NULL DEFAULT 'name',
    groups_claim    text,                 -- e.g. "groups", "roles"

    -- Claims → role mapping. Shape:
    --   {
    --     "default_role":  "tenant_operator",
    --     "groups_to_role": {"sc-admins": "tenant_admin", "sc-billing": "billing"}
    --   }
    claims_map      jsonb NOT NULL DEFAULT '{}'::jsonb,

    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),

    UNIQUE (tenant_id, name)
);

CREATE INDEX IF NOT EXISTS idp_providers_tenant_enabled_idx
    ON idp_providers(tenant_id) WHERE enabled = true;

-- ---- 2. auth_oidc_states (CSRF + replay protection per flow) --------------

CREATE TABLE IF NOT EXISTS auth_oidc_states (
    state         text PRIMARY KEY,
    nonce         text NOT NULL,           -- echoed in id_token; binds session to flow
    tenant_id     uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    provider_id   uuid NOT NULL REFERENCES idp_providers(id) ON DELETE CASCADE,
    redirect_uri  text NOT NULL,
    return_to     text,                    -- where to send the browser after success
    client_ip     inet,
    user_agent    text,
    created_at    timestamptz NOT NULL DEFAULT now(),
    expires_at    timestamptz NOT NULL,
    consumed_at   timestamptz
);

CREATE INDEX IF NOT EXISTS auth_oidc_states_expiry_idx
    ON auth_oidc_states(expires_at) WHERE consumed_at IS NULL;

-- ---- 3. operators: federation columns ------------------------------------

ALTER TABLE operators
    ADD COLUMN IF NOT EXISTS auth_method        text NOT NULL DEFAULT 'local'
        CHECK (auth_method IN ('local','sso')),
    ADD COLUMN IF NOT EXISTS oidc_sub           text,
    ADD COLUMN IF NOT EXISTS last_sso_login_at  timestamptz;

-- One operator per (tenant, sub) so a returning SSO user lands on the same
-- row regardless of email changes upstream.
CREATE UNIQUE INDEX IF NOT EXISTS operators_oidc_sub_uniq
    ON operators(tenant_id, oidc_sub)
    WHERE oidc_sub IS NOT NULL;

INSERT INTO schema_migrations(version) VALUES ('0010_operator_sso') ON CONFLICT DO NOTHING;
