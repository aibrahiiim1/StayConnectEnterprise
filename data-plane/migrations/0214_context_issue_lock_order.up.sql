-- 0214 -- A SIGN-IN CHECK AND A GRANT ON THE SAME STAY NO LONGER DEADLOCK (Product Owner mission, 2026-11-07).
--
-- issue_or_return_pms_context locked the Stay and then the PMS interface runtime row; the grant (authctx ConsumeTx) and IssuePMSTx lock
-- the runtime row and then the Stay. A device of a room resolving while another device of the same room was being granted held one
-- lock each and waited for the other (SQLSTATE 40P01; scd retried once). The function now takes the runtime row first. Nothing else
-- changes: the same checks run under the same two locks, and no row is rewritten.

BEGIN;

CREATE OR REPLACE FUNCTION iam_v2.issue_or_return_pms_context(p_tenant uuid, p_site uuid, p_interface uuid, p_revision uuid, p_stay uuid, p_device uuid, p_guest_network uuid, p_request uuid, p_ttl_seconds integer) RETURNS TABLE(context_id uuid, reused boolean)
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE v_existing uuid; v_lifecycle int; v_ev bigint; v_departed boolean;
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

  -- The interface runtime row first, then the Stay: the order the grant (authctx ConsumeTx) and IssuePMSTx take them in. Stay first
  -- here deadlocked a sign-in check against a grant on the same stay (0214). This is the row pmsd's admission path locks before
  -- inserting a stay_event, so until commit no new occupancy event can be admitted; the predicate below is judged under both locks.
  PERFORM 1 FROM iam_v2.pms_interface_runtime rt
    WHERE rt.tenant_id=p_tenant AND rt.site_id=p_site AND rt.pms_interface_id=p_interface
    FOR UPDATE;

  -- The row lock and the version snapshot still come from the Stay itself; whether the Stay may be authorised
  -- at all is the shared predicate's decision.
  SELECT st.lifecycle_version, st.occupancy_evidence_version, st.status = 'CHECKED_OUT'
    INTO v_lifecycle, v_ev, v_departed
    FROM iam_v2.stays st
   WHERE st.tenant_id=p_tenant AND st.site_id=p_site
     AND st.pms_interface_id=p_interface AND st.id=p_stay
     -- Stay-row conditions INLINE, so the FOR UPDATE recheck evaluates them against the locked tuple.
     AND ((st.status='IN_HOUSE'
           AND st.occupancy_evidence_at IS NOT NULL
           AND st.occupancy_clock_suspect IS NOT TRUE
           AND st.occupancy_evidence_version > 0
           AND st.occupancy_revision_id = p_revision)
          -- A departed stay only while its After Check-out Access is valid (0209).
          OR (st.status='CHECKED_OUT'
              AND EXISTS (SELECT 1 FROM iam_v2.entitlements e
                           WHERE e.tenant_id = st.tenant_id AND e.site_id = st.site_id AND e.stay_id = st.id
                             AND e.status = 'ACTIVE' AND e.end_mode = 'GRACE_AFTER_CHECKOUT'
                             AND (e.window_ends_at IS NULL OR e.window_ends_at > now()))))
   FOR UPDATE OF st;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'CONTEXT_INVALID: stay % is not eligible for a PMS context', p_stay;
  END IF;

  -- Feed health, materialization readiness and the freshness ceiling, now judged under both locks. A departed stay's
  -- check-out is already applied and a later reinstatement changes its lifecycle_version, which the context pins.
  IF NOT v_departed THEN
    PERFORM 1 FROM iam_v2.stays st
     WHERE st.id = p_stay
       AND iam_v2.p3_feed_authorizes(p_tenant, p_site, p_interface, p_revision, st.occupancy_evidence_at);
    IF NOT FOUND THEN
      RAISE EXCEPTION 'CONTEXT_INVALID: stay % is not eligible for a PMS context', p_stay;
    END IF;
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
