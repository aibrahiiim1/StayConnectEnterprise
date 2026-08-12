-- 0016 DOWN — reverse of 0016_phase4_payment_coherence.up.sql. Reverses ONLY 0016.
--
-- Restores the 0015 bodies verbatim, so a rollback leaves 0015 exactly as 0015 built it rather than a
-- half-corrected hybrid. The restored posture is WIDER (CREATED may reach a terminal state directly, a
-- terminal settlement may admit a charge, a conflicting provider reference is silently discarded), which is
-- why this is a rollback path and not an operating one.
BEGIN;

DELETE FROM public.schema_migrations WHERE version = '0016_phase4_payment_coherence';

DROP FUNCTION IF EXISTS iam_v2.begin_payment_execution(uuid);
DROP TRIGGER IF EXISTS p4_payment_admission_gate ON iam_v2.payment_transactions;
DROP FUNCTION IF EXISTS iam_v2.p4_payment_admission_gate();

CREATE OR REPLACE FUNCTION iam_v2.p4_payment_status_machine() RETURNS trigger
  LANGUAGE plpgsql SET search_path = iam_v2, pg_temp AS $fn$
BEGIN
  IF TG_OP = 'DELETE' THEN
    RAISE EXCEPTION 'PAYMENT_IMMUTABLE: payment transactions are never deleted'
      USING ERRCODE = 'feature_not_supported';
  END IF;

  IF ROW(NEW.tenant_id, NEW.site_id, NEW.settlement_id, NEW.merchant_account_id, NEW.transaction_type,
         NEW.parent_transaction_id, NEW.provider, NEW.provider_ref, NEW.idempotency_key,
         NEW.amount_minor, NEW.currency, NEW.currency_exponent, NEW.intent_created_at)
     IS DISTINCT FROM
     ROW(OLD.tenant_id, OLD.site_id, OLD.settlement_id, OLD.merchant_account_id, OLD.transaction_type,
         OLD.parent_transaction_id, OLD.provider, OLD.provider_ref, OLD.idempotency_key,
         OLD.amount_minor, OLD.currency, OLD.currency_exponent, OLD.intent_created_at) THEN
    RAISE EXCEPTION 'PAYMENT_IDENTITY_IMMUTABLE: only status and the provider reference may change'
      USING ERRCODE = 'check_violation';
  END IF;

  -- write-once: NULL -> a value is the provider answering; anything else is rewriting history
  IF OLD.provider_txn_ref IS NOT NULL AND NEW.provider_txn_ref IS DISTINCT FROM OLD.provider_txn_ref THEN
    RAISE EXCEPTION 'PAYMENT_EXTERNAL_REF_IMMUTABLE: the provider reference is assigned once'
      USING ERRCODE = 'check_violation';
  END IF;

  IF NEW.status = OLD.status THEN
    RETURN NEW;
  END IF;
  IF OLD.status IN ('CAPTURED','FAILED','EXPIRED','CANCELLED','UNKNOWN') THEN
    RAISE EXCEPTION 'PAYMENT_STATUS_TERMINAL: % is terminal (attempted % -> %)',
      OLD.status, OLD.status, NEW.status USING ERRCODE = 'check_violation';
  END IF;
  IF OLD.status = 'CREATED' AND NEW.status NOT IN ('PENDING','FAILED','CANCELLED','UNKNOWN') THEN
    RAISE EXCEPTION 'PAYMENT_STATUS_TRANSITION: CREATED -> % is not an approved transition', NEW.status
      USING ERRCODE = 'check_violation';
  END IF;
  IF OLD.status = 'PENDING' AND NEW.status NOT IN
     ('CAPTURED','FAILED','EXPIRED','CANCELLED','UNKNOWN') THEN
    RAISE EXCEPTION 'PAYMENT_STATUS_TRANSITION: PENDING -> % is not an approved transition', NEW.status
      USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END $fn$;

CREATE OR REPLACE FUNCTION iam_v2.apply_payment_callback_v2(
  p_tenant uuid, p_provider text, p_merchant uuid, p_client_ref text,
  p_provider_event_id text, p_event_type text, p_asserted_status text,
  p_provider_txn_ref text DEFAULT NULL, p_evidence jsonb DEFAULT NULL)
RETURNS text
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE tx record; se record; v_moves boolean; v_bad text;
        v_captured bigint; v_returned bigint; v_target text;
