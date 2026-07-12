-- Phase 8: signed, allow-listed, exactly-once command channel.
CREATE TABLE IF NOT EXISTS appliance_commands (
  id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  command_id    UUID NOT NULL UNIQUE,               -- idempotency key (central)
  appliance_id  UUID NOT NULL REFERENCES appliances(id) ON DELETE CASCADE,
  command_type  TEXT NOT NULL,
  params        JSONB NOT NULL DEFAULT '{}',
  issued_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  expires_at    TIMESTAMPTZ NOT NULL,
  signer_key_id TEXT NOT NULL,
  signature     TEXT NOT NULL,
  status        TEXT NOT NULL DEFAULT 'pending'
                  CHECK (status IN ('pending','delivered','acknowledged','running','succeeded','failed','expired','rejected','cancelled')),
  result        JSONB,
  issued_by     UUID,
  reason        TEXT,
  maintenance_window JSONB,
  delivered_at   TIMESTAMPTZ,
  acknowledged_at TIMESTAMPTZ,
  completed_at   TIMESTAMPTZ);
CREATE INDEX IF NOT EXISTS appliance_commands_appl_idx ON appliance_commands(appliance_id, issued_at DESC);
CREATE INDEX IF NOT EXISTS appliance_commands_status_idx ON appliance_commands(status);
