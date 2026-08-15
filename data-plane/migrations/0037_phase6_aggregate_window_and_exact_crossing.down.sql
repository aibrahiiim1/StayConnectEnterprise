-- Reverse 0037: restore the 0036 accrual tick exactly as it was.
--
-- IT IS FAITHFUL, WHICH MEANS IT REINSTATES TWO DEFECTS: accrual that ignores the entitlement's outer window,
-- and a crossing instant that assumes every contributing session burns the same interval. A rollback that
-- quietly kept the corrections would make the pair untrustworthy in both directions, so the consequence is
-- stated instead of hidden: before rolling this back, the aggregate time mode must be OFF and no entitlement
-- may be in AGGREGATE_ONLINE_TIME mode -- the same disable-then-roll-back ordering 0032 and 0036 require.
BEGIN;

CREATE OR REPLACE FUNCTION iam_v2.p6_tick_online_time(
  p_tenant uuid,
  p_site uuid,
  p_now timestamptz,
  p_max_charge_seconds int
) RETURNS TABLE (entitlement_id uuid, exhausted_at timestamptz)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = iam_v2, pg_temp
AS $$
DECLARE
  e            record;
  s            record;
  v_budget     bigint;
  v_before     bigint;
  v_charged    bigint;
  v_sessions   int;
  v_first_base timestamptz;
  v_base       timestamptz;
  v_ceiling    timestamptz;
  v_raw        bigint;
  v_charge     bigint;
  v_remaining  bigint;
