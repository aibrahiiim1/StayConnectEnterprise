-- AD-42 (audit P6 M3): a reversal the ledger refuses.
--
-- The refund/chargeback ledger trigger refuses a reversal that would take the charge's returned total above what was
-- captured (refunds and chargebacks counted together). After 44fd44cb the Go half logs the refusal, but a lost dispute after a
-- partial refund was still only a log line: finance never saw the missed chargeback, and the 6-hourly sweep repeated the
-- same refusal forever.
--
--   1. iam_v2.card_reversal_reviews gains the kind REVERSAL_REFUSED (+ what was asked and why it was refused). The case
--      is resolved exactly like a dispute (KEEP_ACCESS / END_ACCESS with a reason), by the same operator action.
--   2. iam_v2.p4_record_provider_reversal_v2 does the running-total subtraction (Paymob reports refunds as a running
--      total) INSIDE the parent-row lock, so two concurrent passes cannot both subtract from the same stale total; and a
--      refusal by the ledger opens (once) a REVERSAL_REFUSED case instead of vanishing.
--
-- Additive; the old p4_record_provider_reversal stays and is still what v2 calls. Idempotent. Nothing here initiates,
-- retries or reverses money.

ALTER TABLE iam_v2.card_reversal_reviews
    ADD COLUMN IF NOT EXISTS reversal_kind          text,
    ADD COLUMN IF NOT EXISTS reversal_amount_minor  bigint,
    ADD COLUMN IF NOT EXISTS detail                 text;

ALTER TABLE iam_v2.card_reversal_reviews DROP CONSTRAINT IF EXISTS card_reversal_reviews_kind_check;
ALTER TABLE iam_v2.card_reversal_reviews ADD CONSTRAINT card_reversal_reviews_kind_check
    CHECK (kind IN ('DISPUTE','CHARGEBACK','REVERSAL_REFUSED'));
DO $$ BEGIN
    ALTER TABLE iam_v2.card_reversal_reviews ADD CONSTRAINT card_reversal_reviews_refused_has_detail
        CHECK (kind <> 'REVERSAL_REFUSED'
               OR (reversal_kind IN ('REFUND','CHARGEBACK') AND reversal_amount_minor > 0 AND detail IS NOT NULL
                   AND length(detail) <= 500));
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

-- The guard now also pins the new columns: a review's facts are written once.
CREATE OR REPLACE FUNCTION iam_v2.card_reversal_reviews_guard() RETURNS trigger
  LANGUAGE plpgsql SET search_path = iam_v2, pg_temp AS $f$
BEGIN
  IF TG_OP = 'DELETE' THEN
    RAISE EXCEPTION 'card reversal reviews are never deleted' USING ERRCODE = 'insufficient_privilege';
  END IF;
  IF OLD.resolution IS NOT NULL THEN
    RAISE EXCEPTION 'a resolved review is final' USING ERRCODE = 'insufficient_privilege';
  END IF;
  IF (NEW.id, NEW.tenant_id, NEW.site_id, NEW.settlement_id, NEW.charge_transaction_id, NEW.kind, NEW.provider_ref, NEW.opened_at,
      NEW.reversal_kind, NEW.reversal_amount_minor, NEW.detail)
     IS DISTINCT FROM (OLD.id, OLD.tenant_id, OLD.site_id, OLD.settlement_id, OLD.charge_transaction_id, OLD.kind, OLD.provider_ref, OLD.opened_at,
      OLD.reversal_kind, OLD.reversal_amount_minor, OLD.detail) THEN
    RAISE EXCEPTION 'only the resolution of a review may be written' USING ERRCODE = 'insufficient_privilege';
  END IF;
  RETURN NEW;
END $f$;

