-- Lifecycle + audit for the DEDICATED assignment-signing key(s).
-- Assignment documents are signed with their OWN Ed25519 key — never the license,
-- command, update, CA or auth-callout key. Rotation: register the new key (active),
-- distribute the appliance trust registry, switch signing, then retire the old key.
CREATE TABLE IF NOT EXISTS assignment_signing_keys (
    key_id       text PRIMARY KEY,
    public_key   bytea NOT NULL,
    state        text  NOT NULL CHECK (state IN ('active','retired')) DEFAULT 'active',
    activated_at timestamptz NOT NULL DEFAULT now(),
    retired_at   timestamptz,
    retired_by   uuid,
    reason       text,
    note         text
);
