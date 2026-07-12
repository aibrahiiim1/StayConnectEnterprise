-- Phase 6.1 — explicit session expiry.
--
-- Until now `sessions.duration_seconds` lived only on the originating
-- ticket_template, so any non-voucher path (OTP, PMS, social) had no
-- recoverable record of when the session should end. acctd enforced
-- expiry only for voucher-backed sessions, and scd's boot reconcile
-- (5.5) had to fall back to a hardcoded 1-hour TTL.
--
-- Now expires_at is computed at session start and persisted. NULL means
-- "no time limit" (some tenants run open guest networks).

ALTER TABLE sessions
    ADD COLUMN IF NOT EXISTS expires_at timestamptz;

-- Backfill active rows so the reaper doesn't immediately close them.
-- One hour from started_at matches the prior reconcile-fallback semantics.
UPDATE sessions
   SET expires_at = started_at + interval '1 hour'
 WHERE expires_at IS NULL
   AND state = 'active';

-- The reaper scans by (state, expires_at) — a partial index keeps that
-- cheap even at high session counts.
CREATE INDEX IF NOT EXISTS sessions_active_expires_idx
    ON sessions(expires_at)
 WHERE state = 'active';

-- And by (state, last_activity_at) for the idle path. We index NULLS LAST
-- so freshly-active rows with no traffic yet sort to the end naturally.
CREATE INDEX IF NOT EXISTS sessions_active_idle_idx
    ON sessions(last_activity_at NULLS LAST)
 WHERE state = 'active';

INSERT INTO schema_migrations(version) VALUES ('0015_sessions_expires_at') ON CONFLICT DO NOTHING;
