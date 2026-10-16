-- 0122 -- PLAN CONTROLS ARE ENFORCED, AND USAGE IS COUNTED INCREMENTALLY
-- (audit remediation stream 5; Product-Owner decisions D-C and D-H, 2026-10-16).
--
-- Every control below was already stored on the plan, accepted by the editor and shown to clients, and none of
-- them did anything. This migration makes the database the authority for each of them, so the accounting daemon
-- (acctd) only has to call one definer function per control on its tick:
--
--   1. TIME ALLOWANCE ON A VALIDITY_WINDOW PLAN (time_quota_seconds). It is ONLINE time across the entitlement's
--      sessions -- the device-seconds the Phase-6 accrual already measures for AGGREGATE_ONLINE_TIME (contract
--      section 6.1: two devices online for ten minutes spend twenty) -- with the validity window still the hard
--      outer end. The Phase-6 machinery is reused, not copied: accrual, the exact piecewise crossing, the
--      provable-instant rule, suspension when an instant cannot be proven, and the one expiry writer. Running out
--      terminates with the existing reason TIME; the evidence names the new cause ONLINE_TIME_ALLOWANCE_EXHAUSTED
--      so it can be told from an elapsed window and from the aggregate mode.
--   2. IDLE TIMEOUT and MAXIMUM CONTINUOUS SESSION (idle_timeout_seconds, max_continuous_session_seconds). A
--      SESSION ends -- IDLE_TIMEOUT after N seconds without traffic, MAX_SESSION_DURATION N seconds after it
--      started -- at that true instant. The entitlement, its window, its allowance and the device's place on it
--      are untouched, so the device signs in again and continues.
--   3. DISCONNECT_OLDEST. Admitting a device over the plan's limit first disconnects the entitlement's oldest
--      device (its sessions end, its place is released through deauthorize_entitlement_device) and records it in
--      the append-only iam_v2.device_limit_disconnections. ADMIN_APPROVAL still fails closed here: there is no
--      approval workflow, so the editor refuses it at publish (enum kept for history).
--   4. THE DATA-QUOTA CROSSING NO LONGER RESCANS ALL HISTORY (B-5). p6_data_crossing used to window-sum every
--      accounting record an entitlement ever had, for every live entitlement, every second. Now acctd advances a
--      durable per-binding checkpoint (iam_v2.data_crossing_progress: the last sample_seq examined and the bytes
--      counted under it), and p6_data_crossing answers from the checkpoint plus the few records after it. The
--      exact full derivation still decides the instant -- it runs only when the running total has reached the
--      quota, i.e. once per crossing -- so the answer is the same answer, not an approximation of it. Records are
--      deltas and checkpoints are durable, so restarts and counter resets change nothing. No record is deleted.
--   5. ACCOUNTING-DETAIL RETENTION (D-H). An audited site setting, default UNLIMITED, and a purge operation that
--      does nothing at all while the setting is unlimited. When a site sets a retention, only records older than
--      it, of sessions that have ended, whose every entitlement is TERMINATED, are removed, in bounded batches,
--      and every run that removed anything is recorded. Totals live on the entitlement and the session and are
--      not affected.
--
-- Additive: existing rows are not rewritten. Existing VALIDITY_WINDOW entitlements whose pinned plan revision
-- carries a time allowance start being accounted from their first observation after this migration (the accrual
-- never charges unobserved time), which is the behaviour the plan always promised.

BEGIN;

-- ---------------------------------------------------------------------------------------------------------
-- 1. Which entitlements are accounted in online time.
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION iam_v2.p6_counts_online_time(p_mode text, p_budget bigint) RETURNS boolean
    LANGUAGE sql IMMUTABLE
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
  SELECT p_mode = 'AGGREGATE_ONLINE_TIME'
      OR (p_mode = 'VALIDITY_WINDOW' AND p_budget IS NOT NULL AND p_budget > 0);
$$;
COMMENT ON FUNCTION iam_v2.p6_counts_online_time(text, bigint) IS
  'Whether an entitlement in this time mode, pinned to a plan revision with this time_quota_seconds, is accounted '
  'in online time (0122): always for AGGREGATE_ONLINE_TIME, and for VALIDITY_WINDOW when the plan carries a '
  'positive time allowance. The one definition every online-time function uses.';

CREATE OR REPLACE FUNCTION iam_v2.p6_exhaustion_instant(p_entitlement uuid) RETURNS timestamp with time zone
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE
  e            record;
  a            record;
  v_budget     bigint;
  v_cons_floor bigint;
  v_run_bud    bigint;
  v_val        bigint;
  v_has_budget_history boolean;
BEGIN
  SELECT id, status, time_accounting_mode, consumed_online_seconds, online_time_exhausted_at,
         service_plan_revision_id
    INTO e FROM iam_v2.entitlements WHERE id = p_entitlement;
  IF NOT FOUND OR NOT iam_v2.p6_counts_online_time(e.time_accounting_mode,
       (SELECT spr0.time_quota_seconds FROM iam_v2.service_plan_revisions spr0
         WHERE spr0.id = e.service_plan_revision_id)) THEN
    RETURN NULL;
  END IF;
  IF e.online_time_exhausted_at IS NOT NULL THEN
    RETURN e.online_time_exhausted_at;
  END IF;

  SELECT spr.time_quota_seconds INTO v_budget
    FROM iam_v2.service_plan_revisions spr WHERE spr.id = e.service_plan_revision_id;
  IF v_budget IS NULL OR COALESCE(e.consumed_online_seconds, 0) < v_budget THEN
    RETURN NULL;
  END IF;

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
      RETURN NULL;
    END;
    IF v_run_bud IS NULL THEN
      RETURN NULL;
    END IF;
  ELSE
    v_run_bud := v_budget;
  END IF;

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
      RETURN NULL;
    END;

    IF v_cons_floor IS NOT NULL AND v_run_bud IS NOT NULL AND v_cons_floor >= v_run_bud THEN
      RETURN a.created_at;
    END IF;
  END LOOP;

  -- Warn only while nothing has been done about it. A suspended entitlement has been handled: access is
  -- withheld and the transition says why, so repeating the line every sweep would bury the ones that still
  -- need somebody.
  IF e.status IN ('ACTIVE', 'PENDING') THEN
    RAISE WARNING 'entitlement % is at or over its online-time budget and its audited history cannot prove '
                  'when it crossed; access will be withheld and it will not be terminated for TIME until '
                  'something can date it', p_entitlement;
  END IF;
  RETURN NULL;
END $$;

CREATE OR REPLACE FUNCTION iam_v2.p6_over_budget_now(p_entitlement uuid) RETURNS boolean
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
  SELECT COALESCE(e.consumed_online_seconds, 0) >= spr.time_quota_seconds
    FROM iam_v2.entitlements e
    JOIN iam_v2.service_plan_revisions spr ON spr.id = e.service_plan_revision_id
   WHERE e.id = p_entitlement
     AND iam_v2.p6_counts_online_time(e.time_accounting_mode, spr.time_quota_seconds);
$$;

CREATE OR REPLACE FUNCTION iam_v2.p6_due_terminal(p_entitlement uuid) RETURNS TABLE(reason text, at timestamp with time zone, time_cause text)
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE
  e      record;
  v_win  timestamptz;
  v_data timestamptz;
  v_agg  timestamptz;
