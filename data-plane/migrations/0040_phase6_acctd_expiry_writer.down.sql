-- Reverse 0040. The sweep then requires an identity that may call the primitives directly again -- which is
-- how it ran before Phase 6, and why the aggregate mode must be OFF before rolling back.
BEGIN;

REVOKE SELECT ON iam_v2.accounting_records               FROM svc_acctd;
REVOKE SELECT ON iam_v2.session_entitlement_bindings     FROM svc_acctd;
REVOKE SELECT ON iam_v2.entitlement_devices              FROM svc_acctd;
REVOKE SELECT ON iam_v2.entitlement_termination_evidence FROM svc_acctd;

DROP FUNCTION IF EXISTS iam_v2.p6_expire_entitlement(uuid, timestamptz, text, text);

COMMIT;
