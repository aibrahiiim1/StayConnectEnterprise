-- 0014 DOWN — reverse of 0014_phase4_payment_settlement.up.sql. Reverses ONLY 0014.
--
-- Rolling back removes the payment/settlement GOVERNANCE and leaves mg7's shape. That is a strictly less
-- safe state, which is why it exists only as a rollback path and not as an operating one. Any recorded
-- callback events are append-only financial history and are dropped with their table; nothing in this
-- milestone has ever been applied outside a disposable database, so there is no history to lose.
BEGIN;

DELETE FROM public.schema_migrations WHERE version = '0014_phase4_payment_settlement';

DROP FUNCTION IF EXISTS iam_v2.apply_payment_callback(uuid,text,text,text,jsonb);

DROP TRIGGER IF EXISTS p4_settlement_state_machine ON iam_v2.settlements;
DROP FUNCTION IF EXISTS iam_v2.p4_settlement_state_machine();

DROP TRIGGER IF EXISTS p4_payment_creation_gate ON iam_v2.payment_transactions;
DROP FUNCTION IF EXISTS iam_v2.p4_payment_creation_gate();

DROP TRIGGER IF EXISTS p4_payment_status_machine ON iam_v2.payment_transactions;
DROP FUNCTION IF EXISTS iam_v2.p4_payment_status_machine();

DROP TRIGGER IF EXISTS ao_ptx_events ON iam_v2.payment_transaction_events;
DROP FUNCTION IF EXISTS iam_v2.trg_reject_update_delete_ptx_events();
DROP INDEX IF EXISTS iam_v2.ptx_events_by_txn;
DROP TABLE IF EXISTS iam_v2.payment_transaction_events;
ALTER TABLE iam_v2.payment_transactions DROP CONSTRAINT IF EXISTS payment_transactions_tsi_key;

COMMIT;
