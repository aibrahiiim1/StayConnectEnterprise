-- A DEPARTURE THAT WENT TO REVIEW IS ANSWERED BY THE PMS, NOT BY REPLAYING IT.
--
-- Migration 0069 shipped iam_v2.pms_reoffer_stay_event, which moved a MANUAL_REVIEW departure back to PENDING
-- so the ingestion engine would reconsider it. It cannot work, and it should not: it contradicts two
-- deliberate invariants that were already there.
--
--   1. iam_v2.stay_events IS STRICTLY ONE-WAY. The p3_stay_event_appendonly trigger refuses any change to a
--      terminal row's status, stay_id, processed_at or review_code, and refuses to INSERT a row as anything
--      but PENDING with no result fields. There is no state in which a terminal event becomes pending again.
--      The function failed on the live appliance with exactly that: "terminal stay_events row is immutable".
--
--   2. THE CHECKOUT BOUNDARY MUST BE AN *APPLIED* GO EVENT for that exact stay, pinned as the stay's
--      application lineage (internal/checkout.deriveBoundary). A MANUAL_REVIEW event is not, and could not
--      become one without the engine applying it first -- which is the thing invariant 1 forbids.
--
-- Together those say something the reconciliation screen should state plainly rather than paper over: a
-- departure the engine could not place is resolved by the PMS sending a departure it CAN place, not by this
-- system deciding to try again. The operator's lever is the PMS record, and that is correct -- the PMS is the
-- source of truth for whether a guest has left, and a hotel system that could check a guest out on its own
-- re-reading of an old message would be asserting something it does not know.
--
-- So the action goes, and the LIST stays. The list was the valuable half: 397 distinct departures instead of
-- 12,271 rows, each classified by what evidence it is waiting for. Counting was never the part that needed a
-- button.
--
-- WHAT THIS DOES NOT TOUCH. No event, stay, entitlement or session is read or written here. The re-offer log
-- is dropped only because it is provably empty -- the function never completed once, on any appliance.

BEGIN;

DO $guard$
DECLARE
    n bigint;
BEGIN
    SELECT count(*) INTO n FROM iam_v2.stay_event_reoffers;
    IF n <> 0 THEN
        -- If a re-offer HAD ever succeeded somewhere, its record is evidence of a decision somebody made and
        -- must not be dropped to tidy up a schema. Refuse and let a human decide what to keep.
        RAISE EXCEPTION '0070: iam_v2.stay_event_reoffers holds % row(s); refusing to drop a non-empty audit log', n;
    END IF;
END;
$guard$;

DROP FUNCTION IF EXISTS iam_v2.pms_reoffer_stay_event(uuid, uuid, uuid, uuid, text, text, jsonb);

-- The view carried a reoffer_count, which is now both meaningless and a dependency on the table below.
-- It is dropped and recreated rather than REPLACEd, because CREATE OR REPLACE VIEW cannot remove a column.
-- Everything else about the view is unchanged, character for character.
DROP VIEW IF EXISTS iam_v2.pms_reconciliation_cases;

DROP TRIGGER IF EXISTS stay_event_reoffers_no_update ON iam_v2.stay_event_reoffers;
DROP TABLE IF EXISTS iam_v2.stay_event_reoffers;
DROP FUNCTION IF EXISTS iam_v2.stay_event_reoffers_append_only();

