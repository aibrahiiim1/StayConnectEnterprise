-- 0205 down — the drain of a published refresh blocks Room sign-in again (the 0150 predicate, verbatim).

BEGIN;

DROP FUNCTION IF EXISTS iam_v2.p3_interface_roster_draining(uuid,uuid,uuid);

CREATE OR REPLACE FUNCTION iam_v2.p3_feed_authorizes(
  p_tenant uuid, p_site uuid, p_interface uuid, p_revision uuid, p_evidence_at timestamptz
) RETURNS boolean
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
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
       -- (1) EITHER the feed is live, OR the last published roster stands on its own (D39).
       AND (
             (    rt.transport_status = 'CONNECTED'
              AND rt.sync_status      = 'IN_SYNC'
              AND COALESCE(rt.last_heartbeat_at, rt.last_connected_at) IS NOT NULL
              AND COALESCE(rt.last_heartbeat_at, rt.last_connected_at)
                    > now() - make_interval(secs => iam_v2.p3_cfg_secs(pr.config,'heartbeat_timeout_ms',300)))
             OR
             -- THE LAST GOOD ROSTER, whenever the live branch is not satisfied because the guest list is not
             -- IN_SYNC (a resync requested, running or failed) or the transport is down. Either way the roster
             -- already published is untouched until a replacement completes, so it is exactly as trustworthy as
             -- before the attempt began. A connected feed that is IN_SYNC but silent is NOT covered: that is a
             -- hung link, not a resync, and stays refused.
             (    rt.last_complete_sync_at IS NOT NULL
              AND (rt.transport_status <> 'CONNECTED' OR rt.sync_status <> 'IN_SYNC'))
           )
       AND rt.continuity_status = 'CONTINUOUS'
       AND rt.pinned_revision_id = p_revision
       -- (2) MATERIALIZATION READINESS, unchanged — and the reason the partial generation cannot leak in.
       AND NOT EXISTS (
             SELECT 1 FROM iam_v2.stay_events se
              WHERE se.tenant_id = rt.tenant_id
                AND se.site_id = rt.site_id
                AND se.pms_interface_id = rt.pms_interface_id
                AND se.processing_status = 'PENDING'
                AND (se.admission_kind = 'LIVE'
                     OR se.resync_generation <= rt.published_resync_generation))
       -- (3) the absolute freshness ceiling. For the offline branch this IS the seventy-two hours: the last
       --     good roster is usable until it ages out, and then nothing is.
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
  '(CONNECTED, IN_SYNC, heard within the heartbeat timeout) OR the LAST SUCCESSFULLY PUBLISHED roster whenever '
  'the transport is down or the guest list is not IN_SYNC (D39, D69), in both cases only when the materialized '
  'roster has caught up: no CLAIMABLE PENDING stay_event may exist, where claimable mirrors the applier''s own '
  'predicate (LIVE, or a generation at or below the published one). A resync in flight or failed does NOT '
  'invalidate the published roster; only the evidence age retires it. STABLE and therefore NOT sufficient on '
  'its own: callers take the interface runtime row lock first.';

COMMIT;
