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

DROP FUNCTION iam_v2.restore_room_move_access(uuid, uuid, uuid, integer, uuid, text, boolean);
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
