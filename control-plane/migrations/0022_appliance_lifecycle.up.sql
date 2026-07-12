-- Appliance enrollment lifecycle, assignments, clone-detection alerts.
ALTER TABLE appliances ADD COLUMN IF NOT EXISTS lifecycle_state TEXT NOT NULL DEFAULT 'installed_unenrolled'
  CHECK (lifecycle_state IN ('manufactured','installed_unenrolled','pending_enrollment','pending_approval',
    'claimed','assigned','licensed','online','offline','suspended','license_expired','revoked','decommissioned'));
ALTER TABLE appliances ADD COLUMN IF NOT EXISTS hardware_fingerprint TEXT;
ALTER TABLE appliances ADD COLUMN IF NOT EXISTS cert_fingerprint TEXT;
ALTER TABLE appliances ADD COLUMN IF NOT EXISTS first_seen_at TIMESTAMPTZ;
ALTER TABLE appliances ADD COLUMN IF NOT EXISTS last_public_ip TEXT;
ALTER TABLE appliances ADD COLUMN IF NOT EXISTS hostname TEXT;
ALTER TABLE appliances ADD COLUMN IF NOT EXISTS environment TEXT;

CREATE TABLE IF NOT EXISTS appliance_assignments (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  appliance_id UUID NOT NULL REFERENCES appliances(id) ON DELETE CASCADE,
  tenant_id UUID, site_id UUID,
  prev_tenant_id UUID, prev_site_id UUID,
  operator_id UUID, source_ip TEXT, reason TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now());
CREATE INDEX IF NOT EXISTS appliance_assignments_appl_idx ON appliance_assignments(appliance_id);

CREATE TABLE IF NOT EXISTS appliance_lifecycle_events (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  appliance_id UUID NOT NULL REFERENCES appliances(id) ON DELETE CASCADE,
  from_state TEXT, to_state TEXT NOT NULL,
  actor TEXT, source_ip TEXT, reason TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now());
CREATE INDEX IF NOT EXISTS appliance_lifecycle_appl_idx ON appliance_lifecycle_events(appliance_id, created_at DESC);

CREATE TABLE IF NOT EXISTS appliance_security_alerts (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  appliance_id UUID REFERENCES appliances(id) ON DELETE SET NULL,
  serial TEXT, kind TEXT NOT NULL, detail JSONB,
  source_ip TEXT, resolved BOOLEAN NOT NULL DEFAULT false,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now());
CREATE INDEX IF NOT EXISTS appliance_alerts_unresolved_idx ON appliance_security_alerts(resolved, created_at DESC);
