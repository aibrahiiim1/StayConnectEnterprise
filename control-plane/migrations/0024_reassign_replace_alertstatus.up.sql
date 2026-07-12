-- Phase 6 (reassignment/replacement) + Phase 10 (security-alert triage status).

-- Security-alert triage lifecycle (open → investigating → acknowledged →
-- resolved | false_positive). The legacy boolean `resolved` is retained.
ALTER TABLE appliance_security_alerts ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'open'
  CHECK (status IN ('open','investigating','acknowledged','resolved','false_positive'));
ALTER TABLE appliance_security_alerts ADD COLUMN IF NOT EXISTS acknowledged_by UUID;
ALTER TABLE appliance_security_alerts ADD COLUMN IF NOT EXISTS acknowledged_at TIMESTAMPTZ;

-- Replacement workflow linkage between an outgoing and its replacement box.
ALTER TABLE appliances ADD COLUMN IF NOT EXISTS replacement_of UUID;
ALTER TABLE appliances ADD COLUMN IF NOT EXISTS replaced_by UUID;
ALTER TABLE appliances ADD COLUMN IF NOT EXISTS replacement_pending BOOLEAN NOT NULL DEFAULT false;
