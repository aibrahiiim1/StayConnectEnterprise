-- A DEAD SOCKET IS NOT A DEAD MIRROR.
--
-- Until now p3_feed_authorizes required transport_status='CONNECTED'. The moment Protel dropped, every guest
-- in the building lost the ability to sign in — including guests whose Stay StayConnect had mirrored locally,
-- completely, coherently, and was still holding. The reason given to the guest was the uniform failure, and
-- the reason given to the operator was "Room sign-in unavailable", both of which were true only in the sense
-- that the code had decided so.
--
-- TRANSPORT AVAILABILITY AND MIRROR TRUST ARE DIFFERENT PROPERTIES. The socket answers "can I hear the PMS
-- right now"; the mirror answers "do I hold PMS-derived state I have reason to believe". A hotel whose PMS
-- vendor takes the interface down for an afternoon still knows exactly who is in room 7102, because the PMS
-- told it so this morning and nothing has contradicted it since. Refusing that guest is not caution, it is
-- discarding evidence we already have.
--
-- SO THE RULE SPLITS IN TWO, and only one branch is new:
--
--   LIVE FEED       transport CONNECTED, IN_SYNC, and proven live within the Revision's own
--                   heartbeat_timeout_ms. Unchanged, byte for byte, from 0050/0051. A hung socket that never
--                   returned an error still cannot pass as connected.
--
--   TRUSTED MIRROR  transport NOT connected — DISCONNECTED, DIAL_FAILED, UNKNOWN, anything — but the mirror
--                   itself is coherent: a complete sync has actually happened at some point, and no resync is
--                   in flight right now.
--
-- Everything the two branches share stays outside them and therefore applies to both: the interface is ACTIVE,
-- continuity is CONTINUOUS, the runtime is pinned to the Revision the caller presents, and the evidence is
-- within its ceiling. Those are the conditions that make local state trustworthy, and none of them is relaxed.
--
-- WHAT STILL FAILS CLOSED, deliberately and by construction:
--
--   * never synchronised           last_complete_sync_at IS NULL. No baseline was ever established, so there
--                                  is no mirror to trust — only an empty table that would answer "no such
--                                  Stay" for a hotel full of guests.
--   * unresolved continuity loss   continuity_status <> 'CONTINUOUS'. A detected gap means events went
--                                  missing; the mirror may be describing a guest who has since left.
--   * resync in flight             resync_started_at IS NOT NULL. A complete sync partway through has applied
--                                  some of the truth and none of the rest, which is the one state where the
--                                  mirror is genuinely inconsistent rather than merely stale.
--   * revision drift               pinned_revision_id <> the presented Revision. Unchanged.
--   * stale beyond the ceiling     the evidence bound below. Unchanged.
--   * anything about the Stay      checked out, cancelled, no-show, wrong room, wrong verification value,
--                                  ambiguous match. This function has never read iam_v2.stays and still does
--                                  not; those conditions live inline at each call site under FOR UPDATE OF st,
--                                  for the EvalPlanQual reason documented in 0050. Local-mirror authentication
--                                  does not touch them, so a checked-out guest is refused exactly as before,
--                                  and Checkout-Grace remains the authoritative path for them.
--
-- NO OFFLINE TTL IS INVENTED HERE, and that is a deliberate refusal rather than an omission. How long a
-- disconnected mirror may keep authorising is already answered by the ceiling this function has always
-- applied: one complete_sync_ms cadence plus the heartbeat allowance, or max_auth_cache_age_seconds where an
-- operator has set one. While the feed is live, each complete sync re-stamps occupancy evidence and the
-- ceiling never bites. While it is down, nothing re-stamps anything, so every Stay ages out of eligibility one
-- by one as its own evidence crosses that bound — no cliff, no separate timer, no second number to keep
-- consistent with the first. With the default 86400000 ms cadence that is roughly a day of offline sign-in
-- from each Stay's last evidence stamp. If a property needs longer, the honest lever is
-- max_auth_cache_age_seconds on the Revision, which is an operator decision recorded in configuration — not a
-- constant chosen here because it made a test pass.

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
  'within one complete-sync cadence — the same bound that serves as the offline validity limit, so no separate '
  'offline TTL exists. Transport availability and mirror trust are different properties: a disconnected socket '
  'no longer denies a guest whose Stay is mirrored coherently. Never synchronised, unresolved continuity loss, '
  'a resync in flight, revision drift or stale evidence still authorise nobody. Deliberately does NOT read '
  'iam_v2.stays: callers hold FOR UPDATE OF st and need those conditions inline so EvalPlanQual rechecks them '
  'against the locked tuple.';

COMMIT;
