-- PHASE 6 M3 — the narrowest writer boundary that lets acctd finish the job it is responsible for.
--
-- THE GAP A CATALOG AUDIT COULD NOT SEE. Migration 0039 proved svc_acctd may execute the accrual tick and may
-- NOT execute terminate_entitlement_at_boundary or p6_record_time_termination. Both facts are true and both
-- are desirable -- and together they made the actual expiry sweep impossible to run as that role, because the
-- Go path calls those very operations three statements later, plus two direct UPDATEs to close authorization
-- intervals and end sessions.
--
-- Catalog checks measure what a role MAY do. Only running the real flow as the real role measures whether it
-- CAN do its job. This migration is what closes the difference.
--
-- THE SHAPE, CHOSEN TO MATCH THE EXISTING CONTROLLED-WRITER ARCHITECTURE. iam_v2 already answers this class
-- of problem the same way everywhere: one SECURITY DEFINER function that performs a complete, named
-- operation, opening its own capability scope, so a caller receives an AUDITED OPERATION rather than the
-- write authority to reconstruct it. p6_guest_release_device does exactly this for the guest surface;
-- p6_set_guest_device_self_service for the operator surface. This is the same pattern for the sweep.
--
-- WHAT acctd GAINS: the ability to end an entitlement that has ALREADY reached a terminal condition it
-- computed, at the instant it computed, for one of two reasons.
--
-- WHAT acctd STILL CANNOT DO, and each absence is the point:
--
--   * it cannot terminate for ADMIN, CHECKOUT, REVOKED, TRANSFERRED or any other reason. Those belong to
--     operators, the checkout path and the transfer path. The reason is validated HERE, not by the caller.
--   * it cannot choose an arbitrary instant for an entitlement that has not actually expired: the function
--     refuses an instant in the future, so "end this now because I say so" is not expressible.
--   * it cannot UPDATE iam_v2.entitlements, write a watermark, write termination evidence or close an
--     authorization interval directly. Every one of those still happens under the definer's identity.
--   * it cannot execute the primitives this function calls. It calls them; acctd may not.
BEGIN;

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
  'Ends an entitlement that has already reached a TIME or DATA terminal condition, at the instant the sweep '
  'computed: the existing boundary primitive, the optional Phase-6 time evidence, and the revocation of its '
  'devices and sessions, as ONE audited operation. The accounting daemon holds EXECUTE on this and on '
  'nothing it calls.';

REVOKE ALL ON FUNCTION iam_v2.p6_expire_entitlement(uuid, timestamptz, text, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION iam_v2.p6_expire_entitlement(uuid, timestamptz, text, text) TO svc_acctd;

-- USAGE on the schema. Without it the role cannot name a single object, however many table grants it holds
-- -- which is exactly what the first real-role run of the sweep discovered, and what no catalog audit of
-- table and function privileges would ever have shown.
GRANT USAGE ON SCHEMA iam_v2 TO svc_acctd;

-- The reads the candidate query performs. They are SELECT-only and they are the exact tables the sweep
-- consults to decide that an entitlement has expired.
GRANT SELECT ON iam_v2.accounting_records            TO svc_acctd;
GRANT SELECT ON iam_v2.session_entitlement_bindings  TO svc_acctd;
GRANT SELECT ON iam_v2.entitlement_devices           TO svc_acctd;
GRANT SELECT ON iam_v2.entitlement_termination_evidence TO svc_acctd;

-- Self-asserting: the boundary is only meaningful if the caller still cannot reach around it.
DO $$
DECLARE bad text;
BEGIN
  IF has_function_privilege('public', 'iam_v2.p6_expire_entitlement(uuid,timestamptz,text,text)', 'EXECUTE') THEN
    RAISE EXCEPTION 'PUBLIC can execute the expiry writer';
  END IF;
  SELECT string_agg(format('%s on %s', privilege_type, table_name), ', ')
    INTO bad
    FROM information_schema.role_table_grants
   WHERE table_schema = 'iam_v2' AND grantee = 'svc_acctd' AND privilege_type <> 'SELECT';
  IF bad IS NOT NULL THEN
    RAISE EXCEPTION 'svc_acctd gained write authority in iam_v2: %', bad;
  END IF;
  FOR bad IN
    SELECT p.proname FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
     WHERE n.nspname = 'iam_v2'
       AND p.proname IN ('terminate_entitlement_at_boundary', 'p6_record_time_termination',
                         'begin_controlled_operation', 'authorize_entitlement_device',
                         'deauthorize_entitlement_device')
       AND has_function_privilege('svc_acctd', p.oid, 'EXECUTE')
  LOOP
    RAISE EXCEPTION 'svc_acctd can execute %, which this boundary exists to keep it away from', bad;
  END LOOP;
END $$;

COMMIT;
