-- 0014 — Phase 4: Online Payment and Settlement execution enforcement. Additive, reversible, DARK.
-- Authorization: D18 / T0029 (unchanged). Receipt: T0035.
--
-- MEASURED FIRST. Against the 0011+0012+0013 chain in disposable PostgreSQL:
--
--   payment_transactions triggers      NONE
--   settlements triggers               NONE
--   callback/webhook ledger            NONE
--   present already (mg7)              amount_minor > 0; UNIQUE idempotency_key;
--                                      UNIQUE (tenant, provider, merchant_account, provider_ref);
--                                      status and transaction_type enums; ptx_parent (CHARGE <=> no parent);
--                                      composite FKs to settlements and to the parent transaction
--
-- So mg7 gave the SHAPE and nothing that governs how money moves through it. Everything below is the
-- governance: a one-way status machine, parent consistency, a cumulative refund bound, server-pinned
-- amounts, a deduplicated callback ledger, and the Settlement lifecycle.
--
-- SEPARATE RAILS. PMS Posting and Online Payment settle the same commercial Settlement by two different
-- mechanisms. A PMS passive REVERSAL row is NOT a provider REFUND and can never become one: they are
-- different tables, governed here by different rules, and 0013 already makes the reversal non-executable.
BEGIN;

-- ============================================================================
-- (1) The provider callback ledger — append-only, deduplicated, correlated.
-- ============================================================================
-- A webhook is retried by every provider that has one. Applying the same callback twice is how a single
-- payment becomes two grants, so dedupe cannot be "the handler checks first" -- it has to be a uniqueness
-- constraint that survives concurrency, restarts and replays.
-- The composite-FK pattern this schema uses everywhere needs a matching unique key. mg7 gave
-- payment_transactions UNIQUE (tenant, site, settlement, id) but not (tenant, site, id), so the scope-
-- carrying foreign key below had nothing to point at. Adding it is additive and weakens nothing.
ALTER TABLE iam_v2.payment_transactions ADD CONSTRAINT payment_transactions_tsi_key
  UNIQUE (tenant_id, site_id, id);

CREATE TABLE iam_v2.payment_transaction_events (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL, site_id uuid NOT NULL,
  payment_transaction_id uuid NOT NULL,
  -- provider_event_id is the provider's OWN identifier for this delivery. It is the dedupe key.
  provider_event_id text NOT NULL,
  event_type text NOT NULL,
  -- the status this callback asserted; NULL for informational events that assert nothing
  asserted_status text CHECK (asserted_status IS NULL OR asserted_status IN
    ('CREATED','PENDING','CAPTURED','FAILED','EXPIRED','CANCELLED','UNKNOWN')),
  applied boolean NOT NULL DEFAULT false,
  -- detail carries protocol facts only. It must never carry a raw provider payload: a callback body can
  -- contain card metadata and provider secrets, and this is an append-only ledger.
  detail jsonb NOT NULL DEFAULT '{}',
  received_at timestamptz NOT NULL DEFAULT now(),
  -- THE dedupe: one delivery per provider event, per transaction, forever.
  UNIQUE (payment_transaction_id, provider_event_id),
  FOREIGN KEY (tenant_id, site_id, payment_transaction_id)
    REFERENCES iam_v2.payment_transactions (tenant_id, site_id, id));

CREATE INDEX ptx_events_by_txn ON iam_v2.payment_transaction_events (payment_transaction_id, received_at);

CREATE OR REPLACE FUNCTION iam_v2.trg_reject_update_delete_ptx_events() RETURNS trigger
  LANGUAGE plpgsql AS $fn$
BEGIN RAISE EXCEPTION 'payment_transaction_events is append-only (no % )', TG_OP; END $fn$;
CREATE TRIGGER ao_ptx_events BEFORE UPDATE OR DELETE ON iam_v2.payment_transaction_events
  FOR EACH ROW EXECUTE FUNCTION iam_v2.trg_reject_update_delete_ptx_events();

