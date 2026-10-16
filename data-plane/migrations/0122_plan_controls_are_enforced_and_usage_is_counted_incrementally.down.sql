-- 0122 down -- the plan controls stop being enforced; KEEP every record of what they did.
--
-- The functions return to their pre-0122 bodies (time allowance on VALIDITY_WINDOW plans unaccounted again,
-- DISCONNECT_OLDEST failing closed again, the data crossing derived by full rescan again) and the new operations
-- go. Kept: iam_v2.device_limit_disconnections, the retention setting with its change log and purge runs, and
-- the termination evidence -- a rollback must not erase the audit of what was done to clients' access. The
-- derived checkpoint tables (data_crossing_progress / data_crossing_state) are caches with no meaning of their
-- own and are dropped. The restored evidence CHECK is NOT VALID, so evidence already written with the 0122 cause
-- stays as it was while no new row can use it.

BEGIN;

DROP FUNCTION IF EXISTS iam_v2.p6_end_sessions_over_plan_limits(uuid,uuid,timestamptz,boolean);
DROP FUNCTION IF EXISTS iam_v2.p6_advance_data_crossing(uuid,uuid);
DROP FUNCTION IF EXISTS iam_v2.purge_accounting_detail(uuid,uuid,integer);
DROP FUNCTION IF EXISTS iam_v2.accounting_retention_set(uuid,uuid,integer,text,text);
DROP FUNCTION IF EXISTS iam_v2.accounting_retention_get(uuid,uuid);

-- accounting_records goes back to the shared immutability guard: no DELETE path at all.
DROP TRIGGER IF EXISTS ao_accounting ON iam_v2.accounting_records;
CREATE TRIGGER ao_accounting BEFORE DELETE OR UPDATE ON iam_v2.accounting_records
  FOR EACH ROW EXECUTE FUNCTION iam_v2.trg_reject_update_delete();
DROP FUNCTION IF EXISTS iam_v2.accounting_records_append_only();

CREATE OR REPLACE FUNCTION iam_v2.p6_data_crossing(p_entitlement uuid) RETURNS timestamp with time zone
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

DROP FUNCTION IF EXISTS iam_v2.p6_data_crossing_full(uuid);
DROP TABLE IF EXISTS iam_v2.data_crossing_state;
DROP TABLE IF EXISTS iam_v2.data_crossing_progress;

CREATE OR REPLACE FUNCTION iam_v2.authorize_entitlement_device(p_ent uuid, p_device uuid, p_at timestamp with time zone) RETURNS uuid
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE v_t uuid; v_s uuid; v_status text; v_limit int; v_policy text; v_open int; v_seq bigint; v_id uuid; v_existing uuid;
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
    IF COALESCE(v_policy,'REJECT_NEW_DEVICE') <> 'REJECT_NEW_DEVICE' THEN
      RAISE EXCEPTION 'device limit policy % is not implemented in this phase (fail closed)', v_policy;
    END IF;
    RAISE EXCEPTION 'MAX_DEVICES_REACHED: entitlement % already has % of % devices authorized', p_ent, v_open, v_limit;
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
  IF NOT FOUND OR e.time_accounting_mode <> 'AGGREGATE_ONLINE_TIME' THEN
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
     AND e.time_accounting_mode = 'AGGREGATE_ONLINE_TIME'
     AND spr.time_quota_seconds IS NOT NULL;
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
    reason := 'TIME'; time_cause := 'AGGREGATE_ONLINE_TIME_EXHAUSTED';
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
  IF p_cause NOT IN ('VALIDITY_WINDOW_ELAPSED','AGGREGATE_ONLINE_TIME_EXHAUSTED','AGGREGATE_OUTER_WINDOW_EXPIRED') THEN
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
       AND en.time_accounting_mode = 'AGGREGATE_ONLINE_TIME'
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

DROP FUNCTION IF EXISTS iam_v2.p6_counts_online_time(text,bigint);

ALTER TABLE iam_v2.entitlement_termination_evidence DROP CONSTRAINT IF EXISTS ete_allowance_carries_its_budget;
ALTER TABLE iam_v2.entitlement_termination_evidence
  DROP CONSTRAINT IF EXISTS entitlement_termination_evidence_cause_detail_check;
ALTER TABLE iam_v2.entitlement_termination_evidence
  ADD CONSTRAINT entitlement_termination_evidence_cause_detail_check CHECK (cause_detail = ANY (ARRAY[
    'VALIDITY_WINDOW_ELAPSED', 'AGGREGATE_ONLINE_TIME_EXHAUSTED', 'AGGREGATE_OUTER_WINDOW_EXPIRED'])) NOT VALID;

DO $g$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
    REVOKE SELECT ON iam_v2.accounting_retention_changes, iam_v2.accounting_purge_runs,
                     iam_v2.device_limit_disconnections FROM svc_edged;
  END IF;
END $g$;

COMMIT;
