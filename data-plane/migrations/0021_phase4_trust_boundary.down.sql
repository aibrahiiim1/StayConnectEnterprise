-- Reverse 0021: hand the primitives back and drop the high-level operations. This restores the 0018/0020
-- posture, which is exactly what "reverse" means even though 0021 exists because that posture was too wide.
BEGIN;
REVOKE EXECUTE ON FUNCTION iam_v2.p4_grant_paid_entitlement(uuid,uuid,uuid) FROM sc_payment_runtime;
REVOKE EXECUTE ON FUNCTION
  iam_v2.p4_apply_provider_outcome(text,text,text,text,text,jsonb) FROM sc_payment_runtime;
DROP FUNCTION IF EXISTS iam_v2.p4_grant_paid_entitlement(uuid,uuid,uuid);
DROP FUNCTION IF EXISTS iam_v2.p4_apply_provider_outcome(text,text,text,text,text,jsonb);
DROP FUNCTION IF EXISTS iam_v2.p4_assert_financial_actor(uuid,uuid);
GRANT EXECUTE ON FUNCTION
  iam_v2.apply_payment_callback_v2(uuid,text,uuid,text,text,text,text,text,jsonb) TO sc_payment_runtime;
GRANT EXECUTE ON FUNCTION iam_v2.p4_insert_entitlement(
  uuid,uuid,uuid,uuid,uuid,uuid,jsonb,uuid,uuid,text,text,timestamptz,uuid) TO sc_payment_runtime;
GRANT EXECUTE ON FUNCTION
  iam_v2.p4_terminate_live_entitlement_for_subject(uuid,uuid,uuid,uuid,uuid) TO sc_payment_runtime;
GRANT EXECUTE ON FUNCTION iam_v2.p4_mark_purchase_granted(uuid) TO sc_payment_runtime;
DELETE FROM public.schema_migrations WHERE version = '0021_phase4_trust_boundary';
COMMIT;
