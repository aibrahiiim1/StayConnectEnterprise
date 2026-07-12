DROP INDEX IF EXISTS sessions_active_idle_idx;
DROP INDEX IF EXISTS sessions_active_expires_idx;
ALTER TABLE sessions DROP COLUMN IF EXISTS expires_at;
DELETE FROM schema_migrations WHERE version = '0015_sessions_expires_at';
