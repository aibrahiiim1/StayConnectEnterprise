-- Reverse 0019. The recovery gate triggers go first, then the objects they depend on, then the epoch
-- tables. begin_payment_execution is restored to its 0016 body verbatim -- the version that predates the
-- recovery check -- so the reversal really is the earlier behaviour rather than a rewrite of it.
BEGIN;

DROP TRIGGER IF EXISTS p4_recovery_gate_payments ON iam_v2.payment_transactions;
DROP TRIGGER IF EXISTS p4_recovery_gate_outbox ON iam_v2.posting_outbox;

CREATE OR REPLACE FUNCTION iam_v2.begin_payment_execution(p_txn uuid)
RETURNS text
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE tx record; se record;
BEGIN
  SELECT * INTO tx FROM iam_v2.payment_transactions WHERE id = p_txn FOR UPDATE;
  IF tx.id IS NULL THEN
    RAISE EXCEPTION 'PAYMENT_INTENT_UNKNOWN: %', p_txn USING ERRCODE = 'foreign_key_violation';
  END IF;
  IF tx.status = 'PENDING' THEN
    RETURN 'ALREADY_EXECUTING';
  END IF;
  IF tx.status <> 'CREATED' THEN
    RAISE EXCEPTION 'PAYMENT_NOT_EXECUTABLE: intent is %; only a CREATED intent may begin execution',
      tx.status USING ERRCODE = 'check_violation';
  END IF;

  SELECT * INTO se FROM iam_v2.settlements WHERE id = tx.settlement_id FOR UPDATE;
  IF tx.transaction_type = 'CHARGE' AND se.status = 'REQUIRED' THEN
    UPDATE iam_v2.settlements SET status = 'IN_PROGRESS' WHERE id = se.id;
  ELSIF tx.transaction_type = 'CHARGE' AND se.status <> 'IN_PROGRESS' THEN
    RAISE EXCEPTION 'SETTLEMENT_NOT_EXECUTABLE: settlement is %; a charge executes from REQUIRED or '
                    'IN_PROGRESS only', se.status USING ERRCODE = 'check_violation';
  END IF;

  UPDATE iam_v2.payment_transactions SET status = 'PENDING' WHERE id = p_txn;
  RETURN 'EXECUTING';
END $fn$;
REVOKE EXECUTE ON FUNCTION iam_v2.begin_payment_execution(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION iam_v2.begin_payment_execution(uuid) TO sc_payment_runtime;

DROP VIEW IF EXISTS iam_v2.v_financial_recovery;
DROP FUNCTION IF EXISTS iam_v2.p4_release_financial_recovery(uuid,uuid,uuid,text);
DROP FUNCTION IF EXISTS iam_v2.p4_resolve_recovery_hold(uuid,text,uuid,text);
DROP FUNCTION IF EXISTS iam_v2.p4_declare_financial_recovery(uuid,uuid,uuid,text);
DROP FUNCTION IF EXISTS iam_v2.p4_reconcile_financial_epoch(uuid,uuid,text);
DROP FUNCTION IF EXISTS iam_v2.p4_recovery_gate();
DROP TRIGGER IF EXISTS ao_recovery_holds ON iam_v2.financial_recovery_holds;
DROP FUNCTION IF EXISTS iam_v2.p4_recovery_hold_immutable();
DROP TABLE IF EXISTS iam_v2.financial_recovery_holds;
DROP FUNCTION IF EXISTS iam_v2.p4_financial_recovery_active(uuid,uuid);
DROP TABLE IF EXISTS iam_v2.financial_epochs;

DELETE FROM public.schema_migrations WHERE version = '0019_phase4_financial_recovery';
COMMIT;
