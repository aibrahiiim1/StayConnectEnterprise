-- Withdrawing edged's EXECUTE on the restore-rollback detector.
--
-- WHAT THIS COSTS. edged's startup reconcile returns permission denied again, so migration 0023's
-- restore-rollback detection stops running in any live process. While Phase 4 is DARK that is a loud log
-- line and startup continues; with transmission enabled edged refuses to serve, which is the deliberate
-- posture and not a fault of this direction.
--
-- WHAT IT DOES NOT COST. No financial record changes. A site already in recovery STAYS in recovery: the
-- hold lives in iam_v2.financial_epochs and in the rails, not in this privilege.

BEGIN;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
    REVOKE EXECUTE ON FUNCTION
      iam_v2.p4_reconcile_financial_epoch_v2(uuid, uuid, text, bigint, boolean) FROM svc_edged;
  END IF;
END $$;

DELETE FROM public.schema_migrations
 WHERE version = '0086_a_detector_that_cannot_be_called_is_not_a_detector';

COMMIT;
