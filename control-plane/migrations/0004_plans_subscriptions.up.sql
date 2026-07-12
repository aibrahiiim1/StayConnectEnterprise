-- Plans, PlanLimits, TenantSubscription, TenantLimitOverride,
-- UsageCounter (hypertable), SubscriptionEvent, Invoice/InvoiceLine.

CREATE TABLE IF NOT EXISTS plans (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    code           text NOT NULL UNIQUE,           -- starter|pro|enterprise|custom-<slug>
    name           text NOT NULL,
    description    text,
    billing_cycle  text NOT NULL CHECK (billing_cycle IN ('monthly','yearly')),
    price_cents    int NOT NULL DEFAULT 0,
    currency       text NOT NULL DEFAULT 'USD',
    trial_days     int NOT NULL DEFAULT 0,
    is_public      boolean NOT NULL DEFAULT true,
    is_active      boolean NOT NULL DEFAULT true,
    sort_order     int NOT NULL DEFAULT 0,
    metadata       jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);

-- Plan limits: one row per (plan, key). value_type drives which *_value column is authoritative.
CREATE TABLE IF NOT EXISTS plan_limits (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    plan_id    uuid NOT NULL REFERENCES plans(id) ON DELETE CASCADE,
    key        text NOT NULL,
    value_type text NOT NULL CHECK (value_type IN ('int','bool','string')),
    int_value  bigint,
    bool_value boolean,
    str_value  text,
    unit       text,             -- e.g. 'mbps','gb','devices','rpm','days'
    UNIQUE (plan_id, key),
    CHECK (
        (value_type = 'int'    AND int_value  IS NOT NULL AND bool_value IS NULL AND str_value IS NULL) OR
        (value_type = 'bool'   AND bool_value IS NOT NULL AND int_value  IS NULL AND str_value IS NULL) OR
        (value_type = 'string' AND str_value  IS NOT NULL AND int_value  IS NULL AND bool_value IS NULL)
    )
);

CREATE TABLE IF NOT EXISTS tenant_subscriptions (
    id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id             uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    plan_id               uuid NOT NULL REFERENCES plans(id) ON DELETE RESTRICT,
    status                text NOT NULL
                              CHECK (status IN ('trialing','active','past_due','canceled','paused')),
    billing_cycle         text NOT NULL CHECK (billing_cycle IN ('monthly','yearly')),
    current_period_start  timestamptz NOT NULL,
    current_period_end    timestamptz NOT NULL,
    trial_end             timestamptz,
    cancel_at_period_end  boolean NOT NULL DEFAULT false,
    canceled_at           timestamptz,
    ended_at              timestamptz,
    external_ref          text,                     -- Stripe sub id / PMS contract id
    metadata              jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at            timestamptz NOT NULL DEFAULT now(),
    updated_at            timestamptz NOT NULL DEFAULT now(),
    CHECK (current_period_end > current_period_start)
);

-- Only one non-terminal subscription per tenant at a time.
CREATE UNIQUE INDEX IF NOT EXISTS tenant_subscriptions_one_active
    ON tenant_subscriptions(tenant_id)
    WHERE status IN ('trialing','active','past_due','paused');

CREATE INDEX IF NOT EXISTS tenant_subscriptions_tenant_idx ON tenant_subscriptions(tenant_id, status);
CREATE INDEX IF NOT EXISTS tenant_subscriptions_period_end_idx ON tenant_subscriptions(current_period_end)
    WHERE status IN ('trialing','active','past_due');

CREATE TABLE IF NOT EXISTS tenant_limit_overrides (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id  uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    key        text NOT NULL,
    value_type text NOT NULL CHECK (value_type IN ('int','bool','string')),
    int_value  bigint,
    bool_value boolean,
    str_value  text,
    reason     text,
    expires_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    created_by uuid REFERENCES operators(id),
    UNIQUE (tenant_id, key),
    CHECK (
        (value_type = 'int'    AND int_value  IS NOT NULL AND bool_value IS NULL AND str_value IS NULL) OR
        (value_type = 'bool'   AND bool_value IS NOT NULL AND int_value  IS NULL AND str_value IS NULL) OR
        (value_type = 'string' AND str_value  IS NOT NULL AND int_value  IS NULL AND bool_value IS NULL)
    )
);

