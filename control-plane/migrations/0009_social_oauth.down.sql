DROP INDEX IF EXISTS social_oauth_states_expiry_idx;
DROP TABLE IF EXISTS social_oauth_states;
DELETE FROM schema_migrations WHERE version = '0009_social_oauth';
