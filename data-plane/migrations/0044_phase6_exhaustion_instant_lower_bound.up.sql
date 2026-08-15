-- PHASE 6 M4 — the adjustment history is a LOWER BOUND on consumption, not a reconstruction of it.
--
-- WHAT 0043 CLAIMED, AND WHY IT WAS TOO STRONG. It said the state at every point in the history is knowable
-- because both counters move only through audited adjustments. That is true of the BUDGET. It is not true of
-- consumption: ordinary accrual advances consumed_online_seconds inside the tick, and the tick writes no
-- adjustment row. So walking the adjustments does not tell you what consumption WAS at a historical instant;
-- it tells you the last value anybody wrote down.
--
-- WHAT IS ACTUALLY TRUE, and what this function now relies on:
--
--   * consumption only ever INCREASES outside of an audited adjustment (accrual adds; a decrease is only
--     expressible through entitlement_adjustments, which the counter guard enforces). So the value carried
--     forward from the last adjustment is a LOWER BOUND on the true consumption at any later instant.
--   * a lower bound that has ALREADY reached the budget proves exhaustion at that instant. The true value was
--     at least that, so the entitlement was at or over budget then, whatever accrual did in between.
--   * a lower bound BELOW the budget proves nothing at all. The true consumption may have been higher --
--     accrual is invisible here -- so a budget cut under it cannot be dated from this evidence.
--
-- THE ASYMMETRY IS THE WHOLE POINT. It licenses exactly one direction of conclusion, and the direction it
-- licenses is the one that cannot invent history. Where the evidence is insufficient the answer is NULL, the
-- entitlement is not terminated for TIME, and it remains subject to its window and its data quota. An
-- accounting system may under-claim; it may not make things up.
--
-- Also: the seed of the running budget used to cast old_value OUTSIDE the protected parse path, so a
-- malformed value RAISED instead of failing safe -- and it raised inside the expiry sweep, which would have
-- aborted the whole site's pass rather than skipping one unreadable entitlement.
BEGIN;

CREATE OR REPLACE FUNCTION iam_v2.p6_exhaustion_instant(p_entitlement uuid)
RETURNS timestamptz
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = iam_v2, pg_temp
AS $$
DECLARE
  e            record;
  a            record;
  v_budget     bigint;      -- the budget as it stands now
  v_cons_floor bigint;      -- LOWER BOUND on consumption, from the last adjustment that recorded it
  v_run_bud    bigint;      -- the budget as it stood while walking
  v_val        bigint;
  v_has_budget_history boolean;
