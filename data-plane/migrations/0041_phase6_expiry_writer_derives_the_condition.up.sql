-- PHASE 6 M3 — the expiry writer stops trusting its caller.
--
-- WHAT 0040 STILL ALLOWED. p6_expire_entitlement took an entitlement id, an instant and a reason, checked
-- that the reason was TIME or DATA and that the instant was not in the future, and then terminated. Every one
-- of those inputs came from the caller. So svc_acctd could end ANY live entitlement it could name, at any past
-- instant, by labelling it 'DATA' -- which is generic destructive termination authority wearing two labels.
-- The narrow-looking vocabulary check was the only thing between an accounting daemon and revoking a guest's
-- access on its own say-so.
--
-- A controlled writer's job is not to validate what the caller SAYS. It is to establish the fact itself.
--
-- SO THE WRITER NOW DERIVES THE TERMINAL CONDITION FROM AUTHORITATIVE STATE, and takes nothing but the
-- entitlement id. It answers three questions from the database:
--
--   * has the immutable outer window elapsed?
--   * has the data quota been crossed, and at which sample?
--   * has an AGGREGATE_ONLINE_TIME budget been exhausted, and when?
--
-- ...takes the EARLIEST of whatever it finds, and terminates with THAT reason at THAT instant. If nothing is
-- due, it refuses. A caller can no longer end a healthy entitlement, misdate an ending, or relabel one
-- condition as another, because it no longer supplies any of those things.
--
-- ONE SOURCE OF DATA-CROSSING TRUTH. The crossing is computed by p6_data_crossing, and the expiry sweep's
-- candidate query calls the same function. Two implementations of that running-sum would be two answers that
-- drift, and the drift would decide which condition ended a guest's access.
BEGIN;

-- ---------------------------------------------------------------------------------------------------------
-- 1. The data crossing, in one place
-- ---------------------------------------------------------------------------------------------------------
-- The sample at which attributed usage first reached the plan's quota -- not when a sweep noticed. NULL when
-- there is no quota or it has not been crossed. Attribution follows the session/entitlement binding intervals,
-- so usage counts against the entitlement it was actually taken under.
CREATE OR REPLACE FUNCTION iam_v2.p6_data_crossing(p_entitlement uuid)
RETURNS timestamptz
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = iam_v2, pg_temp
AS $$
  SELECT min(x.sampled_at)
    FROM iam_v2.entitlements e
    JOIN iam_v2.service_plan_revisions spr ON spr.id = e.service_plan_revision_id
    CROSS JOIN LATERAL (
      SELECT ar.sampled_at,
             sum(ar.bytes_up + ar.bytes_down) OVER (ORDER BY ar.sampled_at, ar.id) AS running
        FROM iam_v2.accounting_records ar
        JOIN iam_v2.session_entitlement_bindings b ON b.session_id = ar.session_id
         AND b.entitlement_id = e.id AND b.bound_from <= ar.sampled_at
         AND (b.bound_until IS NULL OR b.bound_until > ar.sampled_at)
    ) x
   WHERE e.id = p_entitlement
     AND spr.data_quota_bytes IS NOT NULL
     AND x.running >= spr.data_quota_bytes;
$$;

COMMENT ON FUNCTION iam_v2.p6_data_crossing(uuid) IS
  'The instant attributed usage first reached the plan quota, or NULL. The single implementation: both the '
  'expiry sweep''s candidate query and the sanctioned expiry writer call it, so they cannot disagree about '
  'when -- or whether -- a guest ran out of data.';

-- ---------------------------------------------------------------------------------------------------------
-- 2. What, if anything, is actually due
-- ---------------------------------------------------------------------------------------------------------
-- Returns no row when the entitlement is healthy. That is the whole authorization decision: the writer below
-- terminates only what this function says is already over.
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

COMMENT ON FUNCTION iam_v2.p6_due_terminal(uuid) IS
  'The terminal condition an entitlement has ALREADY reached -- earliest of outer window, data crossing and '
  'aggregate exhaustion -- or no row when it is healthy. This is the fact the expiry writer establishes for '
  'itself instead of accepting from its caller.';

-- ---------------------------------------------------------------------------------------------------------
-- 3. The writer, which now takes only an id
-- ---------------------------------------------------------------------------------------------------------
DROP FUNCTION IF EXISTS iam_v2.p6_expire_entitlement(uuid, timestamptz, text, text);

CREATE OR REPLACE FUNCTION iam_v2.p6_expire_entitlement(p_entitlement uuid)
RETURNS TABLE (reason text, at timestamptz, devices int, sessions int)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = iam_v2, pg_temp
AS $$
DECLARE
  due        record;
  v_status   text;
  v_devices  int;
  v_sessions int;
