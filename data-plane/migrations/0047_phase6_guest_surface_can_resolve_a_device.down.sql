-- 0047 DOWN — take the device-resolution grants back off svc_scd.
--
-- WHAT THIS REINSTATES, SAID PLAINLY. With these revoked, svc_scd holds SELECT on iam_v2.devices and nothing
-- more, which is the 0033 state — and in that state the Phase-6 guest surface cannot resolve the device that
-- is asking, so every list and every release answers UNAVAILABLE with "permission denied for table devices"
-- in the log. That is the defect 0047 fixed, deliberately restored, because a down migration that quietly
-- kept the fix would make the rollback rehearsal a fiction.
--
-- It is safe to run only while the Phase-6 guest surface is dark, which is where the phase currently is. On an
-- appliance actually serving the capability this is not a rollback, it is an outage.
\set ON_ERROR_STOP on

REVOKE SELECT ON iam_v2.service_plan_revisions FROM svc_scd;
REVOKE SELECT ON iam_v2.entitlements FROM svc_scd;
REVOKE UPDATE (mac, last_seen, last_ip) ON iam_v2.devices FROM svc_scd;
REVOKE INSERT ON iam_v2.devices FROM svc_scd;

COMMENT ON TABLE iam_v2.devices IS NULL;
