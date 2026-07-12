-- Signed appliance-assignment documents. One CURRENT row per appliance holds the
-- latest vendor-signed assignment the appliance should fetch and apply. Version is
-- monotonic per appliance; the edge accepts only a strictly-greater version.
CREATE TABLE IF NOT EXISTS appliance_signed_assignments (
    appliance_id     uuid PRIMARY KEY REFERENCES appliances(id) ON DELETE CASCADE,
    assignment_id    uuid NOT NULL,
    version          bigint NOT NULL,
    tenant_id        uuid,
    site_id          uuid,
    state            text NOT NULL CHECK (state IN ('assigned','unassigned','revoked')),
    identity_key_fpr text,
    signed_doc       jsonb NOT NULL,
    issued_at        timestamptz NOT NULL DEFAULT now(),
    expires_at       timestamptz,
    updated_at       timestamptz NOT NULL DEFAULT now()
);

-- History of every signed assignment issued, for audit + replay defense proofs.
CREATE TABLE IF NOT EXISTS appliance_assignment_history (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    appliance_id  uuid NOT NULL,
    assignment_id uuid NOT NULL,
    version       bigint NOT NULL,
    tenant_id     uuid,
    site_id       uuid,
    state         text NOT NULL,
    signed_doc    jsonb NOT NULL,
    issued_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_assignment_history_appliance ON appliance_assignment_history (appliance_id, version DESC);
