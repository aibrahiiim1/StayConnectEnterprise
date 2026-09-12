-- A QUEUE THAT GAVE UP, AND A DEPARTURE NOBODY COULD PLACE.
--
-- Two unrelated backlogs, both of which had grown past the point where an operator could act on them, and
-- neither of which the product gave anybody a way to act on at all.
--
-- ONE — THE CLOUD SYNC QUEUE. public.sync_outbox retries a record twelve times with exponential backoff and
-- then sets dead = true. Nothing in the product ever clears that flag, and DrainOnce's SELECT filters
-- dead = false, so a record that exhausted its retries is never offered again. On the appliance this was
-- written for, that is 9 395 records at the HEAD of the sequence — the oldest telemetry there is — which
-- would be silently skipped the moment the link came up, leaving a contiguous gap at Central that neither
-- side would report. The screen called them "given up on" and offered no button.
--
-- The other half of the same table is the opposite problem: successfully delivered rows are never removed.
-- 76 MB and growing at 180 rows an hour, on an appliance whose disk is also the guest database.
--
-- So: a supported recovery path for exhausted records, a retention period for DELIVERED records that a hotel
-- administrator sets (30 days by default), and an accounting function that makes every record fall into
-- exactly one bucket so "recovery complete" can be checked rather than asserted. RETENTION DELETES DELIVERED
-- ROWS ONLY. Pending and exhausted records are never removed by a timer — a queue that cannot be delivered
-- must not be made to look empty by deleting it, and the growth concern is answered honestly on the screen
-- instead.
--
-- TWO — THE DEPARTURES. This PMS reports every checkout as a GO carrying a room and no reservation number, so
-- a departure that cannot be matched to exactly one in-house stay goes to MANUAL_REVIEW. Two things then
-- happen that together made the number on the dashboard meaningless:
--
--   * every reconnect restages the entire roster under a new resync generation, and an unapplied record
--     comes back with it — so ONE physical departure that nobody could place became 12 026 rows across 220
--     generations, counted once each;
--   * the dashboard counted rows, for all time, with no grouping and no window.
--
-- The rows are not wrong and are not touched here: the event history is the record of what the PMS actually
-- said, and it stays exactly as it is. What this adds is a view that collapses them into the DISTINCT CASES
-- an operator can work — one per departure, with the repeat count beside it — plus the two lists that make a
-- case decidable (rooms holding more than one current stay, and stays past their planned departure), and a
-- single audited operation that re-offers one event to the engine when fresh evidence has changed the answer.
--
-- RE-OFFERING IS NOT A SECOND CHECKOUT PATH. It puts the recorded event back in front of the same engine,
-- which applies the same resolution rules and the same Checkout Converter it would have used the first time.
-- The prior terminal state is copied into an append-only log before the row moves, so nothing is erased: the
-- mutable column is a processing state machine, and iam_v2.stay_event_reoffers is its history.
--
-- WHAT THIS DOES NOT DO. It closes no stay by itself, deletes no event, resolves nothing on a planned
-- departure date, and hides nothing from the count. A case that still has no authoritative answer stays in
-- the list and says which evidence is missing.

BEGIN;

-- =========================================================================================================
-- PART ONE — THE CLOUD SYNC QUEUE
-- =========================================================================================================

-- ---------------------------------------------------------------------------------------------------------
-- 1. How long delivered records are kept.
--
-- Site-scoped, typed, bounded, in the idiom iam_v2.site_guest_signin_protection established: real columns
-- with real CHECK bounds, a version that increments on every change, and absence of a row meaning the
-- approved default rather than "no retention". There is no enable flag, for the reason given there.
--
-- The unit is DAYS and it is in the column name, because "30" has been read as hours.
-- ---------------------------------------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS iam_v2.site_cloud_sync_settings (
    tenant_id               uuid   NOT NULL,
    site_id                 uuid   NOT NULL,
    delivered_retention_days integer NOT NULL DEFAULT 30,
    config_version          bigint NOT NULL DEFAULT 1,
    updated_at              timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, site_id),
    CONSTRAINT scs_retention_days_bounds
        CHECK (delivered_retention_days BETWEEN 1 AND 365),
    CONSTRAINT scs_version_positive CHECK (config_version >= 1)
);