BEGIN
  SELECT id, status, time_accounting_mode, window_ends_at
    INTO e FROM iam_v2.entitlements WHERE id = p_entitlement;
  IF NOT FOUND OR e.status NOT IN ('ACTIVE', 'PENDING', 'SUSPENDED') THEN
    RETURN;
  END IF;

  IF e.window_ends_at IS NOT NULL AND e.window_ends_at <= now() THEN
    v_win := e.window_ends_at;
  END IF;
  v_data := iam_v2.p6_data_crossing(p_entitlement);
  -- Exhaustion counts as a terminal condition only when its instant is provable.
  v_agg := iam_v2.p6_exhaustion_instant(p_entitlement);

  IF v_win IS NULL AND v_data IS NULL AND v_agg IS NULL THEN
    RETURN;
  END IF;

  at := LEAST(COALESCE(v_win, 'infinity'::timestamptz),
              COALESCE(v_data, 'infinity'::timestamptz),
              COALESCE(v_agg, 'infinity'::timestamptz));
  IF v_data IS NOT NULL AND v_data = at THEN
    reason := 'DATA'; time_cause := NULL;
  ELSIF v_agg IS NOT NULL AND v_agg = at THEN
    reason := 'TIME';
    -- Which online-time clock ran out: the aggregate mode's budget, or the time allowance a
    -- VALIDITY_WINDOW plan carries (0122). Same terminal reason, distinguishable evidence.
    time_cause := CASE WHEN e.time_accounting_mode = 'AGGREGATE_ONLINE_TIME'
                       THEN 'AGGREGATE_ONLINE_TIME_EXHAUSTED' ELSE 'ONLINE_TIME_ALLOWANCE_EXHAUSTED' END;
  ELSE
    reason := 'TIME';
    time_cause := CASE WHEN e.time_accounting_mode = 'AGGREGATE_ONLINE_TIME'
                       THEN 'AGGREGATE_OUTER_WINDOW_EXPIRED' ELSE 'VALIDITY_WINDOW_ELAPSED' END;
  END IF;
  RETURN NEXT;
END $$;

CREATE OR REPLACE FUNCTION iam_v2.p6_record_time_termination(p_entitlement uuid, p_cause text) RETURNS void
    LANGUAGE plpgsql
    AS $$
DECLARE e record; v_budget bigint;
BEGIN
  IF p_cause NOT IN ('VALIDITY_WINDOW_ELAPSED','AGGREGATE_ONLINE_TIME_EXHAUSTED','AGGREGATE_OUTER_WINDOW_EXPIRED',
                     'ONLINE_TIME_ALLOWANCE_EXHAUSTED') THEN
    RAISE EXCEPTION 'unknown time-termination cause %', p_cause USING ERRCODE = 'invalid_parameter_value';
  END IF;
  SELECT id, tenant_id, site_id, status, terminal_reason, terminated_at, time_accounting_mode,
         window_ends_at, consumed_online_seconds, service_plan_revision_id
    INTO e FROM iam_v2.entitlements WHERE id = p_entitlement FOR SHARE;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'no such entitlement %', p_entitlement USING ERRCODE = 'foreign_key_violation';
  END IF;
  SELECT spr.time_quota_seconds INTO v_budget FROM iam_v2.service_plan_revisions spr
   WHERE spr.id = e.service_plan_revision_id;

  INSERT INTO iam_v2.entitlement_termination_evidence
    (entitlement_id, tenant_id, site_id, terminal_reason, cause_detail, time_mode,
     budget_seconds, consumed_online_seconds, window_ends_at, terminated_at)
  VALUES (e.id, e.tenant_id, e.site_id, e.terminal_reason, p_cause, e.time_accounting_mode,
          v_budget, e.consumed_online_seconds, e.window_ends_at, e.terminated_at);
END $$;

CREATE OR REPLACE FUNCTION iam_v2.p6_suspend_over_budget(p_tenant uuid, p_site uuid) RETURNS TABLE(entitlement uuid, devices integer, sessions integer)
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE
  e          record;
  v_devices  int;
  v_sessions int;
BEGIN
  FOR e IN
    SELECT en.id
      FROM iam_v2.entitlements en
     WHERE en.tenant_id = p_tenant AND en.site_id = p_site
       AND iam_v2.p6_counts_online_time(en.time_accounting_mode,
             (SELECT spr0.time_quota_seconds FROM iam_v2.service_plan_revisions spr0
               WHERE spr0.id = en.service_plan_revision_id))
       AND en.status IN ('ACTIVE', 'PENDING')          -- already SUSPENDED: nothing to do, and nothing to say
       AND iam_v2.p6_over_budget_now(en.id)
       AND iam_v2.p6_exhaustion_instant(en.id) IS NULL -- datable ones terminate through the normal path
     ORDER BY en.id
     FOR UPDATE
  LOOP
    -- The status change goes through the SAME writer every other status change uses, so the transition
    -- history stays the single account of what happened to this entitlement. now() is honest here: this is
    -- when the system decided to withhold access, not a claim about when the budget ran out.
    PERFORM iam_v2.apply_entitlement_transition(e.id, 'SUSPENDED', now(), 'AGGREGATE_OVER_BUDGET');

    -- Fail closed on what it already holds. Closing an authorization interval is a device-authorization
    -- write, so the scope is declared, exactly as the expiry writer does.
    PERFORM iam_v2.begin_controlled_operation('device_auth');

    WITH closed AS (
      UPDATE iam_v2.entitlement_device_authorizations a
         SET deauthorized_at = GREATEST(now(), a.authorized_at)
       WHERE a.entitlement_id = e.id AND a.deauthorized_at IS NULL
       RETURNING a.entitlement_id, a.device_id)
    UPDATE iam_v2.entitlement_devices ed
       SET status = 'DISCONNECTED', disconnected_reason = 'ENTITLEMENT_SUSPENDED'
      FROM closed
     WHERE ed.entitlement_id = closed.entitlement_id AND ed.device_id = closed.device_id;
    GET DIAGNOSTICS v_devices = ROW_COUNT;

    UPDATE iam_v2.sessions se
       SET state = 'ended', ended = GREATEST(now(), se.started), end_reason = 'ENTITLEMENT_SUSPENDED'
     WHERE se.entitlement_id = e.id AND se.state IN ('active', 'PENDING_ENFORCEMENT');
    GET DIAGNOSTICS v_sessions = ROW_COUNT;

    entitlement := e.id; devices := v_devices; sessions := v_sessions;
    RETURN NEXT;
  END LOOP;
END $$;

CREATE OR REPLACE FUNCTION iam_v2.p6_tick_online_time(p_tenant uuid, p_site uuid, p_now timestamp with time zone, p_max_charge_seconds integer, p_capped_entitlements uuid[] DEFAULT '{}'::uuid[], p_caps timestamp with time zone[] DEFAULT '{}'::timestamp with time zone[]) RETURNS TABLE(entitlement_id uuid, exhausted_at timestamp with time zone)
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE
  e           record;
  s           record;
  v_budget    bigint;
  v_before    bigint;
  v_charged   numeric;      -- exact seconds charged this tick, before flooring
  v_base      timestamptz;
  v_ceiling   timestamptz;  -- the last instant that is billable at all
  v_billable  timestamptz;  -- the last instant this tick may charge (observation bound)
  v_charge    numeric;
  v_remaining numeric;
  -- The billable intervals of this tick, one pair per contributing session. They are what the crossing is
  -- computed from: a rate that changes at every boundary cannot be derived from a count alone.
  v_starts    timestamptz[];
  v_ends      timestamptz[];
  v_points    timestamptz[];
  v_acc       numeric;
  v_rate      int;
  v_seg       numeric;
  v_p0        timestamptz;
  v_p1        timestamptz;
  v_cross     timestamptz;
  v_cap       timestamptz;   -- the caller's earliest known terminal instant for this entitlement
  k           int;
  i           int;
