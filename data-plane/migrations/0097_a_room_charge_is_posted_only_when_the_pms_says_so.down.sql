-- Roll back 0097. Refuses while any room-charge posting or onboarding exists: those are financial records.
BEGIN;
DO $guard$
BEGIN
  IF EXISTS (SELECT 1 FROM iam_v2.pms_financial_onboardings)
     OR EXISTS (SELECT 1 FROM iam_v2.pms_postings WHERE idempotency_key LIKE 'room-charge:%') THEN
    RAISE EXCEPTION '0097 down refused: room-charge records exist';
  END IF;
END $guard$;
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

ALTER FUNCTION iam_v2.p4_consume_retry_authorization() SECURITY INVOKER;
DO $rv$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_scd') THEN
    REVOKE SELECT ON iam_v2.package_settlement_mappings, iam_v2.stay_folios FROM svc_scd;
  END IF;
END $rv$;
DROP FUNCTION IF EXISTS iam_v2.p4_room_charge_has_records(uuid,uuid);
DROP FUNCTION IF EXISTS iam_v2.p4_posting_review_apply(uuid,uuid,uuid);
DROP FUNCTION IF EXISTS iam_v2.p4_posting_settlement_outcome(uuid);
DROP FUNCTION IF EXISTS iam_v2.p4_create_room_charge_posting(uuid,uuid,uuid);
DROP INDEX IF EXISTS iam_v2.psm_one_live_per_interface;
ALTER TABLE iam_v2.package_settlement_mappings DROP CONSTRAINT IF EXISTS psm_posting_code_wire_safe;
DROP FUNCTION IF EXISTS iam_v2.pms_interface_financially_ready(uuid,uuid,uuid);
DROP FUNCTION IF EXISTS iam_v2.pms_interface_financial_onboard(uuid,uuid,uuid,uuid,text,text,smallint,text,text,uuid);
DROP TRIGGER IF EXISTS pms_financial_onboardings_append_only ON iam_v2.pms_financial_onboardings;
DROP TABLE IF EXISTS iam_v2.pms_financial_onboardings;
DROP FUNCTION IF EXISTS iam_v2.pms_financial_onboardings_append_only();
DO $r$ BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='sc_posting_runtime') THEN
    EXECUTE 'REVOKE ALL ON ALL TABLES IN SCHEMA iam_v2 FROM sc_posting_runtime';
    EXECUTE 'REVOKE ALL ON ALL FUNCTIONS IN SCHEMA iam_v2 FROM sc_posting_runtime';
    EXECUTE 'REVOKE USAGE ON SCHEMA iam_v2 FROM sc_posting_runtime';
  END IF;
END $r$;
COMMIT;
