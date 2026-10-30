-- edge-migrate: no-transaction
-- 0191 down -- drop the two admission indexes.
DROP INDEX CONCURRENTLY IF EXISTS iam_v2.accounting_checkpoints_class_idx;
DROP INDEX CONCURRENTLY IF EXISTS iam_v2.accounting_checkpoints_site_epoch_idx;
