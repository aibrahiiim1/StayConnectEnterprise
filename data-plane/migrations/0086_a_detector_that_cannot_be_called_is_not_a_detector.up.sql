-- THE DETECTOR HAD NO CALLER; THEN IT HAD A CALLER THAT COULD NOT CALL IT.
--
-- Migration 0023 exists to notice one thing: that this database is OLDER than the appliance knows it should
-- be, because it was restored. iam_v2.p4_reconcile_financial_epoch_v2 is where that decision is made, and
-- payment.ReconcileEpoch is the process half of it. Until this delivery nothing in any running service
-- called it at all. Wiring it into edged's startup fixed the first half of that and left the second:
--
--   EDGED_DB_URL on the appliance authenticates as svc_edged, verified by reading /etc/stayconnect/edged.env
--   on PRE-LIVE 172.21.60.25. The function is REVOKEd from PUBLIC and granted only to sc_payment_runtime
--   (migration 0017), and svc_edged holds no membership in that role. Probed live:
--
--       has_function_privilege('svc_edged','iam_v2.p4_reconcile_financial_epoch_v2(...)','EXECUTE')  -> f
--       pg_has_role('svc_edged','sc_payment_runtime','USAGE')                                        -> f
--
-- So the reconcile returned permission denied on every startup. While Phase 4 is DARK that is a log line
-- and nothing else -- which is the posture the caller was written for -- but the detector still did not
-- run, and once transmission were ever enabled the same failure would have exited edged on every boot.
-- A detector nobody can call is exactly as useful as a detector nobody calls.
--
-- WHY THE NARROW EXECUTE RATHER THAN A PAYMENT-RUNTIME CONNECTION. 0017 says plainly that switching the
-- payment service to sc_payment_runtime "is a deployment step that is deliberately not taken while Phase 4
-- is DARK", and it needs more than a DSN change. This grant is the established shape in this schema
-- instead: a SECURITY DEFINER kernel with a pinned search_path, and one EXECUTE to the one role that has to
-- reach it. svc_edged gains no table privilege, no other function, and no way to move money.
--
-- WHAT A COMPROMISED CALLER COULD DO WITH IT, stated rather than waved away. The function takes the system
-- identity and the marker generation as PARAMETERS, so a caller can lie about them. Every lie it can tell
-- lands on the safe side or changes nothing:
--
--   a different identity, a higher generation, a missing marker  -> RECOVERY, which HOLDS money movement
--   the true values                                              -> the correct answer
--   the stored values when a restore really happened             -> detection avoided
--
-- The last one is the only harmful case, and it is available to any caller ALREADY, by simply not calling
-- the function -- which is precisely the state this delivery found the system in. The grant therefore adds
-- no capability an attacker did not have by omission, and removes the failure mode where an honest caller
-- cannot report an honest answer. Nothing here releases a hold: the function's only outcomes are
-- INITIALIZED, UNCHANGED, RECOVERY_ACTIVE and RECOVERY_ENTERED, and the release path lives in the operator
-- surface behind its own controlled operation.
--
-- NOT ENABLED BY THIS: no financial traffic, no transmission, no provider, no settlement. Phase 4 remains
-- DARK. This lets a detector that can only ever hold money actually run.

BEGIN;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
    GRANT EXECUTE ON FUNCTION
      iam_v2.p4_reconcile_financial_epoch_v2(uuid, uuid, text, bigint, boolean) TO svc_edged;
  END IF;
END $$;

-- The boundary this must NOT have widened: svc_edged still holds nothing on the financial tables, and the
-- membership it does not have, it still does not have.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
    IF pg_has_role('svc_edged', 'sc_payment_runtime', 'USAGE') THEN
      RAISE EXCEPTION 'svc_edged became a member of sc_payment_runtime; this migration grants ONE function, '
                      'not a financial role';
    END IF;
    IF has_table_privilege('svc_edged', 'iam_v2.financial_epochs', 'UPDATE')
       OR has_table_privilege('svc_edged', 'iam_v2.financial_epochs', 'INSERT')
       OR has_table_privilege('svc_edged', 'iam_v2.financial_epochs', 'DELETE') THEN
      RAISE EXCEPTION 'svc_edged can write iam_v2.financial_epochs directly, which would let it move an '
                      'epoch without the kernel that decides whether it should move';
    END IF;
  END IF;
END $$;

INSERT INTO public.schema_migrations (version)
  VALUES ('0086_a_detector_that_cannot_be_called_is_not_a_detector')
  ON CONFLICT (version) DO NOTHING;

COMMIT;