COMMENT ON TABLE iam_v2.site_cloud_sync_settings IS
  'Per-site settings for reporting to the StayConnect cloud. Absence of a row means the approved defaults (delivered records kept 30 days), never "retention is off".';
COMMENT ON COLUMN iam_v2.site_cloud_sync_settings.delivered_retention_days IS
  'How many days a SUCCESSFULLY DELIVERED sync record is kept before it is removed, in days. Records still waiting, and records the appliance gave up on, are never removed by this setting.';

-- The change log. Append-only by trigger, and the only writer is the definer function below, so a change
-- cannot be made without the row that says who made it.
CREATE TABLE IF NOT EXISTS iam_v2.cloud_sync_settings_changes (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   uuid NOT NULL,
    site_id     uuid NOT NULL,
    changed_at  timestamptz NOT NULL DEFAULT now(),
    changed_by  text NOT NULL CHECK (length(btrim(changed_by)) > 0),
    reason      text CHECK (reason IS NULL OR length(reason) <= 500),
    old_delivered_retention_days integer,
    new_delivered_retention_days integer NOT NULL,
    new_config_version bigint NOT NULL
);

CREATE INDEX IF NOT EXISTS cloud_sync_settings_changes_recent_idx
    ON iam_v2.cloud_sync_settings_changes (tenant_id, site_id, changed_at DESC);

CREATE OR REPLACE FUNCTION iam_v2.cloud_sync_settings_changes_append_only()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'iam_v2.cloud_sync_settings_changes is append-only: % refused', TG_OP
        USING ERRCODE = 'restrict_violation';
END;
$$;

DROP TRIGGER IF EXISTS cloud_sync_settings_changes_no_update ON iam_v2.cloud_sync_settings_changes;
CREATE TRIGGER cloud_sync_settings_changes_no_update
    BEFORE UPDATE OR DELETE ON iam_v2.cloud_sync_settings_changes
    FOR EACH ROW EXECUTE FUNCTION iam_v2.cloud_sync_settings_changes_append_only();

-- Read. is_default distinguishes "never configured" from "configured to the same number", which is the
-- difference between a property that accepted the standard and one that chose it.
CREATE OR REPLACE FUNCTION iam_v2.cloud_sync_settings_get(p_tenant uuid, p_site uuid)
RETURNS TABLE (
    delivered_retention_days integer,
    config_version bigint,
    updated_at timestamptz,
    is_default boolean
)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $$
    SELECT COALESCE(s.delivered_retention_days, 30),
           COALESCE(s.config_version, 0),
           s.updated_at,
           (s.tenant_id IS NULL)
      FROM (SELECT p_tenant AS t, p_site AS s) k
      LEFT JOIN iam_v2.site_cloud_sync_settings s
             ON s.tenant_id = k.t AND s.site_id = k.s;
$$;

-- Write. Validates, takes a per-site advisory lock, upserts and writes the change row IN THE SAME
-- TRANSACTION. No runtime role holds UPDATE on the settings table or INSERT on the log, so the audit row is
-- mandatory by privilege and not by convention.
CREATE OR REPLACE FUNCTION iam_v2.cloud_sync_settings_set(
    p_tenant uuid, p_site uuid, p_days integer, p_operator text, p_reason text DEFAULT NULL
) RETURNS bigint
LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $$
DECLARE
    v_old integer;
    v_new_version bigint;
