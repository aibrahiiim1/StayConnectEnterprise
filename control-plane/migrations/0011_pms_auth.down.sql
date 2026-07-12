DELETE FROM plan_limits WHERE key = 'feature.auth.pms';
DROP INDEX IF EXISTS pms_attempts_ip_idx;
DROP INDEX IF EXISTS pms_attempts_room_idx;
DROP TABLE IF EXISTS pms_attempts;
DELETE FROM schema_migrations WHERE version = '0011_pms_auth';
