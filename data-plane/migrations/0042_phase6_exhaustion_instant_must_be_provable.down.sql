-- Reverse 0042: restore the 0041 due-terminal and the 0038 tick.
--
-- FAITHFUL, AND THEREFORE IT REINSTATES THE FABRICATION: both restored functions fall back to now() for an
-- entitlement that reached its budget without a stamp, writing a historical instant nobody observed. Disable
-- the aggregate mode before rolling back.
BEGIN;

CREATE OR REPLACE FUNCTION iam_v2.p6_due_terminal(p_entitlement uuid)
RETURNS TABLE (reason text, at timestamptz, time_cause text)
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = iam_v2, pg_temp
AS $$
DECLARE
  e        record;
  v_budget bigint;
  v_win    timestamptz;
  v_data   timestamptz;
  v_agg    timestamptz;
BEGIN
  SELECT id, status, time_accounting_mode, window_ends_at, consumed_online_seconds,
         online_time_exhausted_at, service_plan_revision_id
    INTO e
    FROM iam_v2.entitlements WHERE id = p_entitlement;
  IF NOT FOUND OR e.status NOT IN ('ACTIVE', 'PENDING', 'SUSPENDED') THEN
    RETURN;   -- nothing is due for an entitlement that does not exist or has already ended
  END IF;

  -- (a) the immutable outer window
  IF e.window_ends_at IS NOT NULL AND e.window_ends_at <= now() THEN
    v_win := e.window_ends_at;
  END IF;

  -- (b) the data quota, from the one implementation
  v_data := iam_v2.p6_data_crossing(p_entitlement);

  -- (c) an exhausted online-time budget. The instant was computed inside the tick that crossed it and stored;
  --     the consumption comparison is the belt to that braces, for a budget reached without a stamp.
  IF e.time_accounting_mode = 'AGGREGATE_ONLINE_TIME' THEN
    SELECT spr.time_quota_seconds INTO v_budget
      FROM iam_v2.service_plan_revisions spr WHERE spr.id = e.service_plan_revision_id;
    IF v_budget IS NOT NULL AND (e.online_time_exhausted_at IS NOT NULL
                                 OR COALESCE(e.consumed_online_seconds, 0) >= v_budget) THEN
      v_agg := COALESCE(e.online_time_exhausted_at, now());
    END IF;
  END IF;

  IF v_win IS NULL AND v_data IS NULL AND v_agg IS NULL THEN
    RETURN;   -- healthy
  END IF;

  -- THE EARLIEST WINS. Section 6.1: the first reached terminal condition triggers ONE transition, and the
  -- reason follows the instant rather than the other way round.
  at := LEAST(COALESCE(v_win, 'infinity'::timestamptz),
              COALESCE(v_data, 'infinity'::timestamptz),
              COALESCE(v_agg, 'infinity'::timestamptz));
  IF v_data IS NOT NULL AND v_data = at THEN
    reason := 'DATA';
    time_cause := NULL;
  ELSIF v_agg IS NOT NULL AND v_agg = at THEN
    reason := 'TIME';
    time_cause := 'AGGREGATE_ONLINE_TIME_EXHAUSTED';
  ELSE
    reason := 'TIME';
    time_cause := CASE WHEN e.time_accounting_mode = 'AGGREGATE_ONLINE_TIME'
                       THEN 'AGGREGATE_OUTER_WINDOW_EXPIRED' ELSE 'VALIDITY_WINDOW_ELAPSED' END;
  END IF;
  RETURN NEXT;
END $$;

CREATE OR REPLACE FUNCTION iam_v2.p6_tick_online_time(
  p_tenant uuid,
  p_site uuid,
  p_now timestamptz,
  p_max_charge_seconds int,
  -- THE CAPS. Parallel arrays of (entitlement, earliest terminal instant already known to the caller). The
  -- caller is the expiry sweep, which ALREADY computes the true DATA crossing and the window elapse; passing
  -- them in keeps that computation in the one place that owns it. An entitlement not named here has no cap.
  p_capped_entitlements uuid[] DEFAULT '{}'::uuid[],
  p_caps timestamptz[] DEFAULT '{}'::timestamptz[]
) RETURNS TABLE (entitlement_id uuid, exhausted_at timestamptz)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = iam_v2, pg_temp
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
       AND en.time_accounting_mode = 'AGGREGATE_ONLINE_TIME'
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

    -- Already exhausted and still live: charge nothing more, report again with the ORIGINAL instant so a
    -- caller that failed to terminate last time still converges.
    IF v_budget IS NOT NULL AND (e.online_time_exhausted_at IS NOT NULL OR v_before >= v_budget) THEN
      entitlement_id := e.id;
      exhausted_at   := COALESCE(e.online_time_exhausted_at, p_now);
      IF e.online_time_exhausted_at IS NULL THEN
        UPDATE iam_v2.entitlements SET online_time_exhausted_at = p_now WHERE id = e.id;
      END IF;
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

DROP FUNCTION IF EXISTS iam_v2.p6_exhaustion_instant(uuid);

REVOKE ALL ON FUNCTION iam_v2.p6_due_terminal(uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.p6_tick_online_time(uuid, uuid, timestamptz, int, uuid[], timestamptz[]) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION iam_v2.p6_tick_online_time(uuid, uuid, timestamptz, int, uuid[], timestamptz[]) TO svc_acctd;

COMMIT;
