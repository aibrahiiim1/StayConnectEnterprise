-- Restore 0053/0050: authorise without waiting for the materialized roster.
--
-- Rolling this back re-opens the race this migration closed. p3_feed_authorizes returns to judging feed health
-- alone, and issue_or_return_pms_context stops taking the runtime-row linearization lock, so an authorisation
-- can once again commit on a snapshot that predates an already-durable occupancy event. Grant-time
-- revalidation in internal/authctx is a Go change and is reverted with the binary, not here.
--
-- The partial index is dropped last: nothing depends on it once the predicate no longer reads it.

BEGIN;

CREATE OR REPLACE FUNCTION iam_v2.p3_feed_authorizes(
  p_tenant uuid, p_site uuid, p_interface uuid, p_revision uuid, p_evidence_at timestamptz
) RETURNS boolean
LANGUAGE sql STABLE AS $fn$
  SELECT EXISTS (
    SELECT 1
      FROM iam_v2.pms_interfaces pi
      JOIN iam_v2.pms_interface_revisions pr
        ON pr.tenant_id=pi.tenant_id AND pr.site_id=pi.site_id
       AND pr.pms_interface_id=pi.id AND pr.id=p_revision
      JOIN iam_v2.pms_interface_runtime rt
        ON rt.tenant_id=pi.tenant_id AND rt.site_id=pi.site_id AND rt.pms_interface_id=pi.id
     WHERE pi.tenant_id=p_tenant AND pi.site_id=p_site AND pi.id=p_interface
       AND pi.lifecycle_state='ACTIVE'
       AND p_evidence_at IS NOT NULL
       -- (1) EITHER the feed is live, OR the mirror is trustworthy without it. Exactly one of these branches
       --     can hold at a time, because they disagree about transport_status by construction.
       AND (
             (    rt.transport_status = 'CONNECTED'
              AND rt.sync_status      = 'IN_SYNC'
              AND COALESCE(rt.last_heartbeat_at, rt.last_connected_at) IS NOT NULL
              AND COALESCE(rt.last_heartbeat_at, rt.last_connected_at)
                    > now() - make_interval(secs => iam_v2.p3_cfg_secs(pr.config,'heartbeat_timeout_ms',300)))
             OR
             (    rt.transport_status <> 'CONNECTED'
              AND rt.last_complete_sync_at IS NOT NULL
              AND rt.resync_started_at IS NULL)
           )
       -- (2) shared by both branches: no unresolved continuity loss, and the runtime is serving the Revision
       --     the caller presented rather than some other one.
       AND rt.continuity_status = 'CONTINUOUS'
       AND rt.pinned_revision_id = p_revision
       -- (3) the absolute ceiling, unchanged. This is also the offline bound: nothing re-stamps evidence while
       --     the feed is down, so eligibility decays on its own instead of on a timer invented for the
       --     purpose. An explicit max_auth_cache_age_seconds overrides it where an operator has chosen one.
       AND p_evidence_at > now() - make_interval(secs =>
             CASE
               WHEN (pr.config->>'max_auth_cache_age_seconds') ~ '^[1-9][0-9]{0,5}$'
                AND (pr.config->>'max_auth_cache_age_seconds')::int <= 604800
                 THEN (pr.config->>'max_auth_cache_age_seconds')::int
               ELSE iam_v2.p3_cfg_secs(pr.config, 'complete_sync_ms', 86400)
                  + iam_v2.p3_cfg_secs(pr.config, 'heartbeat_timeout_ms', 300)
             END)
  );
$fn$;

COMMENT ON FUNCTION iam_v2.p3_feed_authorizes(uuid,uuid,uuid,uuid,timestamptz) IS
  'Single source of truth for the feed half of PMS Room sign-in eligibility. Authorises on EITHER a live feed '
  '(transport CONNECTED, IN_SYNC, live within the Revision heartbeat_timeout_ms) OR a trusted local mirror '
  '(transport not connected, a complete sync has happened, no resync in flight). Both branches additionally '
  'require interface ACTIVE, continuity CONTINUOUS, runtime pinned to the presented Revision, and evidence '
  'within one complete-sync cadence.';

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
     -- Feed health + the freshness ceiling, judged against the locked tuple's own evidence timestamp.
     AND iam_v2.p3_feed_authorizes(p_tenant, p_site, p_interface, p_revision, st.occupancy_evidence_at)
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


DROP INDEX IF EXISTS iam_v2.se_pending_claimable;

COMMIT;
