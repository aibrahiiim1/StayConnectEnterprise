-- 0015 — Phase 4: payment/settlement hardening. Additive, reversible, DARK. No data.
-- Authorization: D18 / T0029 (unchanged). Receipt: T0036. 0014 and T0035 are preserved unchanged.
--
-- 0014 established the payment governance and got five things wrong. Each is corrected here additively.
--
--   (1) NEITHER BOUND WAS CONCURRENCY-SAFE. "one live CHARGE per settlement" and the cumulative refund
--       bound were both SELECT-then-decide inside a BEFORE INSERT trigger. Two concurrent transactions each
--       see the pre-state, each pass, and both commit. A count() in a trigger is not a constraint.
--
--   (2) THE SETTLEMENT MACHINE WAS WIDER THAN THE CONTRACT. §16 is
--       REQUIRED -> IN_PROGRESS -> SETTLED | FAILED | MANUAL_REVIEW. 0014 also allowed REQUIRED -> FAILED
--       and REQUIRED -> MANUAL_REVIEW directly, which lets a settlement reach a terminal state without
--       ever having been attempted.
--
--   (3) CALLBACK DETAIL WAS AN ARBITRARY BLOB with a comment asking callers not to abuse it. The Manual
--       Review ledger learned this lesson in 0013/T0034; the payment ledger had not.
--
--   (4) CALLBACK CORRELATION TRUSTED THE CALLER. apply_payment_callback took the INTERNAL transaction id
--       as an argument, so whoever called it decided which money the provider was talking about. And the
--       dedupe key was (payment_transaction_id, provider_event_id), so one provider event could be applied
--       to two different internal transactions by simply naming a different one.
--
--   (5) REFUND CAPTURE DID NOT MOVE THE SETTLEMENT. Child rows and Settlement state were independently
--       writable, so the authoritative status could disagree with its own arithmetic.
--
-- Plus one thing 0014 could not express at all: the DURABLE-BEFORE-SIDE-EFFECT rule. mg7 made provider_ref
-- NOT NULL and 0014 made it immutable, so a row could not exist before the provider had answered -- which
-- is precisely the crash window where a provider takes money and StayConnect has no record of intending it.
BEGIN;

-- ============================================================================
-- (6) Durable local intent, separate from the external reference.
-- ============================================================================
-- provider_ref keeps its mg7 meaning and its uniqueness, but it is now explicitly the LOCAL reference we
-- generate BEFORE any external call: a durable idempotency root that exists whatever happens next. What
-- the provider assigns arrives later and lands in provider_txn_ref, which is nullable at creation and
-- write-once afterwards.
--
-- PROVIDER-CONTRACT ASSUMPTION, stated rather than assumed: this design requires that the provider accepts
-- a client-supplied reference on the charge request and ECHOES IT BACK on every callback about that
-- charge. That capability is common (Stripe client_reference_id/metadata, Adyen merchantReference,
-- Checkout.com reference) but it is NOT universal, and no provider integration in this milestone has been
-- verified against a real endpoint. A provider that cannot echo a client reference would need a different
-- correlation strategy, and adopting one is a separate, evidenced decision.
ALTER TABLE iam_v2.payment_transactions
  ADD COLUMN provider_txn_ref text,
  ADD COLUMN intent_created_at timestamptz NOT NULL DEFAULT now(),
  ADD CONSTRAINT ptx_provider_txn_ref_bounded
    CHECK (provider_txn_ref IS NULL OR (length(provider_txn_ref) BETWEEN 1 AND 200
                                        AND provider_txn_ref !~ '[\x00-\x1f\x7f]')),
  ADD CONSTRAINT ptx_local_ref_bounded
    CHECK (length(provider_ref) BETWEEN 1 AND 200 AND provider_ref !~ '[\x00-\x1f\x7f]');

-- The external reference is unique per provider+merchant once it exists, so two internal transactions can
-- never claim the same provider transaction.
CREATE UNIQUE INDEX ptx_provider_txn_ref_identity
  ON iam_v2.payment_transactions (tenant_id, provider, merchant_account_id, provider_txn_ref)
  WHERE provider_txn_ref IS NOT NULL;

COMMENT ON COLUMN iam_v2.payment_transactions.provider_ref IS
  'LOCAL durable intent reference, generated before any external call and sent to the provider as the '
  'client reference. It is the idempotency root: it exists even if the process dies before the provider '
  'answers.';
