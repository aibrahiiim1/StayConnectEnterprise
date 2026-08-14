-- 0016 — Phase 4: payment/settlement coherence. Additive, reversible, DARK. No data.
-- Authorization: D18 / T0029 (unchanged). Receipt: T0037. 0011-0015 and T0030-T0036 preserved unchanged.
--
-- Four corrections to 0015, each of which 0015 got closer to but did not finish.
--
--   (1) THE CHARGE MACHINE WAS STILL WIDER THAN THE CONTRACT. §16 is
--       CREATED -> PENDING -> CAPTURED | FAILED | EXPIRED | CANCELLED | UNKNOWN. 0015 still allowed
--       CREATED -> FAILED | CANCELLED | UNKNOWN directly, so an intent could reach a terminal state
--       without ever having been attempted -- the same shape of widening 0015 removed from the SETTLEMENT
--       machine while leaving it in the payment machine.
--
--   (2) SETTLEMENT ADMISSION AND PAYMENT ADMISSION DISAGREED. ptx_one_live_charge_per_settlement only
--       excludes a LIVE charge, so a settlement that had already reached terminal FAILED could accept a
--       fresh CREATED charge. That charge could later be CAPTURED, and the settlement machine has no
--       FAILED -> SETTLED edge -- leaving captured money whose settlement says it failed.
--
--   (3) REQUIRED -> IN_PROGRESS HAPPENED INSIDE THE CALLBACK. That is after the provider has already been
--       asked for money. The transition belongs at the durable execution boundary: the moment the intent
--       becomes executable, before any external side effect exists.
--
--   (4) A CONFLICTING PROVIDER REFERENCE WAS SILENTLY IGNORED. apply_payment_callback_v2 wrote
--       provider_txn_ref only when it was NULL, so a later callback carrying a DIFFERENT provider
--       reference for the same intent was accepted and its conflict discarded. Two provider transactions
--       claiming one intent is exactly the ambiguity that must fail closed.
BEGIN;

-- ============================================================================
-- (1) The CHARGE machine, exactly as §16 defines it.
-- ============================================================================
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

  -- (4) write-once, and a CONFLICT is an error rather than a silent discard.
  IF OLD.provider_txn_ref IS NOT NULL AND NEW.provider_txn_ref IS DISTINCT FROM OLD.provider_txn_ref THEN
    RAISE EXCEPTION 'PAYMENT_EXTERNAL_REF_CONFLICT: this intent is already pinned to provider reference '
                    '%; a different reference (%) means two provider transactions claim one intent',
      OLD.provider_txn_ref, coalesce(NEW.provider_txn_ref, '<null>') USING ERRCODE = 'check_violation';
  END IF;

  IF NEW.status = OLD.status THEN
    RETURN NEW;
  END IF;
  IF OLD.status IN ('CAPTURED','FAILED','EXPIRED','CANCELLED','UNKNOWN') THEN
    RAISE EXCEPTION 'PAYMENT_STATUS_TERMINAL: % is terminal (attempted % -> %)',
      OLD.status, OLD.status, NEW.status USING ERRCODE = 'check_violation';
  END IF;
  -- §16: the ONLY edge out of CREATED is PENDING. An intent that was never attempted cannot be terminal.
  IF OLD.status = 'CREATED' AND NEW.status <> 'PENDING' THEN
    RAISE EXCEPTION 'PAYMENT_STATUS_TRANSITION: CREATED -> % is not an approved transition; the only edge '
                    'out of CREATED is PENDING (section 16)', NEW.status USING ERRCODE = 'check_violation';
  END IF;
  IF OLD.status = 'PENDING' AND NEW.status NOT IN
     ('CAPTURED','FAILED','EXPIRED','CANCELLED','UNKNOWN') THEN
    RAISE EXCEPTION 'PAYMENT_STATUS_TRANSITION: PENDING -> % is not an approved transition', NEW.status
      USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END $fn$;

