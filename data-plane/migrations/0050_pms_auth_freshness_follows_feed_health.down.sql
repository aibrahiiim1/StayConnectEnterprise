-- Restore migration 0010's issue_or_return_pms_context and drop the shared eligibility predicate.
--
-- This puts the ORIGINAL rule back verbatim, including the 300-second default that made Room sign-in expire
-- five minutes after check-in. That is what a rollback means here: the behaviour returns to what it was, feed
-- health stops being consulted, and a disconnected interface again authorises guests for five minutes on
-- stored evidence. Anyone rolling this back should expect PMS Room sign-in to stop working for real guests.

BEGIN;

CREATE OR REPLACE FUNCTION iam_v2.issue_or_return_pms_context(
    p_tenant uuid, p_site uuid, p_interface uuid, p_revision uuid, p_stay uuid,
    p_device uuid, p_guest_network uuid, p_request uuid, p_ttl_seconds int)
  RETURNS TABLE (context_id uuid, reused boolean)
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
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

  SELECT st.lifecycle_version, st.occupancy_evidence_version INTO v_lifecycle, v_ev
    FROM iam_v2.stays st
    JOIN iam_v2.pms_interfaces pi
      ON pi.tenant_id=st.tenant_id AND pi.site_id=st.site_id AND pi.id=st.pms_interface_id
    JOIN iam_v2.pms_interface_revisions pr
      ON pr.tenant_id=st.tenant_id AND pr.site_id=st.site_id
     AND pr.pms_interface_id=st.pms_interface_id AND pr.id=p_revision
   WHERE st.tenant_id=p_tenant AND st.site_id=p_site AND st.pms_interface_id=p_interface AND st.id=p_stay
     AND st.status='IN_HOUSE' AND pi.lifecycle_state='ACTIVE'
     AND st.occupancy_evidence_at IS NOT NULL AND st.occupancy_clock_suspect IS NOT TRUE
     AND st.occupancy_evidence_version > 0 AND st.occupancy_revision_id = p_revision
     AND st.occupancy_evidence_at > now() - make_interval(secs =>
           CASE WHEN (pr.config->>'max_auth_cache_age_seconds') ~ '^[1-9][0-9]{0,5}$'
                THEN CASE WHEN (pr.config->>'max_auth_cache_age_seconds')::int <= 604800
                          THEN (pr.config->>'max_auth_cache_age_seconds')::int ELSE 300 END
                ELSE 300 END)
   FOR UPDATE OF st;
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
END $fn$;

DROP FUNCTION IF EXISTS iam_v2.p3_feed_authorizes(uuid, uuid, uuid, uuid, timestamptz);
DROP FUNCTION IF EXISTS iam_v2.p3_cfg_secs(jsonb, text, int);

COMMIT;
