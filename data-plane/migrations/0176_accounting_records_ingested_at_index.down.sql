-- edge-migrate: no-transaction
-- 0176 down -- drop the rollup/purge index.
DROP INDEX CONCURRENTLY IF EXISTS iam_v2.accounting_records_site_ingested_idx;
