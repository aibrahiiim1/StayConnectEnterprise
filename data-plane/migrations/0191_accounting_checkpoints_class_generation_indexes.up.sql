-- edge-migrate: no-transaction
-- 0191 -- TWO INDEXES ON accounting_checkpoints FOR THE ADMISSION OF A NEW SESSION (capacity acceptance, 2026-10-30)
--
-- Admitting a session calls three database operations, and two of them read accounting_checkpoints without an index that fits:
--
--   * iam_v2.allocate_class_generation reconciles its counter against "the highest generation pinned by any checkpoint of this tenant and site":
--     max(source_epoch) over the whole table, a sequential scan. With 13,700 checkpoints it took 5.6 ms of a 24 ms call; the table grows by one row
--     per session and is kept for the accounting retention period, so at 300 sign-ins a minute the call grows by about 0.4 ms every minute.
--   * iam_v2.activate_session_enforcement (and register_class_origin) ask "does another session hold this bridge and class minor at this
--     generation or later": a filter on (tenant, site, bridge, class_minor) over the same table.
--
-- The first index makes the maximum a single descending index probe; the second answers the contested-class question from the index.
-- Both are built CONCURRENTLY (no write lock on a table every accounting pass writes to), which PostgreSQL refuses inside a transaction: this file runs
-- in scripts/edge-migrate.sh's no-transaction mode. Idempotent: IF NOT EXISTS. A failed concurrent build leaves an INVALID index; drop it
-- (DROP INDEX CONCURRENTLY) and re-run. Nothing is changed in the data or in any function; the down migration drops the indexes.

CREATE INDEX CONCURRENTLY IF NOT EXISTS accounting_checkpoints_site_epoch_idx
    ON iam_v2.accounting_checkpoints (tenant_id, site_id, source_epoch DESC);

CREATE INDEX CONCURRENTLY IF NOT EXISTS accounting_checkpoints_class_idx
    ON iam_v2.accounting_checkpoints (tenant_id, site_id, bridge, class_minor, source_epoch);
