-- Phase 7: appliance-specific, single-use, signed offline activation packages.
CREATE TABLE IF NOT EXISTS offline_activation_packages (
  package_id   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  appliance_id UUID NOT NULL REFERENCES appliances(id) ON DELETE CASCADE,
  serial       TEXT,
  tenant_id    UUID,
  site_id      UUID,
  nonce        TEXT NOT NULL UNIQUE,
  signer_key_id TEXT NOT NULL,
  issued_by    UUID,
  issued_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  expires_at   TIMESTAMPTZ NOT NULL,
  consumed_at  TIMESTAMPTZ,
  consumed_by_source_ip TEXT,
  reconciled_at TIMESTAMPTZ);
CREATE INDEX IF NOT EXISTS offline_pkg_appl_idx ON offline_activation_packages(appliance_id, issued_at DESC);
