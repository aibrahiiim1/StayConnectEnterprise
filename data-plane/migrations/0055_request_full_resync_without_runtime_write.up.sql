-- EDGED ASKS FOR A RESYNC WITHOUT BEING ABLE TO WRITE THE RUNTIME ROW.
--
-- The first deployment of the Full Resync control failed with "permission denied for table
-- pms_interface_runtime", and that refusal was correct. svc_edged has never held write privilege on the
-- runtime row and must not gain it: that row carries transport_status, sync_status, continuity_status,
-- runtime_generation and the pinned revision — the state a pmsd worker uses to prove it still owns a socket.
-- An admin process able to write any of it could invalidate ownership, fake a connection, or hand itself a
-- generation, and no amount of care in the handler would take that capability away again.
--
-- So edged gets a FUNCTION, not a grant. Exactly the shape migration 0052 used for the checkout-grace config
-- lock, and for the same reason: the caller needs one specific effect, not the privilege that would allow it.
--
-- WHAT THE FUNCTION CAN TOUCH is the entire security argument. It writes the five command columns and the two
-- stage columns. It cannot write transport, sync, continuity, generations or pins, because it does not
-- mention them — and being SECURITY DEFINER does not help a caller reach what the body never names.
--
-- resync_command_generation is taken from the row's OWN runtime_generation rather than from an argument. A
-- caller cannot aim a command at a generation of its choosing, so the binding to "the worker that owns this
-- socket right now" is established here and cannot be forged by whoever calls it.
--
-- The preconditions live in the WHERE clause rather than in the caller. edged checks them too, but only to
-- give the operator a specific message; between that check and this write the transport can drop or another
-- resync can start, so the authoritative refusal has to be here, in the same statement that writes.

BEGIN;

CREATE OR REPLACE FUNCTION iam_v2.request_full_resync(
  p_tenant uuid, p_site uuid, p_interface uuid, p_reason text
) RETURNS uuid
LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE v_id uuid;
BEGIN
  -- The reason vocabulary is closed HERE as well as in edged. A bounded set enforced only in the process that
  -- happens to call today is not a bound; it is a convention that survives until the next caller.
  IF p_reason IS NULL OR p_reason NOT IN
     ('SUSPECTED_STALE_GUEST_LIST','AFTER_PMS_MAINTENANCE','OPERATOR_VERIFICATION','SUPPORT_REQUEST') THEN
    RAISE EXCEPTION 'unbounded resync reason' USING ERRCODE = 'check_violation';
  END IF;

  UPDATE iam_v2.pms_interface_runtime rt
     SET resync_command_id            = gen_random_uuid(),
         resync_command_requested_at  = now(),
         resync_command_reason        = p_reason,
         resync_command_generation    = rt.runtime_generation,
         resync_command_claimed_at    = NULL,
         sync_stage                   = 'REQUESTING_FULL_SYNC',
         sync_stage_at                = now(),
         sync_failure_code            = NULL,
         updated_at                   = now()
    FROM iam_v2.pms_interfaces pi
   WHERE pi.tenant_id = rt.tenant_id AND pi.site_id = rt.site_id AND pi.id = rt.pms_interface_id
     AND rt.tenant_id = p_tenant AND rt.site_id = p_site AND rt.pms_interface_id = p_interface
     -- there is a worker with a live socket to receive this
     AND pi.lifecycle_state = 'ACTIVE'
     AND rt.transport_status = 'CONNECTED'
     -- a resync is not already running, and no earlier request is still waiting to be claimed
     AND rt.sync_status <> 'RESYNC_IN_PROGRESS'
     AND rt.resync_command_id IS NULL
  RETURNING rt.resync_command_id INTO v_id;

  RETURN v_id; -- NULL when no row qualified; the caller explains which precondition failed
END;
$fn$;

COMMENT ON FUNCTION iam_v2.request_full_resync(uuid,uuid,uuid,text) IS
  'Records an operator full-resync request without granting the caller write access to '
  'iam_v2.pms_interface_runtime. Writes only the command and stage columns; cannot touch transport, sync, '
  'continuity, generations or pins. resync_command_generation is taken from the row own runtime_generation so '
  'a caller cannot aim a command at a generation it chose. Returns NULL when the interface is not ACTIVE and '
  'CONNECTED, a resync is already running, or a request is already pending.';

REVOKE ALL ON FUNCTION iam_v2.request_full_resync(uuid,uuid,uuid,text) FROM PUBLIC;

-- The grant is conditional because the service roles are created by the Gate-P role scripts, not by
-- migrations, and the disposable CI databases have no svc_edged at all. An unconditional GRANT fails there
-- with "role does not exist" and takes the whole migration down with it, which is how this one first broke.
-- The authoritative grant lives in deploy/gatep/svc-edged-phase345-admin-grants.sql regardless: a
-- migration-only grant does not survive a reconcile.
DO $do$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
    GRANT EXECUTE ON FUNCTION iam_v2.request_full_resync(uuid,uuid,uuid,text) TO svc_edged;
  END IF;
END
$do$;

COMMIT;
