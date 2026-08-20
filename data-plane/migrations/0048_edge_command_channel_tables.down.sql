-- Reversing 0048 does NOT drop these tables. They hold the appliance's executed-command, installed-update
-- and consumed-offline-package ledgers, which are evidence of what was done to the machine; dropping them to
-- undo a schema change would destroy an audit trail to fix a bookkeeping problem.
--
-- Down-migrating simply relinquishes the claim that the migration ledger owns them.
SELECT 1;