BEGIN
    IF p_operator IS NULL OR length(btrim(p_operator)) = 0 THEN
        RAISE EXCEPTION 'cloud sync settings: an operator identity is required'
            USING ERRCODE = 'invalid_parameter_value';
    END IF;
    IF p_days IS NULL OR p_days < 1 OR p_days > 365 THEN
        RAISE EXCEPTION 'delivered record retention must be between 1 and 365 days (got %)', p_days
            USING ERRCODE = 'check_violation';
    END IF;

    PERFORM pg_advisory_xact_lock(hashtext('cloud_sync_settings'), hashtext(p_site::text));

    SELECT s.delivered_retention_days INTO v_old
      FROM iam_v2.site_cloud_sync_settings s
     WHERE s.tenant_id = p_tenant AND s.site_id = p_site
       FOR UPDATE;

    INSERT INTO iam_v2.site_cloud_sync_settings AS s
           (tenant_id, site_id, delivered_retention_days, config_version, updated_at)
    VALUES (p_tenant, p_site, p_days, 1, now())
    ON CONFLICT (tenant_id, site_id) DO UPDATE
       SET delivered_retention_days = EXCLUDED.delivered_retention_days,
           config_version = s.config_version + 1,
           updated_at = now()
    RETURNING s.config_version INTO v_new_version;

    INSERT INTO iam_v2.cloud_sync_settings_changes
           (tenant_id, site_id, changed_by, reason,
            old_delivered_retention_days, new_delivered_retention_days, new_config_version)
    VALUES (p_tenant, p_site, btrim(p_operator), NULLIF(btrim(COALESCE(p_reason,'')), ''),
            v_old, p_days, v_new_version);

    RETURN v_new_version;
END;
$$;

-- ---------------------------------------------------------------------------------------------------------
-- 2. Recovering records the appliance gave up on.
--
-- The log is written first and is append-only, so a recovery cannot happen without the row that says who
-- asked for it, why, which sequence range moved and how old the oldest record was.
-- ---------------------------------------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.sync_outbox_recovery_log (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    requested_at  timestamptz NOT NULL DEFAULT now(),
    requested_by  text NOT NULL CHECK (length(btrim(requested_by)) > 0),
    reason        text NOT NULL CHECK (length(btrim(reason)) >= 3 AND length(reason) <= 500),
    rows_recovered integer NOT NULL CHECK (rows_recovered >= 0),
    seq_from      bigint,
    seq_to        bigint,
    oldest_created_at timestamptz,
    exhausted_remaining bigint NOT NULL CHECK (exhausted_remaining >= 0)
);

COMMENT ON TABLE public.sync_outbox_recovery_log IS
  'Append-only record of every time exhausted-retry sync records were returned to the queue. Payloads are never copied here.';

CREATE OR REPLACE FUNCTION public.sync_outbox_recovery_log_append_only()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'public.sync_outbox_recovery_log is append-only: % refused', TG_OP
        USING ERRCODE = 'restrict_violation';
END;
$$;

DROP TRIGGER IF EXISTS sync_outbox_recovery_log_no_update ON public.sync_outbox_recovery_log;
CREATE TRIGGER sync_outbox_recovery_log_no_update
    BEFORE UPDATE OR DELETE ON public.sync_outbox_recovery_log
    FOR EACH ROW EXECUTE FUNCTION public.sync_outbox_recovery_log_append_only();

-- Recover a BOUNDED batch of exhausted records, oldest sequence first.
--
-- Bounded on purpose. 9 395 records released at once would be drained by a loop that publishes 100 at a time
-- against a Central that is also serving every other appliance; and if something is wrong with the link, a
-- small batch discovers it cheaply. The caller repeats until exhausted_remaining reaches zero.
--
-- OLDEST FIRST, AND THAT MATTERS HERE. The exhausted records on this appliance are seq 1..9 395 — they are
-- the head of the sequence, older than everything still pending. Central's consumer keys on (appliance, seq)
-- and the appliance drains in seq order, so recovering them before the pending tail drains is what keeps the
-- delivered order the same as the recorded order.
--
-- The payload is not read, not copied and not modified. attempts is reset so the row gets a full retry
-- budget rather than dying again on its next failure; last_error is kept, because why it died the first time
-- is the most useful thing about it.
CREATE OR REPLACE FUNCTION public.sync_outbox_recover_exhausted(
    p_operator text, p_reason text, p_limit integer DEFAULT 1000
) RETURNS TABLE (rows_recovered integer, seq_from bigint, seq_to bigint, exhausted_remaining bigint)
LANGUAGE plpgsql SECURITY DEFINER SET search_path = public, pg_temp AS $$
DECLARE
    v_count integer := 0;
    v_from bigint;
    v_to bigint;
    v_oldest timestamptz;
    v_remaining bigint;
