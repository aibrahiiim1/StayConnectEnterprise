-- Restore migration 0050's continuity term, which also accepted UNKNOWN.
--
-- UNKNOWN is the value a runtime row is born with and means continuity was never established, so rolling this
-- back re-opens the one state where the predicate is wider than the contract it documents: an interface that
-- has never completed a resync would again be able to authorise guests if it ever reached IN_SYNC without
-- CONTINUOUS. Nothing else in the function changes.

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
       -- (1) the feed is maintaining the mirror RIGHT NOW. A disconnected, out-of-sync or gapped interface
       --     authorises nobody, however recent its stored evidence happens to look.
       AND rt.transport_status  = 'CONNECTED'
       AND rt.sync_status       = 'IN_SYNC'
       AND rt.continuity_status IN ('CONTINUOUS','UNKNOWN')
       AND rt.pinned_revision_id = p_revision
       -- (2) proven liveness, so a silently hung socket cannot masquerade as a healthy one.
       AND COALESCE(rt.last_heartbeat_at, rt.last_connected_at) IS NOT NULL
       AND COALESCE(rt.last_heartbeat_at, rt.last_connected_at)
             > now() - make_interval(secs => iam_v2.p3_cfg_secs(pr.config, 'heartbeat_timeout_ms', 300))
       -- (3) an absolute ceiling: one full resync cadence plus the heartbeat allowance, so a Stay the PMS has
       --     silently stopped carrying cannot authorise forever on a feed that is healthy for other guests.
       --     An explicit max_auth_cache_age_seconds overrides it where an operator has chosen one.
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
  'Single source of truth for the FEED-health half of PMS Room sign-in eligibility: interface ACTIVE, '
  'transport CONNECTED, sync IN_SYNC, continuity CONTINUOUS, pinned to the presented Revision, live within '
  'heartbeat_timeout_ms, and the supplied evidence timestamp within one complete-sync cadence. Silence from '
  'a healthy feed confirms an unchanged Stay; a disconnected feed authorises nobody. Deliberately does NOT '
  'read iam_v2.stays: callers '
  'hold FOR UPDATE OF st and need those conditions inline so EvalPlanQual rechecks them against the locked '
  'tuple.';

DO $grant$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_scd') THEN
    GRANT EXECUTE ON FUNCTION iam_v2.p3_feed_authorizes(uuid, uuid, uuid, uuid, timestamptz) TO svc_scd;
  END IF;
END $grant$;

COMMIT;