COMMENT ON TABLE iam_v2.payment_transaction_events IS
  'Append-only provider callback ledger. UNIQUE (payment_transaction_id, provider_event_id) is the '
  'duplicate-callback defence: a replayed webhook cannot be applied twice, whatever the caller does.';

-- ============================================================================
-- (2) The payment status machine — one-way, and terminal means terminal.
-- ============================================================================
-- CHARGE:  CREATED -> PENDING -> CAPTURED | FAILED | EXPIRED | CANCELLED | UNKNOWN
--
-- UNKNOWN is the same financial safety state it is on the PMS rail: the provider's outcome could not be
-- determined, so nothing may be assumed and nothing may be blindly retried. It is reachable from any
-- non-terminal state and it is TERMINAL here -- leaving it is a Manual Review decision, not an automatic
-- transition, and this migration deliberately provides no automatic path out.
CREATE OR REPLACE FUNCTION iam_v2.p4_payment_status_machine() RETURNS trigger
  LANGUAGE plpgsql SET search_path = iam_v2, pg_temp AS $fn$
BEGIN
  IF TG_OP = 'DELETE' THEN
    RAISE EXCEPTION 'PAYMENT_IMMUTABLE: payment transactions are never deleted'
      USING ERRCODE = 'feature_not_supported';
  END IF;

  -- identity is immutable: everything that says WHICH money this is
  IF ROW(NEW.tenant_id, NEW.site_id, NEW.settlement_id, NEW.merchant_account_id, NEW.transaction_type,
         NEW.parent_transaction_id, NEW.provider, NEW.provider_ref, NEW.idempotency_key,
         NEW.amount_minor, NEW.currency, NEW.currency_exponent)
     IS DISTINCT FROM
     ROW(OLD.tenant_id, OLD.site_id, OLD.settlement_id, OLD.merchant_account_id, OLD.transaction_type,
         OLD.parent_transaction_id, OLD.provider, OLD.provider_ref, OLD.idempotency_key,
         OLD.amount_minor, OLD.currency, OLD.currency_exponent) THEN
    RAISE EXCEPTION 'PAYMENT_IDENTITY_IMMUTABLE: only status may change on a payment transaction'
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

CREATE TRIGGER p4_payment_status_machine
  BEFORE UPDATE OR DELETE ON iam_v2.payment_transactions
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p4_payment_status_machine();

-- ============================================================================
-- (3) Creation rules — server-pinned money, parent consistency, refund bounds.
-- ============================================================================
CREATE OR REPLACE FUNCTION iam_v2.p4_payment_creation_gate() RETURNS trigger
  LANGUAGE plpgsql SET search_path = iam_v2, pg_temp AS $fn$
