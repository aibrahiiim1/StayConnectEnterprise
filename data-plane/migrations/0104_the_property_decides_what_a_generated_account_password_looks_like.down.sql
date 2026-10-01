-- Removing the generated-password format setting and guest_access_accounts.created_at.
--
-- WHAT THIS COSTS. The Client accounts "Password format" control has nowhere to save to, and edged refuses to
-- GENERATE a password (it requires the reader function rather than silently reverting to a constant).
-- Operator-typed passwords keep working. The format history is dropped with its table, and the creation
-- times recorded since 0104 are dropped with their column -- take a copy first if either record is wanted.
--
-- WHAT IT DOES NOT COST. No credential is touched: a generated password is stored as a hash like any other.

BEGIN;

DROP FUNCTION IF EXISTS iam_v2.account_password_settings_set(uuid,uuid,text,integer,text,text);
DROP FUNCTION IF EXISTS iam_v2.account_password_settings_get(uuid,uuid);

DROP TRIGGER IF EXISTS account_password_settings_changes_append_only
  ON iam_v2.account_password_settings_changes;
DROP TABLE IF EXISTS iam_v2.account_password_settings_changes;
DROP FUNCTION IF EXISTS iam_v2.account_password_settings_changes_append_only();

DROP TABLE IF EXISTS iam_v2.site_account_password_settings;

DROP INDEX IF EXISTS iam_v2.gaa_site_created;
ALTER TABLE iam_v2.guest_access_accounts DROP COLUMN IF EXISTS created_at;

COMMIT;