BEGIN
    IF p_operator IS NULL OR length(btrim(p_operator)) = 0 THEN
        RAISE EXCEPTION 'sync outbox recovery: an operator identity is required'
            USING ERRCODE = 'invalid_parameter_value';
    END IF;
    IF p_reason IS NULL OR length(btrim(p_reason)) < 3 THEN
        RAISE EXCEPTION 'sync outbox recovery: a reason of at least 3 characters is required'
            USING ERRCODE = 'invalid_parameter_value';
    END IF;
    IF p_limit IS NULL OR p_limit < 1 OR p_limit > 20000 THEN
        RAISE EXCEPTION 'sync outbox recovery: batch size must be between 1 and 20000 (got %)', p_limit
            USING ERRCODE = 'check_violation';
    END IF;

    -- One recovery at a time. Two concurrent callers would otherwise each release an overlapping batch and
    -- both report having moved it.
    PERFORM pg_advisory_xact_lock(hashtext('sync_outbox_recover'));

    WITH picked AS (
        SELECT o.seq
          FROM public.sync_outbox o
         WHERE o.sent_at IS NULL AND o.dead = true
         ORDER BY o.seq ASC
         LIMIT p_limit
         FOR UPDATE
    ), moved AS (
        UPDATE public.sync_outbox o
           SET dead = false, attempts = 0, next_attempt_at = now()
          FROM picked
         WHERE o.seq = picked.seq
        RETURNING o.seq, o.created_at
    )
    SELECT count(*)::integer, min(seq), max(seq), min(created_at)
      INTO v_count, v_from, v_to, v_oldest
      FROM moved;

    SELECT count(*) INTO v_remaining
      FROM public.sync_outbox o
     WHERE o.sent_at IS NULL AND o.dead = true;

    INSERT INTO public.sync_outbox_recovery_log
           (requested_by, reason, rows_recovered, seq_from, seq_to, oldest_created_at, exhausted_remaining)
    VALUES (btrim(p_operator), btrim(p_reason), v_count, v_from, v_to, v_oldest, v_remaining);

    RETURN QUERY SELECT v_count, v_from, v_to, v_remaining;
END;
$$;

-- Retention for DELIVERED records only. The WHERE clause names sent_at IS NOT NULL and nothing else: there is
-- no parameter, flag or code path in this function that can reach a record which has not been delivered.
CREATE OR REPLACE FUNCTION public.sync_outbox_prune_delivered(p_days integer)
RETURNS bigint
LANGUAGE plpgsql SECURITY DEFINER SET search_path = public, pg_temp AS $$
DECLARE
    v_deleted bigint;
BEGIN
    IF p_days IS NULL OR p_days < 1 OR p_days > 365 THEN
        RAISE EXCEPTION 'delivered record retention must be between 1 and 365 days (got %)', p_days
            USING ERRCODE = 'check_violation';
    END IF;
    WITH gone AS (
        DELETE FROM public.sync_outbox
         WHERE sent_at IS NOT NULL
           AND sent_at < now() - make_interval(days => p_days)
        RETURNING 1
    )
    SELECT count(*) INTO v_deleted FROM gone;
    RETURN v_deleted;
END;
$$;

-- Every record in exactly one bucket, plus the ages and the size. This exists so "the backlog is recovered"
-- can be CHECKED: delivered + pending + exhausted = total, and an operator or a report can say which of the
-- three a record ended up in rather than inferring it from a falling number.
CREATE OR REPLACE FUNCTION public.sync_outbox_accounting()
RETURNS TABLE (
    delivered bigint, pending bigint, exhausted bigint, total bigint,
    oldest_pending timestamptz, newest_created timestamptz,
    oldest_exhausted timestamptz, bytes bigint
)
-- plpgsql rather than sql, and not for style. A LANGUAGE sql body is fully resolved when the function is
-- CREATED, so it would refuse to be created anywhere public.sync_outbox is absent — which is every schema
-- fixture that builds iam_v2 alone, including the one the gates run these migrations against. A plpgsql body
-- resolves at call time, so the migration applies on an iam_v2-only schema and the function works wherever
-- the queue actually exists. The same reasoning already applies to the two functions above it.
LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = public, pg_temp AS $$
BEGIN
    RETURN QUERY
    SELECT count(*) FILTER (WHERE o.sent_at IS NOT NULL),
           count(*) FILTER (WHERE o.sent_at IS NULL AND o.dead = false),
           count(*) FILTER (WHERE o.sent_at IS NULL AND o.dead = true),
           count(*),
           min(o.created_at) FILTER (WHERE o.sent_at IS NULL AND o.dead = false),
           max(o.created_at),
           min(o.created_at) FILTER (WHERE o.sent_at IS NULL AND o.dead = true),
           pg_total_relation_size('public.sync_outbox')
      FROM public.sync_outbox o;