BEGIN
  SELECT id, time_accounting_mode, consumed_online_seconds, online_time_exhausted_at, service_plan_revision_id
    INTO e FROM iam_v2.entitlements WHERE id = p_entitlement;
  IF NOT FOUND OR e.time_accounting_mode <> 'AGGREGATE_ONLINE_TIME' THEN
    RETURN NULL;
  END IF;
  -- (1) THE AUTHORITATIVE CASE. The tick computes the crossing inside the tick that crosses it, so an
  --     entitlement that ran out through ordinary accrual always has this. Its absence is precisely what
  --     tells us accrual did NOT cross it, which is why the reasoning below is allowed at all.
  IF e.online_time_exhausted_at IS NOT NULL THEN
    RETURN e.online_time_exhausted_at;
  END IF;

  SELECT spr.time_quota_seconds INTO v_budget
    FROM iam_v2.service_plan_revisions spr WHERE spr.id = e.service_plan_revision_id;
  IF v_budget IS NULL OR COALESCE(e.consumed_online_seconds, 0) < v_budget THEN
    RETURN NULL;   -- not exhausted at all
  END IF;

  -- (2) SEED THE RUNNING BUDGET FROM THE START OF THE HISTORY, inside the protected path. A malformed
  --     old_value must fail safe like every other unreadable value -- raising here would abort the sweep for
  --     every entitlement on the site, not just this one.
  SELECT EXISTS (SELECT 1 FROM iam_v2.entitlement_adjustments adj
                  WHERE adj.entitlement_id = p_entitlement
                    AND adj.field IN ('time_quota_seconds', 'service_plan_revision_id'))
    INTO v_has_budget_history;

  IF v_has_budget_history THEN
    BEGIN
      SELECT CASE WHEN first_adj.field = 'time_quota_seconds'
                  THEN NULLIF(first_adj.old_value, '')::bigint
                  ELSE (SELECT spr.time_quota_seconds FROM iam_v2.service_plan_revisions spr
                         WHERE spr.id = NULLIF(first_adj.old_value, '')::uuid) END
        INTO v_run_bud
        FROM (SELECT adj.field, adj.old_value FROM iam_v2.entitlement_adjustments adj
               WHERE adj.entitlement_id = p_entitlement
                 AND adj.field IN ('time_quota_seconds', 'service_plan_revision_id')
               ORDER BY adj.created_at, adj.id LIMIT 1) first_adj;
    EXCEPTION WHEN others THEN
      RETURN NULL;   -- unreadable seed: nothing is claimed, and the sweep continues
    END;
    IF v_run_bud IS NULL THEN
      RETURN NULL;   -- a budget history we cannot anchor is a history we cannot reason from
    END IF;
  ELSE
    v_run_bud := v_budget;   -- the budget was never adjusted, so it has always been the current one
  END IF;

  -- (3) WALK. v_cons_floor stays NULL until an adjustment records a consumption value; until then there is no
  --     lower bound at all and no crossing can be claimed, however large the current consumption is.
  v_cons_floor := NULL;

  FOR a IN
    SELECT adj.field, adj.old_value, adj.new_value, adj.created_at
      FROM iam_v2.entitlement_adjustments adj
     WHERE adj.entitlement_id = p_entitlement
       AND adj.field IN ('consumed_online_seconds', 'time_quota_seconds', 'service_plan_revision_id')
     ORDER BY adj.created_at, adj.id
  LOOP
    BEGIN
      IF a.field = 'consumed_online_seconds' THEN
        v_val := NULLIF(a.new_value, '')::bigint;
        IF v_val IS NULL THEN RETURN NULL; END IF;
        v_cons_floor := v_val;
      ELSIF a.field = 'time_quota_seconds' THEN
        v_val := NULLIF(a.new_value, '')::bigint;
        IF v_val IS NULL THEN RETURN NULL; END IF;
        v_run_bud := v_val;
      ELSE
        SELECT spr.time_quota_seconds INTO v_val FROM iam_v2.service_plan_revisions spr
         WHERE spr.id = NULLIF(a.new_value, '')::uuid;
        IF v_val IS NULL THEN RETURN NULL; END IF;
        v_run_bud := v_val;
      END IF;
    EXCEPTION WHEN others THEN
      RETURN NULL;   -- unreadable history: nothing is claimed
    END;

    -- THE ONE CONCLUSION THE EVIDENCE LICENSES. The lower bound has already reached the budget as it stood
    -- here, so the true consumption -- which is at least the bound -- had too.
    IF v_cons_floor IS NOT NULL AND v_run_bud IS NOT NULL AND v_cons_floor >= v_run_bud THEN
      RETURN a.created_at;
    END IF;
  END LOOP;

  -- (4) Exhausted now, and no instant in the history can be shown to be the crossing. Most often this is an
  --     entitlement whose consumption came from ordinary accrual -- invisible here -- followed by a budget
  --     cut: the cut may well be the moment it ran out, and the evidence cannot show that it is.
  RAISE WARNING 'entitlement % is at or over its online-time budget and its audited history cannot prove '
                'when it crossed; it will not be terminated for TIME until something can', p_entitlement;
  RETURN NULL;
END $$;

COMMENT ON FUNCTION iam_v2.p6_exhaustion_instant(uuid) IS
  'The instant an AGGREGATE_ONLINE_TIME budget ran out, from evidence only: the tick''s stamp, else the '
  'first instant at which the adjustment-recorded LOWER BOUND on consumption had already reached the budget '
  'as it then stood, else NULL. Ordinary accrual writes no adjustment, so the history bounds consumption '
  'from below and can never be used to argue that a budget cut found the entitlement under budget.';

REVOKE ALL ON FUNCTION iam_v2.p6_exhaustion_instant(uuid) FROM PUBLIC;

COMMIT;