BEGIN
  IF p_max_charge_seconds IS NULL OR p_max_charge_seconds <= 0 THEN
    RAISE EXCEPTION 'a charge bound is required: unbounded accrual would charge unobserved time'
      USING ERRCODE = 'invalid_parameter_value';
  END IF;

  FOR e IN
    SELECT en.id, en.consumed_online_seconds, en.service_plan_revision_id, en.online_time_exhausted_at,
           en.window_ends_at
      FROM iam_v2.entitlements en
     WHERE en.tenant_id = p_tenant AND en.site_id = p_site
       AND iam_v2.p6_counts_online_time(en.time_accounting_mode,
             (SELECT spr0.time_quota_seconds FROM iam_v2.service_plan_revisions spr0
               WHERE spr0.id = en.service_plan_revision_id))
       AND en.status IN ('ACTIVE', 'PENDING', 'SUSPENDED')
     ORDER BY en.id
     FOR UPDATE
  LOOP
    SELECT spr.time_quota_seconds INTO v_budget
      FROM iam_v2.service_plan_revisions spr WHERE spr.id = e.service_plan_revision_id;

    -- The caller's cap for this entitlement, if it named one. A DATA crossing that already happened is a
    -- terminal instant like any other: nothing after it was access, so nothing after it is billable.
    v_cap := NULL;
    IF array_length(p_capped_entitlements, 1) IS NOT NULL THEN
      FOR i IN 1 .. array_length(p_capped_entitlements, 1) LOOP
        IF p_capped_entitlements[i] = e.id THEN
          v_cap := p_caps[i];
          EXIT;
        END IF;
      END LOOP;
    END IF;

    v_before  := COALESCE(e.consumed_online_seconds, 0);

    -- Already exhausted and still live: charge nothing more, and report again so a caller that failed to
    -- terminate last time still converges -- but ONLY with a provable instant.
    --
    -- This branch used to stamp now() when the entitlement had reached its budget without a stamp, which
    -- wrote a historical fact nobody observed and then handed it to the termination, the evidence row and
    -- every session end time. p6_exhaustion_instant answers from evidence or answers NULL, and NULL means
    -- nothing is claimed: no stamp, no report, no termination for TIME. The entitlement stays subject to its
    -- window and its data quota, and the next tick that observes real online time crosses the budget
    -- properly and stamps it then.
    IF v_budget IS NOT NULL AND (e.online_time_exhausted_at IS NOT NULL OR v_before >= v_budget) THEN
      exhausted_at := iam_v2.p6_exhaustion_instant(e.id);
      IF exhausted_at IS NULL THEN
        CONTINUE;
      END IF;
      entitlement_id := e.id;
      RETURN NEXT;
      CONTINUE;
    END IF;

    v_charged := 0;
    v_starts  := ARRAY[]::timestamptz[];
    v_ends    := ARRAY[]::timestamptz[];

    FOR s IN
      SELECT se.id, se.state, se.started, se.ended, w.accounted_through
        FROM iam_v2.sessions se
        LEFT JOIN iam_v2.session_online_watermarks w ON w.session_id = se.id
       WHERE se.entitlement_id = e.id
         AND (se.state = 'active' OR (se.ended IS NOT NULL AND w.accounted_through IS NOT NULL
                                      AND w.accounted_through < se.ended))
       ORDER BY se.id
       FOR UPDATE OF se
    LOOP
      -- THE CEILING: the earliest applicable boundary. now, the session's own end, and -- the correction --
      -- the entitlement's immutable outer window. Access ended at the window, so no second after it was ever
      -- access, whatever the session row still says.
      v_ceiling := LEAST(p_now, COALESCE(s.ended, p_now),
                         COALESCE(e.window_ends_at, 'infinity'::timestamptz),
                         COALESCE(v_cap, 'infinity'::timestamptz));

      -- First observation: charge nothing, baseline here. A session with no watermark has never been seen by
      -- this path, so the interval since it started is not evidence of anything -- it may have been spent
      -- PENDING_ENFORCEMENT with no forwarding at all.
      IF s.accounted_through IS NULL THEN
        IF EXTRACT(EPOCH FROM (v_ceiling - s.started)) > p_max_charge_seconds THEN
          INSERT INTO iam_v2.online_time_skipped_intervals
            (tenant_id, site_id, session_id, entitlement_id, skipped_from, skipped_to, cause)
          VALUES (p_tenant, p_site, s.id, e.id, s.started, v_ceiling, 'UNOBSERVED_GAP');
        END IF;
        INSERT INTO iam_v2.session_online_watermarks
          (tenant_id, site_id, session_id, accounted_through, accounted_seconds)
        VALUES (p_tenant, p_site, s.id, GREATEST(v_ceiling, s.started), 0)
        ON CONFLICT (session_id) DO NOTHING;
        CONTINUE;
      END IF;

      v_base := s.accounted_through;
      IF v_ceiling <= v_base THEN
        CONTINUE;   -- nothing billable: already charged through, or entirely past the boundary
      END IF;

      -- The observation bound. Anything beyond it was not watched and is recorded rather than charged.
      v_billable := LEAST(v_ceiling, v_base + make_interval(secs => p_max_charge_seconds));
      IF v_billable < v_ceiling THEN
        INSERT INTO iam_v2.online_time_skipped_intervals
          (tenant_id, site_id, session_id, entitlement_id, skipped_from, skipped_to, cause)
        VALUES (p_tenant, p_site, s.id, e.id, v_billable, v_ceiling, 'UNOBSERVED_GAP');
      END IF;

      v_charge := EXTRACT(EPOCH FROM (v_billable - v_base));
      IF v_charge <= 0 THEN
        CONTINUE;
      END IF;

      -- The watermark advances to the CEILING: the skipped remainder has been recorded, and leaving the
      -- watermark short of it would charge it on the next tick after all.
      INSERT INTO iam_v2.session_online_watermarks
        (tenant_id, site_id, session_id, accounted_through, accounted_seconds)
      VALUES (p_tenant, p_site, s.id, v_ceiling, FLOOR(v_charge)::bigint)
      ON CONFLICT (session_id) DO UPDATE
        SET accounted_through = EXCLUDED.accounted_through,
            accounted_seconds = iam_v2.session_online_watermarks.accounted_seconds + EXCLUDED.accounted_seconds,
            updated_at = now();

      v_charged := v_charged + v_charge;
      v_starts  := v_starts || v_base;
      v_ends    := v_ends || v_billable;
    END LOOP;

    IF v_charged <= 0 THEN
      CONTINUE;
    END IF;

    IF v_budget IS NOT NULL AND v_before + v_charged >= v_budget THEN
      v_remaining := GREATEST(0, v_budget - v_before);

      -- THE PIECEWISE CROSSING. Every interval boundary is a point where the burn rate changes, so the
      -- budget is walked segment by segment: rate = how many intervals cover the segment, and the crossing
      -- falls inside the first segment whose contribution reaches what was left.
      SELECT array_agg(p ORDER BY p) INTO v_points
        FROM (SELECT DISTINCT unnest(v_starts || v_ends) AS p) x;

      v_acc   := 0;
      v_cross := NULL;
      FOR k IN 1 .. COALESCE(array_length(v_points, 1), 0) - 1 LOOP
        v_p0 := v_points[k];
        v_p1 := v_points[k + 1];
        v_rate := 0;
        FOR i IN 1 .. array_length(v_starts, 1) LOOP
          IF v_starts[i] <= v_p0 AND v_ends[i] >= v_p1 THEN
            v_rate := v_rate + 1;
          END IF;
        END LOOP;
        IF v_rate = 0 THEN
          CONTINUE;   -- a gap: nobody is online, and nothing burns
        END IF;
        v_seg := EXTRACT(EPOCH FROM (v_p1 - v_p0)) * v_rate;
        IF v_acc + v_seg >= v_remaining THEN
          v_cross := v_p0 + make_interval(secs => ((v_remaining - v_acc) / v_rate)::double precision);
          EXIT;
        END IF;
        v_acc := v_acc + v_seg;
      END LOOP;
      IF v_cross IS NULL THEN
        -- Only reachable through floating-point slack at the very last instant; the last boundary is the
        -- honest answer and it is never later than the window or now, both of which capped every interval.
        v_cross := v_points[array_length(v_points, 1)];
      END IF;

      exhausted_at := v_cross;
      UPDATE iam_v2.entitlements
         SET consumed_online_seconds = v_budget, online_time_exhausted_at = v_cross
       WHERE id = e.id;
      entitlement_id := e.id;
      RETURN NEXT;
    ELSE
      UPDATE iam_v2.entitlements
         SET consumed_online_seconds = v_before + FLOOR(v_charged)::bigint
       WHERE id = e.id;
    END IF;
  END LOOP;