COMMENT ON COLUMN iam_v2.payment_transactions.provider_txn_ref IS
  'The reference the PROVIDER assigned. NULL until the provider answers; write-once thereafter.';

-- ============================================================================
-- (1) Concurrency-proof bounds.
-- ============================================================================
-- One live CHARGE per settlement becomes a PARTIAL UNIQUE INDEX. This is the right shape because it is not
-- a check at all -- it is an impossibility. Two concurrent inserts contend on the index and exactly one
-- commits, with no lock the application has to remember to take.
CREATE UNIQUE INDEX ptx_one_live_charge_per_settlement
  ON iam_v2.payment_transactions (settlement_id)
  WHERE transaction_type = 'CHARGE' AND status IN ('CREATED','PENDING','CAPTURED','UNKNOWN');

COMMENT ON INDEX iam_v2.ptx_one_live_charge_per_settlement IS
  'Concurrency-proof replacement for 0014''s SELECT-then-decide duplicate-charge check. A count() inside a '
  'BEFORE INSERT trigger cannot see a concurrent uncommitted sibling; a unique index can.';

-- The cumulative refund bound cannot be an index -- it is a SUM over siblings. It is made safe by taking a
-- transaction-scoped advisory lock on the PARENT before summing, so children of one parent serialize and
-- children of different parents never contend. Namespace 47 is reserved for payment parents (0 stay-events,
-- 7 capacity, 11 device slot, 41 financial review are already taken).
CREATE OR REPLACE FUNCTION iam_v2.ns_payment_parent(p text) RETURNS bigint
  LANGUAGE sql IMMUTABLE AS $$ SELECT hashtextextended(p, 47) $$;

CREATE OR REPLACE FUNCTION iam_v2.p4_payment_creation_gate() RETURNS trigger
  LANGUAGE plpgsql SET search_path = iam_v2, pg_temp AS $fn$
