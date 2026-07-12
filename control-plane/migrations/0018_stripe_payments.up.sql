-- Phase 12.1 — Stripe payments.
--
-- Three tables:
--   stripe_accounts   — per-tenant Stripe credentials (one enabled row)
--   payments          — a checkout session + its lifecycle, keyed by
--                       stripe_session_id so idempotent retries converge
--   stripe_events     — raw Stripe event IDs we've already processed,
--                       used as an anti-replay / deduplication gate

CREATE TABLE IF NOT EXISTS stripe_accounts (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    enabled         boolean NOT NULL DEFAULT true,
    display_name    text,
    publishable_key text NOT NULL,                 -- pk_live_… or pk_test_…
    secret_key      text NOT NULL,                 -- sk_… (write-only on API)
    webhook_secret  text NOT NULL,                 -- whsec_… for HMAC verify
    -- Where Stripe should send the browser after checkout.
    -- {CHECKOUT_SESSION_ID} is a literal placeholder we substitute at
    -- session-create time so the guest portal can poll by session id.
    success_url     text NOT NULL,
    cancel_url      text NOT NULL,
    last_success_at timestamptz,
    last_error      text,
    last_error_at   timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS stripe_accounts_tenant_enabled_idx
    ON stripe_accounts(tenant_id) WHERE enabled = true;

CREATE TABLE IF NOT EXISTS payments (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id          uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    site_id            uuid REFERENCES sites(id) ON DELETE SET NULL,
    template_id        uuid NOT NULL REFERENCES ticket_templates(id),
    stripe_session_id  text NOT NULL UNIQUE,
    stripe_payment_intent text,
    status             text NOT NULL DEFAULT 'pending'
                       CHECK (status IN ('pending','paid','failed','expired','cancelled')),
    amount_cents       bigint NOT NULL,
    currency           text NOT NULL,
    -- Filled when the webhook lands + voucher issuance succeeds.
    voucher_id         uuid REFERENCES vouchers(id) ON DELETE SET NULL,
    -- Device hints from the guest flow — kept so we can build per-site
    -- dashboards and attribute conversions to a physical site.
    client_ip          inet,
    client_mac         macaddr,
    created_at         timestamptz NOT NULL DEFAULT now(),
    completed_at       timestamptz,
    updated_at         timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS payments_tenant_status_idx
    ON payments(tenant_id, status, created_at DESC);

CREATE TABLE IF NOT EXISTS stripe_events (
    -- Stripe event id is globally unique (evt_…) — use it as PK so
    -- INSERT … ON CONFLICT DO NOTHING is our idempotency gate.
    event_id     text PRIMARY KEY,
    tenant_id    uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    event_type   text NOT NULL,
    received_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS stripe_events_received_at_idx
    ON stripe_events(received_at);

INSERT INTO schema_migrations(version) VALUES ('0018_stripe_payments') ON CONFLICT DO NOTHING;
