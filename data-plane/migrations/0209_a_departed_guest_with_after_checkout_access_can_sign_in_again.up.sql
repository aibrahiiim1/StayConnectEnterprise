-- 0209 -- A DEPARTED GUEST WHOSE AFTER CHECK-OUT ACCESS IS STILL VALID CAN SIGN IN AGAIN (Product Owner, 2026-11-05; D93 defect).
--
-- D93: a device the stay used before check-out reconnects while the stay's After Check-out Access remains valid; a new
-- device is refused (AFTER_CHECKOUT_NEW_DEVICE_REJECTED, authorize_entitlement_device). Room sign-in could never reach
-- that rule: a checked-out stay is CHECKED_OUT, and issue_or_return_pms_context issued a context to IN_HOUSE stays only,
-- so every departed guest was refused as STAY_NOT_ELIGIBLE, even with a valid After Check-out entitlement.
--
-- WHAT THIS MIGRATION CHANGES, AND ONLY THAT: issue_or_return_pms_context also issues a context to a CHECKED_OUT stay that
-- holds an ACTIVE, in-window GRACE_AFTER_CHECKOUT entitlement. Such a context can only JOIN that entitlement: it carries no
-- offer (scd offers nothing to a stay with live access), and a purchase is refused while the entitlement is live. Which
-- devices may join is still decided by authorize_entitlement_device alone. The IN_HOUSE arm is unchanged, including its
-- occupancy, revision and feed-health conditions. No table, row, privilege or allowance changes: CREATE OR REPLACE keeps
-- the function's owner and grants. scd re-checks the same rule when the context is consumed (internal/authctx).

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
