DROP INDEX IF EXISTS vouchers_code_global_uniq;
ALTER TABLE vouchers DROP CONSTRAINT IF EXISTS vouchers_batch_id_fkey;
DROP TABLE IF EXISTS voucher_batches CASCADE;
DELETE FROM schema_migrations WHERE version = '0006_voucher_batches';
