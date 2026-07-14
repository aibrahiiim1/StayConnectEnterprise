-- Phase 20 — appliance WAN/LAN (system) network change audit trail.
-- Every system-network operation (validate/apply/confirm/rollback) records who,
-- from where, the before/after, and every result. Distinct from
-- network_apply_events (guest-network revisions) — this is the appliance's own
-- WAN/Management + LAN base networking.
CREATE TABLE IF NOT EXISTS system_network_audit (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    actor             TEXT        NOT NULL,          -- operator email/id (or 'system')
    actor_id          UUID,                          -- operator id when known
    source_ip         TEXT,                          -- client IP that requested it
    action            TEXT        NOT NULL CHECK (action IN ('validate','apply','confirm','rollback','auto_rollback')),
    target            TEXT        NOT NULL CHECK (target IN ('wan','lan','both')),
    previous_config   JSONB,
    requested_config  JSONB,
    validation_result JSONB,
    apply_result      TEXT,
    confirm_result    TEXT,
    rollback_result   TEXT,
    failure_reason    TEXT,
    backup_path       TEXT,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS system_network_audit_created_idx ON system_network_audit (created_at DESC);
