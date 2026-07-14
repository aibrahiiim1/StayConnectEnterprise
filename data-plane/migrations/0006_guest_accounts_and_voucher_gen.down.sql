ALTER TABLE sessions DROP COLUMN IF EXISTS guest_account_id;
DROP TABLE IF EXISTS guest_accounts;
ALTER TABLE voucher_batches DROP COLUMN IF EXISTS exclude_ambiguous;
ALTER TABLE voucher_batches DROP COLUMN IF EXISTS code_prefix;
ALTER TABLE voucher_batches DROP COLUMN IF EXISTS char_mode;
ALTER TABLE voucher_batches DROP COLUMN IF EXISTS code_length;