BEGIN
  IF p_max_charge_seconds IS NULL OR p_max_charge_seconds <= 0 THEN
    RAISE EXCEPTION 'a charge bound is required: unbounded accrual would charge unobserved time'
      USING ERRCODE = 'invalid_parameter_value';
  END IF;

  -- Entitlements in this mode that are still live, LOCKED IN ID ORDER. The lock order is the global one
  -- (L3 entitlement first), which is what lets this run alongside device admission and guest self-service
  -- release without inventing a lock of its own.
  FOR e IN
    SELECT en.id, en.consumed_online_seconds, en.service_plan_revision_id, en.online_time_exhausted_at
      FROM iam_v2.entitlements en
     WHERE en.tenant_id = p_tenant AND en.site_id = p_site
       AND en.time_accounting_mode = 'AGGREGATE_ONLINE_TIME'
       AND en.status IN ('ACTIVE', 'PENDING', 'SUSPENDED')
     ORDER BY en.id
     FOR UPDATE
  LOOP
    SELECT spr.time_quota_seconds INTO v_budget
      FROM iam_v2.service_plan_revisions spr WHERE spr.id = e.service_plan_revision_id;

    v_before     := COALESCE(e.consumed_online_seconds, 0);

    -- ALREADY EXHAUSTED AND STILL LIVE. Charge nothing more -- the budget is spent and consumption is
    -- capped -- but report it again, with the ORIGINAL crossing instant, so a caller that failed to
    -- terminate last time still converges.
    IF v_budget IS NOT NULL AND (e.online_time_exhausted_at IS NOT NULL OR v_before >= v_budget) THEN
      entitlement_id := e.id;
      exhausted_at   := COALESCE(e.online_time_exhausted_at, p_now);
      IF e.online_time_exhausted_at IS NULL THEN
        UPDATE iam_v2.entitlements SET online_time_exhausted_at = p_now WHERE id = e.id;
      END IF;
      RETURN NEXT;
      CONTINUE;
    END IF;

    v_charged    := 0;
    v_sessions   := 0;
    v_first_base := NULL;

    -- Every session that could still owe time: eligible now, or ended with its watermark behind its end.
    -- The second case is what stops a session that ended while the service was down from silently losing
    -- the interval it genuinely was online -- bounded, as always, by the charge limit.
    FOR s IN
      SELECT se.id, se.state, se.started, se.ended,
             w.accounted_through, w.accounted_seconds
        FROM iam_v2.sessions se
        LEFT JOIN iam_v2.session_online_watermarks w ON w.session_id = se.id
       WHERE se.entitlement_id = e.id
         AND (se.state = 'active' OR (se.ended IS NOT NULL AND w.accounted_through IS NOT NULL
                                      AND w.accounted_through < se.ended))
       ORDER BY se.id
       FOR UPDATE OF se
    LOOP
      -- THE BASELINE, AND THE FIRST-OBSERVATION RULE.
      --
      -- A session with a watermark accrues from it. A session WITHOUT one has never been observed by this
      -- accounting path, and the interval since it started is therefore not evidence of anything: it may
      -- have spent that time PENDING_ENFORCEMENT with no forwarding at all, or the service may simply not
      -- have been running. Baselining at `started` charged exactly that interval -- up to the bound -- which
      -- is the defect 3.2z exists to prevent, and the gate caught it: a session that waited half an hour to
      -- be enforced was billed ten minutes the moment it went active.
      --
      -- So the first observation charges NOTHING and baselines at now. Everything after it is real observed
      -- time. If the unobserved interval was long enough to matter, it is recorded as skipped, which is what
      -- keeps under-charging visible rather than silent.
      IF s.accounted_through IS NULL THEN
        IF EXTRACT(EPOCH FROM (p_now - s.started)) > p_max_charge_seconds THEN
          INSERT INTO iam_v2.online_time_skipped_intervals
            (tenant_id, site_id, session_id, entitlement_id, skipped_from, skipped_to, cause)
          VALUES (p_tenant, p_site, s.id, e.id, s.started, p_now, 'UNOBSERVED_GAP');
        END IF;
        INSERT INTO iam_v2.session_online_watermarks
          (tenant_id, site_id, session_id, accounted_through, accounted_seconds)
        VALUES (p_tenant, p_site, s.id, LEAST(p_now, COALESCE(s.ended, p_now)), 0)
        ON CONFLICT (session_id) DO NOTHING;
        CONTINUE;
      END IF;
      v_base    := s.accounted_through;
      -- THE CEILING. An ended session stops at its end instant, never at now: time after a session ended is
      -- not time the guest was online, whatever the sweep's clock says.
      v_ceiling := LEAST(p_now, COALESCE(s.ended, p_now));

      v_raw := GREATEST(0, FLOOR(EXTRACT(EPOCH FROM (v_ceiling - v_base)))::bigint);
      IF v_raw = 0 THEN
        CONTINUE;
      END IF;
      v_charge := LEAST(v_raw, p_max_charge_seconds::bigint);

      IF v_charge < v_raw THEN
        -- The unobserved remainder. Recorded, not charged.
        INSERT INTO iam_v2.online_time_skipped_intervals
          (tenant_id, site_id, session_id, entitlement_id, skipped_from, skipped_to, cause)
        VALUES (p_tenant, p_site, s.id, e.id,
                v_base + make_interval(secs => v_charge), v_ceiling, 'UNOBSERVED_GAP');
      END IF;

      -- The watermark advances to the CEILING, not to base+charge: the skipped interval has been accounted
      -- for by being recorded, and leaving the watermark behind would charge it on the next tick after all.
      INSERT INTO iam_v2.session_online_watermarks
        (tenant_id, site_id, session_id, accounted_through, accounted_seconds)
      VALUES (p_tenant, p_site, s.id, v_ceiling, v_charge)
      ON CONFLICT (session_id) DO UPDATE
        SET accounted_through = EXCLUDED.accounted_through,
            accounted_seconds = iam_v2.session_online_watermarks.accounted_seconds + EXCLUDED.accounted_seconds,
            updated_at = now();

      v_charged := v_charged + v_charge;
      IF s.state = 'active' THEN
        v_sessions := v_sessions + 1;
      END IF;
      IF v_first_base IS NULL OR v_base < v_first_base THEN
        v_first_base := v_base;
      END IF;
    END LOOP;

    IF v_charged = 0 THEN
      CONTINUE;
    END IF;

    -- Exhaustion is decided against the budget, and the stored total is CAPPED at it. Remaining time is
    -- (budget - consumed) and can therefore never be negative -- a guest is never told they owe minutes.
    IF v_budget IS NOT NULL AND v_before + v_charged >= v_budget THEN
      v_remaining := GREATEST(0, v_budget - v_before);

      -- THE CROSSING INSTANT, COMPUTED INSIDE THE TICK. With n sessions consuming, the budget drains n times
      -- faster, so it ran out at base + remaining/n -- not at the moment this sweep happened to look. This is
      -- the same rule the byte path already follows: a crossing is recorded at the sample that crossed it.
      exhausted_at := LEAST(p_now,
        COALESCE(v_first_base, p_now) + make_interval(secs => v_remaining::numeric / GREATEST(v_sessions, 1)));
      UPDATE iam_v2.entitlements
         SET consumed_online_seconds = v_budget, online_time_exhausted_at = exhausted_at
       WHERE id = e.id;
      entitlement_id := e.id;
      RETURN NEXT;
    ELSE
      UPDATE iam_v2.entitlements
         SET consumed_online_seconds = v_before + v_charged
       WHERE id = e.id;
    END IF;
  END LOOP;
END $$;

COMMENT ON FUNCTION iam_v2.p6_tick_online_time(uuid, uuid, timestamptz, int) IS
  'One AGGREGATE_ONLINE_TIME accrual tick for a site (0036 behaviour, restored by the 0037 down migration).';

REVOKE ALL ON FUNCTION iam_v2.p6_tick_online_time(uuid, uuid, timestamptz, int) FROM PUBLIC;

COMMIT;
