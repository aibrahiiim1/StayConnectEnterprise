-- Phase 9: signed software update assignments + status.
CREATE TABLE IF NOT EXISTS appliance_update_assignments (
  update_id    UUID PRIMARY KEY,
  appliance_id UUID NOT NULL REFERENCES appliances(id) ON DELETE CASCADE,
  component    TEXT NOT NULL,
  version      TEXT NOT NULL,
  sha256       TEXT NOT NULL,
  status       TEXT NOT NULL DEFAULT 'assigned'
                 CHECK (status IN ('assigned','delivered','downloading','installing','succeeded','failed','rolled_back','rejected')),
  result       JSONB,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  completed_at TIMESTAMPTZ);
CREATE INDEX IF NOT EXISTS appliance_update_appl_idx ON appliance_update_assignments(appliance_id, created_at DESC);
