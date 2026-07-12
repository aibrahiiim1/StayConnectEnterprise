DELETE FROM plan_limits WHERE key LIKE 'feature.auth.%';
DROP INDEX IF EXISTS auth_otps_recent_idx;
DROP INDEX IF EXISTS auth_otps_dest_idx;
DROP TABLE IF EXISTS auth_otps;
DROP INDEX IF EXISTS guests_phone_idx;
DROP INDEX IF EXISTS guests_email_idx;
ALTER TABLE guests
    DROP COLUMN IF EXISTS phone_verified_at,
    DROP COLUMN IF EXISTS email_verified_at,
    DROP COLUMN IF EXISTS phone,
    DROP COLUMN IF EXISTS email;
ALTER TABLE tenants DROP COLUMN IF EXISTS auth_methods;
DELETE FROM schema_migrations WHERE version = '0008_email_otp';