DECLARE se record; pu record; par record; v_refunded bigint; v_open int;
BEGIN
  IF NEW.currency !~ '^[A-Z]{3}$' THEN
    RAISE EXCEPTION 'PAYMENT_CURRENCY_INVALID: % is not an ISO-4217 code', NEW.currency
      USING ERRCODE = 'check_violation';
  END IF;
  IF NEW.currency_exponent < 0 OR NEW.currency_exponent > 4 THEN
    RAISE EXCEPTION 'PAYMENT_EXPONENT_INVALID: %', NEW.currency_exponent USING ERRCODE = 'check_violation';
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
    -- ONLINE_PAYMENT only. The PMS rail settles through pms_postings and must never acquire a provider
    -- charge, and a NOT_REQUIRED settlement has nothing to charge for.
    IF se.method <> 'ONLINE_PAYMENT' THEN
      RAISE EXCEPTION 'PAYMENT_WRONG_RAIL: settlement method is %; an online payment charge requires '
                      'ONLINE_PAYMENT', se.method USING ERRCODE = 'check_violation';
    END IF;
    -- SERVER-PINNED MONEY. The amount, currency and exponent come from the Purchase the Settlement points
    -- at. A request cannot choose what it pays by sending a different number.
    IF NEW.amount_minor IS DISTINCT FROM pu.amount_minor
       OR NEW.currency IS DISTINCT FROM pu.currency
       OR NEW.currency_exponent IS DISTINCT FROM pu.currency_exponent THEN
      RAISE EXCEPTION 'PAYMENT_AMOUNT_NOT_SERVER_PINNED: charge %/%/% <> pinned purchase %/%/%',
        NEW.amount_minor, NEW.currency, NEW.currency_exponent,
        pu.amount_minor, pu.currency, pu.currency_exponent USING ERRCODE = 'check_violation';
    END IF;
    -- ONE live charge per settlement. mg7's UNIQUE idempotency_key stops a replay of the SAME command;
    -- this stops a SECOND, differently-keyed command against money that is already being taken.
    SELECT count(*) INTO v_open FROM iam_v2.payment_transactions
     WHERE settlement_id = NEW.settlement_id AND transaction_type = 'CHARGE'
       AND status IN ('CREATED','PENDING','CAPTURED','UNKNOWN');
    IF v_open > 0 THEN
      RAISE EXCEPTION 'PAYMENT_DUPLICATE_CHARGE: settlement % already has a live or captured charge',
        NEW.settlement_id USING ERRCODE = 'unique_violation';
    END IF;
    RETURN NEW;
  END IF;

  -- REFUND / CHARGEBACK: a child of a CAPTURED charge, in every dimension.
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
  -- Cumulative bound. Only refunds that are still live or already settled count against the parent;
  -- a FAILED or CANCELLED refund returned nothing and must not consume the allowance.
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

CREATE TRIGGER p4_payment_creation_gate
  BEFORE INSERT ON iam_v2.payment_transactions
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p4_payment_creation_gate();

-- ============================================================================
-- (4) The Settlement lifecycle — one authoritative truth.
-- ============================================================================
-- REQUIRED -> IN_PROGRESS -> SETTLED | FAILED | MANUAL_REVIEW
-- SETTLED  -> PARTIALLY_REVERSED | REVERSED   (only through child financial records)
--
-- MANUAL_REVIEW is deliberately not terminal: it exists so an uncertain settlement can be decided, and the
-- decision has to be able to land somewhere.
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
       (OLD.status = 'REQUIRED'      AND NEW.status IN ('IN_PROGRESS','FAILED','MANUAL_REVIEW'))
    OR (OLD.status = 'IN_PROGRESS'   AND NEW.status IN ('SETTLED','FAILED','MANUAL_REVIEW'))
    OR (OLD.status = 'MANUAL_REVIEW' AND NEW.status IN ('SETTLED','FAILED'))
    OR (OLD.status = 'SETTLED'       AND NEW.status IN ('PARTIALLY_REVERSED','REVERSED'))
    OR (OLD.status = 'PARTIALLY_REVERSED' AND NEW.status = 'REVERSED')
    OR (OLD.status = 'NOT_REQUIRED'  AND NEW.status = 'NOT_REQUIRED')
  ) THEN
    RAISE EXCEPTION 'SETTLEMENT_TRANSITION: % -> % is not an approved transition', OLD.status, NEW.status
      USING ERRCODE = 'check_violation';
  END IF;

  -- SETTLED requires evidence on the rail that settled it. A settlement cannot simply be declared paid.
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

  -- The reversal states are arithmetic, not opinion: they must match what the child records say.
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

CREATE TRIGGER p4_settlement_state_machine
  BEFORE UPDATE ON iam_v2.settlements
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p4_settlement_state_machine();

