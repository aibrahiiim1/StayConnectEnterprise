-- Email accounts: self-service registration, verification and reset (migration 0117; contract
-- docs/architecture/ONEGATE_EMAIL_ACCOUNTS_SELF_SERVICE.md). Idempotent; included by gatep-grants.sql after its
-- revoke, so a Gate-P reconcile reproduces exactly what the migration granted -- a grant only a migration makes
-- would be revoked by the first reconcile, and every registration would then fail with "permission denied".
--
--   svc_scd    creates the self-service account; of an account it writes only the password (reset) and the
--              verification and grace state, never the enabled flag, validity, lockout or package; owns the
--              emailed-link outbox (account_email_tokens); reads the settings through the getter only.
--   svc_edged  reads and writes the settings only through the definer functions (a role that could write the
--              table directly could change a setting without a change row) and reads their history. Its DML on
--              guest_access_accounts is granted in svc-edged-phase2-commerce-grants.sql.
GRANT INSERT ON iam_v2.guest_access_accounts TO svc_scd;
GRANT UPDATE (password_hash, email_verified_at, verification_deadline, verification_reminder_sent_at,
              grace_until, grace_extensions_used) ON iam_v2.guest_access_accounts TO svc_scd;
GRANT SELECT, INSERT, UPDATE ON iam_v2.account_email_tokens TO svc_scd;
GRANT EXECUTE ON FUNCTION iam_v2.self_service_settings_get(uuid,uuid) TO svc_scd;

GRANT EXECUTE ON FUNCTION iam_v2.self_service_settings_get(uuid,uuid) TO svc_edged;
GRANT EXECUTE ON FUNCTION iam_v2.self_service_settings_set(uuid,uuid,boolean,integer,text,integer,integer,integer,integer,integer,integer,text,text) TO svc_edged;
GRANT SELECT ON iam_v2.self_service_settings_changes TO svc_edged;