-- Effective limits = plan_limits merged with tenant_limit_overrides (override wins if not expired).
CREATE OR REPLACE VIEW tenant_effective_limits AS
WITH sub AS (
    SELECT ts.tenant_id, ts.plan_id
      FROM tenant_subscriptions ts
     WHERE ts.status IN ('trialing','active','past_due','paused')
),
plan_vals AS (
    SELECT s.tenant_id, pl.key, pl.value_type, pl.int_value, pl.bool_value, pl.str_value, pl.unit,
           'plan'::text AS source
      FROM sub s
      JOIN plan_limits pl ON pl.plan_id = s.plan_id
),
ovr_vals AS (
    SELECT o.tenant_id, o.key, o.value_type, o.int_value, o.bool_value, o.str_value, NULL::text AS unit,
           'override'::text AS source
      FROM tenant_limit_overrides o
     WHERE o.expires_at IS NULL OR o.expires_at > now()
),
merged AS (
    SELECT * FROM ovr_vals
    UNION ALL
    SELECT pv.*
      FROM plan_vals pv
      LEFT JOIN ovr_vals ov
        ON ov.tenant_id = pv.tenant_id AND ov.key = pv.key
     WHERE ov.tenant_id IS NULL
)
SELECT * FROM merged;

-- Usage counters (rolled up by period) — Timescale hypertable.
CREATE TABLE IF NOT EXISTS usage_counters (
    ts           timestamptz NOT NULL,
    tenant_id    uuid NOT NULL,
    key          text NOT NULL,               -- matches plan_limits.key
    period_start timestamptz NOT NULL,
    period_end   timestamptz NOT NULL,
    value        bigint NOT NULL
);
SELECT create_hypertable('usage_counters', 'ts', if_not_exists => TRUE, chunk_time_interval => INTERVAL '7 days');
CREATE INDEX IF NOT EXISTS usage_counters_tenant_key_idx ON usage_counters(tenant_id, key, ts DESC);

CREATE TABLE IF NOT EXISTS subscription_events (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    subscription_id uuid REFERENCES tenant_subscriptions(id) ON DELETE SET NULL,
    type         text NOT NULL,               -- created|plan_changed|renewed|canceled|paused|resumed|dunning|trial_ended|limit_breached
    from_plan_id uuid REFERENCES plans(id),
    to_plan_id   uuid REFERENCES plans(id),
    at           timestamptz NOT NULL DEFAULT now(),
    actor_type   text CHECK (actor_type IN ('operator','system','billing','api')),
    actor_id     text,
    payload      jsonb NOT NULL DEFAULT '{}'::jsonb
);
CREATE INDEX IF NOT EXISTS subscription_events_tenant_idx ON subscription_events(tenant_id, at DESC);

CREATE TABLE IF NOT EXISTS invoices (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    subscription_id uuid REFERENCES tenant_subscriptions(id) ON DELETE SET NULL,
    external_ref  text,                        -- Stripe invoice id
    number        text,                        -- human-readable invoice number
    status        text NOT NULL CHECK (status IN ('draft','open','paid','void','uncollectible')),
    currency      text NOT NULL,
    subtotal_cents int NOT NULL DEFAULT 0,
    tax_cents      int NOT NULL DEFAULT 0,
    total_cents    int NOT NULL DEFAULT 0,
    period_start  timestamptz,
    period_end    timestamptz,
    issued_at     timestamptz NOT NULL DEFAULT now(),
    due_at        timestamptz,
    paid_at       timestamptz,
    metadata      jsonb NOT NULL DEFAULT '{}'::jsonb
);
CREATE INDEX IF NOT EXISTS invoices_tenant_idx ON invoices(tenant_id, issued_at DESC);

CREATE TABLE IF NOT EXISTS invoice_lines (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    invoice_id  uuid NOT NULL REFERENCES invoices(id) ON DELETE CASCADE,
    description text NOT NULL,
    quantity    int NOT NULL DEFAULT 1,
    unit_cents  int NOT NULL DEFAULT 0,
    amount_cents int NOT NULL DEFAULT 0,
    metadata    jsonb NOT NULL DEFAULT '{}'::jsonb
);

INSERT INTO schema_migrations(version) VALUES ('0004_plans_subscriptions') ON CONFLICT DO NOTHING;
