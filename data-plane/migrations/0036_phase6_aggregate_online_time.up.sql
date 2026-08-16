-- PHASE 6 M3 — AGGREGATE_ONLINE_TIME consumption.
--
-- ADDITIVE ONLY, and DARK: this migration creates one evidence table and one function. Nothing calls the
-- function until the Phase-6 aggregate flag is on, and no existing entitlement is in AGGREGATE_ONLINE_TIME
-- mode, so applying it changes the behaviour of exactly nothing.
--
-- WHAT THE MODE IS
--
--   A budget of ONLINE SECONDS, shared across the entitlement's devices, inside an outer hard-validity
--   window. Two eligible devices online for ten minutes consume TWENTY aggregate minutes -- the contract's
--   shared device-minute semantics, which fall out of summing per-session accrual rather than needing a
--   second mechanism.
--
-- THE THREE THINGS THIS FUNCTION HAS TO GET RIGHT
--
--   1. IDEMPOTENCY. Bytes are safe today because they are cumulative counters compared against a watermark.
--      Wall-clock time has no counter: charge "now - last time I looked" twice and the guest pays twice. So
--      every accrual charges (ceiling - accounted_through) and advances the watermark to that same ceiling,
--      in ONE transaction. A replayed tick then charges exactly zero, because the watermark already moved.
--
--   2. ELAPSED TIME IS NOT ONLINE TIME. A watermark proves idempotency and NOTHING about whether the
--      interval it charges was time the guest was actually online. An appliance powered off for six hours
--      would bill six hours the moment it came back: the session row still says 'active' because nothing was
--      running to end it. Accrual is therefore BOUNDED by observation -- each tick charges at most
--      p_max_charge_seconds, and any remainder beyond that bound is recorded as a SKIPPED interval and never
--      charged. The system under-charges by design: taking minutes from a guest who was not using them
--      cannot be undone without an audited adjustment, while declining to charge gives away at most one
--      bound's worth per outage.
--
--   3. ELIGIBILITY IS 'active' ONLY. Never PENDING_ENFORCEMENT. That state means a grant whose kernel
--      authorization is still converging -- the guest may have no forwarding at all yet -- and charging it
--      would bill for access that was intended rather than delivered. This is deliberately NOT the same
--      predicate as removal safety (Phase-6 M2), which DOES include PENDING_ENFORCEMENT: for charging, an
--      unproven state must not count; for removing, it must. Each resolves in the direction that cannot harm
--      the guest, and one predicate could not do both.
--
-- WHAT IT DELIBERATELY DOES NOT DO
--
--   * It does not terminate anything. Exhaustion is REPORTED, with the instant it happened, and the caller
--     terminates through the one existing boundary path (terminate_entitlement_at_boundary + revoke) inside
--     the same transaction. A second termination path is a second set of rules to keep in step.
--   * It does not touch the outer window. That is entitlements.window_ends_at, already handled by the
--     existing expiry sweep with the existing TIME reason.
--   * It does not widen any contract vocabulary.
--   * It grants nothing. Phase 6 is DARK and privileges are derived from a real audit in M4.
BEGIN;

-- ---------------------------------------------------------------------------------------------------------
-- 1. Skipped intervals: the evidence that keeps under-charging honest
-- ---------------------------------------------------------------------------------------------------------
-- Declining to charge unobserved time is correct. Declining SILENTLY is not: "the guest was online for six
-- hours and was charged four minutes" must be answerable afterwards, and the only way to answer it is to have
-- written down what was skipped and why at the moment it was skipped.
CREATE TABLE iam_v2.online_time_skipped_intervals (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL,
  site_id uuid NOT NULL,
  session_id uuid NOT NULL,
  entitlement_id uuid NOT NULL,
  skipped_from timestamptz NOT NULL,
  skipped_to timestamptz NOT NULL,
  -- UNOBSERVED_GAP is the only cause today: the accounting service was not running (or not reaching this
  -- entitlement) for longer than one tick's charge bound. It is a CHECK rather than free text so a second
  -- cause has to be introduced deliberately.
  cause text NOT NULL CHECK (cause IN ('UNOBSERVED_GAP')),
  recorded_at timestamptz NOT NULL DEFAULT now(),
  CHECK (skipped_to > skipped_from)
);