CREATE VIEW iam_v2.pms_reconciliation_cases AS
    WITH rt AS (
        SELECT tenant_id, site_id, pms_interface_id, published_resync_generation,
               sync_stage, last_complete_sync_at
          FROM iam_v2.pms_interface_runtime
    ),
    roster AS (
        SELECT se.tenant_id, se.site_id, se.pms_interface_id,
               btrim(COALESCE(se.payload->>'reservation','')) AS reservation,
               upper(btrim(COALESCE(se.payload->>'room',''))) AS room
          FROM iam_v2.stay_events se
          JOIN rt ON rt.tenant_id = se.tenant_id AND rt.site_id = se.site_id
                 AND rt.pms_interface_id = se.pms_interface_id
         WHERE se.admission_kind = 'RESYNC'
           AND se.resync_generation = rt.published_resync_generation
           AND rt.published_resync_generation > 0
           AND se.event_type IN ('GI','GC')
    ),
    ev AS (
        SELECT se.id, se.tenant_id, se.site_id, se.pms_interface_id,
               se.received_at, se.pms_timestamp_utc, se.clock_suspect, se.review_code,
               se.resync_generation,
               btrim(COALESCE(se.payload->>'reservation','')) AS reservation,
               upper(btrim(COALESCE(se.payload->>'room',''))) AS room
          FROM iam_v2.stay_events se
         WHERE se.event_type = 'GO' AND se.processing_status = 'MANUAL_REVIEW'
    ),
    grouped AS (
        SELECT tenant_id, site_id, pms_interface_id,
               CASE WHEN reservation <> '' THEN 'reservation:' || reservation
                    ELSE 'room:' || room END        AS case_key,
               max(reservation)                     AS reservation,
               max(room)                            AS room,
               count(*)::int                        AS repeat_count,
               count(DISTINCT resync_generation)::int AS generations,
               min(received_at)                     AS first_seen_at,
               max(received_at)                     AS last_seen_at,
               -- The event an operator would act on is the most recent copy: same content, newest admission.
               (array_agg(id ORDER BY received_at DESC, id DESC))[1]              AS latest_event_id,
               (array_agg(review_code ORDER BY received_at DESC, id DESC))[1]     AS latest_review_code,
               -- The instant the departure describes, trusted the same way the engine trusts it.
               (array_agg(CASE WHEN clock_suspect OR pms_timestamp_utc IS NULL
                               THEN received_at ELSE pms_timestamp_utc END
                          ORDER BY received_at DESC, id DESC))[1]                 AS event_at
          FROM ev
         GROUP BY tenant_id, site_id, pms_interface_id,
                  CASE WHEN reservation <> '' THEN 'reservation:' || reservation ELSE 'room:' || room END
    ),
    enriched AS (
        SELECT g.*,
               rt.published_resync_generation,
               rt.sync_stage,
               rt.last_complete_sync_at,
               (SELECT count(*)::int FROM iam_v2.stays s
                 WHERE s.tenant_id = g.tenant_id AND s.site_id = g.site_id
                   AND s.pms_interface_id = g.pms_interface_id
                   AND s.status = 'IN_HOUSE'
                   AND ((g.reservation <> '' AND s.external_reservation_id = g.reservation)
                        OR (g.reservation = '' AND s.normalized_room_number = g.room))) AS candidate_stays,
               (SELECT s.id FROM iam_v2.stays s
                 WHERE s.tenant_id = g.tenant_id AND s.site_id = g.site_id
                   AND s.pms_interface_id = g.pms_interface_id
                   AND s.status = 'IN_HOUSE'
                   AND ((g.reservation <> '' AND s.external_reservation_id = g.reservation)
                        OR (g.reservation = '' AND s.normalized_room_number = g.room))
                 ORDER BY s.id LIMIT 1)                                            AS candidate_stay_id,
               -- arrival, NOT occupancy_evidence_at. The latter records when the PMS last SPOKE about this
               -- occupancy and is restamped by every roster, so after a reconnect it reads as "now" for most
               -- of the property; see internal/stayengine/chronology.go for the measured version of that.
               (SELECT s.arrival FROM iam_v2.stays s
                 WHERE s.tenant_id = g.tenant_id AND s.site_id = g.site_id
                   AND s.pms_interface_id = g.pms_interface_id
                   AND s.status = 'IN_HOUSE'
                   AND ((g.reservation <> '' AND s.external_reservation_id = g.reservation)
                        OR (g.reservation = '' AND s.normalized_room_number = g.room))
                 ORDER BY s.id LIMIT 1)                                            AS candidate_arrival,
               EXISTS (SELECT 1 FROM roster ro
                        WHERE ro.tenant_id = g.tenant_id AND ro.site_id = g.site_id
                          AND ro.pms_interface_id = g.pms_interface_id
                          AND ((g.reservation <> '' AND ro.reservation = g.reservation)
                               OR (g.reservation = '' AND ro.room = g.room)))      AS roster_present
          FROM grouped g
          LEFT JOIN rt ON rt.tenant_id = g.tenant_id AND rt.site_id = g.site_id
                      AND rt.pms_interface_id = g.pms_interface_id
    )
    SELECT e.tenant_id, e.site_id, e.pms_interface_id, e.case_key,
           e.reservation, e.room, e.repeat_count, e.generations,
           e.first_seen_at, e.last_seen_at, e.event_at,
           e.latest_event_id, e.latest_review_code,
           e.candidate_stays, e.candidate_stay_id, e.candidate_arrival, e.roster_present,
           e.last_complete_sync_at,
           CASE
             WHEN e.candidate_stays = 0 AND e.reservation = ''       THEN 'SUPERSEDED_ROOM_EMPTY'
             WHEN e.candidate_stays = 0                              THEN 'NEEDS_PMS_EVIDENCE'
             WHEN e.candidate_stays > 1                              THEN 'ROOM_SHARED'
             WHEN e.reservation = '' AND e.candidate_arrival IS NOT NULL
              AND e.candidate_arrival > (e.event_at AT TIME ZONE 'UTC')::date
                                                                     THEN 'LATER_OCCUPANT'
             WHEN e.last_complete_sync_at IS NULL                    THEN 'NEEDS_PMS_EVIDENCE'
             WHEN e.roster_present                                   THEN 'ROSTER_CONTRADICTS'
             ELSE 'RESOLVABLE'
           END AS resolution_state
      FROM enriched e;