-- ============================================================================
-- (2) Admission coherence: a terminal Settlement admits no further charge.
-- ============================================================================
-- The live-charge index cannot express this, because the offending settlement has NO live charge -- that is
-- precisely why it looked admissible. This is a trigger check because it is a cross-row rule about the
-- SETTLEMENT's state, and the settlement row is locked here so a concurrent transition cannot slip past.
CREATE OR REPLACE FUNCTION iam_v2.p4_payment_admission_gate() RETURNS trigger
  LANGUAGE plpgsql SET search_path = iam_v2, pg_temp AS $fn$
DECLARE v_status text;
BEGIN
  -- FOR UPDATE: a settlement moving to a terminal state concurrently must not be able to interleave with
  -- a charge being admitted against it.
  SELECT status INTO v_status FROM iam_v2.settlements WHERE id = NEW.settlement_id FOR UPDATE;
  IF v_status IN ('SETTLED','FAILED','PARTIALLY_REVERSED','REVERSED') AND NEW.transaction_type = 'CHARGE' THEN
    RAISE EXCEPTION 'PAYMENT_SETTLEMENT_CLOSED: settlement is %; it cannot admit another charge. A charge '
                    'admitted here could later be CAPTURED while the settlement stays terminal, leaving '
                    'captured money whose settlement says otherwise', v_status
      USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END $fn$;

CREATE TRIGGER p4_payment_admission_gate
  BEFORE INSERT ON iam_v2.payment_transactions
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p4_payment_admission_gate();

-- ============================================================================
-- (3) The durable execution boundary.
-- ============================================================================
-- begin_payment_execution is what a runtime calls at the moment an intent becomes executable -- after the
-- durable intent row exists, BEFORE any provider is contacted. It moves the settlement REQUIRED ->
-- IN_PROGRESS and the intent CREATED -> PENDING in ONE transaction, so a crash between the two is not a
-- reachable state, and a provider that takes money always has a durable local record saying we asked.
--
-- It is idempotent: calling it again on an already-PENDING intent returns ALREADY_EXECUTING rather than
-- failing, so a retry of the caller does not become a second financial decision.
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

-- ============================================================================
-- (3b)(4) The callback, with the execution boundary already crossed.
-- ============================================================================
-- The callback no longer needs to invent IN_PROGRESS: by the time a provider can call back, execution has
-- begun. What it does is apply the outcome and keep the Settlement consistent in the same transaction.
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

  SELECT * INTO tx FROM iam_v2.payment_transactions
   WHERE tenant_id = p_tenant AND provider = p_provider AND merchant_account_id = p_merchant
     AND provider_ref = p_client_ref
   FOR UPDATE;
  IF tx.id IS NULL THEN
    RAISE EXCEPTION 'CALLBACK_UNCORRELATED: no payment intent matches this provider/merchant/client '
                    'reference; the callback is not applied' USING ERRCODE = 'no_data_found';
  END IF;

  -- (4) A conflicting provider reference is an AMBIGUITY, not a detail to discard. Raise before anything
  -- is recorded, so the conflict cannot be buried in an applied event.
  IF p_provider_txn_ref IS NOT NULL AND tx.provider_txn_ref IS NOT NULL
     AND p_provider_txn_ref <> tx.provider_txn_ref THEN
    RAISE EXCEPTION 'PAYMENT_EXTERNAL_REF_CONFLICT: this intent is pinned to provider reference %; the '
                    'callback carries %. Two provider transactions cannot claim one intent',
      tx.provider_txn_ref, p_provider_txn_ref USING ERRCODE = 'check_violation';
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
    RETURN 'DUPLICATE';
  END;

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
      UPDATE iam_v2.settlements SET status = 'SETTLED' WHERE id = se.id AND status = 'IN_PROGRESS';
    ELSIF p_asserted_status IN ('FAILED','EXPIRED','CANCELLED') THEN
      UPDATE iam_v2.settlements SET status = 'FAILED' WHERE id = se.id AND status = 'IN_PROGRESS';
    ELSIF p_asserted_status = 'UNKNOWN' THEN
      UPDATE iam_v2.settlements SET status = 'MANUAL_REVIEW' WHERE id = se.id AND status = 'IN_PROGRESS';
    END IF;
  ELSIF p_asserted_status = 'CAPTURED' THEN
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

INSERT INTO public.schema_migrations (version) VALUES ('0016_phase4_payment_coherence') ON CONFLICT DO NOTHING;

COMMIT;
