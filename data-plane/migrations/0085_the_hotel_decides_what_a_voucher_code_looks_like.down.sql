-- Removing the per-site voucher code format setting.
--
-- WHAT THIS COSTS. The Hotel Admin screen that chooses between digits-only and mixed codes, and their
-- length, stops having anywhere to save to. The issuance path is written to require the reader function, so
-- after this migration issuance refuses rather than silently reverting to a hardcoded format -- a deliberate
-- choice, because a voucher printed in a format nobody selected is worse than a voucher not printed.
--
-- WHAT IT DOES NOT COST. No voucher is touched. Codes already issued keep working: redemption matches a
-- stored blind index and has never consulted this setting. The format history is dropped with its table, so
-- take a copy first if the record is wanted -- which is why this direction exists at all rather than being
-- refused.
--
-- ORDER MATTERS. The functions are dropped before the tables they read, and the trigger with its table.

BEGIN;

DROP FUNCTION IF EXISTS iam_v2.voucher_code_settings_set(uuid,uuid,text,integer,text,text);
DROP FUNCTION IF EXISTS iam_v2.voucher_code_settings_get(uuid,uuid);

DROP TRIGGER IF EXISTS voucher_code_settings_changes_append_only
  ON iam_v2.voucher_code_settings_changes;
DROP TABLE IF EXISTS iam_v2.voucher_code_settings_changes;
DROP FUNCTION IF EXISTS iam_v2.voucher_code_settings_changes_append_only();

DROP TABLE IF EXISTS iam_v2.site_voucher_code_settings;

COMMIT;
