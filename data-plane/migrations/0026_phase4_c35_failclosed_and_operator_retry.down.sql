-- Reverse 0026. The 0025 gate body is restored verbatim -- the version that accepted an archive with no
-- external receipt -- because that is what reversing this migration means, even though 0026 exists
-- precisely because self-certification is not a compliance archive. Recorded receipt EVIDENCE is kept: a
-- row that says an authority acknowledged custody is a fact about the outside world, and dropping the
-- columns would delete it.
BEGIN;
DROP VIEW IF EXISTS iam_v2.v_zero_attempt_recovery_queue;
DROP FUNCTION IF EXISTS iam_v2.p4_record_compliance_receipt(uuid,text,text);

CREATE OR REPLACE FUNCTION iam_v2.p4_assert_compliance_archived(p_tenant uuid)
RETURNS void
  LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM iam_v2.compliance_archives
                  WHERE tenant_id = p_tenant AND purpose = 'CROSS_CUSTOMER_PURGE') THEN
    RAISE EXCEPTION 'COMPLIANCE_ARCHIVE_MISSING: tenant % has no compliance archive; its data may not be '
                    'purged until one has been produced and its digest recorded', p_tenant
      USING ERRCODE = 'check_violation';
  END IF;
END $fn$;
REVOKE EXECUTE ON FUNCTION iam_v2.p4_assert_compliance_archived(uuid) FROM PUBLIC;

ALTER TABLE iam_v2.compliance_archives DROP CONSTRAINT IF EXISTS ca_receipt_evidence_matches_flag;
DELETE FROM public.schema_migrations WHERE version = '0026_phase4_c35_failclosed_and_operator_retry';
COMMIT;
