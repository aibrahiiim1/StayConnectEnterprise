-- PUBLICATION IS NOT MATERIALIZATION.
--
-- p3_feed_authorizes treated sync_status='IN_SYNC' as evidence that the guest roster was usable. It is not:
-- IN_SYNC means a generation was PUBLISHED, and says nothing about whether that generation's events have been
-- applied to iam_v2.stays — the table Room resolution actually reads.
--
-- Measured on the PRE-LIVE appliance, 2026-08-25: the publish barrier fell at 23:17:57.801404, the first
-- generation-20 event was applied 211 ms later, and the last reached a terminal state 3.487993 s after the
-- barrier. Throughout that window room_auth_ready was true and roughly 129 in-house guests did not yet exist
-- in iam_v2.stays. Nobody attempted authentication, so the race was never run — this migration closes it
-- before somebody does.
--
-- AND IT IS NOT A FULL-SYNC PROBLEM. stayengine claims LIVE events unconditionally, so in ordinary steady
-- state a GO can be durable and unapplied while the guest is still IN_HOUSE in the materialized view. A
-- resync-generation watermark would have closed the visible 3.5-second window and left that one open every
-- day. The bound below is over PENDING EVENTS, not over generations, for exactly that reason.
--
-- ============================================================================================================
-- WHICH PENDING ROWS BLOCK — this term mirrors the applier's claim predicate and must keep mirroring it.
--
-- stayengine.ProcessNext claims:
--     processing_status='PENDING' AND (admission_kind='LIVE' OR resync_generation <= published_resync_generation)
--
-- Anything it CANNOT legally claim must not block authentication, or auth waits forever on work that will
-- never happen:
--
--   LIVE PENDING .................. BLOCKS. Claimable now; the everyday race.
--   published RESYNC PENDING ...... BLOCKS. Claimable now; the post-publish drain.
--   open generation (gen > published) NO. Not claimable. IN_SYNC is false during DS→DE anyway.
--   ABANDONED generation .......... NO. This is the one that matters. An interrupted resync leaves rows
--                                   PENDING at a generation that will never publish. Blocking on them would
--                                   permanently disable Room sign-in — including D39 offline sign-in, where
--                                   the mirror is otherwise perfectly serviceable. The same
--                                   `<= published_resync_generation` term excludes them by construction.
--   APPLIED / MANUAL_REVIEW /
--   SKIPPED_DUPLICATE ............. NO. Terminal.
--
-- ============================================================================================================
-- THIS PREDICATE ALONE IS NOT SUFFICIENT, and saying so here matters more than the SQL.
--
-- The function is STABLE and READ COMMITTED gives every statement its own snapshot, so a caller can evaluate
-- it as true, have a concurrent pmsd transaction commit a new PENDING event, and then commit its own
-- authorisation — authorising on a snapshot that predates a fact already durable. Demonstrated deterministically
-- against PostgreSQL 16:
--
--     A1 00:37:52.967  auth predicate = true, Stay locked
--     B  00:37:56.014  pmsd admission COMMITTED (locks the RUNTIME row; never waits on the Stay lock)
--     A3 00:38:00.982  auth COMMITTED — 4.97 s after the conflicting event was durable
--
-- SERIALIZABLE does not help: the auth transaction reads stay_events and writes stays while admission writes
-- stay_events and reads nothing auth wrote, so there is no dangerous cycle for SSI to abort. Also measured —
-- both transactions committed cleanly under SERIALIZABLE.
--
-- The linearization point is therefore a LOCK, and it must be taken by the CALLER: a STABLE function cannot
-- take one. Callers take pms_interface_runtime FOR UPDATE — the same row admission already serializes on —
-- AFTER the Stay lock, preserving the L1 Stay-first order. issue_or_return_pms_context does it below;
-- internal/authctx does it in Go for the paths that build their own statements.
--
-- Lock order stays acyclic: auth takes stays → runtime, admission takes runtime only and never takes a Stay
-- lock, and the applier takes advisory → stay_events → stays while reading runtime by plain JOIN. Nothing
-- acquires stays after runtime, so no cycle exists.

BEGIN;

