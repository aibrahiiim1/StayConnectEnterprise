-- PHASE 6 M4 — "we cannot date the crossing" must not mean "keep browsing".
--
-- THE GAP. 0044 refuses to invent a historical exhaustion instant when the audited history cannot prove one.
-- That conservatism is right and stays. But refusing to TERMINATE was silently also refusing to DENY: an
-- entitlement whose consumption is at or over its budget RIGHT NOW kept its sessions, kept its forwarding,
-- and could admit new devices, because the only lever the sweep had was a terminal transition it could not
-- honestly make. Historical truth and current safety are different questions, and the code was answering the
-- first one for both.
--
-- THE MECHANISM IS ALREADY IN THE SCHEMA, and it is not a new idea about what anything means:
--
--   * iam_v2.entitlements.status already admits SUSPENDED, alongside PENDING/ACTIVE/TERMINATED;
--   * iam_v2.authorize_entitlement_device REFUSES anything that is not ACTIVE, so a suspended entitlement
--     admits no new device and therefore gets no new session;
--   * the shaping plan (enforce.PlanForSite) selects sessions only where the entitlement is ACTIVE, so a
--     suspended entitlement's traffic is not in the plan netd applies -- existing forwarding is torn down.
--
-- SUSPENDED already means "not entitled to forwarding right now, and not ended". That is exactly the state
-- being described, so nothing is redefined: no new terminal reason, no terminal_reason written at all, no
-- terminated_at, no TIME evidence. The entitlement has not been declared to have ended, because nobody can
-- say when it did.
--
-- THE EVIDENCE IS BOUNDED BY CONSTRUCTION. Suspension writes ONE state transition, with the machine-readable
-- reason AGGREGATE_OVER_BUDGET, through the same apply_entitlement_transition every other status change uses.
-- It is not a log line repeated every sweep: the second sweep finds the entitlement already SUSPENDED and
-- does nothing. The transition history is where an operator already looks to find out what happened to an
-- entitlement, and the online-time admin view shows the status beside the consumption that caused it.
--
-- CONVERGENCE IS PRESERVED. A suspended entitlement stays in the terminal-condition candidate set, so if
-- durable evidence later establishes the true crossing -- a stamp, or an audited adjustment -- it terminates
-- through the ONE existing boundary path, at the true instant, with the correct TIME evidence.
BEGIN;

-- ---------------------------------------------------------------------------------------------------------
-- 1. Is it over budget RIGHT NOW? A question about current state only.
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION iam_v2.p6_over_budget_now(p_entitlement uuid)
RETURNS boolean
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = iam_v2, pg_temp
AS $$
  SELECT COALESCE(e.consumed_online_seconds, 0) >= spr.time_quota_seconds
    FROM iam_v2.entitlements e
    JOIN iam_v2.service_plan_revisions spr ON spr.id = e.service_plan_revision_id
   WHERE e.id = p_entitlement
     AND e.time_accounting_mode = 'AGGREGATE_ONLINE_TIME'
     AND spr.time_quota_seconds IS NOT NULL;
$$;

COMMENT ON FUNCTION iam_v2.p6_over_budget_now(uuid) IS
  'Whether an AGGREGATE_ONLINE_TIME entitlement has spent its budget as of NOW, from current authoritative '
  'state. It says nothing about WHEN that happened, which is a separate and much harder question.';

-- ---------------------------------------------------------------------------------------------------------
-- 2. Deny access, without claiming to know when it ended
-- ---------------------------------------------------------------------------------------------------------
-- Suspends every aggregate entitlement of the site that is over budget now and whose crossing instant cannot
-- be proven, and tears down the access it still holds. Idempotent: an entitlement already SUSPENDED or
-- TERMINATED is skipped, so repeated sweeps do nothing and leave no repeated evidence.
-- The OUT column is named `entitlement`, not `entitlement_id`: the latter collided with the column of the
-- same name inside the statements below and PostgreSQL resolved it as ambiguous. Renaming it needs the drop,
-- because a return type cannot be changed in place.
DROP FUNCTION IF EXISTS iam_v2.p6_suspend_over_budget(uuid, uuid);

CREATE OR REPLACE FUNCTION iam_v2.p6_suspend_over_budget(p_tenant uuid, p_site uuid)
RETURNS TABLE (entitlement uuid, devices int, sessions int)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = iam_v2, pg_temp
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
       SET status = 'DISCONNECTED', disconnected_reason = 'ENTITLEMENT_ENDED'
      FROM closed
     WHERE ed.entitlement_id = closed.entitlement_id AND ed.device_id = closed.device_id;
    GET DIAGNOSTICS v_devices = ROW_COUNT;

    UPDATE iam_v2.sessions se
       SET state = 'ended', ended = GREATEST(now(), se.started), end_reason = 'ENTITLEMENT_ENDED'
     WHERE se.entitlement_id = e.id AND se.state IN ('active', 'PENDING_ENFORCEMENT');
    GET DIAGNOSTICS v_sessions = ROW_COUNT;

    entitlement := e.id; devices := v_devices; sessions := v_sessions;
    RETURN NEXT;
  END LOOP;
END $$;

COMMENT ON FUNCTION iam_v2.p6_suspend_over_budget(uuid, uuid) IS
  'Withholds access from aggregate entitlements that are over budget now and whose crossing instant cannot '
  'be proven: SUSPENDED through the ordinary transition writer, devices and sessions closed. It writes no '
  'terminated_at, no terminal_reason and no TIME evidence, because it is not a claim that the entitlement '
  'ended -- only that it may not carry traffic while the question is open. Idempotent, so the evidence is '
  'one transition rather than one per sweep.';

-- ---------------------------------------------------------------------------------------------------------
-- 3. Stop warning about a state that has now been acted on
-- ---------------------------------------------------------------------------------------------------------
-- The warning exists to surface an anomaly nobody has handled. Once the entitlement is suspended it HAS been
-- handled, and repeating the line every sweep would bury the ones that still matter.
CREATE OR REPLACE FUNCTION iam_v2.p6_exhaustion_instant(p_entitlement uuid)
RETURNS timestamptz
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = iam_v2, pg_temp
AS $BODY$
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
END $BODY$;

REVOKE ALL ON FUNCTION iam_v2.p6_over_budget_now(uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.p6_suspend_over_budget(uuid, uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.p6_exhaustion_instant(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION iam_v2.p6_suspend_over_budget(uuid, uuid) TO svc_acctd;

DO $$
BEGIN
  IF has_function_privilege('public', 'iam_v2.p6_suspend_over_budget(uuid,uuid)', 'EXECUTE') THEN
    RAISE EXCEPTION 'PUBLIC can suspend entitlements';
  END IF;
  IF has_function_privilege('svc_acctd', 'iam_v2.apply_entitlement_transition(uuid,text,timestamptz,text)', 'EXECUTE') THEN
    RAISE EXCEPTION 'svc_acctd can change entitlement status directly; it must go through the sanctioned writer';
  END IF;
END $$;

COMMIT;
