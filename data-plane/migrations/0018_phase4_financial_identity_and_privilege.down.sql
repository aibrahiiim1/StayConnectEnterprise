-- Reverse 0018. Grants first, then the objects they name, then the table the FK depends on: dropping in
-- the other order leaves a role holding a privilege on something that no longer exists.
BEGIN;

REVOKE ALL ON iam_v2.v_financial_payments, iam_v2.v_financial_settlements, iam_v2.v_financial_review_queue
  FROM sc_financial_readonly, sc_financial_operator;
-- 0017's posture for this role was SELECT on the whole schema. Restoring it is what "reverse" means, even
-- though 0018 exists precisely because that posture was too wide.
GRANT SELECT ON ALL TABLES IN SCHEMA iam_v2 TO sc_financial_readonly;

REVOKE EXECUTE ON FUNCTION
  iam_v2.record_posting_review_action(uuid,text,uuid,text,jsonb,int,bigint) FROM sc_financial_operator;

DROP VIEW IF EXISTS iam_v2.v_financial_review_queue;
DROP VIEW IF EXISTS iam_v2.v_financial_settlements;
DROP VIEW IF EXISTS iam_v2.v_financial_payments;

DROP FUNCTION IF EXISTS iam_v2.p4_mark_purchase_granted(uuid);
DROP FUNCTION IF EXISTS iam_v2.p4_insert_entitlement(
  uuid,uuid,uuid,uuid,uuid,uuid,jsonb,uuid,uuid,text,text,timestamptz,uuid);
DROP FUNCTION IF EXISTS iam_v2.p4_terminate_live_entitlement_for_subject(uuid,uuid,uuid,uuid,uuid);
DROP FUNCTION IF EXISTS iam_v2.p4_resolve_payment_account(uuid,uuid);

DROP TRIGGER IF EXISTS p4_payment_identity_gate ON iam_v2.payment_transactions;
DROP FUNCTION IF EXISTS iam_v2.p4_payment_identity_gate();
ALTER TABLE iam_v2.payment_transactions DROP CONSTRAINT IF EXISTS ptx_merchant_account_configured;

DROP TABLE IF EXISTS iam_v2.payment_provider_accounts;

DELETE FROM public.schema_migrations WHERE version = '0018_phase4_financial_identity_and_privilege';
COMMIT;
