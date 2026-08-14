-- Reverse 0023. The restore-event history is dropped with the table it lives in; nothing else records it,
-- and re-creating a partial copy elsewhere would be worse than losing it cleanly on an explicit rollback.
BEGIN;
DROP FUNCTION IF EXISTS iam_v2.p4_current_restore_generation(uuid,uuid);
DROP FUNCTION IF EXISTS iam_v2.p4_reconcile_financial_epoch_v2(uuid,uuid,text,bigint,boolean);
DROP FUNCTION IF EXISTS iam_v2.p4_record_supported_restore(uuid,uuid,bigint,text,timestamptz,text);
DROP TABLE IF EXISTS iam_v2.financial_restore_events;
ALTER TABLE iam_v2.financial_epochs DROP COLUMN IF EXISTS restore_generation;
DELETE FROM public.schema_migrations WHERE version = '0023_phase4_restore_generation';
COMMIT;
