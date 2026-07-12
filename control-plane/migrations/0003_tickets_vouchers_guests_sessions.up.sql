-- Ticket templates, vouchers, guests, sessions, accounting hypertable

CREATE TABLE IF NOT EXISTS ticket_templates (
    id                        uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id                 uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    code                      text NOT NULL,
    name                      text NOT NULL,
    description               text,
    duration_seconds          int,                     -- NULL = unlimited time
    data_cap_bytes            bigint,                  -- NULL = unlimited data
    down_kbps                 int,                     -- NULL = no cap
    up_kbps                   int,
    max_concurrent_devices    int NOT NULL DEFAULT 1,
    validity_seconds          int,                     -- how long a voucher is usable after first auth
    schedule                  jsonb,                   -- e.g. {"daily":{"from":"06:00","to":"22:00"}}
    price_cents               int,
    currency                  text,
    is_active                 boolean NOT NULL DEFAULT true,
    created_at                timestamptz NOT NULL DEFAULT now(),
    updated_at                timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, code)
);

CREATE TABLE IF NOT EXISTS vouchers (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    template_id     uuid NOT NULL REFERENCES ticket_templates(id) ON DELETE RESTRICT,
    code            text NOT NULL,
    batch_id        uuid,
    state           text NOT NULL DEFAULT 'unused'
                        CHECK (state IN ('unused','active','exhausted','expired','revoked')),
    issued_at       timestamptz NOT NULL DEFAULT now(),
    activated_at    timestamptz,
    expires_at      timestamptz,
    bytes_used      bigint NOT NULL DEFAULT 0,
    seconds_used    int NOT NULL DEFAULT 0,
    metadata        jsonb NOT NULL DEFAULT '{}'::jsonb,
    UNIQUE (tenant_id, code)
);

CREATE INDEX IF NOT EXISTS vouchers_batch_idx ON vouchers(batch_id);
CREATE INDEX IF NOT EXISTS vouchers_state_idx ON vouchers(tenant_id, state);

CREATE TABLE IF NOT EXISTS guests (
    id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id            uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    mac                  macaddr NOT NULL,
    device_fingerprint   text,
    display_name         text,
    email                text,
    phone                text,
    consent_accepted_at  timestamptz,
    consent_version      text,
    last_seen_at         timestamptz,
    created_at           timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, mac)
);

CREATE TABLE IF NOT EXISTS sessions (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id        uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    site_id          uuid NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
    appliance_id     uuid NOT NULL REFERENCES appliances(id) ON DELETE CASCADE,
    guest_id         uuid NOT NULL REFERENCES guests(id) ON DELETE CASCADE,
    voucher_id       uuid REFERENCES vouchers(id) ON DELETE SET NULL,
    ip               inet NOT NULL,
    mac              macaddr NOT NULL,
    started_at       timestamptz NOT NULL DEFAULT now(),
    last_activity_at timestamptz NOT NULL DEFAULT now(),
    ended_at         timestamptz,
    end_reason       text CHECK (end_reason IN ('quota_bytes','quota_time','admin','idle','dhcp_expired','policy')),
    bytes_up         bigint NOT NULL DEFAULT 0,
    bytes_down       bigint NOT NULL DEFAULT 0,
    state            text NOT NULL DEFAULT 'active'
                         CHECK (state IN ('pending','active','suspended','closed'))
);

CREATE INDEX IF NOT EXISTS sessions_tenant_active_idx ON sessions(tenant_id, state) WHERE state = 'active';
CREATE INDEX IF NOT EXISTS sessions_appliance_active_idx ON sessions(appliance_id, state) WHERE state = 'active';

-- Accounting time-series (Timescale hypertable)
CREATE TABLE IF NOT EXISTS accounting_records (
    ts           timestamptz NOT NULL,
    session_id   uuid NOT NULL,
    tenant_id    uuid NOT NULL,
    appliance_id uuid NOT NULL,
    bytes_up     bigint NOT NULL,
    bytes_down   bigint NOT NULL
);

SELECT create_hypertable('accounting_records', 'ts', if_not_exists => TRUE, chunk_time_interval => INTERVAL '1 day');
CREATE INDEX IF NOT EXISTS accounting_records_session_idx ON accounting_records(session_id, ts DESC);
CREATE INDEX IF NOT EXISTS accounting_records_tenant_idx  ON accounting_records(tenant_id, ts DESC);

-- Audit log (immutable, append-only) — also a hypertable for retention
CREATE TABLE IF NOT EXISTS audit_log (
    ts          timestamptz NOT NULL DEFAULT now(),
    tenant_id   uuid,
    actor_type  text NOT NULL CHECK (actor_type IN ('operator','system','appliance','guest','api')),
    actor_id    text,
    action      text NOT NULL,
    target_type text,
    target_id   text,
    ip          inet,
    user_agent  text,
    payload     jsonb NOT NULL DEFAULT '{}'::jsonb
);
SELECT create_hypertable('audit_log', 'ts', if_not_exists => TRUE, chunk_time_interval => INTERVAL '7 days');
CREATE INDEX IF NOT EXISTS audit_log_tenant_idx ON audit_log(tenant_id, ts DESC);

INSERT INTO schema_migrations(version) VALUES ('0003_tickets_vouchers_guests_sessions') ON CONFLICT DO NOTHING;
