-- Reverses 0168. The down refuses while any REVERSAL_REFUSED case exists: turning a financial review into another kind
-- would falsify it.
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM iam_v2.card_reversal_reviews WHERE kind = 'REVERSAL_REFUSED') THEN
        RAISE EXCEPTION '0168 down refused: REVERSAL_REFUSED review cases exist; resolve and archive them first';
    END IF;
END $$;
DROP FUNCTION IF EXISTS iam_v2.p4_record_provider_reversal_v2(uuid,uuid,uuid,text,bigint,boolean,text,text);
ALTER TABLE iam_v2.card_reversal_reviews DROP CONSTRAINT IF EXISTS card_reversal_reviews_refused_has_detail;
ALTER TABLE iam_v2.card_reversal_reviews DROP CONSTRAINT IF EXISTS card_reversal_reviews_kind_check;
ALTER TABLE iam_v2.card_reversal_reviews ADD CONSTRAINT card_reversal_reviews_kind_check CHECK (kind IN ('DISPUTE','CHARGEBACK'));
CREATE OR REPLACE FUNCTION iam_v2.card_reversal_reviews_guard() RETURNS trigger
  LANGUAGE plpgsql SET search_path = iam_v2, pg_temp AS $f$
BEGIN
  IF TG_OP = 'DELETE' THEN
    RAISE EXCEPTION 'card reversal reviews are never deleted' USING ERRCODE = 'insufficient_privilege';
  END IF;
  IF OLD.resolution IS NOT NULL THEN
    RAISE EXCEPTION 'a resolved review is final' USING ERRCODE = 'insufficient_privilege';
  END IF;
  IF (NEW.id, NEW.tenant_id, NEW.site_id, NEW.settlement_id, NEW.charge_transaction_id, NEW.kind, NEW.provider_ref, NEW.opened_at)
     IS DISTINCT FROM (OLD.id, OLD.tenant_id, OLD.site_id, OLD.settlement_id, OLD.charge_transaction_id, OLD.kind, OLD.provider_ref, OLD.opened_at) THEN
    RAISE EXCEPTION 'only the resolution of a review may be written' USING ERRCODE = 'insufficient_privilege';
  END IF;
  RETURN NEW;
END $f$;
ALTER TABLE iam_v2.card_reversal_reviews
    DROP COLUMN IF EXISTS detail,
    DROP COLUMN IF EXISTS reversal_amount_minor,
    DROP COLUMN IF EXISTS reversal_kind;