CREATE INDEX online_time_skipped_intervals_ent_idx
  ON iam_v2.online_time_skipped_intervals (entitlement_id, skipped_from);

COMMENT ON TABLE iam_v2.online_time_skipped_intervals IS
  'Intervals of apparently-elapsed session time that were deliberately NOT charged to an AGGREGATE_ONLINE_TIME '
  'budget because the accounting service was not observing them. Under-charging is the intended failure '
  'direction; this table is what makes it visible rather than silent.';

-- Append-only. An interval that was skipped stays skipped: rewriting it later would be inventing an
-- observation nobody made.
CREATE OR REPLACE FUNCTION iam_v2.p6_skipped_intervals_append_only() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'iam_v2.online_time_skipped_intervals is append-only (attempted %)', TG_OP
    USING ERRCODE = 'restrict_violation';
END $$;

CREATE TRIGGER p6_skipped_intervals_append_only
  BEFORE UPDATE OR DELETE ON iam_v2.online_time_skipped_intervals
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p6_skipped_intervals_append_only();

-- ---------------------------------------------------------------------------------------------------------
-- 2. The durable crossing instant
-- ---------------------------------------------------------------------------------------------------------
-- ADDITIVE, NULLABLE, and nothing reads it outside this mode. It exists because "when did the budget run
-- out" must survive the tick that discovered it.
--
-- The tick REPORTS exhaustion and the caller terminates. If the caller crashes between the two, the next
-- tick has to report it again -- an entitlement over budget that is still live must be terminated, and a
-- sweep that reported the crossing only once would leave it running forever. But re-reporting must not
-- re-date it: the budget ran out when it ran out, and stamping the retry with the retry's clock would be the
-- same defect as stamping the original with sweep time. So the instant is written down the first time and
-- read back on every later tick.
ALTER TABLE iam_v2.entitlements ADD COLUMN IF NOT EXISTS online_time_exhausted_at timestamptz;

COMMENT ON COLUMN iam_v2.entitlements.online_time_exhausted_at IS
  'For AGGREGATE_ONLINE_TIME: the instant the online-time budget was exhausted, computed inside the tick that '
  'crossed it. Stable across retries -- a re-reported exhaustion carries the original instant, never the '
  'clock of the tick that re-reported it.';

-- ---------------------------------------------------------------------------------------------------------
-- 3. The tick
-- ---------------------------------------------------------------------------------------------------------
-- Returns one row per entitlement whose budget was exhausted during THIS tick, with the instant it happened.
-- An entitlement that is still within budget returns nothing; an entitlement already terminated is skipped
-- entirely, because a late tick may never reopen a terminal entitlement (contract 6.4, stated for bytes and
-- true for the same reason here).
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
  'One AGGREGATE_ONLINE_TIME accrual tick for a site. Charges only sessions in state active, bounded by '
  'p_max_charge_seconds so unobserved time is recorded as skipped rather than charged, advances the durable '
  'per-session watermark in the same transaction so a replay charges zero, and RETURNS the entitlements whose '
  'budget was exhausted with the instant it happened. It terminates nothing: the caller does that through the '
  'one existing boundary path in this same transaction.';

-- A function's ACL starts NULL, and NULL means PUBLIC EXECUTE. This one writes accounting state, so it is
-- revoked from PUBLIC here and granted to the exact runtime role in the M4 privilege audit -- never earlier.
REVOKE ALL ON FUNCTION iam_v2.p6_tick_online_time(uuid, uuid, timestamptz, int) FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.p6_skipped_intervals_append_only() FROM PUBLIC;

COMMIT;
