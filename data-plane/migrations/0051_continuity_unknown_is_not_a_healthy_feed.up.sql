-- UNKNOWN continuity is "never established", not "fine so far". Authentication must not accept it.
--
-- Migration 0050 documented the rule as CONNECTED + IN_SYNC + CONTINUOUS and then wrote the predicate as
-- continuity_status IN ('CONTINUOUS','UNKNOWN'). The comment was the contract; the code was wider than it.
--
-- WHAT UNKNOWN ACTUALLY MEANS, from the runtime state machine rather than from the name:
--
--   * iam_v2.pms_interface_runtime.continuity_status is declared NOT NULL DEFAULT 'UNKNOWN', so it is the
--     value a runtime row is BORN with, before the interface has done anything at all;
--   * the only transitions to CONTINUOUS are pgRepo.PublishResyncGeneration, which sets it in the same
--     UPDATE as sync_status='IN_SYNC' when a complete DS…DE generation publishes, and workerSink.OnDomainEvent
--     when a LIVE event is admitted;
--   * MarkGapAndRequireResync moves it to GAP_DETECTED on a continuity fault;
--   * nothing ever sets it back to UNKNOWN.
--
-- So UNKNOWN is reachable in exactly one situation: an interface that has never completed a resync. That is
-- the state in which we know least about the feed, and it was the one state where a missing positive signal
-- was being read as an absent negative one.
--
-- ACCEPTING IT ALSO BOUGHT NOTHING. Because publishing a resync sets IN_SYNC and CONTINUOUS in a single
-- statement, sync_status='IN_SYNC' already implies continuity_status='CONTINUOUS'; there is no window in
-- which a healthy interface is IN_SYNC while still UNKNOWN. The disjunct could only ever have mattered if
-- some future path set IN_SYNC without CONTINUOUS — precisely the case where it should refuse, and would
-- instead have authorised guests on an interface whose continuity had never been established.
--
-- Requiring CONTINUOUS therefore changes no behaviour any healthy interface can observe, and closes the one
-- state where the predicate disagreed with its own contract. Verified on PRE-LIVE before this migration was
-- written: the live interface reports CONNECTED / IN_SYNC / CONTINUOUS.
--
-- Only the continuity term changes. Everything else in p3_feed_authorizes is 0050's, unaltered.

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
       -- (1) the feed is maintaining the mirror RIGHT NOW. A disconnected, out-of-sync, gapped or
       --     never-established interface authorises nobody, however recent its stored evidence looks.
       AND rt.transport_status  = 'CONNECTED'
       AND rt.sync_status       = 'IN_SYNC'
       AND rt.continuity_status = 'CONTINUOUS'
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
  'heartbeat_timeout_ms, and the supplied evidence timestamp within one complete-sync cadence. UNKNOWN '
  'continuity means never established and authorises nobody. Silence from a healthy feed confirms an '
  'unchanged Stay; a disconnected feed authorises nobody. Deliberately does NOT read iam_v2.stays: callers '
  'hold FOR UPDATE OF st and need those conditions inline so EvalPlanQual rechecks them against the locked '
  'tuple.';

DO $grant$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_scd') THEN
    GRANT EXECUTE ON FUNCTION iam_v2.p3_feed_authorizes(uuid, uuid, uuid, uuid, timestamptz) TO svc_scd;
  END IF;
END $grant$;

COMMIT;
