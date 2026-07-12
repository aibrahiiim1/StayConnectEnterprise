DROP INDEX IF EXISTS operators_oidc_sub_uniq;
ALTER TABLE operators
    DROP COLUMN IF EXISTS last_sso_login_at,
    DROP COLUMN IF EXISTS oidc_sub,
    DROP COLUMN IF EXISTS auth_method;

DROP INDEX IF EXISTS auth_oidc_states_expiry_idx;
DROP TABLE IF EXISTS auth_oidc_states;

DROP INDEX IF EXISTS idp_providers_tenant_enabled_idx;
DROP TABLE IF EXISTS idp_providers;

DELETE FROM schema_migrations WHERE version = '0010_operator_sso';
