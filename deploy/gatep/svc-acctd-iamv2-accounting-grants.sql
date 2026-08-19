-- svc_acctd: the accounting and enforcement-planning surface.
--
-- Found under LIVE traffic, not by reading code: with a real IAM-v2 session active and shaped, acctd logged
--
--   "phase3: absolute counter observation refused ... permission denied for function
--    ingest_absolute_counters"
--
-- once per second, and iam_v2.accounting_records stayed at 0. The session was genuinely online and genuinely
-- metered by the kernel, and none of it was being recorded -- the exact shape of failure where a health
-- surface reports fine for a path that is not actually writing. acctd does not crash on it, so nothing
-- surfaced it except looking at the log while traffic was flowing.
--
-- (ingest_absolute_counters was granted to svc_netd earlier because netd references it too. netd is not the
-- caller here; acctd is. Granting the service that mentions a function rather than the one that calls it is
-- how this stayed missing.)
--
-- Derived from the iam_v2 objects referenced in cmd/acctd.

GRANT USAGE ON SCHEMA iam_v2 TO svc_acctd;

-- ---- accounting: the records acctd owns -----------------------------------
GRANT SELECT, INSERT, UPDATE ON iam_v2.accounting_records         TO svc_acctd;
GRANT SELECT, INSERT, UPDATE ON iam_v2.accounting_checkpoints     TO svc_acctd;
GRANT SELECT, INSERT, UPDATE ON iam_v2.delayed_accounting_records TO svc_acctd;
GRANT SELECT, INSERT, UPDATE ON iam_v2.entitlement_boundary_watermarks TO svc_acctd;
GRANT SELECT, INSERT, UPDATE ON iam_v2.session_entitlement_bindings    TO svc_acctd;

-- ---- what it plans and meters against (read only) -------------------------
-- The enforcement plan joins sessions to entitlements and their pinned revisions. acctd must never alter
-- what a package or plan IS, so every one of these is SELECT.
GRANT SELECT ON iam_v2.sessions                  TO svc_acctd;
GRANT SELECT ON iam_v2.entitlements              TO svc_acctd;
GRANT SELECT ON iam_v2.devices                   TO svc_acctd;
GRANT SELECT ON iam_v2.service_plans             TO svc_acctd;
GRANT SELECT ON iam_v2.service_plan_revisions    TO svc_acctd;
GRANT SELECT ON iam_v2.internet_packages         TO svc_acctd;
GRANT SELECT ON iam_v2.internet_package_revisions TO svc_acctd;
GRANT SELECT ON iam_v2.purchases                 TO svc_acctd;
GRANT SELECT ON iam_v2.stays                     TO svc_acctd;
GRANT SELECT ON iam_v2.pms_interfaces            TO svc_acctd;

-- ---- the controlled writers it drives -------------------------------------
GRANT EXECUTE ON FUNCTION iam_v2.begin_controlled_operation(text)   TO svc_acctd;
GRANT EXECUTE ON FUNCTION iam_v2.p3_controlled_operation_open(text) TO svc_acctd;
GRANT EXECUTE ON FUNCTION iam_v2.p5_controlled_operation_open(text) TO svc_acctd;
GRANT EXECUTE ON FUNCTION iam_v2.ingest_absolute_counters(uuid, uuid, uuid, uuid, text, integer, bigint, bigint, bigint, timestamptz) TO svc_acctd;
GRANT EXECUTE ON FUNCTION iam_v2.register_class_origin(uuid, uuid, uuid, uuid, text, integer, bigint, bigint, bigint, timestamptz) TO svc_acctd;
GRANT EXECUTE ON FUNCTION iam_v2.apply_entitlement_transition(uuid, text, timestamptz, text) TO svc_acctd;
GRANT EXECUTE ON FUNCTION iam_v2.authorize_entitlement_device(uuid, uuid, timestamptz) TO svc_acctd;
GRANT EXECUTE ON FUNCTION iam_v2.rebind_session_entitlement(uuid, uuid, timestamptz) TO svc_acctd;
GRANT EXECUTE ON FUNCTION iam_v2.terminate_entitlement_at_boundary(uuid, timestamptz, text) TO svc_acctd;
GRANT EXECUTE ON FUNCTION iam_v2.entitlement_usage_bytes(uuid, timestamptz) TO svc_acctd;

-- NOT granted: DELETE on anything, and no write to packages, plans or their revisions. Metering must never
-- be able to change what was sold.
