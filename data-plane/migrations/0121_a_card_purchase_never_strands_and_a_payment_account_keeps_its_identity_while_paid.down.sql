-- 0121 down -- removes the stranded-purchase sweep and the account identity guards. Purchases already failed by
-- the sweep stay failed (their settlement transitions are history), and their sweep records go with the table.
-- An scd built before 0121 never calls the sweep.
BEGIN;
DROP TRIGGER IF EXISTS payment_account_secret_guard ON iam_v2.payment_provider_secret_generations;
DROP FUNCTION IF EXISTS iam_v2.payment_account_secret_guard();
DROP TRIGGER IF EXISTS payment_account_identity_guard ON iam_v2.payment_provider_accounts;
DROP FUNCTION IF EXISTS iam_v2.payment_account_identity_guard();
DROP FUNCTION IF EXISTS iam_v2.payment_account_has_open_checkouts(uuid);
DROP FUNCTION IF EXISTS iam_v2.p4_fail_unstarted_card_purchases(uuid, uuid, integer);
DROP TABLE IF EXISTS iam_v2.card_purchase_sweeps;
DROP FUNCTION IF EXISTS iam_v2.card_purchase_sweeps_append_only();
COMMIT;
