-- Phase 5.1 — Appliance identity + enrollment.
--
-- Each appliance carries a persistent Ed25519 keypair. To bind a keypair to
-- an appliance row in this DB, an operator mints a single-use bootstrap
-- token and installs it on the appliance. scd's first boot POSTs
-- {bootstrap_token, serial, public_key} to /v1/appliances/enroll; the server
-- hashes the token, finds the matching row, and binds the key to the
-- appliance (creating the row if the token was pre-minted for an unknown
-- serial).
--
-- Tokens are stored hashed (sha256), never plaintext. Consuming is a one-way
-- flip of consumed_at; revocation pre-consumption is just DELETE.

CREATE TABLE IF NOT EXISTS appliance_bootstrap_tokens (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id      uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    site_id        uuid NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
    -- Expected serial. When set, enrollment MUST present this serial; when
    -- NULL, any serial the operator assigns at enrollment time is accepted.
    expected_serial text,
    token_hash     bytea NOT NULL,      -- sha256(plaintext token), 32 bytes
    token_hint     text NOT NULL,       -- last 4 chars of plaintext for UI display
    created_by     uuid REFERENCES operators(id) ON DELETE SET NULL,
    expires_at     timestamptz NOT NULL,
    consumed_at    timestamptz,
    consumed_by_appliance uuid REFERENCES appliances(id) ON DELETE SET NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    UNIQUE (token_hash)
);
CREATE INDEX IF NOT EXISTS appliance_bootstrap_tokens_tenant_idx
    ON appliance_bootstrap_tokens(tenant_id, consumed_at);

-- Track the appliance's current session / nonce window so we can reject
-- replayed JWTs within the jti cache TTL. A cheap in-process LRU on the
-- control-plane is enough for phase 5.1; this column only records the most
-- recent successful auth so the admin UI can surface it.
ALTER TABLE appliances
    ADD COLUMN IF NOT EXISTS identity_verified_at timestamptz;

-- Extend the status lifecycle: pending (row exists, key not yet bound) →
-- enrolled (key bound, no traffic yet) → online (5.4 heartbeat seen) →
-- offline (missed heartbeats) → retired (operator removed).
ALTER TABLE appliances
    DROP CONSTRAINT IF EXISTS appliances_status_check;
ALTER TABLE appliances
    ADD CONSTRAINT appliances_status_check
    CHECK (status IN ('pending','enrolled','online','offline','retired'));

INSERT INTO schema_migrations(version) VALUES ('0013_appliance_enrollment') ON CONFLICT DO NOTHING;
