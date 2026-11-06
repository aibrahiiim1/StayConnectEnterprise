-- 0213 down -- a stay takes each package revision once again, and room moves are no longer restored (the 0212 state).
--
-- REFUSES rather than rewrites when renewals or room-move restorations already exist: a second purchase of a revision or a
-- ROOM_MOVE_RESTORE purchase is a record of what was sold and granted, and the old index and CHECK cannot hold it.

BEGIN;

DO $guard$
BEGIN
  IF EXISTS (SELECT 1 FROM iam_v2.purchases WHERE purchase_seq > 1) THEN
    RAISE EXCEPTION '0213 down: renewal purchases exist; they are not rewritten';
  END IF;
  IF EXISTS (SELECT 1 FROM iam_v2.purchases WHERE trigger = 'ROOM_MOVE_RESTORE') THEN
    RAISE EXCEPTION '0213 down: room-move restorations exist; they are not rewritten';
  END IF;
END $guard$;

CREATE OR REPLACE FUNCTION iam_v2.p4_apply_refund_policy(p_tenant uuid, p_site uuid, p_charge uuid) RETURNS text
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE
  ch record; se record; v_captured bigint; v_refunded bigint; v_charged_back bigint; v_ent uuid; v_status text; v_tr uuid;
BEGIN
  SELECT * INTO ch FROM iam_v2.payment_transactions
   WHERE id = p_charge AND tenant_id = p_tenant AND site_id = p_site AND transaction_type = 'CHARGE';
  IF NOT FOUND OR ch.status <> 'CAPTURED' THEN
    RETURN 'NOT_A_CAPTURED_CHARGE';
  END IF;
  SELECT * INTO se FROM iam_v2.settlements WHERE id = ch.settlement_id FOR UPDATE;
  v_captured := ch.amount_minor;
  SELECT COALESCE(sum(amount_minor) FILTER (WHERE transaction_type = 'REFUND'), 0),
         COALESCE(sum(amount_minor) FILTER (WHERE transaction_type = 'CHARGEBACK'), 0)
    INTO v_refunded, v_charged_back
    FROM iam_v2.payment_transactions WHERE parent_transaction_id = p_charge AND status = 'CAPTURED';

  IF v_charged_back > 0 THEN
    INSERT INTO iam_v2.card_reversal_reviews (tenant_id, site_id, settlement_id, charge_transaction_id, kind, provider_ref)
    VALUES (p_tenant, p_site, se.id, p_charge, 'CHARGEBACK', 'ledger')
    ON CONFLICT (charge_transaction_id, kind, provider_ref) DO NOTHING;
    RETURN 'CHARGEBACK_REVIEW';
  END IF;
  IF v_refunded <= 0 THEN
    RETURN 'NO_REFUND';
  END IF;
  IF v_refunded < v_captured THEN
    RETURN 'PARTIAL_NO_ACTION';
  END IF;

  SELECT id, status INTO v_ent, v_status FROM iam_v2.entitlements
   WHERE tenant_id = p_tenant AND site_id = p_site AND purchase_id = se.purchase_id;
  IF v_ent IS NULL THEN
    RETURN 'NO_ENTITLEMENT';
  END IF;
  IF EXISTS (SELECT 1 FROM iam_v2.refund_access_terminations WHERE entitlement_id = v_ent) THEN
    RETURN 'ALREADY_ENDED';
  END IF;
  IF v_status = 'TERMINATED' THEN
    RETURN 'ALREADY_TERMINATED'; -- ended for another reason before the refund; nothing to record
  END IF;
  v_tr := iam_v2.p4_end_access_funded_by(v_ent, 'REVOKED');
  INSERT INTO iam_v2.refund_access_terminations
    (entitlement_id, tenant_id, site_id, settlement_id, charge_transaction_id, captured_minor, refunded_minor, transition_id)
  VALUES (v_ent, p_tenant, p_site, se.id, p_charge, v_captured, v_refunded, v_tr);
  RETURN 'ACCESS_ENDED';
END $$;

DROP FUNCTION iam_v2.restore_room_move_access(uuid, uuid, uuid, integer, uuid, text, boolean);
DROP FUNCTION iam_v2.entitlement_funding_reversed(uuid);
DROP FUNCTION iam_v2.stay_next_applied_checkin(uuid, uuid);
DROP FUNCTION iam_v2.stay_event_room(jsonb);
DROP TABLE iam_v2.room_move_restorations;
DROP FUNCTION iam_v2.room_move_restorations_append_only();

ALTER TABLE iam_v2.purchases DROP CONSTRAINT purchases_trigger_check;
ALTER TABLE iam_v2.purchases ADD CONSTRAINT purchases_trigger_check CHECK (trigger = ANY (ARRAY['GUEST_SELECTION','VOUCHER_REDEMPTION',
  'ACCOUNT_AUTO_GRANT','OTP_SOCIAL_DEFAULT','CHECKOUT_GRACE','EMERGENCY_GRACE','POST_STAY_CONVERSION','CROSS_PMS_TRANSFER','ADMIN_GRANT',
  'RENEWAL']));

DROP INDEX iam_v2.purchase_once_per_stay;
CREATE UNIQUE INDEX purchase_once_per_stay ON iam_v2.purchases (stay_id, package_revision_id)
  WHERE state IN ('PENDING','AWAITING_SETTLEMENT','MANUAL_REVIEW','GRANTED') AND trigger = 'GUEST_SELECTION';
DROP TRIGGER purchase_renewal_guard ON iam_v2.purchases;
DROP FUNCTION iam_v2.purchase_renewal_guard();
DROP FUNCTION iam_v2.stay_package_purchase_refusal(uuid, uuid);

ALTER TABLE iam_v2.internet_package_revisions DROP CONSTRAINT ipr_max_purchases_per_stay;

COMMIT;
