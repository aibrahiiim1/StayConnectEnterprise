-- 0120 down -- Disable no longer suspends the account's package, and the removed-account guard goes.
--
-- Packages already SUSPENDED by ACCOUNT_DISABLED stay suspended: their transitions are history, and lifting
-- them here would invent an Enable nobody performed. Re-applying the up migration lets Enable resume them.

BEGIN;

DROP TRIGGER IF EXISTS guest_access_account_removed_is_final ON iam_v2.guest_access_accounts;
DROP FUNCTION IF EXISTS iam_v2.guest_access_account_removed_is_final();
DROP FUNCTION IF EXISTS iam_v2.account_entitlements_follow_access(uuid,uuid,uuid);

COMMIT;
