-- Phase 20 carryover D — structured auto-rollback audit. Widen the action
-- vocabulary to the full network.* event sequence and add a shared revision_id
-- + reason so one apply's events (requested/started/pending/confirmed OR
-- timeout/rollback.started/succeeded|failed) are correlated.
ALTER TABLE system_network_audit DROP CONSTRAINT IF EXISTS system_network_audit_action_check;
ALTER TABLE system_network_audit ADD COLUMN IF NOT EXISTS revision_id UUID;
ALTER TABLE system_network_audit ADD COLUMN IF NOT EXISTS reason TEXT;
ALTER TABLE system_network_audit ADD COLUMN IF NOT EXISTS deadline TIMESTAMPTZ;
CREATE INDEX IF NOT EXISTS system_network_audit_revision_idx ON system_network_audit (revision_id);
