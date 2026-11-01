-- 0203 down: the secondary storage can no longer be removed and its change history is dropped. The current setting is kept.
BEGIN;

DROP TRIGGER IF EXISTS ledger_destination_record_change ON public.ledger_destination;
DROP FUNCTION IF EXISTS public.ledger_destination_record_change();
DROP FUNCTION IF EXISTS public.ledger_destination_clear(text);
DROP FUNCTION IF EXISTS public.ledger_destination_history(integer);
DROP TABLE IF EXISTS public.ledger_destination_changes;
DROP FUNCTION IF EXISTS public.ledger_destination_changes_append_only();

COMMIT;