BEGIN
  -- The entitlement row is locked FIRST, in the global lock order, so the condition cannot be established
  -- against state another transaction is changing underneath.
  SELECT status INTO v_status FROM iam_v2.entitlements WHERE id = p_entitlement FOR UPDATE;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'no such entitlement %', p_entitlement USING ERRCODE = 'foreign_key_violation';
  END IF;
  IF v_status = 'TERMINATED' THEN
    -- Idempotent: a re-run, or a sweep that lost the race, changes nothing.
    RETURN;
  END IF;

  SELECT d.reason, d.at, d.time_cause INTO due FROM iam_v2.p6_due_terminal(p_entitlement) d;
  IF due.reason IS NULL THEN
    -- THE REFUSAL THAT MATTERS. A caller naming a healthy entitlement gets nothing: no termination, no
    -- revocation, no evidence. It cannot end access by asserting that access is over.
    RAISE EXCEPTION 'entitlement % has not reached any terminal condition', p_entitlement
      USING ERRCODE = 'insufficient_privilege';
  END IF;

  -- The ONE termination path, with the reason and instant this function derived.
  PERFORM iam_v2.terminate_entitlement_at_boundary(p_entitlement, due.at, due.reason);

  -- Phase-6 time evidence, likewise derived: which clock ran out is not a caller's claim either.
  IF due.time_cause IS NOT NULL THEN
    PERFORM iam_v2.p6_record_time_termination(p_entitlement, due.time_cause);
  END IF;

  PERFORM iam_v2.begin_controlled_operation('device_auth');

  WITH closed AS (
    UPDATE iam_v2.entitlement_device_authorizations a
       SET deauthorized_at = GREATEST(due.at, a.authorized_at)
     WHERE a.entitlement_id = p_entitlement AND a.deauthorized_at IS NULL
     RETURNING a.entitlement_id, a.device_id)
  UPDATE iam_v2.entitlement_devices ed
     SET status = 'DISCONNECTED', disconnected_reason = 'ENTITLEMENT_ENDED'
    FROM closed
   WHERE ed.entitlement_id = closed.entitlement_id AND ed.device_id = closed.device_id;
  GET DIAGNOSTICS v_devices = ROW_COUNT;

  UPDATE iam_v2.sessions
     SET state = 'ended', ended = GREATEST(due.at, started), end_reason = 'ENTITLEMENT_ENDED'
   WHERE entitlement_id = p_entitlement AND state IN ('active', 'PENDING_ENFORCEMENT');
  GET DIAGNOSTICS v_sessions = ROW_COUNT;

  reason := due.reason; at := due.at; devices := v_devices; sessions := v_sessions;
  RETURN NEXT;
END $$;

COMMENT ON FUNCTION iam_v2.p6_expire_entitlement(uuid) IS
  'Ends an entitlement that the DATABASE says has already reached a terminal condition, at the instant it '
  'reached it, with the reason and time-cause derived here rather than supplied. Refuses a healthy '
  'entitlement. The accounting daemon holds EXECUTE on this and on nothing it calls.';

REVOKE ALL ON FUNCTION iam_v2.p6_expire_entitlement(uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.p6_due_terminal(uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.p6_data_crossing(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION iam_v2.p6_expire_entitlement(uuid) TO svc_acctd;
-- The sweep's candidate query calls the crossing directly, so the same one implementation answers both.
GRANT EXECUTE ON FUNCTION iam_v2.p6_data_crossing(uuid) TO svc_acctd;

DO $$
DECLARE bad text;
BEGIN
  IF has_function_privilege('public', 'iam_v2.p6_expire_entitlement(uuid)', 'EXECUTE') THEN
    RAISE EXCEPTION 'PUBLIC can execute the expiry writer';
  END IF;
  IF has_function_privilege('svc_acctd', 'iam_v2.p6_due_terminal(uuid)', 'EXECUTE') THEN
    RAISE EXCEPTION 'svc_acctd can call p6_due_terminal directly; it is the writer''s own evidence step';
  END IF;
  SELECT string_agg(format('%s on %s', privilege_type, table_name), ', ')
    INTO bad
    FROM information_schema.role_table_grants
   WHERE table_schema = 'iam_v2' AND grantee = 'svc_acctd' AND privilege_type <> 'SELECT';
  IF bad IS NOT NULL THEN
    RAISE EXCEPTION 'svc_acctd holds write authority in iam_v2: %', bad;
  END IF;
END $$;

COMMIT;