END;
$$;

-- =========================================================================================================
-- PART TWO — THE DEPARTURES
-- =========================================================================================================

-- ---------------------------------------------------------------------------------------------------------
-- 3. Re-offering a recorded event to the engine.
--
-- The append-only log carries the terminal state the event had BEFORE it was re-offered, so moving the
-- processing_status column back to PENDING loses nothing: the history moves into the log, and the column
-- goes back to being what it is — where this event currently sits in the pipeline.
-- ---------------------------------------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS iam_v2.stay_event_reoffers (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id      uuid NOT NULL,
    site_id        uuid NOT NULL,
    pms_interface_id uuid NOT NULL,
    stay_event_id  uuid NOT NULL,
    requested_at   timestamptz NOT NULL DEFAULT now(),
    requested_by   text NOT NULL CHECK (length(btrim(requested_by)) > 0),
    reason         text NOT NULL CHECK (length(btrim(reason)) >= 3 AND length(reason) <= 500),
    prior_processing_status text NOT NULL,
    prior_review_code       text,
    prior_processed_at      timestamptz,
    -- The evidence the operator was acting on, captured at the moment of the decision so a later reader can
    -- see what was true then rather than what is true now. Bounded, and carries no guest name.
    evidence       jsonb NOT NULL DEFAULT '{}'::jsonb,
    CONSTRAINT ser_evidence_is_object CHECK (jsonb_typeof(evidence) = 'object')
);

CREATE INDEX IF NOT EXISTS stay_event_reoffers_event_idx
    ON iam_v2.stay_event_reoffers (stay_event_id, requested_at DESC);
CREATE INDEX IF NOT EXISTS stay_event_reoffers_scope_idx
    ON iam_v2.stay_event_reoffers (tenant_id, site_id, requested_at DESC);

COMMENT ON TABLE iam_v2.stay_event_reoffers IS
  'Append-only record of every PMS event returned to the ingestion engine for re-evaluation, with the terminal state it held before and the evidence the operator acted on. Carries no guest name.';

CREATE OR REPLACE FUNCTION iam_v2.stay_event_reoffers_append_only()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'iam_v2.stay_event_reoffers is append-only: % refused', TG_OP
        USING ERRCODE = 'restrict_violation';
END;
$$;

DROP TRIGGER IF EXISTS stay_event_reoffers_no_update ON iam_v2.stay_event_reoffers;
CREATE TRIGGER stay_event_reoffers_no_update
    BEFORE UPDATE OR DELETE ON iam_v2.stay_event_reoffers
    FOR EACH ROW EXECUTE FUNCTION iam_v2.stay_event_reoffers_append_only();

-- Re-offer exactly one event.
--
-- ONLY a MANUAL_REVIEW event may be re-offered. An APPLIED event has already changed a stay and re-running it
-- would be a second application; a SKIPPED_DUPLICATE was already answered; a PENDING one is in the queue
-- already. The engine decides the outcome — this function has no opinion about what the answer should be and
-- writes nothing to iam_v2.stays.
CREATE OR REPLACE FUNCTION iam_v2.pms_reoffer_stay_event(
    p_tenant uuid, p_site uuid, p_iface uuid, p_event uuid,
    p_operator text, p_reason text, p_evidence jsonb DEFAULT '{}'::jsonb
) RETURNS boolean
LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $$
DECLARE
    v_status text;
    v_code   text;
    v_at     timestamptz;