BEGIN
  IF p_provider_event_id IS NULL OR btrim(p_provider_event_id) = '' THEN
    RAISE EXCEPTION 'CALLBACK_EVENT_ID_REQUIRED: a callback without a provider event id cannot be deduplicated'
      USING ERRCODE = 'check_violation';
  END IF;
  v_bad := iam_v2.p4_callback_evidence_safe(p_evidence);
  IF v_bad IS NOT NULL THEN
    RAISE EXCEPTION 'CALLBACK_EVIDENCE_UNSAFE: %', v_bad USING ERRCODE = 'check_violation';
  END IF;

  -- TRUSTED CORRELATION. The transaction is resolved from the provider's own identity triple; the caller
  -- cannot nominate one.
  SELECT * INTO tx FROM iam_v2.payment_transactions
   WHERE tenant_id = p_tenant AND provider = p_provider AND merchant_account_id = p_merchant
     AND provider_ref = p_client_ref
   FOR UPDATE;
  IF tx.id IS NULL THEN
    RAISE EXCEPTION 'CALLBACK_UNCORRELATED: no payment intent matches this provider/merchant/client '
                    'reference; the callback is not applied' USING ERRCODE = 'no_data_found';
  END IF;

  v_moves := p_asserted_status IS NOT NULL AND p_asserted_status <> tx.status;

  BEGIN
    INSERT INTO iam_v2.payment_transaction_events
      (tenant_id, site_id, payment_transaction_id, provider, merchant_account_id,
       provider_event_id, event_type, asserted_status, detail, applied)
    VALUES (tx.tenant_id, tx.site_id, tx.id, tx.provider, tx.merchant_account_id,
            btrim(p_provider_event_id), p_event_type, p_asserted_status,
            coalesce(p_evidence, '{}'::jsonb), v_moves);
  EXCEPTION WHEN unique_violation THEN
    RETURN 'DUPLICATE';                 -- a replayed webhook changes nothing, ever
  END;

  -- record the provider's own reference the first time it appears
  IF p_provider_txn_ref IS NOT NULL AND tx.provider_txn_ref IS NULL THEN
    UPDATE iam_v2.payment_transactions SET provider_txn_ref = p_provider_txn_ref WHERE id = tx.id;
  END IF;

  IF NOT v_moves THEN
    RETURN 'NOOP';
  END IF;
  UPDATE iam_v2.payment_transactions SET status = p_asserted_status WHERE id = tx.id;

  SELECT * INTO se FROM iam_v2.settlements WHERE id = tx.settlement_id FOR UPDATE;

  IF tx.transaction_type = 'CHARGE' THEN
    IF p_asserted_status = 'CAPTURED' THEN
      IF se.status = 'REQUIRED' THEN
        UPDATE iam_v2.settlements SET status = 'IN_PROGRESS' WHERE id = se.id;
      END IF;
      UPDATE iam_v2.settlements SET status = 'SETTLED' WHERE id = se.id AND status = 'IN_PROGRESS';
    ELSIF p_asserted_status IN ('FAILED','EXPIRED','CANCELLED') THEN
      IF se.status = 'REQUIRED' THEN
        UPDATE iam_v2.settlements SET status = 'IN_PROGRESS' WHERE id = se.id;
      END IF;
      UPDATE iam_v2.settlements SET status = 'FAILED' WHERE id = se.id AND status = 'IN_PROGRESS';
    ELSIF p_asserted_status = 'UNKNOWN' THEN
      IF se.status = 'REQUIRED' THEN
        UPDATE iam_v2.settlements SET status = 'IN_PROGRESS' WHERE id = se.id;
      END IF;
      UPDATE iam_v2.settlements SET status = 'MANUAL_REVIEW' WHERE id = se.id AND status = 'IN_PROGRESS';
    END IF;
  ELSIF p_asserted_status = 'CAPTURED' THEN
    -- (5) A captured refund MOVES the authoritative settlement, in this transaction, from its own
    -- durable arithmetic. Child state and settlement state cannot drift apart because they are written
    -- together.
    SELECT coalesce(sum(amount_minor),0) INTO v_captured FROM iam_v2.payment_transactions
     WHERE settlement_id = tx.settlement_id AND transaction_type = 'CHARGE' AND status = 'CAPTURED';
    SELECT coalesce(sum(amount_minor),0) INTO v_returned FROM iam_v2.payment_transactions
     WHERE settlement_id = tx.settlement_id AND transaction_type IN ('REFUND','CHARGEBACK')
       AND status = 'CAPTURED';
    v_target := CASE WHEN v_returned >= v_captured THEN 'REVERSED' ELSE 'PARTIALLY_REVERSED' END;
    IF se.status IN ('SETTLED','PARTIALLY_REVERSED') AND se.status <> v_target THEN
      UPDATE iam_v2.settlements SET status = v_target WHERE id = se.id;
    END IF;
  END IF;
  RETURN 'APPLIED';
END $fn$;
REVOKE EXECUTE ON FUNCTION
  iam_v2.apply_payment_callback_v2(uuid,text,uuid,text,text,text,text,text,jsonb) FROM PUBLIC;

COMMIT;
