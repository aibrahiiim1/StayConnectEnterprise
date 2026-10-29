-- edge-migrate: no-transaction
-- 0176 -- AN INDEX ON accounting_records(site, ingested_at) FOR THE USAGE ROLLUP AND THE RAW PURGE (audit AD-48, AD-62 (a))
--
-- The rollup reads raw samples by a watermark on ingested_at and the purge removes rolled-up samples by the same column;
-- without an index each is a sequential scan of the biggest table on the appliance. It is built CONCURRENTLY (no write
-- lock on the table every accounting pass inserts into), which PostgreSQL refuses inside a transaction: this file is
-- run by scripts/edge-migrate.sh in no-transaction mode, with the ledger row as a separate step after the statement.
-- Idempotent: IF NOT EXISTS, so a run that stopped before its ledger row is simply repeated. A failed concurrent build
-- leaves an INVALID index; drop it (DROP INDEX CONCURRENTLY) and re-run.

CREATE INDEX CONCURRENTLY IF NOT EXISTS accounting_records_site_ingested_idx
    ON iam_v2.accounting_records (tenant_id, site_id, ingested_at);
