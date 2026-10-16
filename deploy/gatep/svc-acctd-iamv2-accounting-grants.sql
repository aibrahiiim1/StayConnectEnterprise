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
-- DERIVED STATEMENT BY STATEMENT from cmd/acctd and internal/enforce (audit B-7, migration 0124). Every write
-- acctd performs goes through a SECURITY DEFINER writer, so it holds NO table write in iam_v2 -- which is what
-- migration 0039 said and asserted, and what this file had drifted away from (INSERT/UPDATE on five accounting
-- tables, EXECUTE on the device-authorization and boundary-termination writers). gatep-grants.sql re-asserts
-- 0039's shape after every reconcile, so the drift cannot recur silently.

GRANT USAGE ON SCHEMA iam_v2 TO svc_acctd;

-- ---- what it reads ----------------------------------------------------------------------------------------
--   sessions, entitlements, service_plan_revisions   the enforcement plan (PlanForSite), the expiry candidates
--                                                    (EnforceExpiries) and the live-session list the counters
--                                                    are keyed by.
--   accounting_records, session_entitlement_bindings the pre-Phase-6 data-crossing expression, used only on a
--                                                    schema without iam_v2.p6_data_crossing (enforce.go
--                                                    dataCrossingLegacy). Read-only.
-- acctd must never alter what a package or plan IS, so every one of these is SELECT.
GRANT SELECT ON iam_v2.sessions                     TO svc_acctd;
GRANT SELECT ON iam_v2.entitlements                 TO svc_acctd;
GRANT SELECT ON iam_v2.service_plan_revisions       TO svc_acctd;
GRANT SELECT ON iam_v2.accounting_records           TO svc_acctd;
GRANT SELECT ON iam_v2.session_entitlement_bindings TO svc_acctd;

-- ---- the controlled writers it drives -------------------------------------
-- begin_controlled_operation: the startup capability check (writerguard.verifyCapability) requires it.
GRANT EXECUTE ON FUNCTION iam_v2.begin_controlled_operation(text)   TO svc_acctd;
-- The absolute counter ingest, once per live session per tick (cmd/acctd/phase3_accounting.go).
GRANT EXECUTE ON FUNCTION iam_v2.ingest_absolute_counters(uuid, uuid, uuid, uuid, text, integer, bigint, bigint, bigint, timestamptz) TO svc_acctd;

-- The DATA-quota crossing the expiry sweep derives for every candidate it considers.
--
-- THIS GRANT HAS TO LIVE HERE, NOT IN THE MIGRATION THAT CREATED THE FUNCTION. Migration 0041 does grant it,
-- and that grant is real — until the next Gate-P reconcile, whose preamble REVOKEs ALL PRIVILEGES ON ALL
-- FUNCTIONS IN SCHEMA iam_v2 from every service role before the per-service files re-grant. A privilege named
-- only in a migration is therefore removed by the first reconcile after it and never comes back, which is why
-- the live appliance logged "permission denied for function p6_data_crossing" on every sweep — roughly once a
-- second, indefinitely, with the expiry sweep failing before it could evaluate a single candidate.
--
-- Read-only and derivation-only: it computes when an entitlement's data allowance is crossed and writes
-- nothing. It enables no Phase-6 capability that migration 0041 did not already intend.
--
-- scripts/gatep-grant-survives-reconcile.sh is the regression check, and it asserts both halves: that this
-- privilege survives a reconcile, and that a migration-only grant does not.
GRANT EXECUTE ON FUNCTION iam_v2.p6_data_crossing(uuid) TO svc_acctd;

-- The rest of the Phase-6 expiry sweep, here for the same reason and proven the hard way: the expiry writer,
-- the fail-closed suspension and the aggregate online-time tick (enforce.go EnforceExpiries). They are listed
-- together because they are one operation; enumerated explicitly rather than granted by wildcard so each one
-- remains a decision.
GRANT EXECUTE ON FUNCTION iam_v2.p6_expire_entitlement(uuid) TO svc_acctd;
GRANT EXECUTE ON FUNCTION iam_v2.p6_suspend_over_budget(uuid, uuid) TO svc_acctd;
GRANT EXECUTE ON FUNCTION iam_v2.p6_tick_online_time(
  uuid, uuid, timestamptz, int, uuid[], timestamptz[]) TO svc_acctd;

-- ---- what acctd used to hold and does not use (removed by audit B-7) --------------------------------------
--   INSERT, UPDATE on accounting_records, accounting_checkpoints, delayed_accounting_records,
--                     entitlement_boundary_watermarks, session_entitlement_bindings
--                     -- every accounting write is ingest_absolute_counters', as its owner; a raw write to
--                        these tables is refused by the controlled-writer guard anyway
--   SELECT on accounting_checkpoints, delayed_accounting_records, entitlement_boundary_watermarks, devices,
--             service_plans, internet_packages, internet_package_revisions, purchases, stays, pms_interfaces,
--             session_online_watermarks, entitlement_devices, entitlement_termination_evidence
--                     -- no acctd statement reads them; the definer tick and expiry writer read them as owner
--   EXECUTE on register_class_origin (netd's), apply_entitlement_transition, authorize_entitlement_device,
--              rebind_session_entitlement, entitlement_usage_bytes, p6_due_terminal,
--              terminate_entitlement_at_boundary, p3/p5_controlled_operation_open
--                     -- none is called by acctd. terminate_entitlement_at_boundary was the pre-0041 expiry
--                        fallback (enforce.go, phase6Writers=false); that path also needs UPDATE on the
--                        device-authorization tables, which acctd has never held, so it could not complete as
--                        this role, and 0039 named the function as one acctd must not execute.
--
-- NOT granted: DELETE on anything, and no write to packages, plans or their revisions. Metering must never
-- be able to change what was sold.
