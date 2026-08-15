-- Reverse 0046: restore the 0045 writer, which closes devices and sessions as ENTITLEMENT_ENDED.
--
-- FAITHFUL, AND THEREFORE IT REINSTATES THE FALSE CLAIM at the child level: the rows an operator reads first
-- will again say the entitlement ended when the record deliberately does not say so.
BEGIN;

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
  'be proven: SUSPENDED through the ordinary transition writer, devices and sessions closed with the '
  'non-terminal reason ENTITLEMENT_SUSPENDED. No terminated_at, no terminal_reason and no TIME evidence at '
  'either level, because nothing here is a claim that the entitlement ended.';

REVOKE ALL ON FUNCTION iam_v2.p6_suspend_over_budget(uuid, uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION iam_v2.p6_suspend_over_budget(uuid, uuid) TO svc_acctd;

COMMIT;