DECLARE se record; pu record; par record; v_refunded bigint;
BEGIN
  IF NEW.currency !~ '^[A-Z]{3}$' THEN
    RAISE EXCEPTION 'PAYMENT_CURRENCY_INVALID: % is not an ISO-4217 code', NEW.currency
      USING ERRCODE = 'check_violation';
  END IF;
  IF NEW.currency_exponent < 0 OR NEW.currency_exponent > 4 THEN
    RAISE EXCEPTION 'PAYMENT_EXPONENT_INVALID: %', NEW.currency_exponent USING ERRCODE = 'check_violation';
  END IF;
  -- A new financial intent starts at CREATED. Inserting one already CAPTURED would skip the whole machine.
  IF NEW.status <> 'CREATED' THEN
    RAISE EXCEPTION 'PAYMENT_MUST_START_CREATED: a payment transaction is created as CREATED, not %',
      NEW.status USING ERRCODE = 'check_violation';
  END IF;
  IF NEW.provider_txn_ref IS NOT NULL THEN
    RAISE EXCEPTION 'PAYMENT_EXTERNAL_REF_TOO_EARLY: provider_txn_ref is assigned by the provider, not at '
                    'creation' USING ERRCODE = 'check_violation';
  END IF;

  SELECT * INTO se FROM iam_v2.settlements
   WHERE tenant_id = NEW.tenant_id AND site_id = NEW.site_id AND id = NEW.settlement_id;
  IF se.id IS NULL THEN
    RAISE EXCEPTION 'PAYMENT_SETTLEMENT_UNKNOWN: settlement % is not in this tenant/site', NEW.settlement_id
      USING ERRCODE = 'foreign_key_violation';
  END IF;
  SELECT * INTO pu FROM iam_v2.purchases
   WHERE tenant_id = NEW.tenant_id AND site_id = NEW.site_id AND id = se.purchase_id;

  IF NEW.transaction_type = 'CHARGE' THEN
    IF se.method <> 'ONLINE_PAYMENT' THEN
      RAISE EXCEPTION 'PAYMENT_WRONG_RAIL: settlement method is %; an online payment charge requires '
                      'ONLINE_PAYMENT', se.method USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.amount_minor IS DISTINCT FROM pu.amount_minor
       OR NEW.currency IS DISTINCT FROM pu.currency
       OR NEW.currency_exponent IS DISTINCT FROM pu.currency_exponent THEN
      RAISE EXCEPTION 'PAYMENT_AMOUNT_NOT_SERVER_PINNED: charge %/%/% <> pinned purchase %/%/%',
        NEW.amount_minor, NEW.currency, NEW.currency_exponent,
        pu.amount_minor, pu.currency, pu.currency_exponent USING ERRCODE = 'check_violation';
    END IF;
    -- the duplicate-charge bound is now ptx_one_live_charge_per_settlement, enforced at COMMIT
    RETURN NEW;
  END IF;

  -- REFUND / CHARGEBACK. Serialize on the parent FIRST, so the sum below cannot miss a concurrent sibling.
  PERFORM pg_advisory_xact_lock(iam_v2.ns_payment_parent(NEW.parent_transaction_id::text));

  SELECT * INTO par FROM iam_v2.payment_transactions WHERE id = NEW.parent_transaction_id;
  IF par.id IS NULL THEN
    RAISE EXCEPTION 'PAYMENT_PARENT_UNKNOWN: %', NEW.parent_transaction_id
      USING ERRCODE = 'foreign_key_violation';
  END IF;
  IF par.transaction_type <> 'CHARGE' THEN
    RAISE EXCEPTION 'PAYMENT_PARENT_NOT_A_CHARGE: parent is %', par.transaction_type
      USING ERRCODE = 'check_violation';
  END IF;
  IF par.status <> 'CAPTURED' THEN
    RAISE EXCEPTION 'PAYMENT_PARENT_NOT_CAPTURED: parent is %; there is nothing to return', par.status
      USING ERRCODE = 'check_violation';
  END IF;
  IF par.tenant_id <> NEW.tenant_id OR par.site_id <> NEW.site_id
     OR par.settlement_id <> NEW.settlement_id THEN
    RAISE EXCEPTION 'PAYMENT_PARENT_OUT_OF_SCOPE: the parent belongs to a different tenant/site/settlement'
      USING ERRCODE = 'check_violation';
  END IF;
  IF par.merchant_account_id <> NEW.merchant_account_id OR par.provider <> NEW.provider THEN
    RAISE EXCEPTION 'PAYMENT_PARENT_MERCHANT_MISMATCH: money returns to the account it came from'
      USING ERRCODE = 'check_violation';
  END IF;
  IF par.currency <> NEW.currency OR par.currency_exponent <> NEW.currency_exponent THEN
    RAISE EXCEPTION 'PAYMENT_PARENT_CURRENCY_MISMATCH: %/% <> %/% (no implicit FX)',
      NEW.currency, NEW.currency_exponent, par.currency, par.currency_exponent
      USING ERRCODE = 'check_violation';
  END IF;
  SELECT coalesce(sum(amount_minor), 0) INTO v_refunded
    FROM iam_v2.payment_transactions
   WHERE parent_transaction_id = NEW.parent_transaction_id
     AND transaction_type IN ('REFUND','CHARGEBACK')
     AND status IN ('CREATED','PENDING','CAPTURED','UNKNOWN');
  IF v_refunded + NEW.amount_minor > par.amount_minor THEN
    RAISE EXCEPTION 'PAYMENT_REFUND_EXCEEDS_CHARGE: % already returned + % requested > % captured',
      v_refunded, NEW.amount_minor, par.amount_minor USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END $fn$;

-- ============================================================================
-- (6b) The status machine allows provider_txn_ref to be assigned exactly once.
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

-- ============================================================================
-- (2) The Settlement machine, exactly as §16 defines it.
-- ============================================================================
-- REQUIRED -> IN_PROGRESS only. A settlement that was never attempted cannot be FAILED or under review;
-- provider execution moves it to IN_PROGRESS first, in the same transaction that records the intent.
CREATE OR REPLACE FUNCTION iam_v2.p4_settlement_state_machine() RETURNS trigger
  LANGUAGE plpgsql SET search_path = iam_v2, pg_temp AS $fn$
