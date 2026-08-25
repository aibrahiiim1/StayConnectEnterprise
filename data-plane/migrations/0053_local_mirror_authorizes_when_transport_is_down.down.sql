-- Restore 0051's rule, in which a disconnected socket authorises nobody.
--
-- Rolling this back re-imposes the behaviour the Product Owner's local-first rule was written to remove: the
-- moment the PMS transport drops, every guest in the property loses Room sign-in, including guests whose Stay
-- StayConnect holds mirrored, complete and coherent. Already-authorised Entitlements and Sessions are
-- unaffected either way — they never consulted this predicate — so the effect of the rollback is confined to
-- NEW sign-ins during an outage.
--
-- Nothing else changes. The fail-closed conditions this migration added on the offline branch
-- (last_complete_sync_at, resync_started_at) simply become unreachable, because the branch itself is gone.

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
       AND rt.transport_status = 'CONNECTED'
       AND rt.sync_status      = 'IN_SYNC'
       AND rt.continuity_status = 'CONTINUOUS'
       AND rt.pinned_revision_id = p_revision
       AND COALESCE(rt.last_heartbeat_at, rt.last_connected_at) IS NOT NULL
       AND COALESCE(rt.last_heartbeat_at, rt.last_connected_at)
             > now() - make_interval(secs => iam_v2.p3_cfg_secs(pr.config, 'heartbeat_timeout_ms', 300))
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
  'Single source of truth for the FEED-health half of PMS Room sign-in eligibility: interface ACTIVE, transport '
  'CONNECTED, sync IN_SYNC, continuity CONTINUOUS, pinned to the presented Revision, live within '
  'heartbeat_timeout_ms, and the supplied evidence timestamp within one complete-sync cadence. UNKNOWN '
  'continuity means never established and authorises nobody. Silence from a healthy feed confirms an unchanged '
  'Stay; a disconnected feed authorises nobody. Deliberately does NOT read iam_v2.stays: callers hold FOR '
  'UPDATE OF st and need those conditions inline so EvalPlanQual rechecks them against the locked tuple.';

COMMIT;
