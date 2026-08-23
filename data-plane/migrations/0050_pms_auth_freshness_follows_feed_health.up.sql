-- PMS Room sign-in freshness: judge the FEED, not the calendar.
--
-- THE RULE BEING REPLACED MADE ROOM SIGN-IN UNUSABLE. A Stay could authenticate only while its own occupancy
-- evidence was younger than max_auth_cache_age_seconds, which is unset on every interface, so the 300-second
-- default applied. Occupancy evidence is stamped when the PMS says something about that Stay, and the PMS says
-- nothing about a guest who is simply staying in their room. So a guest could sign in for five minutes after
-- check-in and never again — while the interface was connected, healthy, and actively confirming that nothing
-- about that Stay had changed. Silence from a healthy feed is CONFIRMATION, not decay.
--
-- Reading it as decay also failed in the other direction. When the link dropped, every Stay kept its evidence
-- timestamp and stayed eligible for a further five minutes with no feed at all behind it. The bound was too
-- tight to be usable and too loose to be safe, because it was measuring the wrong thing.
--
-- WHAT AUTHORISES A GUEST NOW is the health of the feed the evidence came from:
--
--   * the Stay is IN_HOUSE on an ACTIVE interface, with coherent evidence produced by the SAME published
--     Revision the caller presents (unchanged — these were never the problem);
--   * the interface is CONNECTED, IN_SYNC and CONTINUOUS right now, so the mirror is being maintained;
--   * it has proven liveness within its own heartbeat_timeout_ms, so a hung socket that never returned an
--     error cannot pass as connected;
--   * and the evidence is no older than one complete-sync cadence plus that heartbeat allowance, so a Stay the
--     PMS has silently stopped carrying cannot authorise forever on a feed that is healthy for other reasons.
--
-- Every bound is read from the interface's own Revision config (heartbeat_timeout_ms, complete_sync_ms) rather
-- than invented here, and max_auth_cache_age_seconds still wins where an operator has set it explicitly.
--
-- The practical effect: a guest who checked in yesterday signs in today while Protel is healthy, and NOBODY
-- signs in the moment Protel goes away. Both were broken before, in opposite directions.
--
-- The predicate also now lives in ONE place. It previously existed as three hand-maintained copies — this
-- function, and two queries in internal/authctx — which is the drift that let the rule be wrong in triplicate.

BEGIN;

-- ---------------------------------------------------------------------------------------------------------
-- Read a millisecond bound out of a Revision config as whole seconds, fail-closed.
--
-- Revision config is operator-authored JSON, so every read is treated as hostile: a missing key, a string, a
-- negative number, a float or an absurd value all fall back to the caller's default rather than producing a
-- window nobody intended. The regex bounds the digit count BEFORE any cast, so no input can raise here — this
-- is called from inside an authentication predicate, where an exception would be an outage.
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION iam_v2.p3_cfg_secs(p_config jsonb, p_key text, p_default int)
RETURNS int LANGUAGE sql IMMUTABLE AS $fn$
  SELECT CASE
           WHEN (p_config->>p_key) ~ '^[1-9][0-9]{0,10}$'
            AND (p_config->>p_key)::bigint BETWEEN 1000 AND 604800000
             THEN ((p_config->>p_key)::bigint / 1000)::int
           ELSE p_default
         END;
$fn$;

COMMENT ON FUNCTION iam_v2.p3_cfg_secs(jsonb,text,int) IS
  'Fail-closed millisecond-to-seconds reader for operator-authored Revision config. Any missing, malformed, '
  'out-of-range or non-integer value yields the supplied default. Cannot raise: callers are on the '
  'authentication path, where an exception would be an outage.';

-- ---------------------------------------------------------------------------------------------------------
-- The single definition of "the FEED behind this interface currently authorises guests, and evidence stamped
-- at p_evidence_at is still within its ceiling".
--
-- IT DELIBERATELY DOES NOT LOOK AT THE STAY ROW, and that boundary is load-bearing rather than tidy.
--
-- Callers hold `FOR UPDATE OF st` and rely on PostgreSQL's EvalPlanQual recheck: when a concurrent Checkout
-- commits while a consume is waiting on the Stay lock, the qual is re-evaluated against the UPDATED tuple, and
-- the consume is correctly refused. That recheck only re-evaluates conditions written against `st` itself. A
-- STABLE function re-reading iam_v2.stays internally would answer from the statement snapshot instead — the
-- one taken before the Checkout committed — and would still report the Stay as IN_HOUSE. Folding the Stay
-- conditions in here would therefore have let a checked-out guest consume their context, silently, under
-- exactly the concurrency this system serializes so carefully.
--
-- So the Stay-row conditions stay inline at each call site, where the lock protects them, and the evidence
-- timestamp is PASSED IN so the ceiling is also judged against the locked tuple. What lives here is the part
-- that was genuinely triplicated and genuinely wrong: the feed-health and freshness rule.
--
-- STABLE, not IMMUTABLE: it reads now() and mutable rows. SECURITY INVOKER (the default) is deliberate — the
-- caller's privileges still apply, so this cannot become a way to read state a role could not otherwise see.
-- ---------------------------------------------------------------------------------------------------------
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
       AND rt.transport_status = 'CONNECTED'
       AND rt.sync_status      = 'IN_SYNC'
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
  'Single source of truth for the FEED-health half of PMS Room sign-in eligibility: interface ACTIVE, transport '
  'connected, in sync, continuous, pinned to the presented Revision, live within heartbeat_timeout_ms, and the '
  'supplied evidence timestamp within one complete-sync cadence. Silence from a healthy feed confirms an '
  'unchanged Stay; a disconnected feed authorises nobody. Deliberately does NOT read iam_v2.stays: callers hold '
  'FOR UPDATE OF st and need those conditions inline so EvalPlanQual rechecks them against the locked tuple.';

-- ---------------------------------------------------------------------------------------------------------
-- issue_or_return_pms_context now asks the shared predicate instead of carrying its own copy of the rule.
--
-- Everything else is unchanged from migration 0010, deliberately: the controlled-operation scope, the refusal
-- to reuse a CONSUMED context, the FOR UPDATE row lock on the Stay, and the versions pinned into the issued
-- context. Only the eligibility test moves.
-- ---------------------------------------------------------------------------------------------------------
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

-- Least privilege, mirroring the existing grant on issue_or_return_pms_context: scd is the only service that
-- authenticates guests, so it is the only role that may ask whether a Stay is authorisable. The SECURITY
-- DEFINER function reaches these as its owner and needs no additional grant.
--
-- Guarded on role existence because the Gate-P service roles are provisioned per environment: the disposable
-- databases the integration gates build have no svc_scd, and a bare GRANT would abort the migration there.
DO $grant$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_scd') THEN
    GRANT EXECUTE ON FUNCTION iam_v2.p3_cfg_secs(jsonb, text, int) TO svc_scd;
    GRANT EXECUTE ON FUNCTION iam_v2.p3_feed_authorizes(uuid, uuid, uuid, uuid, timestamptz) TO svc_scd;
  END IF;
END $grant$;

COMMIT;
