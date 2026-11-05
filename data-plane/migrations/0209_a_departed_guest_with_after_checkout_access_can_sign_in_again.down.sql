-- 0209 down -- room sign-in issues a PMS context to IN_HOUSE stays only again, as before 0209.
--
-- The function body is exactly the one 0209 replaced (from the production baseline). A departed guest is then refused
-- room sign-in as STAY_NOT_ELIGIBLE even while their After Check-out Access is valid; nothing else changes and no row is
-- rewritten. A context already issued to a departed stay expires within its few minutes' lifetime.

BEGIN;
CREATE OR REPLACE FUNCTION iam_v2.issue_or_return_pms_context(p_tenant uuid, p_site uuid, p_interface uuid, p_revision uuid, p_stay uuid, p_device uuid, p_guest_network uuid, p_request uuid, p_ttl_seconds integer) RETURNS TABLE(context_id uuid, reused boolean)
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE v_existing uuid; v_lifecycle int; v_ev bigint;
BEGIN
  PERFORM iam_v2.begin_controlled_operation('auth_context');
  IF p_request IS NULL THEN
    RAISE EXCEPTION 'CONTEXT_INVALID: a PMS context must name the resolution it came from';
  END IF;
  SELECT id INTO v_existing FROM iam_v2.auth_contexts
    WHERE tenant_id = p_tenant AND site_id = p_site AND resolution_request_id = p_request
      AND consumed_at IS NULL AND expires_at > now()
    FOR UPDATE;
  IF v_existing IS NOT NULL THEN
    RETURN QUERY SELECT v_existing, true;
    RETURN;
  END IF;

  -- The row lock and the version snapshot still come from the Stay itself; whether the Stay may be authorised
  -- at all is the shared predicate's decision.
  SELECT st.lifecycle_version, st.occupancy_evidence_version INTO v_lifecycle, v_ev
    FROM iam_v2.stays st
   WHERE st.tenant_id=p_tenant AND st.site_id=p_site
     AND st.pms_interface_id=p_interface AND st.id=p_stay
     -- Stay-row conditions INLINE, so the FOR UPDATE recheck evaluates them against the locked tuple.
     AND st.status='IN_HOUSE'
     AND st.occupancy_evidence_at IS NOT NULL
     AND st.occupancy_clock_suspect IS NOT TRUE
     AND st.occupancy_evidence_version > 0
     AND st.occupancy_revision_id = p_revision
   FOR UPDATE OF st;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'CONTEXT_INVALID: stay % is not eligible for a PMS context', p_stay;
  END IF;

  -- THE LINEARIZATION POINT, taken AFTER the Stay lock so the global L1 Stay-first order is preserved.
  --
  -- This is the same row pmsd's admission path locks before inserting a stay_event, so from here until this
  -- transaction commits no new occupancy event can be admitted for this interface. That converts the
  -- readiness check below from "true a moment ago" into "true as of a point no admission can precede".
  --
  -- Deliberately a separate statement from the Stay lock above: the predicate MUST be evaluated after both
  -- locks are held, and folding it into the Stay query would evaluate it while admission could still commit.
  PERFORM 1 FROM iam_v2.pms_interface_runtime rt
    WHERE rt.tenant_id=p_tenant AND rt.site_id=p_site AND rt.pms_interface_id=p_interface
    FOR UPDATE;

  -- Feed health, materialization readiness and the freshness ceiling, now judged under both locks.
  PERFORM 1 FROM iam_v2.stays st
   WHERE st.id = p_stay
     AND iam_v2.p3_feed_authorizes(p_tenant, p_site, p_interface, p_revision, st.occupancy_evidence_at);
  IF NOT FOUND THEN
    RAISE EXCEPTION 'CONTEXT_INVALID: stay % is not eligible for a PMS context', p_stay;
  END IF;

  RETURN QUERY
    INSERT INTO iam_v2.auth_contexts
      (tenant_id, site_id, method, stay_id, pms_interface_id, authentication_interface_revision_id,
       device_id, guest_network_id, pinned_lifecycle_version, pinned_occupancy_evidence_version,
       resolution_request_id, expires_at)
    VALUES (p_tenant, p_site, 'PMS', p_stay, p_interface, p_revision, p_device, p_guest_network,
            v_lifecycle, v_ev, p_request, now() + make_interval(secs => p_ttl_seconds))
    RETURNING id, false;
END $$;

COMMIT;
