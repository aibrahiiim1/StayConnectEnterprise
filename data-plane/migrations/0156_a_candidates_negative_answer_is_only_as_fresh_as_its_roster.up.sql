-- 0156 — A CANDIDATE'S "NO SUCH GUEST" IS ONLY AS FRESH AS ITS ROSTER.
--
-- iam_v2.p3_interface_can_authorise (0155) asked p3_feed_authorizes about one interface with now() as the evidence time. That makes the absolute freshness ceiling (the
-- revision's max_auth_cache_age) trivially true: for an interface that is disconnected and answering from its last published roster, the ceiling is exactly what bounds
-- how old that roster may be. A shared STRICT network needs every candidate's negative answer to be trustworthy, so a disconnected candidate whose roster has aged out
-- must be STALE, not a confident NO_MATCH that lets another interface's guest in.
--
-- The evidence time of a candidate's answer is therefore the age of its roster: now() while the feed is live and in sync (the heartbeat bound inside the predicate
-- already guards that branch), and the time of its last complete synchronisation while it is not.

BEGIN;

CREATE OR REPLACE FUNCTION iam_v2.p3_interface_can_authorise(p_tenant uuid, p_site uuid, p_interface uuid)
  RETURNS boolean
  LANGUAGE sql STABLE SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
  SELECT COALESCE(bool_or(iam_v2.p3_feed_authorizes(pi.tenant_id, pi.site_id, pi.id, pi.current_revision_id,
           CASE WHEN rt.transport_status = 'CONNECTED' AND rt.sync_status = 'IN_SYNC' THEN now() ELSE rt.last_complete_sync_at END)), false)
    FROM iam_v2.pms_interfaces pi
    LEFT JOIN iam_v2.pms_interface_runtime rt
      ON rt.tenant_id = pi.tenant_id AND rt.site_id = pi.site_id AND rt.pms_interface_id = pi.id
   WHERE pi.tenant_id = p_tenant AND pi.site_id = p_site AND pi.id = p_interface AND pi.lifecycle_state = 'ACTIVE';
$fn$;

COMMIT;