END $$;

COMMENT ON FUNCTION iam_v2.p6_tick_online_time(uuid, uuid, timestamptz, integer, uuid[], timestamptz[]) IS
  'One online-time accrual tick for a site: AGGREGATE_ONLINE_TIME entitlements, and VALIDITY_WINDOW entitlements '
  'whose plan carries a time allowance (0122). Billable only to the earliest of now, session end, the immutable '
  'outer window and any cap the caller passes; charges only sessions in state active, within the per-tick '
  'observation bound. Exhaustion is the exact piecewise crossing, re-reported only with a provable instant. It '
  'terminates nothing.';

-- The evidence row of an allowance exhaustion: TIME, a VALIDITY_WINDOW entitlement, the real budget and a
-- consumption that reached it. The existing aggregate rules are kept word for word; only the cause is added.
ALTER TABLE iam_v2.entitlement_termination_evidence
  DROP CONSTRAINT IF EXISTS entitlement_termination_evidence_cause_detail_check;
ALTER TABLE iam_v2.entitlement_termination_evidence
  ADD CONSTRAINT entitlement_termination_evidence_cause_detail_check CHECK (cause_detail = ANY (ARRAY[
    'VALIDITY_WINDOW_ELAPSED', 'AGGREGATE_ONLINE_TIME_EXHAUSTED', 'AGGREGATE_OUTER_WINDOW_EXPIRED',
    'ONLINE_TIME_ALLOWANCE_EXHAUSTED']));
ALTER TABLE iam_v2.entitlement_termination_evidence DROP CONSTRAINT IF EXISTS ete_allowance_carries_its_budget;
ALTER TABLE iam_v2.entitlement_termination_evidence
  ADD CONSTRAINT ete_allowance_carries_its_budget CHECK (cause_detail <> 'ONLINE_TIME_ALLOWANCE_EXHAUSTED'
    OR (time_mode = 'VALIDITY_WINDOW' AND budget_seconds IS NOT NULL AND consumed_online_seconds IS NOT NULL
        AND consumed_online_seconds >= budget_seconds));

-- ---------------------------------------------------------------------------------------------------------
-- 2. Idle timeout and maximum continuous session.
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION iam_v2.p6_end_sessions_over_plan_limits(p_tenant uuid, p_site uuid, p_now timestamptz,
                                                                   p_judge_idle boolean)
    RETURNS TABLE(session_id uuid, entitlement_id uuid, end_reason text, ended_at timestamptz)
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
#variable_conflict use_column
DECLARE
  s          record;
  v_last     timestamptz;
  v_max_end  timestamptz;
  v_idle_end timestamptz;
  v_end      timestamptz;
BEGIN
  IF p_now IS NULL THEN
    RAISE EXCEPTION 'a reference instant is required' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  FOR s IN
    SELECT se.id, se.entitlement_id AS ent, se.started,
           COALESCE(spr.idle_timeout_seconds, 0) AS idle_s,
           COALESCE(spr.max_continuous_session_seconds, 0) AS max_s
      FROM iam_v2.sessions se
      JOIN iam_v2.entitlements e ON e.id = se.entitlement_id
      JOIN iam_v2.service_plan_revisions spr ON spr.id = e.service_plan_revision_id
     WHERE se.tenant_id = p_tenant AND se.site_id = p_site
       AND se.state = 'active' AND se.ended IS NULL
       AND (COALESCE(spr.idle_timeout_seconds, 0) > 0 OR COALESCE(spr.max_continuous_session_seconds, 0) > 0)
     ORDER BY se.id
     FOR UPDATE OF se
  LOOP
    v_max_end := NULL; v_idle_end := NULL;
    IF s.max_s > 0 THEN
      v_max_end := s.started + make_interval(secs => s.max_s);
    END IF;
    -- IDLE is judged only from evidence. The checkpoint's prev_sampled_at moves only when the session's counters
    -- moved (an unchanged reading is a replay and writes nothing), so it is the last instant traffic was seen. A
    -- session the accounting has never observed has no such evidence and is not judged; neither is any session
    -- when the caller says this tick's accounting pass did not complete -- silence from a broken meter is not
    -- idleness.
    IF p_judge_idle AND s.idle_s > 0 THEN
      SELECT max(cp.prev_sampled_at) INTO v_last FROM iam_v2.accounting_checkpoints cp WHERE cp.session_id = s.id;
      IF v_last IS NOT NULL THEN
        v_idle_end := GREATEST(v_last, s.started) + make_interval(secs => s.idle_s);
      END IF;
    END IF;
    v_end := LEAST(COALESCE(v_max_end, 'infinity'::timestamptz), COALESCE(v_idle_end, 'infinity'::timestamptz));
    IF v_end = 'infinity'::timestamptz OR v_end > p_now THEN
      CONTINUE;
    END IF;
    session_id := s.id; entitlement_id := s.ent; ended_at := GREATEST(v_end, s.started);
    end_reason := CASE WHEN v_max_end IS NOT NULL AND v_max_end = v_end THEN 'MAX_SESSION_DURATION'
                       ELSE 'IDLE_TIMEOUT' END;
    -- The SESSION ends at the instant the limit was reached. Nothing else moves: the entitlement stays ACTIVE
    -- with its window and allowance, and the device keeps its place on it, so signing in again simply opens a
    -- new session.
    UPDATE iam_v2.sessions se2 SET state = 'ended', ended = p6_end_sessions_over_plan_limits.ended_at,
           end_reason = p6_end_sessions_over_plan_limits.end_reason
     WHERE se2.id = s.id;
    RETURN NEXT;
  END LOOP;
END $$;
COMMENT ON FUNCTION iam_v2.p6_end_sessions_over_plan_limits(uuid, uuid, timestamptz, boolean) IS
  'Ends live sessions whose pinned plan''s maximum continuous session (MAX_SESSION_DURATION, started + N) or idle '
  'timeout (IDLE_TIMEOUT, last metered traffic + N) has been reached, at that instant (0122). Idle is judged only '
  'when the caller''s accounting pass completed and only from a session''s own checkpoint. The entitlement and the '
  'device''s place are untouched.';

