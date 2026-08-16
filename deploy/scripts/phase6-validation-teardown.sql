-- PHASE-6 CONTROLLED VALIDATION -- TEARDOWN, KEYED ON THE RESERVED STAY.
--
-- Everything the validation grants hangs off one reserved stay, 6d5f0000-0000-4000-8000-000000000102, which
-- exists for nothing else. That is the identifier: not a marker written into a business vocabulary, not a
-- shape the rows happen to have, but a durable foreign key to a row this validation created and nothing else
-- points at. It needs no parameters, and it works from a cold start -- a run killed before it could record
-- anything still leaves state this file finds, because the stay id was decided in advance.
--
-- WHY THE ENTITLEMENT IS NEW EVERY RUN AND THE STAY IS NOT. An entitlement cannot be reset and reused: its
-- state transitions and its termination evidence are append-only by trigger and the evidence is keyed on the
-- entitlement, so a second exhaustion would collide with the first run's row. The durable skeleton is
-- therefore reused and the grant is fresh -- and this file ends whatever grants it finds, however many runs
-- have accumulated.
--
-- WHAT IT DELIBERATELY DOES NOT DO. It does not disable a single append-only trigger and it does not delete an
-- audit row. entitlement_state_transitions, entitlement_device_authorizations, the termination evidence and
-- the guest device actions are protected BY TRIGGER, and the only way to delete them is to turn that
-- protection off -- which would make the harness that proves the appliance is safe also the demonstration of
-- how to disarm it. Convenience is not a reason.
--
-- So teardown ENDS things rather than erasing them: each live entitlement goes through the sanctioned boundary
-- path, exactly as a real one would, and that path closes its sessions and releases its devices. What remains
-- is terminated business state and its audit trail -- inert, carrying no access, and permanently identifiable
-- through the reserved stay. Removing even that residue is a separate, explicit step: restoring the appliance
-- database from the backup taken before the run, which reverses every synthetic row without deleting an audit
-- record or weakening a protection.
\set ON_ERROR_STOP on

DO $$
DECLARE
  c_stay CONSTANT uuid := '6d5f0000-0000-4000-8000-000000000102';
  r record;
BEGIN
  -- 1. End every live entitlement of the reserved stay through the ONE boundary path. Not an UPDATE of the
  --    status column: the boundary path is what closes sessions, releases devices and writes the evidence,
  --    and a teardown that bypassed it would leave exactly the orphaned live session it exists to remove.
  FOR r IN SELECT id FROM iam_v2.entitlements WHERE stay_id = c_stay AND status <> 'TERMINATED'
  LOOP
    PERFORM iam_v2.terminate_entitlement_at_boundary(r.id, now(), 'ADMIN');
  END LOOP;

  -- 2. After step 1 there should be no live session and no authorized binding anywhere in the scope. These
  --    two statements are here because "should be" is the assumption that leaves a device forwarding traffic
  --    after a failed run.
  UPDATE iam_v2.sessions
     SET state = 'ended', ended = COALESCE(ended, now()), end_reason = COALESCE(end_reason, 'ADMIN')
   WHERE entitlement_id IN (SELECT id FROM iam_v2.entitlements WHERE stay_id = c_stay)
     AND state IN ('active', 'PENDING_ENFORCEMENT');

  -- entitlement_devices has no released_at, and its status vocabulary is AUTHORIZED/DISCONNECTED -- not the
  -- "RELEASED" this file first assumed from the name of the release operation. Both mistakes aborted the whole
  -- DO block, which is why the next run then collided with ent_live_stay: the earlier grant had never
  -- actually been ended, and the failure was invisible because teardown ran with its output discarded.
  UPDATE iam_v2.entitlement_devices
     SET status = 'DISCONNECTED', disconnected_reason = COALESCE(disconnected_reason, 'ADMIN')
   WHERE entitlement_id IN (SELECT id FROM iam_v2.entitlements WHERE stay_id = c_stay)
     AND status = 'AUTHORIZED';

  -- 3. Watermarks are ordinary operational rows with no audit role, and they are the one thing that would
  --    make a later run charge online time from a previous run's clock.
  DELETE FROM iam_v2.session_online_watermarks
   WHERE session_id IN (SELECT s.id FROM iam_v2.sessions s
                         WHERE s.entitlement_id IN (SELECT id FROM iam_v2.entitlements WHERE stay_id = c_stay));
END $$;

SELECT 'P6_SCOPE_INERT'
  WHERE NOT EXISTS (SELECT 1 FROM iam_v2.entitlements
                     WHERE stay_id = '6d5f0000-0000-4000-8000-000000000102' AND status <> 'TERMINATED')
    AND NOT EXISTS (SELECT 1 FROM iam_v2.sessions
                     WHERE entitlement_id IN (SELECT id FROM iam_v2.entitlements
                                               WHERE stay_id = '6d5f0000-0000-4000-8000-000000000102')
                       AND state IN ('active', 'PENDING_ENFORCEMENT'))
    AND NOT EXISTS (SELECT 1 FROM iam_v2.entitlement_devices
                     WHERE entitlement_id IN (SELECT id FROM iam_v2.entitlements
                                               WHERE stay_id = '6d5f0000-0000-4000-8000-000000000102')
                       AND status = 'AUTHORIZED');