BEGIN
    IF p_operator IS NULL OR length(btrim(p_operator)) = 0 THEN
        RAISE EXCEPTION 'reoffer: an operator identity is required'
            USING ERRCODE = 'invalid_parameter_value';
    END IF;
    IF p_reason IS NULL OR length(btrim(p_reason)) < 3 THEN
        RAISE EXCEPTION 'reoffer: a reason of at least 3 characters is required'
            USING ERRCODE = 'invalid_parameter_value';
    END IF;

    SELECT se.processing_status, se.review_code, se.processed_at
      INTO v_status, v_code, v_at
      FROM iam_v2.stay_events se
     WHERE se.id = p_event AND se.tenant_id = p_tenant
       AND se.site_id = p_site AND se.pms_interface_id = p_iface
       FOR UPDATE;

    IF NOT FOUND THEN
        RETURN false;
    END IF;
    IF v_status <> 'MANUAL_REVIEW' THEN
        RETURN false;
    END IF;

    INSERT INTO iam_v2.stay_event_reoffers
           (tenant_id, site_id, pms_interface_id, stay_event_id, requested_by, reason,
            prior_processing_status, prior_review_code, prior_processed_at, evidence)
    VALUES (p_tenant, p_site, p_iface, p_event, btrim(p_operator), btrim(p_reason),
            v_status, v_code, v_at, COALESCE(p_evidence, '{}'::jsonb));

    UPDATE iam_v2.stay_events
       SET processing_status = 'PENDING', review_code = NULL, processed_at = NULL
     WHERE id = p_event;

    RETURN true;
END;
$$;

-- ---------------------------------------------------------------------------------------------------------
-- 4. The operator's three lists.
--
-- All three are VIEWS over facts that already exist. Nothing here stores a derived case, because a stored
-- case is a second thing to keep true: the moment the roster changes, a stored row is stale and a view is not.
-- ---------------------------------------------------------------------------------------------------------

-- Rooms currently holding more than one in-house stay. SHARED OCCUPANCY IS ORDINARY AND LEGAL — this list is
-- not a duplicate report, and is titled accordingly on the screen. It exists because it is the fact that
-- makes a room-keyed departure undecidable, and the operator needs to see it beside the case.
CREATE OR REPLACE VIEW iam_v2.pms_rooms_multi_occupancy AS
    SELECT s.tenant_id, s.site_id, s.pms_interface_id,
           s.normalized_room_number AS room,
           count(*)::int              AS stays_in_room,
           min(s.arrival)             AS earliest_arrival,
           max(s.departure)           AS latest_planned_departure,
           array_agg(s.id ORDER BY s.arrival NULLS LAST, s.id) AS stay_ids
      FROM iam_v2.stays s
     WHERE s.status = 'IN_HOUSE' AND COALESCE(s.normalized_room_number,'') <> ''
     GROUP BY s.tenant_id, s.site_id, s.pms_interface_id, s.normalized_room_number
    HAVING count(*) > 1;

-- In-house stays whose planned departure date has passed.
--
-- A PLANNED DEPARTURE DATE IS NOT A CHECKOUT. This view says only "the PMS expected them to leave and our
-- mirror still has them here", and the roster_present column beside it is the fact that decides what that
-- means: still on the PMS's own in-house roster (so they extended, or the PMS has not processed the
-- departure) versus absent from it (so the mirror is behind). Neither column closes anything.
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
                      AND (ro.reservation = s.external_reservation_id
                           OR (ro.reservation = '' AND ro.room = s.normalized_room_number))) AS roster_present
      FROM iam_v2.stays s
     WHERE s.status = 'IN_HOUSE' AND s.departure IS NOT NULL AND s.departure < CURRENT_DATE;

-- THE CASES. One row per distinct unresolved departure, however many times it has been restaged.
--
-- The key is the reservation number when the event carries one and the room when it does not, which is the
-- same identity the engine resolves by — so a case and the thing that failed to resolve are the same object.
-- repeat_count is the number of recorded rows behind the case and is shown, not hidden: 32 copies of one
-- departure is a fact about the feed that an operator should be able to see.
--
-- resolution_state is computed from CURRENT evidence and is the whole point of the view:
--
--   SUPERSEDED_ROOM_EMPTY  the room holds nobody now. Whoever this departure was about has gone; there is no
--                          stay to close and nothing to correct. NOT "resolved" — nobody proved this event
--                          was applied — but nothing is outstanding either.
--   ROOM_SHARED            more than one stay in the room. Undecidable from a room number, by construction.
--   LATER_OCCUPANT         one stay, but it began AFTER this departure was raised. Applying it would check
--                          out somebody who is resident. This is the case the engine now refuses on its own.
--   ROSTER_CONTRADICTS     the candidate is still on the PMS's own current in-house roster. Fresh
--                          authoritative evidence says they are here, so the old departure is not applied.
--   RESOLVABLE             one candidate, it predates the event, and the latest complete roster does NOT
--                          contain it. Two independent facts agreeing that this stay has departed.
--   NEEDS_PMS_EVIDENCE     anything else, including every case seen while no complete roster is available.
CREATE OR REPLACE VIEW iam_v2.pms_reconciliation_cases AS
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
           (SELECT count(*)::int FROM iam_v2.stay_event_reoffers r
             WHERE r.stay_event_id = e.latest_event_id)                            AS reoffer_count,
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