-- ---------------------------------------------------------------------------------------------------------
-- 3. DISCONNECT_OLDEST.
-- ---------------------------------------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS iam_v2.device_limit_disconnections (
  id                     uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id              uuid NOT NULL,
  site_id                uuid NOT NULL,
  entitlement_id         uuid NOT NULL,
  disconnected_device_id uuid NOT NULL,
  admitted_device_id     uuid NOT NULL,
  device_limit           integer NOT NULL CONSTRAINT dld_limit CHECK (device_limit >= 1),
  sessions_ended         integer NOT NULL CONSTRAINT dld_sessions CHECK (sessions_ended >= 0),
  disconnected_at        timestamptz NOT NULL,
  recorded_at            timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS dld_entitlement ON iam_v2.device_limit_disconnections (entitlement_id, disconnected_at DESC);
CREATE INDEX IF NOT EXISTS dld_site ON iam_v2.device_limit_disconnections (tenant_id, site_id, disconnected_at DESC);
COMMENT ON TABLE iam_v2.device_limit_disconnections IS
  'Every device the DISCONNECT_OLDEST device-limit policy disconnected to admit a new one (0122): which device '
  'left, which device was admitted, the limit, how many sessions ended. Append-only.';

CREATE OR REPLACE FUNCTION iam_v2.device_limit_disconnections_append_only() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'iam_v2.device_limit_disconnections is append-only: % refused', TG_OP
    USING ERRCODE = 'restrict_violation';
END $$;
DROP TRIGGER IF EXISTS device_limit_disconnections_append_only ON iam_v2.device_limit_disconnections;
CREATE TRIGGER device_limit_disconnections_append_only BEFORE UPDATE OR DELETE ON iam_v2.device_limit_disconnections
  FOR EACH ROW EXECUTE FUNCTION iam_v2.device_limit_disconnections_append_only();

CREATE OR REPLACE FUNCTION iam_v2.authorize_entitlement_device(p_ent uuid, p_device uuid, p_at timestamp with time zone) RETURNS uuid
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE v_t uuid; v_s uuid; v_status text; v_limit int; v_policy text; v_open int; v_seq bigint; v_id uuid; v_existing uuid;
        v_old record; v_ended int;
BEGIN
  -- This operation writes a capability-scoped family, so it declares its own scope. Doing it here
  -- rather than relying on ownership is what lets Gate-P give every function its own owner without
  -- any of them losing the right to perform its own writes.
  PERFORM iam_v2.begin_controlled_operation('device_auth');
  SELECT tenant_id, site_id, status INTO v_t, v_s, v_status FROM iam_v2.entitlements WHERE id = p_ent FOR UPDATE;
  IF v_t IS NULL THEN RAISE EXCEPTION 'entitlement % not found', p_ent; END IF;
  IF v_status <> 'ACTIVE' THEN
    RAISE EXCEPTION 'device authorization requires an ACTIVE entitlement (status %)', v_status;
  END IF;
  -- the device must exist in the SAME tenant/site scope (never another tenant's device)
  PERFORM 1 FROM iam_v2.devices WHERE id = p_device AND tenant_id = v_t AND site_id = v_s;
  IF NOT FOUND THEN RAISE EXCEPTION 'device % is not in the entitlement scope', p_device; END IF;
  -- IDEMPOTENT: an already-open interval for this device is returned unchanged
  SELECT id INTO v_existing FROM iam_v2.entitlement_device_authorizations
    WHERE entitlement_id = p_ent AND device_id = p_device AND deauthorized_at IS NULL;
  IF v_existing IS NOT NULL THEN
    UPDATE iam_v2.entitlement_devices SET last_authorized = p_at
      WHERE entitlement_id = p_ent AND device_id = p_device;
    RETURN v_existing;
  END IF;
  -- device limit is enforced HERE, under the entitlement row lock, so concurrent authorizations cannot both win
  SELECT spr.max_concurrent_devices, spr.device_limit_policy INTO v_limit, v_policy
    FROM iam_v2.entitlements e JOIN iam_v2.service_plan_revisions spr ON spr.id = e.service_plan_revision_id
    WHERE e.id = p_ent;
  SELECT count(*) INTO v_open FROM iam_v2.entitlement_device_authorizations
    WHERE entitlement_id = p_ent AND deauthorized_at IS NULL;
  IF v_limit IS NOT NULL AND v_open >= v_limit THEN
    IF COALESCE(v_policy, 'REJECT_NEW_DEVICE') = 'DISCONNECT_OLDEST' THEN
      -- DISCONNECT_OLDEST (0122): make room by disconnecting the entitlement's OLDEST device -- the open place
      -- admitted first -- until one place is free. Its live sessions end (enforcement tears them down from
      -- durable state on its next pass), its place is released through the one primitive that closes an
      -- interval, and the move is recorded. The entitlement's window, allowance and usage are untouched.
      WHILE v_open >= v_limit LOOP
        SELECT a.device_id, a.authorized_at INTO v_old
          FROM iam_v2.entitlement_device_authorizations a
         WHERE a.entitlement_id = p_ent AND a.deauthorized_at IS NULL
         ORDER BY a.authorized_at, a.id LIMIT 1;
        EXIT WHEN v_old.device_id IS NULL;
        UPDATE iam_v2.sessions se
           SET state = 'ended', ended = GREATEST(p_at, se.started), end_reason = 'DEVICE_LIMIT_OLDEST_DISCONNECTED'
         WHERE se.entitlement_id = p_ent AND se.device_id = v_old.device_id
           AND se.state IN ('active', 'PENDING_ENFORCEMENT');
        GET DIAGNOSTICS v_ended = ROW_COUNT;
        PERFORM iam_v2.deauthorize_entitlement_device(p_ent, v_old.device_id, GREATEST(p_at, v_old.authorized_at),
                                                      'DISCONNECTED_FOR_NEWER_DEVICE');
        INSERT INTO iam_v2.device_limit_disconnections
          (tenant_id, site_id, entitlement_id, disconnected_device_id, admitted_device_id, device_limit,
           sessions_ended, disconnected_at)
        VALUES (v_t, v_s, p_ent, v_old.device_id, p_device, v_limit, v_ended, GREATEST(p_at, v_old.authorized_at));
        v_open := v_open - 1;
      END LOOP;
    ELSIF COALESCE(v_policy, 'REJECT_NEW_DEVICE') = 'ADMIN_APPROVAL' THEN
      -- There is no approval workflow, so a plan that names this policy cannot admit another device. Plans can
      -- no longer be published with it (the editor refuses it); a revision published before keeps failing closed.
      RAISE EXCEPTION 'MAX_DEVICES_REACHED: device limit policy ADMIN_APPROVAL is not available (no approval workflow); entitlement % has % of % devices',
        p_ent, v_open, v_limit;
    ELSE
      RAISE EXCEPTION 'MAX_DEVICES_REACHED: entitlement % already has % of % devices authorized', p_ent, v_open, v_limit;
    END IF;
  END IF;
  INSERT INTO iam_v2.entitlement_devices(tenant_id,site_id,entitlement_id,device_id,status,first_authorized,last_authorized)
    VALUES (v_t,v_s,p_ent,p_device,'AUTHORIZED',p_at,p_at)
    ON CONFLICT (entitlement_id,device_id) DO UPDATE SET status='AUTHORIZED', last_authorized=p_at,
      first_authorized = COALESCE(iam_v2.entitlement_devices.first_authorized, p_at), disconnected_reason=NULL;
  SELECT COALESCE(max(seq),0)+1 INTO v_seq FROM iam_v2.entitlement_device_authorizations
    WHERE entitlement_id = p_ent AND device_id = p_device;
  INSERT INTO iam_v2.entitlement_device_authorizations(tenant_id,site_id,entitlement_id,device_id,seq,authorized_at)
    VALUES (v_t,v_s,p_ent,p_device,v_seq,p_at) RETURNING id INTO v_id;
  RETURN v_id;
END $$;

-- ---------------------------------------------------------------------------------------------------------
-- 4. The data-quota crossing, from a durable checkpoint.
-- ---------------------------------------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS iam_v2.data_crossing_progress (
  binding_id     uuid PRIMARY KEY,
  tenant_id      uuid NOT NULL,
  site_id        uuid NOT NULL,
  entitlement_id uuid NOT NULL,
  session_id     uuid NOT NULL,
  examined_seq   bigint NOT NULL DEFAULT 0 CONSTRAINT dcp_seq CHECK (examined_seq >= 0),
  counted_bytes  bigint NOT NULL DEFAULT 0 CONSTRAINT dcp_bytes CHECK (counted_bytes >= 0),
  final          boolean NOT NULL DEFAULT false,
  updated_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS dcp_entitlement ON iam_v2.data_crossing_progress (entitlement_id);
COMMENT ON TABLE iam_v2.data_crossing_progress IS
  'Per session-entitlement binding: the highest accounting sample_seq of the session already examined and the bytes '
  'of those samples that fell inside the binding (0122). A derived checkpoint, never evidence: the records stay the '
  'authority, and the counted bytes can only overstate (an attribution interval that later shrank), which is '
  'answered by the exact derivation. final = the session ended long enough ago that no record can still arrive.';

CREATE TABLE IF NOT EXISTS iam_v2.data_crossing_state (
  entitlement_id uuid PRIMARY KEY,
  tenant_id      uuid NOT NULL,
  site_id        uuid NOT NULL,
  quota_bytes    bigint NOT NULL,
  crossed_at     timestamptz,
  verified_at    timestamptz NOT NULL DEFAULT now()
);
COMMENT ON TABLE iam_v2.data_crossing_state IS
  'The exact data-quota crossing instant of an entitlement, established by the full derivation once the running '
  'total reached quota_bytes (0122). Trusted only while the entitlement''s quota is still quota_bytes.';

-- The exact derivation, unchanged from the function it was (0038/0064): kept as the one sanctioned answer.
CREATE OR REPLACE FUNCTION iam_v2.p6_data_crossing_full(p_entitlement uuid) RETURNS timestamp with time zone
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
  SELECT min(x.sampled_at)
    FROM iam_v2.entitlements e
    LEFT JOIN iam_v2.service_plan_revisions spr ON spr.id = e.service_plan_revision_id
    CROSS JOIN LATERAL (
      SELECT ar.sampled_at,
             sum(ar.bytes_up + ar.bytes_down) OVER (ORDER BY ar.sampled_at, ar.id) AS running
        FROM iam_v2.accounting_records ar
        JOIN iam_v2.session_entitlement_bindings b ON b.session_id = ar.session_id
         AND b.entitlement_id = e.id AND b.bound_from <= ar.sampled_at
         AND (b.bound_until IS NULL OR b.bound_until > ar.sampled_at)
    ) x
   WHERE e.id = p_entitlement
     AND COALESCE(e.data_quota_bytes, spr.data_quota_bytes) IS NOT NULL
     AND x.running >= COALESCE(e.data_quota_bytes, spr.data_quota_bytes);
$$;
COMMENT ON FUNCTION iam_v2.p6_data_crossing_full(uuid) IS
  'The exact data-quota crossing by full derivation over every attributed accounting record (the pre-0122 '
  'p6_data_crossing). Called only once the checkpointed running total says the quota may have been reached.';

CREATE OR REPLACE FUNCTION iam_v2.p6_data_crossing(p_entitlement uuid) RETURNS timestamp with time zone
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE v_quota bigint; st record; v_est numeric;
BEGIN
  SELECT COALESCE(e.data_quota_bytes, spr.data_quota_bytes) INTO v_quota
    FROM iam_v2.entitlements e
    LEFT JOIN iam_v2.service_plan_revisions spr ON spr.id = e.service_plan_revision_id
   WHERE e.id = p_entitlement;
  IF v_quota IS NULL THEN
    RETURN NULL;   -- no quota (or no such entitlement): nothing to cross, and nothing is read
  END IF;
  SELECT s.quota_bytes, s.crossed_at INTO st FROM iam_v2.data_crossing_state s WHERE s.entitlement_id = p_entitlement;
  IF FOUND AND st.crossed_at IS NOT NULL AND st.quota_bytes = v_quota THEN
    RETURN st.crossed_at;
  END IF;
  -- An UPPER BOUND on the attributed bytes: each binding's checkpoint plus whatever arrived after it (a binding
  -- with no checkpoint yet contributes everything it has). Below the quota it proves there is no crossing, and
  -- that is the steady state, answered from an index probe per binding instead of the whole history.
  SELECT COALESCE(sum(COALESCE(pr.counted_bytes, 0) + COALESCE(tail.bytes, 0)), 0) INTO v_est
    FROM iam_v2.session_entitlement_bindings sb
    LEFT JOIN iam_v2.data_crossing_progress pr ON pr.binding_id = sb.id
    LEFT JOIN LATERAL (
      SELECT sum(ar.bytes_up + ar.bytes_down) AS bytes
        FROM iam_v2.accounting_records ar
       WHERE ar.session_id = sb.session_id AND ar.sample_seq > COALESCE(pr.examined_seq, 0)
         AND sb.bound_from <= ar.sampled_at AND (sb.bound_until IS NULL OR sb.bound_until > ar.sampled_at)
    ) tail ON true
   WHERE sb.entitlement_id = p_entitlement;
  IF v_est < v_quota THEN
    RETURN NULL;
  END IF;
  RETURN iam_v2.p6_data_crossing_full(p_entitlement);
END $$;
COMMENT ON FUNCTION iam_v2.p6_data_crossing(uuid) IS
  'The instant attributed usage first reached this entitlement''s quota, or NULL. The quota is the entitlement''s own '
  'frozen data_quota_bytes when it has one (a PER_STAY_NIGHT grant) and the pinned plan revision''s otherwise. Since '
  '0122 it answers from the durable checkpoint (data_crossing_progress / data_crossing_state) and runs the exact '
  'derivation (p6_data_crossing_full) only when the running total has reached the quota. Both the expiry sweep''s '
  'candidate query and the sanctioned expiry writer call it, so they cannot disagree.';

CREATE OR REPLACE FUNCTION iam_v2.p6_advance_data_crossing(p_tenant uuid, p_site uuid) RETURNS integer
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE e record; b record; v_new bigint; v_max bigint; v_est numeric; v_exact timestamptz; n int := 0;
BEGIN
  FOR e IN
    SELECT en.id, COALESCE(en.data_quota_bytes, spr.data_quota_bytes) AS quota
      FROM iam_v2.entitlements en
      LEFT JOIN iam_v2.service_plan_revisions spr ON spr.id = en.service_plan_revision_id
     WHERE en.tenant_id = p_tenant AND en.site_id = p_site
       AND en.status IN ('ACTIVE', 'PENDING', 'SUSPENDED')
       AND COALESCE(en.data_quota_bytes, spr.data_quota_bytes) IS NOT NULL
       AND NOT EXISTS (SELECT 1 FROM iam_v2.data_crossing_state s
                        WHERE s.entitlement_id = en.id AND s.crossed_at IS NOT NULL
                          AND s.quota_bytes = COALESCE(en.data_quota_bytes, spr.data_quota_bytes))
     ORDER BY en.id
  LOOP
    n := n + 1;
    FOR b IN
      SELECT sb.id, sb.session_id, sb.bound_from, sb.bound_until, se.ended,
             COALESCE(pr.examined_seq, 0) AS seq, COALESCE(pr.final, false) AS final
        FROM iam_v2.session_entitlement_bindings sb
        JOIN iam_v2.sessions se ON se.id = sb.session_id
        LEFT JOIN iam_v2.data_crossing_progress pr ON pr.binding_id = sb.id
       WHERE sb.entitlement_id = e.id
    LOOP
      IF b.final THEN
        CONTINUE;   -- an ended session's records are complete; there is nothing new to read
      END IF;
      -- ONLY the records after the checkpoint: the unique (session_id, sample_seq) index answers this directly.
      SELECT COALESCE(sum(CASE WHEN b.bound_from <= ar.sampled_at
                                    AND (b.bound_until IS NULL OR b.bound_until > ar.sampled_at)
                               THEN ar.bytes_up + ar.bytes_down ELSE 0 END), 0),
             max(ar.sample_seq)
        INTO v_new, v_max
        FROM iam_v2.accounting_records ar
       WHERE ar.session_id = b.session_id AND ar.sample_seq > b.seq;
      INSERT INTO iam_v2.data_crossing_progress AS pr
        (binding_id, tenant_id, site_id, entitlement_id, session_id, examined_seq, counted_bytes, final)
      VALUES (b.id, p_tenant, p_site, e.id, b.session_id, COALESCE(v_max, b.seq), v_new,
              -- ingestion refuses an ended session outright, so once it has been ended for a while (longer than
              -- any in-flight observation could take) its record set is closed for good
              b.ended IS NOT NULL AND b.ended < now() - interval '5 minutes')
      ON CONFLICT (binding_id) DO UPDATE
        SET examined_seq = GREATEST(pr.examined_seq, EXCLUDED.examined_seq),
            counted_bytes = pr.counted_bytes + EXCLUDED.counted_bytes,
            final = EXCLUDED.final, updated_at = now();
    END LOOP;

    SELECT COALESCE(sum(pr.counted_bytes), 0) INTO v_est
      FROM iam_v2.data_crossing_progress pr WHERE pr.entitlement_id = e.id;
    IF v_est >= e.quota THEN
      -- The running total says the quota may have been reached: establish the exact instant ONCE.
      v_exact := iam_v2.p6_data_crossing_full(e.id);
      IF v_exact IS NULL THEN
        -- The checkpoint overstated (an attribution interval shrank after it counted). Recount every binding of
        -- this entitlement exactly, so the estimate is honest again and this branch is not taken every tick.
        UPDATE iam_v2.data_crossing_progress pr
           SET counted_bytes = COALESCE((
                 SELECT sum(ar.bytes_up + ar.bytes_down)
                   FROM iam_v2.accounting_records ar
                   JOIN iam_v2.session_entitlement_bindings sb ON sb.id = pr.binding_id
                  WHERE ar.session_id = pr.session_id AND ar.sample_seq <= pr.examined_seq
                    AND sb.bound_from <= ar.sampled_at
                    AND (sb.bound_until IS NULL OR sb.bound_until > ar.sampled_at)), 0),
               updated_at = now()
         WHERE pr.entitlement_id = e.id;
      END IF;
      INSERT INTO iam_v2.data_crossing_state AS s (entitlement_id, tenant_id, site_id, quota_bytes, crossed_at)
      VALUES (e.id, p_tenant, p_site, e.quota, v_exact)
      ON CONFLICT (entitlement_id) DO UPDATE
        SET quota_bytes = EXCLUDED.quota_bytes, crossed_at = EXCLUDED.crossed_at, verified_at = now();
    END IF;
  END LOOP;
  RETURN n;
END $$;
COMMENT ON FUNCTION iam_v2.p6_advance_data_crossing(uuid, uuid) IS
  'Advances the data-crossing checkpoint of every live, quota-bearing entitlement of a site by the accounting '
  'records that arrived since the last call (0122), and establishes the exact crossing instant once the running '
  'total reaches the quota. Called by acctd before the expiry sweep. Deletes nothing.';

-- ---------------------------------------------------------------------------------------------------------
-- 5. Accounting-detail retention: the setting (default unlimited), its change log, and the guarded purge.
-- ---------------------------------------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS iam_v2.site_accounting_retention (
  tenant_id             uuid NOT NULL,
  site_id               uuid NOT NULL,
  detail_retention_days integer
    CONSTRAINT sar_days CHECK (detail_retention_days IS NULL OR detail_retention_days BETWEEN 30 AND 3650),
  config_version        bigint NOT NULL DEFAULT 1 CONSTRAINT sar_version CHECK (config_version >= 1),
  updated_at            timestamptz NOT NULL DEFAULT now(),
  updated_by            text,
  PRIMARY KEY (tenant_id, site_id)
);
COMMENT ON TABLE iam_v2.site_accounting_retention IS
  'How long per-sample accounting detail (iam_v2.accounting_records) is kept (0122). NULL -- the default, and what a '
  'site with no row reads -- is UNLIMITED: nothing is ever purged. Otherwise 30-3650 days. Written only through '
  'iam_v2.accounting_retention_set.';

CREATE TABLE IF NOT EXISTS iam_v2.accounting_retention_changes (
  id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id          uuid NOT NULL,
  site_id            uuid NOT NULL,
  changed_by         text NOT NULL CONSTRAINT arc_actor CHECK (length(btrim(changed_by)) > 0),
  change_reason      text NOT NULL CONSTRAINT arc_reason CHECK (length(btrim(change_reason)) BETWEEN 4 AND 500),
  old_retention_days integer,
  new_retention_days integer,
  new_config_version bigint NOT NULL,
  changed_at         timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS arc_site ON iam_v2.accounting_retention_changes (tenant_id, site_id, changed_at DESC);

CREATE TABLE IF NOT EXISTS iam_v2.accounting_purge_runs (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id       uuid NOT NULL,
  site_id         uuid NOT NULL,
  retention_days  integer NOT NULL,
  cutoff          timestamptz NOT NULL,
  records_deleted integer NOT NULL CONSTRAINT apr_deleted CHECK (records_deleted > 0),
  ran_at          timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS apr_site ON iam_v2.accounting_purge_runs (tenant_id, site_id, ran_at DESC);
COMMENT ON TABLE iam_v2.accounting_purge_runs IS
  'Every retention purge that removed accounting detail (0122): the retention in force, the cutoff and how many '
  'records went. Append-only. A purge that removed nothing writes nothing.';

CREATE OR REPLACE FUNCTION iam_v2.accounting_retention_append_only() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'iam_v2.% is append-only: % refused', TG_TABLE_NAME, TG_OP USING ERRCODE = 'restrict_violation';
END $$;
DROP TRIGGER IF EXISTS accounting_retention_changes_append_only ON iam_v2.accounting_retention_changes;
CREATE TRIGGER accounting_retention_changes_append_only BEFORE UPDATE OR DELETE ON iam_v2.accounting_retention_changes
  FOR EACH ROW EXECUTE FUNCTION iam_v2.accounting_retention_append_only();
DROP TRIGGER IF EXISTS accounting_purge_runs_append_only ON iam_v2.accounting_purge_runs;
CREATE TRIGGER accounting_purge_runs_append_only BEFORE UPDATE OR DELETE ON iam_v2.accounting_purge_runs
  FOR EACH ROW EXECUTE FUNCTION iam_v2.accounting_retention_append_only();

CREATE OR REPLACE FUNCTION iam_v2.accounting_retention_get(p_tenant uuid, p_site uuid)
    RETURNS TABLE(detail_retention_days integer, config_version bigint, updated_at timestamptz, updated_by text)
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
  SELECT s.detail_retention_days, COALESCE(s.config_version, 0), s.updated_at, s.updated_by
    FROM (SELECT 1) one
    LEFT JOIN iam_v2.site_accounting_retention s ON s.tenant_id = p_tenant AND s.site_id = p_site;
$$;

CREATE OR REPLACE FUNCTION iam_v2.accounting_retention_set(p_tenant uuid, p_site uuid, p_days integer,
                                                           p_operator text, p_reason text) RETURNS bigint
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE v_version bigint; v_old integer;
BEGIN
  IF p_operator IS NULL OR btrim(p_operator) = '' THEN
    RAISE EXCEPTION 'an operator label is required' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  IF p_reason IS NULL OR length(btrim(p_reason)) < 4 OR length(btrim(p_reason)) > 500 THEN
    RAISE EXCEPTION 'a reason of 4-500 characters is required to change the accounting retention'
      USING ERRCODE = 'invalid_parameter_value';
  END IF;
  IF p_days IS NOT NULL AND p_days NOT BETWEEN 30 AND 3650 THEN
    RAISE EXCEPTION 'detail_retention_days must be unlimited (null) or 30-3650 (got %)', p_days
      USING ERRCODE = 'invalid_parameter_value';
  END IF;
  PERFORM pg_advisory_xact_lock(hashtext('accounting_retention'), hashtext(p_site::text));
  SELECT s.detail_retention_days INTO v_old
    FROM iam_v2.site_accounting_retention s WHERE s.tenant_id = p_tenant AND s.site_id = p_site FOR UPDATE;
  INSERT INTO iam_v2.site_accounting_retention AS s (tenant_id, site_id, detail_retention_days, updated_by)
  VALUES (p_tenant, p_site, p_days, btrim(p_operator))
  ON CONFLICT (tenant_id, site_id) DO UPDATE
     SET detail_retention_days = EXCLUDED.detail_retention_days,
         updated_by = EXCLUDED.updated_by, config_version = s.config_version + 1, updated_at = now()
  RETURNING s.config_version INTO v_version;
  INSERT INTO iam_v2.accounting_retention_changes
    (tenant_id, site_id, changed_by, change_reason, old_retention_days, new_retention_days, new_config_version)
  VALUES (p_tenant, p_site, btrim(p_operator), btrim(p_reason), v_old, p_days, v_version);
  RETURN v_version;
END $$;

-- accounting_records stays immutable. The one exception is a DELETE made by the retention purge below: it runs
-- as the table owner and declares itself in a transaction-local setting, so no runtime role -- which holds no
-- DELETE and is never the owner -- can reach it. The message for every other attempt is the one it always was.
CREATE OR REPLACE FUNCTION iam_v2.accounting_records_append_only() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP = 'DELETE'
     AND current_setting('iam_v2.accounting_purge', true) = 'retention'
     AND current_user = (SELECT pg_get_userbyid(c.relowner) FROM pg_class c WHERE c.oid = TG_RELID) THEN
    RETURN OLD;
  END IF;
  RAISE EXCEPTION '% is immutable (no UPDATE/DELETE) on %', TG_TABLE_NAME, TG_OP;
END $$;
DROP TRIGGER IF EXISTS ao_accounting ON iam_v2.accounting_records;
CREATE TRIGGER ao_accounting BEFORE DELETE OR UPDATE ON iam_v2.accounting_records
  FOR EACH ROW EXECUTE FUNCTION iam_v2.accounting_records_append_only();

CREATE OR REPLACE FUNCTION iam_v2.purge_accounting_detail(p_tenant uuid, p_site uuid, p_batch integer)
    RETURNS integer
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE v_days integer; v_cutoff timestamptz; v_n integer;
BEGIN
  IF p_batch IS NULL OR p_batch NOT BETWEEN 1 AND 100000 THEN
    RAISE EXCEPTION 'a batch of 1-100000 records is required' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  SELECT s.detail_retention_days INTO v_days
    FROM iam_v2.site_accounting_retention s WHERE s.tenant_id = p_tenant AND s.site_id = p_site;
  IF v_days IS NULL THEN
    RETURN 0;   -- UNLIMITED (the default): nothing is read, nothing is deleted, nothing is recorded
  END IF;
  v_cutoff := now() - make_interval(days => v_days);
  PERFORM set_config('iam_v2.accounting_purge', 'retention', true);
  WITH doomed AS (
    SELECT ar.id
      FROM iam_v2.accounting_records ar
      JOIN iam_v2.sessions se ON se.id = ar.session_id
      JOIN iam_v2.entitlements cur ON cur.id = se.entitlement_id
     WHERE ar.tenant_id = p_tenant AND ar.site_id = p_site
       AND ar.sampled_at < v_cutoff
       AND se.ended IS NOT NULL AND se.ended < v_cutoff
       AND cur.status = 'TERMINATED'
       -- every entitlement the session was ever attributed to has ended: live usage is never touched
       AND NOT EXISTS (SELECT 1 FROM iam_v2.session_entitlement_bindings b
                         JOIN iam_v2.entitlements be ON be.id = b.entitlement_id
                        WHERE b.session_id = ar.session_id AND be.status <> 'TERMINATED')
     LIMIT p_batch)
  DELETE FROM iam_v2.accounting_records ar USING doomed WHERE ar.id = doomed.id;
  GET DIAGNOSTICS v_n = ROW_COUNT;
  PERFORM set_config('iam_v2.accounting_purge', '', true);
  IF v_n > 0 THEN
    INSERT INTO iam_v2.accounting_purge_runs (tenant_id, site_id, retention_days, cutoff, records_deleted)
    VALUES (p_tenant, p_site, v_days, v_cutoff, v_n);
  END IF;
  RETURN v_n;
END $$;
COMMENT ON FUNCTION iam_v2.purge_accounting_detail(uuid, uuid, integer) IS
  'The accounting-detail retention purge (0122). Does nothing while the site''s retention is unlimited (the '
  'default). Otherwise removes at most p_batch accounting records older than the retention, of sessions ended '
  'before it whose every entitlement is TERMINATED, and records the run. Totals on entitlements and sessions are '
  'not touched.';

-- ---------------------------------------------------------------------------------------------------------
-- 6. Ownership (Gate-P re-asserts it; a migration applied outside Gate-P must not leave the wrong owner).
-- ---------------------------------------------------------------------------------------------------------
DO $own$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
    EXECUTE 'ALTER TABLE iam_v2.device_limit_disconnections OWNER TO iam_v2_owner';
    EXECUTE 'ALTER TABLE iam_v2.data_crossing_progress OWNER TO iam_v2_owner';
    EXECUTE 'ALTER TABLE iam_v2.data_crossing_state OWNER TO iam_v2_owner';
    EXECUTE 'ALTER TABLE iam_v2.site_accounting_retention OWNER TO iam_v2_owner';
    EXECUTE 'ALTER TABLE iam_v2.accounting_retention_changes OWNER TO iam_v2_owner';
    EXECUTE 'ALTER TABLE iam_v2.accounting_purge_runs OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.p6_counts_online_time(text,bigint) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.p6_end_sessions_over_plan_limits(uuid,uuid,timestamptz,boolean) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.device_limit_disconnections_append_only() OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.p6_data_crossing_full(uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.p6_advance_data_crossing(uuid,uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.accounting_retention_append_only() OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.accounting_retention_get(uuid,uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.accounting_retention_set(uuid,uuid,integer,text,text) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.accounting_records_append_only() OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.purge_accounting_detail(uuid,uuid,integer) OWNER TO iam_v2_owner';
  END IF;
END $own$;

-- New definer functions are not for PUBLIC.
REVOKE ALL ON FUNCTION iam_v2.p6_end_sessions_over_plan_limits(uuid,uuid,timestamptz,boolean) FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.p6_data_crossing_full(uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.p6_advance_data_crossing(uuid,uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.accounting_retention_get(uuid,uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.accounting_retention_set(uuid,uuid,integer,text,text) FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.purge_accounting_detail(uuid,uuid,integer) FROM PUBLIC;

-- ---------------------------------------------------------------------------------------------------------
-- 7. Least privilege, mirrored in deploy/gatep/svc-plan-controls-grants.sql (a Gate-P reconcile revokes and
--    re-grants from those files, so a grant that lived only here would not survive it).
-- ---------------------------------------------------------------------------------------------------------
DO $g$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_acctd') THEN
    GRANT EXECUTE ON FUNCTION iam_v2.p6_end_sessions_over_plan_limits(uuid,uuid,timestamptz,boolean) TO svc_acctd;
    GRANT EXECUTE ON FUNCTION iam_v2.p6_advance_data_crossing(uuid,uuid) TO svc_acctd;
    GRANT EXECUTE ON FUNCTION iam_v2.purge_accounting_detail(uuid,uuid,integer) TO svc_acctd;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
    GRANT EXECUTE ON FUNCTION iam_v2.accounting_retention_get(uuid,uuid) TO svc_edged;
    GRANT EXECUTE ON FUNCTION iam_v2.accounting_retention_set(uuid,uuid,integer,text,text) TO svc_edged;
    GRANT SELECT ON iam_v2.accounting_retention_changes, iam_v2.accounting_purge_runs,
                    iam_v2.device_limit_disconnections TO svc_edged;
  END IF;
END $g$;

COMMIT;
