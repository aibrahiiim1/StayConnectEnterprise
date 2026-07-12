-- Phase 4.3 — Social login (OAuth2 stubbed for now; provider-agnostic).
--
-- social_oauth_states binds a CSRF nonce to the device that initiated the
-- flow (IP + MAC) so a stolen state can't authorise a different device.
-- Single-use: consumed_at is set on first callback exchange; replays fail.
-- Short-lived: 10-min default expiry.

CREATE TABLE IF NOT EXISTS social_oauth_states (
    state         text PRIMARY KEY,
    tenant_id     uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    appliance_id  uuid REFERENCES appliances(id) ON DELETE SET NULL,
    template_id   uuid REFERENCES ticket_templates(id) ON DELETE SET NULL,
    provider      text NOT NULL,
    client_ip     inet,
    client_mac    macaddr,
    redirect_uri  text NOT NULL,
    user_agent    text,
    created_at    timestamptz NOT NULL DEFAULT now(),
    expires_at    timestamptz NOT NULL,
    consumed_at   timestamptz
);

CREATE INDEX IF NOT EXISTS social_oauth_states_expiry_idx
    ON social_oauth_states(expires_at)
    WHERE consumed_at IS NULL;

INSERT INTO schema_migrations(version) VALUES ('0009_social_oauth') ON CONFLICT DO NOTHING;