DECLARE v_captured bigint; v_returned bigint;
BEGIN
  IF NEW.status = OLD.status THEN
    RETURN NEW;
  END IF;
  IF OLD.purchase_id <> NEW.purchase_id OR OLD.method <> NEW.method THEN
    RAISE EXCEPTION 'SETTLEMENT_IDENTITY_IMMUTABLE: purchase and method are fixed at creation'
      USING ERRCODE = 'check_violation';
  END IF;

  IF NOT (
       (OLD.status = 'REQUIRED'      AND NEW.status = 'IN_PROGRESS')
    OR (OLD.status = 'IN_PROGRESS'   AND NEW.status IN ('SETTLED','FAILED','MANUAL_REVIEW'))
    OR (OLD.status = 'MANUAL_REVIEW' AND NEW.status IN ('SETTLED','FAILED'))
    OR (OLD.status = 'SETTLED'       AND NEW.status IN ('PARTIALLY_REVERSED','REVERSED'))
    OR (OLD.status = 'PARTIALLY_REVERSED' AND NEW.status = 'REVERSED')
  ) THEN
    RAISE EXCEPTION 'SETTLEMENT_TRANSITION: % -> % is not an approved transition (section 16)',
      OLD.status, NEW.status USING ERRCODE = 'check_violation';
  END IF;

  IF NEW.status = 'SETTLED' THEN
    IF NEW.method = 'ONLINE_PAYMENT' THEN
      IF NOT EXISTS (SELECT 1 FROM iam_v2.payment_transactions
                      WHERE settlement_id = NEW.id AND transaction_type = 'CHARGE' AND status = 'CAPTURED') THEN
        RAISE EXCEPTION 'SETTLEMENT_NOT_EVIDENCED: ONLINE_PAYMENT settles only on a CAPTURED charge'
          USING ERRCODE = 'check_violation';
      END IF;
    ELSIF NEW.method = 'PMS_POSTING' THEN
      IF NOT EXISTS (SELECT 1 FROM iam_v2.pms_postings p
                       JOIN iam_v2.posting_attempts a ON a.internal_posting_id = p.id
                      WHERE p.settlement_id = NEW.id AND p.posting_type = 'CHARGE'
                        AND a.outcome = 'ACKED' AND a.pa_as_status = 'OK') THEN
        RAISE EXCEPTION 'SETTLEMENT_NOT_EVIDENCED: PMS_POSTING settles only on a posting the PMS ACKed OK'
          USING ERRCODE = 'check_violation';
      END IF;
    END IF;
  END IF;

  IF NEW.status IN ('PARTIALLY_REVERSED','REVERSED') THEN
    IF NEW.method <> 'ONLINE_PAYMENT' THEN
      RAISE EXCEPTION 'SETTLEMENT_REVERSAL_WRONG_RAIL: only an ONLINE_PAYMENT settlement is reversed by '
                      'provider refunds; the PMS rail records a PASSIVE reversal and is corrected manually'
        USING ERRCODE = 'check_violation';
    END IF;
    SELECT coalesce(sum(amount_minor),0) INTO v_captured FROM iam_v2.payment_transactions
     WHERE settlement_id = NEW.id AND transaction_type = 'CHARGE' AND status = 'CAPTURED';
    SELECT coalesce(sum(amount_minor),0) INTO v_returned FROM iam_v2.payment_transactions
     WHERE settlement_id = NEW.id AND transaction_type IN ('REFUND','CHARGEBACK') AND status = 'CAPTURED';
    IF v_returned = 0 THEN
      RAISE EXCEPTION 'SETTLEMENT_NOT_EVIDENCED: nothing has been returned' USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.status = 'REVERSED' AND v_returned < v_captured THEN
      RAISE EXCEPTION 'SETTLEMENT_PARTIAL: % of % returned; this is PARTIALLY_REVERSED', v_returned, v_captured
        USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.status = 'PARTIALLY_REVERSED' AND v_returned >= v_captured THEN
      RAISE EXCEPTION 'SETTLEMENT_FULL: % of % returned; this is REVERSED', v_returned, v_captured
        USING ERRCODE = 'check_violation';
    END IF;
  END IF;
  RETURN NEW;
END $fn$;

-- ============================================================================
-- (3) Bounded, structured callback evidence.
-- ============================================================================
-- The same lesson as the Manual Review ledger: an append-only financial record cannot be redacted, so the
-- SHAPE has to refuse the payload rather than a comment asking callers not to send one.
--
-- The guarantee is the honest one: structurally closed (a fixed set of scalar keys, no nesting, no arrays),
-- length-bounded, and heuristically screened for recognisable secret shapes. It is not a proof that no
-- secret can ever be typed into a 200-character reference.
CREATE OR REPLACE FUNCTION iam_v2.p4_callback_evidence_safe(p jsonb) RETURNS text
  LANGUAGE plpgsql IMMUTABLE SET search_path = iam_v2, pg_temp AS $fn$
DECLARE k text; v text; allowed text[] := ARRAY['provider_status','provider_reason_code','provider_message',
                                                'provider_received_at','settled_currency','settled_amount_minor'];
