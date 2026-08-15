-- Reverse 0044: restore the 0043 derivation.
--
-- FAITHFUL, AND THEREFORE IT REINSTATES TWO WEAKNESSES: it treats the adjustment history as a reconstruction
-- of consumption rather than a lower bound on it, and it casts the seed value outside the protected parse
-- path so a malformed old_value aborts the sweep instead of failing safe. Disable the aggregate mode before
-- rolling back.
BEGIN;

CREATE OR REPLACE FUNCTION iam_v2.p6_exhaustion_instant(p_entitlement uuid)
RETURNS timestamptz
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = iam_v2, pg_temp
AS $$
DECLARE
  e          record;
  a          record;
  v_budget   bigint;      -- the budget as it stands NOW
  v_run_cons bigint;      -- running consumption while walking the adjustment history
  v_run_bud  bigint;      -- running budget, which adjustments can also move
  v_val      bigint;
BEGIN
  SELECT id, time_accounting_mode, consumed_online_seconds, online_time_exhausted_at, service_plan_revision_id
    INTO e FROM iam_v2.entitlements WHERE id = p_entitlement;
  IF NOT FOUND OR e.time_accounting_mode <> 'AGGREGATE_ONLINE_TIME' THEN
    RETURN NULL;
  END IF;
  -- (1) THE AUTHORITATIVE CASE: the tick computed the crossing inside the tick that crossed it.
  IF e.online_time_exhausted_at IS NOT NULL THEN
    RETURN e.online_time_exhausted_at;
  END IF;

  SELECT spr.time_quota_seconds INTO v_budget
    FROM iam_v2.service_plan_revisions spr WHERE spr.id = e.service_plan_revision_id;
  IF v_budget IS NULL OR COALESCE(e.consumed_online_seconds, 0) < v_budget THEN
    RETURN NULL;   -- not exhausted at all
  END IF;

  -- (2) RECONSTRUCT THE TIMELINE. Both counters move only through audited adjustments, and each row carries
  --     its own before and after, so the state at every point in the history is knowable. The crossing is the
  --     first adjustment after which consumption stands at or above the budget.
  --
  --     The walk starts from the EARLIEST recorded old_value of each counter rather than from zero: an
  --     entitlement whose history begins mid-life (an import, a restore) would otherwise appear to cross at
  --     its first adjustment regardless of what that adjustment did.
  v_run_cons := NULL;

  -- SEED THE RUNNING BUDGET FROM THE START OF THE HISTORY, not from today's value. Using the current budget
  -- as a stand-in is how the first version dated a crossing three hours early: consumption that was under the
  -- budget of the day was compared against a budget that was only cut later, so a perfectly ordinary
  -- adjustment looked like the moment the entitlement ran out. If no adjustment ever moved the budget, then
  -- it has always been the current one and that is the right value everywhere.
  SELECT CASE WHEN first_adj.field = 'time_quota_seconds'
              THEN NULLIF(first_adj.old_value, '')::bigint
              ELSE (SELECT spr.time_quota_seconds FROM iam_v2.service_plan_revisions spr
                     WHERE spr.id = NULLIF(first_adj.old_value, '')::uuid) END
    INTO v_run_bud
    FROM (SELECT adj.field, adj.old_value FROM iam_v2.entitlement_adjustments adj
           WHERE adj.entitlement_id = p_entitlement
             AND adj.field IN ('time_quota_seconds', 'service_plan_revision_id')
           ORDER BY adj.created_at, adj.id LIMIT 1) first_adj;
  IF v_run_bud IS NULL THEN
    v_run_bud := v_budget;
  END IF;

  FOR a IN
    SELECT adj.field, adj.old_value, adj.new_value, adj.created_at
      FROM iam_v2.entitlement_adjustments adj
     WHERE adj.entitlement_id = p_entitlement
       -- The budget moves in two ways: a direct quota adjustment, and a repoint to a different immutable
       -- plan revision. Both are audited, and both can be the moment consumption stopped being under budget.
       AND adj.field IN ('consumed_online_seconds', 'time_quota_seconds', 'service_plan_revision_id')
     ORDER BY adj.created_at, adj.id
  LOOP
    -- Seed each running value from the first row that mentions it, so the history is anchored in what the
    -- record actually says rather than in an assumption about where it began.
    IF a.field = 'consumed_online_seconds' THEN
      BEGIN
        IF v_run_cons IS NULL THEN v_run_cons := NULLIF(a.old_value, '')::bigint; END IF;
        v_val := NULLIF(a.new_value, '')::bigint;
      EXCEPTION WHEN others THEN
        RETURN NULL;   -- unparseable history: nothing is claimed
      END;
      IF v_val IS NULL THEN RETURN NULL; END IF;
      v_run_cons := v_val;
    ELSIF a.field = 'time_quota_seconds' THEN
      BEGIN
        v_val := NULLIF(a.new_value, '')::bigint;
      EXCEPTION WHEN others THEN
        RETURN NULL;
      END;
      IF v_val IS NULL THEN RETURN NULL; END IF;
      v_run_bud := v_val;
    ELSE
      -- A repoint to a different immutable revision. The budget it carries is what the entitlement's became
      -- at that instant; the revisions themselves cannot change, so this reading is stable.
      BEGIN
        SELECT spr.time_quota_seconds INTO v_val FROM iam_v2.service_plan_revisions spr
         WHERE spr.id = NULLIF(a.new_value, '')::uuid;
      EXCEPTION WHEN others THEN
        RETURN NULL;
      END;
      IF v_val IS NULL THEN RETURN NULL; END IF;
      v_run_bud := v_val;
    END IF;

    -- The crossing: consumption has reached the budget as they both stood at this instant. A budget that has
    -- never been adjusted is the current one, which is the value the entitlement has always had.
    IF v_run_cons IS NOT NULL AND v_run_bud IS NOT NULL AND v_run_cons >= v_run_bud THEN
      RETURN a.created_at;
    END IF;
  END LOOP;

  -- (3) The entitlement is exhausted NOW, and its recorded history does not account for it. That is a gap in
  --     the evidence, and a gap is not a licence to pick an instant: the state stays undatable, and nothing
  --     terminates for TIME until something can date it.
  RAISE WARNING 'entitlement % is at or over its online-time budget and no audited adjustment accounts for '
                'the crossing; it will not be terminated for TIME until one does', p_entitlement;
  RETURN NULL;
END $$;

COMMENT ON FUNCTION iam_v2.p6_exhaustion_instant(uuid) IS
  'The instant an AGGREGATE_ONLINE_TIME budget ran out (0043 behaviour, restored by the 0044 down migration).';

REVOKE ALL ON FUNCTION iam_v2.p6_exhaustion_instant(uuid) FROM PUBLIC;

COMMIT;
