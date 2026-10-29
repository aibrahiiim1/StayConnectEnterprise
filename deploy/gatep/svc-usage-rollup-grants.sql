-- Usage rollup and purge budget (migration 0175; audit AD-48). Idempotent; included by gatep-grants.sql after its revoke,
-- so a Gate-P reconcile reproduces what the migration granted. GUARDED on the migration being present.
--   svc_acctd  runs the rollup on its tick (EXECUTE only; the purge it already runs is purge_accounting_detail, granted
--              in svc-plan-controls-grants.sql). No table privilege is added.
--   svc_edged  reads the per-minute / per-hour totals (the Overview and Usage), and reads and changes the rollup
--              settings through the definer functions (operator and reason required, every change logged).
DO $g$
BEGIN
  IF to_regprocedure('iam_v2.rollup_accounting_usage(uuid,uuid,interval)') IS NULL THEN
    RAISE NOTICE 'migration 0175 is not applied; usage-rollup grants skipped';
    RETURN;
  END IF;
  GRANT EXECUTE ON FUNCTION iam_v2.rollup_accounting_usage(uuid,uuid,interval) TO svc_acctd;
  GRANT EXECUTE ON FUNCTION iam_v2.usage_rollup_settings_get(uuid,uuid) TO svc_edged;
  GRANT EXECUTE ON FUNCTION iam_v2.usage_rollup_settings_set(uuid,uuid,integer,integer,integer,text,text) TO svc_edged;
  GRANT SELECT ON iam_v2.accounting_usage_minute, iam_v2.accounting_usage_hour TO svc_edged;
  GRANT SELECT ON iam_v2.site_usage_rollup_changes TO svc_edged;
END $g$;
