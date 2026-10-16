-- REVERSES 0024_reassign_replace_alertstatus: the security-alert triage status and the replacement linkage.

BEGIN;

ALTER TABLE appliances DROP COLUMN IF EXISTS replacement_pending;
ALTER TABLE appliances DROP COLUMN IF EXISTS replaced_by;
ALTER TABLE appliances DROP COLUMN IF EXISTS replacement_of;
ALTER TABLE appliance_security_alerts DROP COLUMN IF EXISTS acknowledged_at;
ALTER TABLE appliance_security_alerts DROP COLUMN IF EXISTS acknowledged_by;
ALTER TABLE appliance_security_alerts DROP COLUMN IF EXISTS status;

COMMIT;
