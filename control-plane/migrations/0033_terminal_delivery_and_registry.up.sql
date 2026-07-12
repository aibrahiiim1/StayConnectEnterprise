-- Two-phase terminal delivery + signed trust registry.

-- Phase-1/Phase-2 state machine for normal revocation/decommission. Credentials
-- (cert + NATS) are pulled only AFTER the appliance acknowledges it adopted the
-- terminal assignment — so a normal retirement never lies about being complete.
CREATE TABLE IF NOT EXISTS appliance_terminal_delivery (
    appliance_id       uuid PRIMARY KEY REFERENCES appliances(id) ON DELETE CASCADE,
    terminal_state     text NOT NULL CHECK (terminal_state IN ('revoked','unassigned','decommissioned')),
    assignment_version bigint NOT NULL,
    delivery_state     text NOT NULL
                          CHECK (delivery_state IN
                             ('terminal_delivery_pending','terminal_adopted',
                              'terminal_delivery_failed','credential_revoked')),
    reason             text,
    emergency          boolean NOT NULL DEFAULT false,
    issued_at          timestamptz NOT NULL DEFAULT now(),
    timeout_at         timestamptz,
    -- signed acknowledgment from the appliance (Phase-1 completion proof)
    acked_at           timestamptz,
    ack_version        bigint,
    ack_fingerprint    text,
    ack_adopted_at     bigint,
    ack_signed         jsonb,
    credential_revoked_at timestamptz
);

-- Last terminal-assignment version the appliance has acknowledged. The strict
-- terminal endpoint only returns a document with a version GREATER than this.
ALTER TABLE appliance_signed_assignments
    ADD COLUMN IF NOT EXISTS last_acked_version bigint NOT NULL DEFAULT 0;

-- Rate-limit / audit ledger for the strict terminal endpoint.
CREATE TABLE IF NOT EXISTS appliance_assignment_fetch_log (
    id           bigserial PRIMARY KEY,
    appliance_id uuid,
    fingerprint  text,
    outcome      text,     -- served | not-modified | denied:<reason>
    at           timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_assignment_fetch_appliance_at
    ON appliance_assignment_fetch_log (appliance_id, at DESC);

-- The current SIGNED trust registry served to appliances, plus its history.
CREATE TABLE IF NOT EXISTS assignment_registry (
    registry_version bigint PRIMARY KEY,
    signed_envelope  jsonb NOT NULL,
    signer_key_id    text NOT NULL,
    issued_at        timestamptz NOT NULL DEFAULT now(),
    not_before       timestamptz,
    not_after        timestamptz,
    is_current       boolean NOT NULL DEFAULT false
);
CREATE UNIQUE INDEX IF NOT EXISTS assignment_registry_one_current
    ON assignment_registry (is_current) WHERE is_current;
