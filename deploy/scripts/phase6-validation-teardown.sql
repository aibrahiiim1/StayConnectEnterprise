-- PHASE-6 CONTROLLED VALIDATION -- TEARDOWN OF THE RESERVED IDENTITIES.
--
-- Everything here names the exact fixed ids and reserved MACs that phase6-validation-scope.sql creates. It
-- cannot reach a row this validation did not create, it needs no parameters, and it is safe from a cold start:
-- a run killed before it recorded anything still leaves state this file can find, because the identities were
-- decided in advance rather than generated.
--
-- WHAT IT DELIBERATELY DOES NOT DO. It does not disable a single append-only trigger, and it does not delete
-- an audit row. entitlement_state_transitions, entitlement_device_authorizations, the termination evidence and
-- the guest device actions are append-only BY TRIGGER, and the only way to delete them is to turn that
-- protection off -- which would mean the harness that proves the appliance is safe is also the thing that
-- demonstrates how to disarm it. Convenience is not a reason.
--
-- So teardown ENDS things rather than erasing them: the entitlement goes through the sanctioned boundary path,
-- exactly as a real one would, and that path closes its sessions and releases its devices. What remains
-- afterwards is terminated business state and its audit trail -- inert, carrying no access, and permanently
-- identifiable as validation state by its reserved ids.
--
-- Removing even that residue is a separate, explicit step in the harness: restoring the appliance database
-- from the backup taken before the run. That reverses every synthetic row without deleting an audit record or
-- weakening a protection, which is why it is the mechanism for the final state rather than a wider DELETE.
\set ON_ERROR_STOP on

DO $$
DECLARE
  c_ent  CONSTANT uuid := '6d5f0000-0000-4000-8000-000000000302';
  c_sess CONSTANT uuid := '6d5f0000-0000-4000-8000-000000000401';
  st text;
BEGIN
  SELECT status INTO st FROM iam_v2.entitlements WHERE id = c_ent;

  -- 1. End it through the ONE boundary path. Not an UPDATE of the status column: the boundary path is what
  --    closes sessions, releases devices and writes the evidence, and a teardown that bypassed it would leave
  --    exactly the orphaned live session it exists to remove.
  IF st IS NOT NULL AND st <> 'TERMINATED' THEN
    PERFORM iam_v2.terminate_entitlement_at_boundary(c_ent, now(), 'ADMIN');
  END IF;

  -- 2. After step 1 there should be no live session and no authorized binding. These two statements are here
  --    because "should be" is the assumption that leaves a device forwarding traffic after a failed run.
  UPDATE iam_v2.sessions
     SET state = 'ended', ended = COALESCE(ended, now()), end_reason = COALESCE(end_reason, 'ADMIN')
   WHERE (id = c_sess OR entitlement_id = c_ent) AND state IN ('active', 'PENDING_ENFORCEMENT');

  UPDATE iam_v2.entitlement_devices
     SET status = 'RELEASED', released_at = COALESCE(released_at, now())
   WHERE entitlement_id = c_ent AND status = 'AUTHORIZED';

  -- 3. Watermarks are ordinary operational rows with no audit role, and they are the one thing that would make
  --    a re-seed charge online time from a previous run's clock.
  DELETE FROM iam_v2.session_online_watermarks WHERE session_id = c_sess;
END $$;

SELECT 'P6_SCOPE_INERT'
  WHERE NOT EXISTS (SELECT 1 FROM iam_v2.entitlements
                     WHERE id = '6d5f0000-0000-4000-8000-000000000302' AND status <> 'TERMINATED')
    AND NOT EXISTS (SELECT 1 FROM iam_v2.sessions
                     WHERE entitlement_id = '6d5f0000-0000-4000-8000-000000000302'
                       AND state IN ('active', 'PENDING_ENFORCEMENT'))
    AND NOT EXISTS (SELECT 1 FROM iam_v2.entitlement_devices
                     WHERE entitlement_id = '6d5f0000-0000-4000-8000-000000000302'
                       AND status = 'AUTHORIZED');
