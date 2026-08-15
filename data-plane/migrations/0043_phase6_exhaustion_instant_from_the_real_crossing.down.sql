-- Reverse 0043: restore the 0042 derivation, which dates exhaustion at the EARLIEST related adjustment.
--
-- FAITHFUL, AND THEREFORE IT REINSTATES THE BACKDATING: an entitlement adjusted harmlessly before the
-- adjustment that actually spent its budget is dated to the harmless one, and every session end time hangs
-- off that instant. Disable the aggregate mode before rolling back.
BEGIN;

CREATE OR REPLACE FUNCTION iam_v2.p6_exhaustion_instant(p_entitlement uuid)
RETURNS timestamptz
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = iam_v2, pg_temp
AS $$
DECLARE
  e        record;
  v_budget bigint;
  v_adj    timestamptz;
BEGIN
  SELECT id, time_accounting_mode, consumed_online_seconds, online_time_exhausted_at, service_plan_revision_id
    INTO e FROM iam_v2.entitlements WHERE id = p_entitlement;
  IF NOT FOUND OR e.time_accounting_mode <> 'AGGREGATE_ONLINE_TIME' THEN
    RETURN NULL;
  END IF;
  -- (1) the authoritative case: the tick computed the crossing inside the tick that crossed it.
  IF e.online_time_exhausted_at IS NOT NULL THEN
    RETURN e.online_time_exhausted_at;
  END IF;

  SELECT spr.time_quota_seconds INTO v_budget
    FROM iam_v2.service_plan_revisions spr WHERE spr.id = e.service_plan_revision_id;
  IF v_budget IS NULL OR COALESCE(e.consumed_online_seconds, 0) < v_budget THEN
    RETURN NULL;   -- not exhausted at all
  END IF;

  -- (2) an audited adjustment that moved the counter or the plan the budget comes from. The earliest one
  --     that could have produced this state is the provable instant: the record says the state changed then.
  SELECT min(a.created_at) INTO v_adj
    FROM iam_v2.entitlement_adjustments a
   WHERE a.entitlement_id = p_entitlement
     AND a.field IN ('consumed_online_seconds', 'time_quota_seconds', 'service_plan_revision_id');
  IF v_adj IS NOT NULL THEN
    RETURN v_adj;
  END IF;

  -- (3) exhausted with nothing to date it by. NOTHING is claimed.
  RAISE WARNING 'entitlement % is at or over its online-time budget with no provable exhaustion instant; '
                'it will not be terminated for TIME until one exists', p_entitlement;
  RETURN NULL;
END $$;

COMMENT ON FUNCTION iam_v2.p6_exhaustion_instant(uuid) IS
  'The instant an AGGREGATE_ONLINE_TIME budget ran out (0042 behaviour, restored by the 0043 down migration).';

REVOKE ALL ON FUNCTION iam_v2.p6_exhaustion_instant(uuid) FROM PUBLIC;

COMMIT;