COMMENT ON VIEW iam_v2.pms_reconciliation_cases IS
  'One row per DISTINCT unresolved PMS departure, with the number of recorded copies beside it and a resolution state computed from current roster and occupancy evidence. Collapses copies for counting; deletes and hides nothing.';

-- =========================================================================================================
-- 5. Grants.
--
-- Written here so the migration is self-contained and asserted below; the DURABLE copies live in
-- deploy/gatep/*.sql, because the first Gate-P reconcile after this replaces whatever a migration granted.
-- =========================================================================================================
REVOKE ALL ON iam_v2.site_cloud_sync_settings FROM PUBLIC;
REVOKE ALL ON iam_v2.cloud_sync_settings_changes FROM PUBLIC;
REVOKE ALL ON iam_v2.stay_event_reoffers FROM PUBLIC;
REVOKE ALL ON public.sync_outbox_recovery_log FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.cloud_sync_settings_get(uuid, uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.cloud_sync_settings_set(uuid, uuid, integer, text, text) FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.pms_reoffer_stay_event(uuid, uuid, uuid, uuid, text, text, jsonb) FROM PUBLIC;
REVOKE ALL ON FUNCTION public.sync_outbox_recover_exhausted(text, text, integer) FROM PUBLIC;
REVOKE ALL ON FUNCTION public.sync_outbox_prune_delivered(integer) FROM PUBLIC;
REVOKE ALL ON FUNCTION public.sync_outbox_accounting() FROM PUBLIC;

DO $grant$
DECLARE
    r text;
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
        -- The operator surface reads the lists and asks for the two operator actions. It gets EXECUTE and
        -- SELECT, and no write privilege on any table involved: the audit rows are written inside the
        -- definer functions, so an edged with a SQL injection still cannot change a setting without logging
        -- it, re-offer an event without logging it, or recover the queue without logging it.
        GRANT SELECT ON iam_v2.cloud_sync_settings_changes TO svc_edged;
        GRANT SELECT ON iam_v2.stay_event_reoffers TO svc_edged;
        GRANT SELECT ON public.sync_outbox_recovery_log TO svc_edged;
        GRANT SELECT ON iam_v2.pms_reconciliation_cases TO svc_edged;
        GRANT SELECT ON iam_v2.pms_rooms_multi_occupancy TO svc_edged;
        GRANT SELECT ON iam_v2.pms_stays_past_departure TO svc_edged;
        GRANT EXECUTE ON FUNCTION iam_v2.cloud_sync_settings_get(uuid, uuid) TO svc_edged;
        GRANT EXECUTE ON FUNCTION iam_v2.cloud_sync_settings_set(uuid, uuid, integer, text, text) TO svc_edged;
        GRANT EXECUTE ON FUNCTION iam_v2.pms_reoffer_stay_event(uuid, uuid, uuid, uuid, text, text, jsonb) TO svc_edged;
        GRANT EXECUTE ON FUNCTION public.sync_outbox_recover_exhausted(text, text, integer) TO svc_edged;
        GRANT EXECUTE ON FUNCTION public.sync_outbox_accounting() TO svc_edged;
    END IF;

    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_scd') THEN
        -- scd owns the queue. It reads the retention setting and applies it; it has no operator action.
        GRANT EXECUTE ON FUNCTION iam_v2.cloud_sync_settings_get(uuid, uuid) TO svc_scd;
        GRANT EXECUTE ON FUNCTION public.sync_outbox_prune_delivered(integer) TO svc_scd;
        GRANT EXECUTE ON FUNCTION public.sync_outbox_accounting() TO svc_scd;
    END IF;

    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_pmsd') THEN
        -- pmsd needs nothing new: a re-offered event is an ordinary PENDING row to it, which is the point.
        NULL;
    END IF;

    -- Named and refused explicitly rather than left to the default, so a reader can see the decision.
    FOREACH r IN ARRAY ARRAY['svc_pmsd','svc_acctd','svc_netd','svc_portald'] LOOP
        IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = r) THEN
            EXECUTE format('REVOKE ALL ON iam_v2.site_cloud_sync_settings FROM %I', r);
            EXECUTE format('REVOKE ALL ON iam_v2.cloud_sync_settings_changes FROM %I', r);
            EXECUTE format('REVOKE ALL ON iam_v2.stay_event_reoffers FROM %I', r);
            EXECUTE format('REVOKE ALL ON public.sync_outbox_recovery_log FROM %I', r);
            EXECUTE format('REVOKE ALL ON FUNCTION public.sync_outbox_recover_exhausted(text, text, integer) FROM %I', r);
            EXECUTE format('REVOKE ALL ON FUNCTION iam_v2.pms_reoffer_stay_event(uuid, uuid, uuid, uuid, text, text, jsonb) FROM %I', r);
        END IF;
    END LOOP;
END;
$grant$;

DO $own$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
        ALTER TABLE iam_v2.site_cloud_sync_settings   OWNER TO iam_v2_owner;
        ALTER TABLE iam_v2.cloud_sync_settings_changes OWNER TO iam_v2_owner;
        ALTER TABLE iam_v2.stay_event_reoffers        OWNER TO iam_v2_owner;
        ALTER VIEW  iam_v2.pms_reconciliation_cases   OWNER TO iam_v2_owner;
        ALTER VIEW  iam_v2.pms_rooms_multi_occupancy  OWNER TO iam_v2_owner;
        ALTER VIEW  iam_v2.pms_stays_past_departure   OWNER TO iam_v2_owner;
        ALTER FUNCTION iam_v2.cloud_sync_settings_get(uuid, uuid) OWNER TO iam_v2_owner;
        ALTER FUNCTION iam_v2.cloud_sync_settings_set(uuid, uuid, integer, text, text) OWNER TO iam_v2_owner;
        ALTER FUNCTION iam_v2.pms_reoffer_stay_event(uuid, uuid, uuid, uuid, text, text, jsonb) OWNER TO iam_v2_owner;
        ALTER FUNCTION iam_v2.cloud_sync_settings_changes_append_only() OWNER TO iam_v2_owner;
        ALTER FUNCTION iam_v2.stay_event_reoffers_append_only() OWNER TO iam_v2_owner;
    END IF;
END;
$own$;

-- =========================================================================================================
-- 6. Assert the privilege shape this migration exists to create.
--
-- A grant that drifts is a control that is not there. These raise rather than warn.
-- =========================================================================================================
DO $verify$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
        IF has_table_privilege('svc_edged','iam_v2.site_cloud_sync_settings','UPDATE')
        OR has_table_privilege('svc_edged','iam_v2.site_cloud_sync_settings','INSERT') THEN
            RAISE EXCEPTION '0069: svc_edged can write the cloud sync settings directly; the change log would be optional';
        END IF;
        IF has_table_privilege('svc_edged','iam_v2.cloud_sync_settings_changes','INSERT') THEN
            RAISE EXCEPTION '0069: svc_edged can write the settings change log directly';
        END IF;
        IF has_table_privilege('svc_edged','iam_v2.stay_event_reoffers','INSERT') THEN
            RAISE EXCEPTION '0069: svc_edged can write the re-offer log directly';
        END IF;
        IF has_table_privilege('svc_edged','public.sync_outbox_recovery_log','INSERT') THEN
            RAISE EXCEPTION '0069: svc_edged can write the recovery log directly';
        END IF;
        IF has_table_privilege('svc_edged','iam_v2.stay_events','UPDATE') THEN
            RAISE EXCEPTION '0069: svc_edged can move a PMS event''s processing state without the re-offer log';
        END IF;
    END IF;
END;
$verify$;

COMMIT;
