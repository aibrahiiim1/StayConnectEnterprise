-- Network pause of room sign-in, staff confirmation of a guest's device (migration 0136; GDPR audit A4).
-- Idempotent; included by gatep-grants.sql after its revoke, so a Gate-P reconcile reproduces what the migration
-- granted. Neither service holds a table write: every change goes through the definer functions.
--
-- GUARDED on the migration being present (audit B-20).
DO $g$
BEGIN
  IF to_regprocedure('iam_v2.guest_signin_network_gate(uuid,uuid,uuid,macaddr)') IS NULL THEN
    RAISE NOTICE 'migration 0136 is not applied; network-pause grants skipped';
    RETURN;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_scd') THEN
    -- Enforcement: ask the gate, report a wrong room sign-in, sweep old confirmations. Nothing else.
    GRANT EXECUTE ON FUNCTION iam_v2.guest_signin_network_gate(uuid,uuid,uuid,macaddr) TO svc_scd;
    GRANT EXECUTE ON FUNCTION iam_v2.guest_signin_note_network_failure(uuid,uuid,uuid,text) TO svc_scd;
    GRANT EXECUTE ON FUNCTION iam_v2.guest_signin_network_prune() TO svc_scd;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
    -- Staff: read and change the settings, see the pauses, end one, confirm a guest's device.
    GRANT EXECUTE ON FUNCTION iam_v2.guest_signin_network_policy_get(uuid,uuid) TO svc_edged;
    GRANT EXECUTE ON FUNCTION iam_v2.guest_signin_network_policy_set(uuid,uuid,integer,integer,integer,text,text) TO svc_edged;
    GRANT EXECUTE ON FUNCTION iam_v2.guest_signin_end_network_pause(uuid,uuid,uuid,text,text) TO svc_edged;
    GRANT EXECUTE ON FUNCTION iam_v2.guest_signin_confirm_device(uuid,uuid,uuid,macaddr,text,text) TO svc_edged;
    GRANT EXECUTE ON FUNCTION iam_v2.guest_signin_network_pause_devices(uuid,uuid,uuid) TO svc_edged;
    GRANT SELECT ON iam_v2.guest_signin_network_pauses, iam_v2.guest_signin_network_policy_changes TO svc_edged;
  END IF;
END $g$;
