-- Reverse 0039: take back exactly what it granted.
--
-- The role itself is NOT dropped. 0039 creates it only if it was absent, and a deployment may have granted
-- it LOGIN, a password and privileges of its own; dropping a role this migration may not have created would
-- reach outside what it did. Revoking its Phase-6 reach is the faithful reverse.
BEGIN;

REVOKE EXECUTE ON FUNCTION iam_v2.p6_tick_online_time(uuid, uuid, timestamptz, int, uuid[], timestamptz[])
  FROM svc_acctd;

REVOKE SELECT ON iam_v2.entitlements              FROM svc_acctd;
REVOKE SELECT ON iam_v2.service_plan_revisions    FROM svc_acctd;
REVOKE SELECT ON iam_v2.sessions                  FROM svc_acctd;
REVOKE SELECT ON iam_v2.session_online_watermarks FROM svc_acctd;

COMMIT;
