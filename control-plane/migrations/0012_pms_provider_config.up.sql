-- Phase 4.5.5a — Per-tenant PMS provider config (Tier-2).
--
-- Tier-1 (provider-type defaults) lives in code; Tier-3 (per-site overrides)
-- is YAGNI for now and will be added when the first chain customer asks.
--
-- The loader in scd reads enabled rows for its tenant on boot, instantiates
-- the right provider implementation for each `kind`, applies the row's
-- config + field_map + normalization + stay_window, and registers it under
-- `name`. Tenants reference the row via tenants.auth_methods.pms.provider.

CREATE TABLE IF NOT EXISTS pms_providers (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name            text NOT NULL,                 -- DB-row id, e.g. "main-pms" or "stub-dev"
    kind            text NOT NULL CHECK (kind IN ('stub','protel-fias','opera-fias','fidelio-fias','mews','apaleo')),
    enabled         boolean NOT NULL DEFAULT true,
    display_name    text,                          -- shown in admin UI

    -- Connection (only the columns relevant to `kind` are populated; the rest are NULL).
    host            text,                          -- FIAS family
    port            int,
    use_tls         boolean NOT NULL DEFAULT false,
    auth_key        text,                          -- FIAS IfcAuthKey
    base_url        text,                          -- REST family (Mews/Apaleo)
    api_key         text,                          -- REST
    property_id     text,                          -- Mews property / Apaleo accountCode
    extra           jsonb NOT NULL DEFAULT '{}'::jsonb,  -- escape hatch for kinds we haven't formalized

    -- Field mapping: canonical field → PMS-specific path/code.
    -- Each kind ships sane defaults in code; this column overrides per tenant.
    -- Examples:
    --   FIAS:  {"room_number": "RN", "last_name": "GN"}
    --   REST:  {"room_number": "assignedRoom.number", "last_name": "lastName"}
    field_map       jsonb NOT NULL DEFAULT '{}'::jsonb,

    -- Per-tenant input shaping.
    --   {"room_format": "%03d", "name_strip_titles": true, "reservation_case": "upper"}
    normalization   jsonb NOT NULL DEFAULT '{}'::jsonb,

    -- Stay-window policy.
    --   {"early_checkin_minutes": 0, "late_checkout_minutes": 60, "min_remaining_seconds": 60}
    stay_window     jsonb NOT NULL DEFAULT '{}'::jsonb,

    -- Health snapshot — written by scd's connect loop. Phase 4.5.5b syncs
    -- these to the admin UI; for 4.5.5a we just persist them.
    status          text NOT NULL DEFAULT 'idle'
                        CHECK (status IN ('idle','connecting','connected','degraded','down')),
    last_record_at  timestamptz,
    last_error      text,
    last_error_at   timestamptz,

    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, name)
);

CREATE INDEX IF NOT EXISTS pms_providers_tenant_enabled_idx
    ON pms_providers(tenant_id) WHERE enabled = true;

INSERT INTO schema_migrations(version) VALUES ('0012_pms_provider_config') ON CONFLICT DO NOTHING;
