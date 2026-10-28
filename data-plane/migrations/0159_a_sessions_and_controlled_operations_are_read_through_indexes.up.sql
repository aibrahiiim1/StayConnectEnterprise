-- 0159 — SESSIONS AND CONTROLLED OPERATIONS ARE READ THROUGH INDEXES (performance audit, 2026-10-24).
--
-- Additive only: four indexes, no data change. Each one serves a query the audit measured scanning the whole table:
--   sessions (entitlement_id)                      D5-H1 / D7-H1: per-entitlement session reads in acctd, the Admin Console
--                                                  and the expiry sweep;
--   sessions (device_id), open sessions only        D2-M2 / D7-M1: the licensed-slot reservation at sign-in and the
--                                                  device list;
--   sessions (tenant_id, site_id, started), live    D1-M1: acctd's per-tick read of live sessions;
--   sessions (tenant_id, site_id, started DESC)     D7: the Admin Console's session lists, newest first;
--   controlled_operation_scope (opened_at)         D1-H1: the controlled-operation janitor.
-- The Admin Console's audit-log index (D7-H1, public.audit_log) is NOT here: a live site applies migrations as
-- iam_v2_owner, which cannot index a public table. It is recorded in docs/AUDIT_BACKLOG.md.
-- A plain CREATE INDEX holds a write lock while it builds; at appliance volumes that is seconds, during an upgrade.

BEGIN;

CREATE INDEX IF NOT EXISTS sessions_entitlement_id ON iam_v2.sessions (entitlement_id);
CREATE INDEX IF NOT EXISTS sessions_open_by_device ON iam_v2.sessions (device_id)
    WHERE ended IS NULL AND state IN ('active', 'PENDING_ENFORCEMENT');
CREATE INDEX IF NOT EXISTS sessions_live_by_site ON iam_v2.sessions (tenant_id, site_id, started)
    WHERE state = 'active' AND ended IS NULL;
CREATE INDEX IF NOT EXISTS sessions_site_started ON iam_v2.sessions (tenant_id, site_id, started DESC);
CREATE INDEX IF NOT EXISTS controlled_operation_scope_opened_at ON iam_v2.controlled_operation_scope (opened_at);

COMMIT;
