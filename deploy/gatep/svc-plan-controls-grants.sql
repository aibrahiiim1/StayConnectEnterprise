-- Plan controls and accounting retention (migration 0122; audit remediation stream 5, Product-Owner decisions
-- D-C and D-H). Idempotent; included by gatep-grants.sql after its revoke, so a Gate-P reconcile reproduces
-- exactly what the migration granted.
--
--   svc_acctd  runs, on its tick, the three operations that enforce what a plan says and keep the sweep cheap:
--              ending sessions over the plan's idle timeout / maximum continuous session, advancing the
--              data-quota crossing checkpoint, and the accounting-detail retention purge (which does nothing while
--              the site's retention is unlimited, the default). EXECUTE only -- no table privilege is added, and
--              DELETE on accounting_records stays ungranted: only the purge's own definer can remove a record.
--   svc_edged  reads and changes the retention setting through its definer functions (operator and reason
--              required, every change logged), and reads the purge runs and the DISCONNECT_OLDEST record.
--
-- DISCONNECT_OLDEST itself needs no new grant: it lives inside iam_v2.authorize_entitlement_device, which svc_scd
-- and svc_acctd already execute.
--
-- GUARDED on the migration being present: a reconcile run after 0122 was rolled back must not fail on a grant of
-- something that no longer exists (the 0116-0118 files are not guarded, and that is a known reconcile trap).
DO $g$
BEGIN
  IF to_regprocedure('iam_v2.p6_end_sessions_over_plan_limits(uuid,uuid,timestamptz,boolean)') IS NULL THEN
    RAISE NOTICE 'migration 0122 is not applied; plan-control grants skipped';
    RETURN;
  END IF;
  GRANT EXECUTE ON FUNCTION iam_v2.p6_end_sessions_over_plan_limits(uuid,uuid,timestamptz,boolean) TO svc_acctd;
  GRANT EXECUTE ON FUNCTION iam_v2.p6_advance_data_crossing(uuid,uuid) TO svc_acctd;
  GRANT EXECUTE ON FUNCTION iam_v2.purge_accounting_detail(uuid,uuid,integer) TO svc_acctd;

  GRANT EXECUTE ON FUNCTION iam_v2.accounting_retention_get(uuid,uuid) TO svc_edged;
  GRANT EXECUTE ON FUNCTION iam_v2.accounting_retention_set(uuid,uuid,integer,text,text) TO svc_edged;
  GRANT SELECT ON iam_v2.accounting_retention_changes, iam_v2.accounting_purge_runs,
                  iam_v2.device_limit_disconnections TO svc_edged;
END $g$;
