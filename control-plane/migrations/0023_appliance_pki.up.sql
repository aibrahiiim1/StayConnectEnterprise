-- Appliance PKI: internal CA versions, CSRs, issued client certificates,
-- revocations and an immutable certificate-event log. The CA PRIVATE key never
-- lives in the database — only the CA certificate (public) is stored/versioned.

CREATE TABLE IF NOT EXISTS appliance_ca_versions (
  version      INT PRIMARY KEY,                 -- monotonically increasing CA generation
  cert_pem     TEXT NOT NULL,                   -- CA certificate (public)
  subject      TEXT NOT NULL,
  key_fingerprint TEXT NOT NULL,                -- sha256 of CA public key
  active       BOOLEAN NOT NULL DEFAULT true,   -- appliances trust all non-retired versions
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  retired_at   TIMESTAMPTZ);

CREATE TABLE IF NOT EXISTS appliance_certificate_requests (
  id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  appliance_id UUID NOT NULL REFERENCES appliances(id) ON DELETE CASCADE,
  csr_pem      TEXT NOT NULL,
  public_key_fingerprint TEXT,
  status       TEXT NOT NULL DEFAULT 'pending'
                 CHECK (status IN ('pending','signed','rejected')),
  requested_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  decided_at   TIMESTAMPTZ,
  decided_by   UUID,
  source_ip    TEXT);
CREATE INDEX IF NOT EXISTS appliance_csr_appl_idx ON appliance_certificate_requests(appliance_id, requested_at DESC);

CREATE TABLE IF NOT EXISTS appliance_certificates (
  id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  appliance_id  UUID NOT NULL REFERENCES appliances(id) ON DELETE CASCADE,
  tenant_id     UUID,
  site_id       UUID,
  serial        TEXT NOT NULL,                  -- appliance serial (convenience)
  cert_serial   TEXT NOT NULL,                  -- X.509 serial number (hex)
  fingerprint_sha256 TEXT NOT NULL UNIQUE,      -- sha256 of DER cert
  public_key_fingerprint TEXT,
  ca_version    INT NOT NULL REFERENCES appliance_ca_versions(version),
  not_before    TIMESTAMPTZ NOT NULL,
  not_after     TIMESTAMPTZ NOT NULL,
  status        TEXT NOT NULL DEFAULT 'active'
                  CHECK (status IN ('active','superseded','revoked','expired')),
  cert_pem      TEXT NOT NULL,
  issued_by     UUID,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  revoked_at    TIMESTAMPTZ,
  revocation_reason TEXT);
CREATE INDEX IF NOT EXISTS appliance_cert_appl_idx ON appliance_certificates(appliance_id, created_at DESC);
-- Exactly one active certificate per appliance.
CREATE UNIQUE INDEX IF NOT EXISTS appliance_cert_one_active
  ON appliance_certificates(appliance_id) WHERE status = 'active';

CREATE TABLE IF NOT EXISTS appliance_certificate_revocations (
  id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  certificate_id UUID NOT NULL REFERENCES appliance_certificates(id) ON DELETE CASCADE,
  appliance_id   UUID,
  fingerprint_sha256 TEXT NOT NULL,
  reason         TEXT,
  revoked_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  revoked_by     UUID);
CREATE INDEX IF NOT EXISTS appliance_cert_revoked_fpr_idx ON appliance_certificate_revocations(fingerprint_sha256);

CREATE TABLE IF NOT EXISTS appliance_certificate_events (
  id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  appliance_id   UUID,
  certificate_id UUID,
  event          TEXT NOT NULL,   -- csr_submitted | issued | delivered | rotated | revoked | expired
  detail         JSONB,
  actor          TEXT,
  source_ip      TEXT,
  created_at     TIMESTAMPTZ NOT NULL DEFAULT now());
CREATE INDEX IF NOT EXISTS appliance_cert_events_idx ON appliance_certificate_events(appliance_id, created_at DESC);

-- Track the cert fingerprint on the appliance row for quick fleet views.
ALTER TABLE appliances ADD COLUMN IF NOT EXISTS current_cert_fingerprint TEXT;
ALTER TABLE appliances ADD COLUMN IF NOT EXISTS cert_not_after TIMESTAMPTZ;