CREATE OR REPLACE FUNCTION iam_v2.p4_record_provider_reversal_v2(
    p_tenant uuid, p_site uuid, p_parent uuid, p_kind text, p_amount_minor bigint, p_cumulative boolean,
    p_provider_event_id text, p_provider_txn_ref text)
  RETURNS text
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE par record; v_recorded bigint; v_amount bigint; v_returned bigint; v_res text; v_n int;
BEGIN
  IF p_kind NOT IN ('REFUND','CHARGEBACK') THEN
    RAISE EXCEPTION 'REVERSAL_KIND: %', p_kind USING ERRCODE = 'check_violation';
  END IF;
  IF p_amount_minor IS NULL OR p_amount_minor <= 0 THEN
    RAISE EXCEPTION 'REVERSAL_AMOUNT: must be positive' USING ERRCODE = 'check_violation';
  END IF;
  IF p_provider_event_id IS NULL OR btrim(p_provider_event_id) = '' THEN
    RAISE EXCEPTION 'REVERSAL_EVENT_ID: the provider event id is the deduplication key' USING ERRCODE = 'check_violation';
  END IF;
  -- The parent lock first: the recorded total is read under it, so two passes cannot subtract from one stale total.
  SELECT * INTO par FROM iam_v2.payment_transactions
   WHERE tenant_id = p_tenant AND site_id = p_site AND id = p_parent AND transaction_type = 'CHARGE' FOR UPDATE;
  IF par.id IS NULL OR par.status <> 'CAPTURED' THEN
    RAISE EXCEPTION 'REVERSAL_PARENT: a reversal belongs to a captured charge' USING ERRCODE = 'check_violation';
  END IF;

  v_amount := p_amount_minor;
  IF p_cumulative THEN
    SELECT COALESCE(sum(amount_minor), 0) INTO v_recorded FROM iam_v2.payment_transactions
     WHERE parent_transaction_id = p_parent AND transaction_type = p_kind AND status = 'CAPTURED';
    v_amount := p_amount_minor - v_recorded;
    IF v_amount <= 0 THEN
      RETURN 'NOTHING_NEW';
    END IF;
  END IF;

  BEGIN
    SELECT iam_v2.p4_record_provider_reversal(p_tenant, p_site, p_parent, p_kind, v_amount, p_provider_event_id, p_provider_txn_ref)
      INTO v_res;
    RETURN v_res;
  EXCEPTION WHEN check_violation THEN
    IF SQLERRM NOT LIKE 'PAYMENT_REFUND_EXCEEDS_CHARGE%' THEN
      RAISE;
    END IF;
    -- The sub-transaction above is rolled back: nothing was recorded. Finance must see it.
    SELECT COALESCE(sum(amount_minor), 0) INTO v_returned FROM iam_v2.payment_transactions
     WHERE parent_transaction_id = p_parent AND transaction_type IN ('REFUND','CHARGEBACK')
       AND status IN ('CREATED','PENDING','CAPTURED','UNKNOWN');
    INSERT INTO iam_v2.card_reversal_reviews
           (tenant_id, site_id, settlement_id, charge_transaction_id, kind, provider_ref,
            reversal_kind, reversal_amount_minor, detail)
    VALUES (p_tenant, p_site, par.settlement_id, p_parent, 'REVERSAL_REFUSED', left(p_provider_event_id, 200),
            p_kind, v_amount,
            left(format('The provider reported a %s of %s %s minor units; the ledger refused it because %s are already returned against a charge of %s.',
                        lower(p_kind), v_amount, par.currency, v_returned, par.amount_minor), 500))
    ON CONFLICT (charge_transaction_id, kind, provider_ref) DO NOTHING;
    GET DIAGNOSTICS v_n = ROW_COUNT;
    RETURN CASE WHEN v_n = 1 THEN 'REVIEW_OPENED' ELSE 'REVIEW_ALREADY_OPEN' END;
  END;
END $fn$;
REVOKE ALL ON FUNCTION iam_v2.p4_record_provider_reversal_v2(uuid,uuid,uuid,text,bigint,boolean,text,text) FROM PUBLIC;

DO $own$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
    EXECUTE 'ALTER FUNCTION iam_v2.p4_record_provider_reversal_v2(uuid,uuid,uuid,text,bigint,boolean,text,text) OWNER TO iam_v2_owner';
  END IF;
END $own$;

DO $g$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'sc_payment_outcome') THEN
    GRANT EXECUTE ON FUNCTION iam_v2.p4_record_provider_reversal_v2(uuid,uuid,uuid,text,bigint,boolean,text,text) TO sc_payment_outcome;
  END IF;
END $g$;
