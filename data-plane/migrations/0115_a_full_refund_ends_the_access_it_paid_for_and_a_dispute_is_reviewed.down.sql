-- 0115 down -- removes the refund policy functions, the review cases and the termination evidence. Entitlements
-- already ended stay ended (their transitions are history); scd built before 0115 never calls these functions.
BEGIN;
DROP FUNCTION IF EXISTS iam_v2.p4_resolve_reversal_review(uuid, uuid, uuid, text, text, text);
DROP FUNCTION IF EXISTS iam_v2.p4_open_dispute_review(uuid, uuid, uuid, text);
DROP FUNCTION IF EXISTS iam_v2.p4_apply_refund_policy(uuid, uuid, uuid);
DROP FUNCTION IF EXISTS iam_v2.p4_end_access_funded_by(uuid, text);
DROP TABLE IF EXISTS iam_v2.refund_access_terminations;
DROP FUNCTION IF EXISTS iam_v2.refund_access_terminations_append_only();
DROP TABLE IF EXISTS iam_v2.card_reversal_reviews;
DROP FUNCTION IF EXISTS iam_v2.card_reversal_reviews_guard();
COMMIT;
