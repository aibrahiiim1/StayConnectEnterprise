-- Reverses 0172. Refused while a LATE_CAPTURE case exists: a financial review is not rewritten.
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM iam_v2.card_reversal_reviews WHERE kind = 'LATE_CAPTURE') THEN
        RAISE EXCEPTION '0172 down refused: LATE_CAPTURE review cases exist; resolve and archive them first';
    END IF;
END $$;
DELETE FROM iam_v2.scrub_registry WHERE table_name = 'iam_v2.site_card_recheck_setting_changes';
DROP FUNCTION IF EXISTS iam_v2.p4_open_late_capture_review(uuid,uuid,uuid,text,bigint,text);
DROP FUNCTION IF EXISTS iam_v2.p4_mark_late_checked(uuid,uuid,uuid);
DROP FUNCTION IF EXISTS iam_v2.p4_late_capture_candidates(uuid,uuid,integer,integer);
DROP FUNCTION IF EXISTS iam_v2.card_recheck_settings_set(uuid,uuid,bigint,integer,text,text);
DROP FUNCTION IF EXISTS iam_v2.card_recheck_settings_get(uuid,uuid);
DROP TABLE IF EXISTS iam_v2.site_card_recheck_setting_changes;
DROP TABLE IF EXISTS iam_v2.site_card_recheck_settings;
ALTER TABLE iam_v2.card_reversal_reviews DROP CONSTRAINT IF EXISTS card_reversal_reviews_late_has_detail;
ALTER TABLE iam_v2.card_reversal_reviews DROP CONSTRAINT IF EXISTS card_reversal_reviews_kind_check;
ALTER TABLE iam_v2.card_reversal_reviews ADD CONSTRAINT card_reversal_reviews_kind_check
    CHECK (kind IN ('DISPUTE','CHARGEBACK','REVERSAL_REFUSED'));
ALTER TABLE iam_v2.payment_checkouts DROP COLUMN IF EXISTS late_checked_at;