-- The predicate above is on the authentication hot path and stay_events carries no index leading with
-- processing_status. PARTIAL on PENDING, which is normally an empty set, so the index is tiny and the lookup
-- is a probe that finds nothing.
--
-- NOT CONCURRENTLY, deliberately. scripts/edge-migrate.sh streams the whole migration into a single psql
-- invocation and every migration here opens its own BEGIN, so CREATE INDEX CONCURRENTLY would abort inside a
-- transaction block. A partial index over an empty set builds instantly; if that ever stops being true this
-- must become an out-of-band operational step rather than a runner change.
CREATE INDEX IF NOT EXISTS se_pending_claimable
    ON iam_v2.stay_events (tenant_id, site_id, pms_interface_id, admission_kind, resync_generation)
 WHERE processing_status = 'PENDING';

COMMENT ON INDEX iam_v2.se_pending_claimable IS
  'Supports the materialization-readiness term in iam_v2.p3_feed_authorizes. Partial on PENDING because that '
  'set is empty in steady state, so the readiness probe costs an index lookup that returns nothing.';

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
       -- (1) EITHER the feed is live, OR the mirror is trustworthy without it (D39). Unchanged.
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
       AND rt.continuity_status = 'CONTINUOUS'
       AND rt.pinned_revision_id = p_revision
       -- (2) MATERIALIZATION READINESS. Applies to BOTH branches: a mirror the applier has not caught up with
       --     is incomplete, and that is a property of the mirror, not of the transport. It is what makes the
       --     offline branch safe as well — a socket that drops mid-drain leaves work the applier continues
       --     without it, so this resolves in seconds rather than requiring the PMS back.
       AND NOT EXISTS (
             SELECT 1 FROM iam_v2.stay_events se
              WHERE se.tenant_id = rt.tenant_id
                AND se.site_id = rt.site_id
                AND se.pms_interface_id = rt.pms_interface_id
                AND se.processing_status = 'PENDING'
                AND (se.admission_kind = 'LIVE'
                     OR se.resync_generation <= rt.published_resync_generation))
       -- (3) the absolute freshness ceiling, unchanged. Also the offline validity bound.
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
  'OR a trusted local mirror (D39), and in BOTH cases only when the materialized roster has caught up: no '
  'CLAIMABLE PENDING stay_event may exist for the interface, where claimable mirrors the applier''s own claim '
  'predicate (LIVE, or a resync generation at or below the published one). Rows of an abandoned or unpublished '
  'generation are deliberately excluded — the applier can never consume them, and blocking on them would '
  'disable Room sign-in permanently including offline. STABLE and therefore NOT sufficient on its own: callers '
  'must take pms_interface_runtime FOR UPDATE after the Stay lock, because READ COMMITTED lets a concurrent '
  'admission commit between this predicate and the caller''s commit.';

-- ------------------------------------------------------------------------------------------------------------
-- The issuing function takes the linearization lock. This is the path scd actually uses; guarding only the Go
-- helpers in internal/authctx would leave the real one open.
-- ------------------------------------------------------------------------------------------------------------
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

  -- Feed health, materialization readiness and the freshness ceiling, now judged under both locks.
  PERFORM 1 FROM iam_v2.stays st
   WHERE st.id = p_stay
     AND iam_v2.p3_feed_authorizes(p_tenant, p_site, p_interface, p_revision, st.occupancy_evidence_at);
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

-- Least privilege, unchanged in shape from 0050. No new grant is required by this migration: the readiness
-- term reads iam_v2.stay_events from INSIDE p3_feed_authorizes, which svc_scd already executes, and the
-- SECURITY DEFINER issuing function reaches everything as its owner. The FOR UPDATE that internal/authctx
-- takes in Go does need SELECT on pms_interface_runtime, which svc_scd already holds — asserted by the
-- least-privilege test rather than assumed here.
DO $grant$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_scd') THEN
    GRANT EXECUTE ON FUNCTION iam_v2.p3_feed_authorizes(uuid, uuid, uuid, uuid, timestamptz) TO svc_scd;
  END IF;
END $grant$;

COMMIT;
