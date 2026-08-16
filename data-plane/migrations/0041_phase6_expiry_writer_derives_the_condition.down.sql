-- Reverse 0041: restore the 0040 writer, which accepts a caller-supplied reason and instant.
--
-- FAITHFUL, AND THEREFORE IT REINSTATES THE WEAKNESS: with this applied, a role holding EXECUTE can end any
-- live entitlement it can name by labelling it TIME or DATA. Disable the aggregate mode and stop the sweep
-- before rolling back.
BEGIN;

DROP FUNCTION IF EXISTS iam_v2.p6_expire_entitlement(uuid);
DROP FUNCTION IF EXISTS iam_v2.p6_due_terminal(uuid);
DROP FUNCTION IF EXISTS iam_v2.p6_data_crossing(uuid);

CREATE OR REPLACE FUNCTION iam_v2.p6_expire_entitlement(
  p_entitlement uuid,
  p_at          timestamptz,
  p_reason      text,
  p_time_cause  text DEFAULT NULL
) RETURNS TABLE (devices int, sessions int)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = iam_v2, pg_temp
AS $$
DECLARE
  v_status text;
  v_devices int;
  v_sessions int;
BEGIN
  -- ONLY THE TWO REASONS THIS SWEEP IS RESPONSIBLE FOR. An accounting daemon that could terminate for
  -- ADMIN would be an accounting daemon that could revoke a guest's access on its own authority.
  IF p_reason NOT IN ('TIME', 'DATA') THEN
    RAISE EXCEPTION 'the expiry sweep may only end an entitlement for TIME or DATA, not %', p_reason
      USING ERRCODE = 'insufficient_privilege';
  END IF;
  IF p_at IS NULL OR p_at > now() THEN
    -- A terminal instant is something that HAPPENED. Accepting a future one would let a caller schedule an
    -- ending, which is a different capability from recording one.
    RAISE EXCEPTION 'a terminal instant may not be in the future (%)', p_at
      USING ERRCODE = 'invalid_parameter_value';
  END IF;

  SELECT status INTO v_status FROM iam_v2.entitlements WHERE id = p_entitlement FOR UPDATE;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'no such entitlement %', p_entitlement USING ERRCODE = 'foreign_key_violation';
  END IF;
  IF v_status = 'TERMINATED' THEN
    -- Idempotent: a re-run of the same sweep must change nothing, which is what makes the sweep safe to
    -- retry after a partial failure.
    devices := 0; sessions := 0;
    RETURN NEXT;
    RETURN;
  END IF;

  -- The ONE termination path, unchanged. This function does not implement terminating; it calls the same
  -- primitive every other terminal path calls.
  PERFORM iam_v2.terminate_entitlement_at_boundary(p_entitlement, p_at, p_reason);

  -- Phase-6 time evidence, when the caller knows which time rule ran out. The trigger on the evidence table
  -- binds it to the transition that just happened, so this cannot describe a termination that did not occur.
  IF p_time_cause IS NOT NULL THEN
    PERFORM iam_v2.p6_record_time_termination(p_entitlement, p_time_cause);
  END IF;

  -- Closing authorization intervals is a device-authorization write, so the scope is declared here rather
  -- than relying on ownership -- the same rule every controlled writer in this schema follows.
  PERFORM iam_v2.begin_controlled_operation('device_auth');

  WITH closed AS (
    UPDATE iam_v2.entitlement_device_authorizations a
       SET deauthorized_at = GREATEST(p_at, a.authorized_at)
     WHERE a.entitlement_id = p_entitlement AND a.deauthorized_at IS NULL
     RETURNING a.entitlement_id, a.device_id)
  UPDATE iam_v2.entitlement_devices ed
     SET status = 'DISCONNECTED', disconnected_reason = 'ENTITLEMENT_ENDED'
    FROM closed
   WHERE ed.entitlement_id = closed.entitlement_id AND ed.device_id = closed.device_id;
  GET DIAGNOSTICS v_devices = ROW_COUNT;

  -- PENDING_ENFORCEMENT is ended too: a grant still converging when its entitlement expired is not exempt,
  -- and leaving it would let the enforcement owner promote it on the next pass -- access created by a
  -- revocation.
  UPDATE iam_v2.sessions
     SET state = 'ended', ended = GREATEST(p_at, started), end_reason = 'ENTITLEMENT_ENDED'
   WHERE entitlement_id = p_entitlement AND state IN ('active', 'PENDING_ENFORCEMENT');
  GET DIAGNOSTICS v_sessions = ROW_COUNT;

  devices := v_devices; sessions := v_sessions;
  RETURN NEXT;
END $$;

COMMENT ON FUNCTION iam_v2.p6_expire_entitlement(uuid, timestamptz, text, text) IS
  'Ends an entitlement at a caller-supplied instant and reason (0040 behaviour, restored by the 0041 down migration).';

REVOKE ALL ON FUNCTION iam_v2.p6_expire_entitlement(uuid, timestamptz, text, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION iam_v2.p6_expire_entitlement(uuid, timestamptz, text, text) TO svc_acctd;

COMMIT;