-- ============================================================================
-- (5) The controlled callback application.
-- ============================================================================
-- Everything a provider callback is allowed to do, in one place, in one transaction: record the delivery
-- (deduplicated), move the status through the approved machine, and advance the Settlement.
--
-- It returns the ACTION taken so a caller can tell a first delivery from a replay without guessing:
--   APPLIED    the status moved
--   DUPLICATE  this provider_event_id was already recorded; nothing changed
--   NOOP       the delivery was recorded but asserted nothing new
CREATE OR REPLACE FUNCTION iam_v2.apply_payment_callback(
  p_txn uuid, p_provider_event_id text, p_event_type text, p_asserted_status text,
  p_detail jsonb DEFAULT '{}'::jsonb)
RETURNS text
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE tx record; se record; v_dup boolean := false; v_moves boolean;
BEGIN
  IF p_provider_event_id IS NULL OR btrim(p_provider_event_id) = '' THEN
    RAISE EXCEPTION 'CALLBACK_EVENT_ID_REQUIRED: a callback without a provider event id cannot be deduplicated'
      USING ERRCODE = 'check_violation';
  END IF;
  SELECT * INTO tx FROM iam_v2.payment_transactions WHERE id = p_txn FOR UPDATE;
  IF tx.id IS NULL THEN
    RAISE EXCEPTION 'CALLBACK_TXN_UNKNOWN: %', p_txn USING ERRCODE = 'foreign_key_violation';
  END IF;

  -- `applied` is decided BEFORE the insert, because the ledger is append-only and cannot be corrected
  -- afterwards. It records whether THIS delivery is the one that moved the status.
  v_moves := p_asserted_status IS NOT NULL AND p_asserted_status <> tx.status;
  BEGIN
    INSERT INTO iam_v2.payment_transaction_events
      (tenant_id, site_id, payment_transaction_id, provider_event_id, event_type, asserted_status, detail, applied)
    VALUES (tx.tenant_id, tx.site_id, p_txn, btrim(p_provider_event_id), p_event_type, p_asserted_status,
            coalesce(p_detail, '{}'::jsonb), v_moves);
  EXCEPTION WHEN unique_violation THEN
    v_dup := true;
  END;
  IF v_dup THEN
    RETURN 'DUPLICATE';           -- a replayed webhook changes nothing, ever
  END IF;

  IF NOT v_moves THEN
    RETURN 'NOOP';
  END IF;

  UPDATE iam_v2.payment_transactions SET status = p_asserted_status WHERE id = p_txn;

  -- Advance the Settlement on the same rail, in the same transaction, so the two cannot disagree.
  SELECT * INTO se FROM iam_v2.settlements WHERE id = tx.settlement_id FOR UPDATE;
  IF tx.transaction_type = 'CHARGE' THEN
    IF p_asserted_status = 'CAPTURED' AND se.status IN ('REQUIRED','IN_PROGRESS') THEN
      IF se.status = 'REQUIRED' THEN
        UPDATE iam_v2.settlements SET status = 'IN_PROGRESS' WHERE id = se.id;
      END IF;
      UPDATE iam_v2.settlements SET status = 'SETTLED' WHERE id = se.id;
    ELSIF p_asserted_status IN ('FAILED','EXPIRED','CANCELLED') AND se.status IN ('REQUIRED','IN_PROGRESS') THEN
      UPDATE iam_v2.settlements SET status = 'FAILED' WHERE id = se.id;
    ELSIF p_asserted_status = 'UNKNOWN' AND se.status IN ('REQUIRED','IN_PROGRESS') THEN
      -- The provider's outcome could not be determined. That is not a failure and not a success; it is a
      -- decision for an operator, and nothing here retries it.
      UPDATE iam_v2.settlements SET status = 'MANUAL_REVIEW' WHERE id = se.id;
    END IF;
  END IF;
  RETURN 'APPLIED';
END $fn$;
REVOKE EXECUTE ON FUNCTION iam_v2.apply_payment_callback(uuid,text,text,text,jsonb) FROM PUBLIC;

INSERT INTO public.schema_migrations (version) VALUES ('0014_phase4_payment_settlement') ON CONFLICT DO NOTHING;

COMMIT;