BEGIN
  IF p IS NULL OR p = '{}'::jsonb THEN RETURN NULL; END IF;
  IF jsonb_typeof(p) <> 'object' THEN RETURN 'callback evidence must be a flat object'; END IF;
  FOR k, v IN SELECT key, value::text FROM jsonb_each_text(p) LOOP
    IF NOT (k = ANY(allowed)) THEN
      RETURN 'callback evidence key ' || k || ' is not in the allowed set: ' || array_to_string(allowed, ', ');
    END IF;
    IF length(v) > 200 THEN RETURN 'callback evidence value for ' || k || ' exceeds 200 characters'; END IF;
    IF v ~ '[\x00-\x1f\x7f]' THEN RETURN 'callback evidence value for ' || k || ' contains control characters'; END IF;
    IF v ~* '\m(pass(word|phrase)?|secret|api[-_ ]?key|token|bearer|authorization|credential|cvv|cvc|pan)\M'
       OR v ~ '(-----BEGIN|eyJ[A-Za-z0-9_-]{10,}|sk_live_|whsec_)'
       OR v ~ '\m(?:\d[ -]?){13,19}\M' THEN
      RETURN 'callback evidence value for ' || k || ' looks like a secret, card number or credential';
    END IF;
  END LOOP;
  -- nesting is refused wholesale: a nested value is a payload
  IF EXISTS (SELECT 1 FROM jsonb_each(p) WHERE jsonb_typeof(value) IN ('object','array')) THEN
    RETURN 'callback evidence must be flat; nested objects and arrays are payloads';
  END IF;
  RETURN NULL;
END $fn$;

ALTER TABLE iam_v2.payment_transaction_events
  ADD CONSTRAINT ptx_event_ids_bounded CHECK (
        length(provider_event_id) BETWEEN 1 AND 200 AND provider_event_id !~ '[\x00-\x1f\x7f]'
    AND length(event_type)        BETWEEN 1 AND 100 AND event_type        !~ '[\x00-\x1f\x7f]'),
  ADD CONSTRAINT ptx_event_detail_safe CHECK (iam_v2.p4_callback_evidence_safe(detail) IS NULL);

-- ============================================================================
-- (4) Provider-event dedupe at the PROVIDER's identity, not the caller's choice.
-- ============================================================================
-- A provider event id is unique within a provider+merchant account. Keying dedupe on the internal
-- transaction let one event be replayed onto a different internal row by naming a different one.
ALTER TABLE iam_v2.payment_transaction_events
  ADD COLUMN provider text,
  ADD COLUMN merchant_account_id uuid;

-- Backfilling an append-only table needs its guard stood down for exactly this statement. It happens
-- inside the migration's single transaction, so no other writer can slip through the window, and the
-- guard is re-enabled below before anything else runs. The values are copied from the transaction each
-- event already points at, so no fact is invented.
ALTER TABLE iam_v2.payment_transaction_events DISABLE TRIGGER ao_ptx_events;
UPDATE iam_v2.payment_transaction_events e
   SET provider = t.provider, merchant_account_id = t.merchant_account_id
  FROM iam_v2.payment_transactions t WHERE t.id = e.payment_transaction_id;
ALTER TABLE iam_v2.payment_transaction_events ENABLE TRIGGER ao_ptx_events;

ALTER TABLE iam_v2.payment_transaction_events
  ALTER COLUMN provider SET NOT NULL,
  ALTER COLUMN merchant_account_id SET NOT NULL;

CREATE UNIQUE INDEX ptx_event_provider_identity
  ON iam_v2.payment_transaction_events (tenant_id, provider, merchant_account_id, provider_event_id);

COMMENT ON INDEX iam_v2.ptx_event_provider_identity IS
  'A provider event is unique within provider + merchant account. 0014 keyed dedupe on the INTERNAL '
  'transaction id, so the same event could be applied to two different internal rows by naming a '
  'different one. This is the correlation the provider actually owns.';

-- ============================================================================
-- (4b)(5) The controlled callback: trusted correlation, atomic settlement effects.
-- ============================================================================
-- The caller no longer says WHICH money this is. It presents what the provider gave it -- provider,
-- merchant account, and the client reference the provider echoed back -- and the database resolves the
-- transaction itself. A caller that names someone else's transaction has no argument in which to do so.
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

-- The 0014 signature took the internal id from the caller. It is superseded and removed so there is
-- exactly ONE callback door.
DROP FUNCTION IF EXISTS iam_v2.apply_payment_callback(uuid,text,text,text,jsonb);

INSERT INTO public.schema_migrations (version) VALUES ('0015_phase4_payment_hardening') ON CONFLICT DO NOTHING;

COMMIT;