DO $own$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
        ALTER VIEW iam_v2.pms_reconciliation_cases OWNER TO iam_v2_owner;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
        GRANT SELECT ON iam_v2.pms_reconciliation_cases TO svc_edged;
    END IF;
END;
$own$;


-- ---------------------------------------------------------------------------------------------------------
-- A LATENT MIS-KEY IN iam_v2.pms_stays_past_departure, corrected while it is still latent.
--
-- The roster-membership test read:  ro.reservation = s.external_reservation_id
--                                OR (ro.reservation = '' AND ro.room = s.normalized_room_number)
--
-- The second branch keys on the ROSTER's reservation being empty. Every roster row this PMS sends carries a
-- reservation number -- 454 of 454, measured -- so that branch is dead, and a stay with no reservation id of
-- its own would be reported ABSENT even when its room is plainly on the roster.
--
-- It produces the right answer today only because every affected stay happens to carry a reservation. The
-- condition belongs on the STAY, which is the thing being looked up:
--
--   has a reservation  -> present iff the roster carries THAT reservation
--   has none           -> present iff the roster carries that room, which is the best available evidence
--
-- AND THE ROOM BRANCH STAYS NARROW ON PURPOSE. Matching a reservation-bearing stay by room would say
-- "present" because SOMEBODY is in that room -- which, on this appliance, is wrong for 315 of the 356
-- overstayed stays: the room is on the roster under a DIFFERENT reservation. A different guest in the same
-- room is not this stay, and treating it as one is the same room-is-not-an-identity error the departure
-- chronology rule exists to prevent.
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE VIEW iam_v2.pms_stays_past_departure AS
    WITH roster AS (
        SELECT se.tenant_id, se.site_id, se.pms_interface_id,
               btrim(COALESCE(se.payload->>'reservation','')) AS reservation,
               upper(btrim(COALESCE(se.payload->>'room',''))) AS room
          FROM iam_v2.stay_events se
          JOIN iam_v2.pms_interface_runtime r
            ON r.tenant_id = se.tenant_id AND r.site_id = se.site_id
           AND r.pms_interface_id = se.pms_interface_id
         WHERE se.admission_kind = 'RESYNC'
           AND se.resync_generation = r.published_resync_generation
           AND r.published_resync_generation > 0
           AND se.event_type IN ('GI','GC')
    )
    SELECT s.tenant_id, s.site_id, s.pms_interface_id, s.id AS stay_id,
           s.normalized_room_number AS room,
           s.external_reservation_id AS reservation,
           s.arrival, s.departure,
           (CURRENT_DATE - s.departure)::int AS days_past_departure,
           EXISTS (SELECT 1 FROM roster ro
                    WHERE ro.tenant_id = s.tenant_id AND ro.site_id = s.site_id
                      AND ro.pms_interface_id = s.pms_interface_id
                      AND ((COALESCE(s.external_reservation_id,'') <> ''
                            AND ro.reservation = s.external_reservation_id)
                        OR (COALESCE(s.external_reservation_id,'') = ''
                            AND ro.room = s.normalized_room_number))) AS roster_present,
           -- Said separately because "not listed" and "somebody else is in that room now" are different
           -- facts, and the second is the one that tells an operator the room has been re-let.
           EXISTS (SELECT 1 FROM roster ro
                    WHERE ro.tenant_id = s.tenant_id AND ro.site_id = s.site_id
                      AND ro.pms_interface_id = s.pms_interface_id
                      AND ro.room = s.normalized_room_number
                      AND ro.reservation IS DISTINCT FROM s.external_reservation_id) AS room_now_holds_another_stay
      FROM iam_v2.stays s
     WHERE s.status = 'IN_HOUSE' AND s.departure IS NOT NULL AND s.departure < CURRENT_DATE;

COMMENT ON VIEW iam_v2.pms_reconciliation_cases IS
  'One row per DISTINCT unresolved PMS departure, with the number of recorded copies beside it and a resolution state computed from current roster and occupancy evidence. READ-ONLY: a departure that went to review is resolved by the PMS sending one that can be applied, never by replaying the old one -- stay_events is one-way and a checkout boundary must be an APPLIED GO event. Collapses copies for counting; deletes and hides nothing.';

COMMIT;
