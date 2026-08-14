BEGIN;
DROP INDEX IF EXISTS iam_v2.outbox_backlog_age;
ALTER TABLE iam_v2.posting_outbox DROP COLUMN IF EXISTS enqueued_at;
DELETE FROM public.schema_migrations WHERE version = '0020_phase4_financial_observability';
COMMIT;
