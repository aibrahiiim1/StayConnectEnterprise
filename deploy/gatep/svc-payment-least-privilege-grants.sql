-- Gate-P: explicit grants that replace a PUBLIC EXECUTE revoked by migration 0096.

-- LEAST PRIVILEGE FOR THE PAYMENT LOGINS (migration 0096). These nine definer functions were reachable through
-- PUBLIC. PUBLIC is revoked in 0096, and svc_payment / svc_payment_outcome gain nothing.
--
-- NARROWED TO THE CALLERS (audit B-7, migration 0124). 0096 handed all nine to all five service roles, "so a
-- reconcile keeps exactly the pre-0096 access" -- which preserved PUBLIC's breadth under another name: netd,
-- acctd and scd could each rewrite the PMS connection and roster-reconciliation settings (the _set functions)
-- and dispose of snapshot cases, although none of them has a statement that does. Each grant below is now held
-- only by a role whose binary calls the function:
--
--   pms_connection_settings_get       edged (PMS screen) and pmsd (internal/pmsd pg.go)
--   pms_connection_settings_set       edged (PMS screen)
--   pms_reconciliation_settings_get   edged (roster reconciliation screen)
--   pms_record_resync_coverage        pmsd (internal/pmsd pg.go)
--
-- scd keeps none of them: it links internal/pmsd only for its secret-sealing types and never builds the
-- connector's repository.
--
-- and nobody keeps pms_reconciliation_settings_set (edged writes through the versioned _set_checked, 0123),
-- pms_dispose_snapshot_cases, pms_roster_of_generation, pms_site_blocked_after_refusals or
-- p5_controlled_operation_open: no binary calls the first four, and the last is called only by the Phase-5
-- controlled-writer TRIGGER in the writer's context -- no runtime role writes a Phase-5 table directly.
GRANT EXECUTE ON FUNCTION iam_v2.pms_connection_settings_get(uuid,uuid,uuid) TO svc_edged;
GRANT EXECUTE ON FUNCTION iam_v2.pms_connection_settings_set(uuid,uuid,uuid,text,text,integer,integer,integer,integer,integer) TO svc_edged;
GRANT EXECUTE ON FUNCTION iam_v2.pms_reconciliation_settings_get(uuid,uuid) TO svc_edged;
GRANT EXECUTE ON FUNCTION iam_v2.pms_connection_settings_get(uuid,uuid,uuid) TO svc_pmsd;
GRANT EXECUTE ON FUNCTION iam_v2.pms_record_resync_coverage(uuid,uuid,uuid,bigint,text[],integer,integer,integer) TO svc_pmsd;

-- 0121: the stranded card-purchase sweep is an outcome, applied through the outcome credential. Guarded so a
-- reconcile still runs on a database rolled back below 0121.
DO $g$
BEGIN
  IF to_regprocedure('iam_v2.p4_fail_unstarted_card_purchases(uuid,uuid,integer)') IS NOT NULL
     AND EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'sc_payment_outcome') THEN
    GRANT EXECUTE ON FUNCTION iam_v2.p4_fail_unstarted_card_purchases(uuid, uuid, integer) TO sc_payment_outcome;
  END IF;
END $g$;
