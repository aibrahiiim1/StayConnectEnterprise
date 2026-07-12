-- Tenants, sites, appliances, networks, operators, walled-garden rules

CREATE TABLE IF NOT EXISTS tenants (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    slug          text NOT NULL UNIQUE,
    name          text NOT NULL,
    status        text NOT NULL DEFAULT 'active'
                      CHECK (status IN ('active','suspended','archived')),
    contact_email text,
    metadata      jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS sites (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    code        text NOT NULL,
    name        text NOT NULL,
    timezone    text NOT NULL DEFAULT 'UTC',
    country     text,
    metadata    jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, code)
);

CREATE TABLE IF NOT EXISTS appliances (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    site_id       uuid NOT NULL REFERENCES sites(id)    ON DELETE CASCADE,
    serial        text NOT NULL UNIQUE,
    name          text NOT NULL,
    model         text,
    version       text,
    enrolled_at   timestamptz,
    last_seen_at  timestamptz,
    status        text NOT NULL DEFAULT 'pending'
                      CHECK (status IN ('pending','online','offline','retired')),
    public_key    text, -- WireGuard / mTLS pub key
    metadata      jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS appliances_tenant_site_idx ON appliances(tenant_id, site_id);

CREATE TABLE IF NOT EXISTS networks (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    appliance_id uuid NOT NULL REFERENCES appliances(id) ON DELETE CASCADE,
    ssid         text,
    vlan_id      int CHECK (vlan_id IS NULL OR (vlan_id BETWEEN 0 AND 4094)),
    cidr         cidr NOT NULL,
    gateway      inet NOT NULL,
    dhcp_enabled boolean NOT NULL DEFAULT true,
    purpose      text NOT NULL DEFAULT 'guest'
                     CHECK (purpose IN ('guest','staff','mgmt')),
    created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS walled_garden_rules (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    site_id     uuid REFERENCES sites(id) ON DELETE CASCADE,
    kind        text NOT NULL CHECK (kind IN ('domain','cidr','ip')),
    value       text NOT NULL,
    ports       int[],
    description text,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS operators (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     uuid REFERENCES tenants(id) ON DELETE CASCADE, -- NULL = platform operator
    email         text NOT NULL UNIQUE,
    display_name  text,
    password_hash text, -- argon2id; or NULL when SSO-only
    status        text NOT NULL DEFAULT 'active'
                      CHECK (status IN ('active','disabled','invited')),
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS operator_roles (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    operator_id uuid NOT NULL REFERENCES operators(id) ON DELETE CASCADE,
    tenant_id   uuid REFERENCES tenants(id) ON DELETE CASCADE,
    role        text NOT NULL CHECK (role IN ('platform_admin','tenant_admin','tenant_operator','viewer','billing'))
);
CREATE UNIQUE INDEX IF NOT EXISTS operator_roles_uniq
    ON operator_roles (operator_id, COALESCE(tenant_id, '00000000-0000-0000-0000-000000000000'::uuid), role);

INSERT INTO schema_migrations(version) VALUES ('0002_tenants_sites_appliances') ON CONFLICT DO NOTHING;
