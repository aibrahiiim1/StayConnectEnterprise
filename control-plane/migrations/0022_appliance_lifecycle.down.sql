DROP TABLE IF EXISTS appliance_security_alerts;
DROP TABLE IF EXISTS appliance_lifecycle_events;
DROP TABLE IF EXISTS appliance_assignments;
ALTER TABLE appliances DROP COLUMN IF EXISTS lifecycle_state, DROP COLUMN IF EXISTS hardware_fingerprint,
  DROP COLUMN IF EXISTS cert_fingerprint, DROP COLUMN IF EXISTS first_seen_at, DROP COLUMN IF EXISTS last_public_ip,
  DROP COLUMN IF EXISTS hostname, DROP COLUMN IF EXISTS environment;
