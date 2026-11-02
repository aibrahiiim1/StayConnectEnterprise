-- 0205 — A GUEST WHO IS FOUND SIGNS IN WHILE A REFRESHED ROSTER IS WRITTEN IN.
--
-- Every complete PMS synchronisation (about every ten minutes on a FIAS link) is published and then drained into the stays
-- one event at a time: ~420 stays take ~3.5 s on the test appliance. p3_feed_authorizes refused EVERY guest for that whole
-- drain, because a claimable PENDING row of the published generation counted as "the roster has not caught up". Guests with
-- correct details were told "we are unable to verify your stay right now" several times an hour (measured 2026-11-02).
--
-- What the drain guard protects is the NEGATIVE answer: a guest whose stay is still among the rows to come must not be told
-- that their details are wrong (the first sync after a reset left ~129 in-house guests unwritten). A POSITIVE answer during
-- the drain is as trustworthy as the published roster already is while a resync is received (D39, 0150): every stay row is
-- either its previous or its refreshed state, both true PMS facts. So:
--
--   * p3_feed_authorizes no longer blocks on the published generation's drain. A claimable LIVE event (a checkout, a move)
--     still blocks: that row is newer than the roster and may take access away.
--   * p3_interface_roster_draining tells scd that a drain is in progress, so a NON-match is not answered while it lasts:
--     scd waits for the drain and asks again, and answers "cannot check right now" (technical, never counted against the
--     guest) if it outlasts the wait.
--
-- An abandoned (unpublished) generation never blocked and does not drain: only rows at or below the published generation count.

BEGIN;

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
             (    rt.last_complete_sync_at IS NOT NULL
              AND (rt.transport_status <> 'CONNECTED' OR rt.sync_status <> 'IN_SYNC'))
           )
       AND rt.continuity_status = 'CONTINUOUS'
       AND rt.pinned_revision_id = p_revision
       -- (2) NO LIVE EVENT WAITING. A claimable LIVE row is newer than everything materialized and may take access away
       --     (a GO). The drain of a published refresh no longer blocks here (0205): positives stand, and scd holds back
       --     negatives while p3_interface_roster_draining says rows are still to come.
       AND NOT EXISTS (
             SELECT 1 FROM iam_v2.stay_events se
              WHERE se.tenant_id = rt.tenant_id
                AND se.site_id = rt.site_id
                AND se.pms_interface_id = rt.pms_interface_id
                AND se.processing_status = 'PENDING'
                AND se.admission_kind = 'LIVE')
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
  'the transport is down or the guest list is not IN_SYNC (D39, D69), in both cases only when no claimable LIVE '
  'stay_event is pending. The drain of a published refresh does not block (0205): a found guest stands, and scd '
  'holds back a non-match while p3_interface_roster_draining is true. STABLE and therefore NOT sufficient on '
  'its own: callers take the interface runtime row lock first.';

-- Is a published refresh still being written into the stays? No guest, Stay or reservation is exposed: one boolean per interface.
CREATE OR REPLACE FUNCTION iam_v2.p3_interface_roster_draining(p_tenant uuid, p_site uuid, p_interface uuid)
  RETURNS boolean
  LANGUAGE sql STABLE SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
  SELECT EXISTS (
    SELECT 1
      FROM iam_v2.pms_interface_runtime rt
      JOIN iam_v2.stay_events se
        ON se.tenant_id = rt.tenant_id AND se.site_id = rt.site_id AND se.pms_interface_id = rt.pms_interface_id
     WHERE rt.tenant_id = p_tenant AND rt.site_id = p_site AND rt.pms_interface_id = p_interface
       AND se.processing_status = 'PENDING'
       AND se.admission_kind = 'RESYNC'
       AND se.resync_generation <= rt.published_resync_generation);
$fn$;

DO $own$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
    EXECUTE 'ALTER FUNCTION iam_v2.p3_interface_roster_draining(uuid,uuid,uuid) OWNER TO iam_v2_owner';
  END IF;
END $own$;

REVOKE ALL ON FUNCTION iam_v2.p3_interface_roster_draining(uuid,uuid,uuid) FROM PUBLIC;
DO $gf$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_scd') THEN
    GRANT EXECUTE ON FUNCTION iam_v2.p3_interface_roster_draining(uuid,uuid,uuid) TO svc_scd;
  END IF;
END
$gf$;

COMMIT;
