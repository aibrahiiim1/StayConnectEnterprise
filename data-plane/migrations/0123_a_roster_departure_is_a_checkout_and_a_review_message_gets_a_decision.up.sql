-- 0123 -- A ROSTER DEPARTURE IS A CHECKOUT, AND A REVIEW MESSAGE GETS A DECISION
-- (audit remediation stream 6, Hotel / PMS: H-2, H-5, H-8, H-13, H-15 / D-D).
--
-- 1. H-2. iam_v2.pms_roster_reconcile used to close a stay the complete published roster no longer lists by
--    setting status = CHECKED_OUT directly. That skipped the checkout conversion Phase-0 invariant 10 and
--    contract section 7.1 require at EVERY checkout: the live stay entitlement must become TERMINATED(CHECKOUT)
--    or be converted to a grace package, its device authorizations closed and its sessions ended. On PRE-LIVE
--    three roster-closed stays kept ACTIVE entitlements (audit T45).
--
--    The function now does what a PMS checkout does: for each stay it would close it queues ONE durable
--    checkout (GO) stay event, admitted under the published resync generation the roster came from, carrying
--    the reservation and the roster boundary as its timestamp. The Stay Engine applies it like any other GO,
--    and the Checkout Converter (internal/checkout) runs in the SAME transaction -- one path for every
--    departure, whatever told us about it. The stay is therefore closed a few seconds later, by the engine,
--    instead of inside this function, and stays_closed now counts the checkouts queued by the run.
--
--    A stay that already has a checkout waiting (from the PMS or an earlier run) gets no second one, and a run
--    that is repeated on the same generation queues nothing new (the event identity is per generation and per
--    stay). The queued events are excluded from the generation's boundary so a later dry run of the same
--    generation measures the same roster.
--
-- 2. H-13. A DRY RUN is now free of side effects other than its own run-ledger row: it no longer establishes
--    or grows the room inventory. Only an APPLY (pmsd, on a complete published generation) may change the
--    building. The console no longer runs a dry run when the page opens (edged); it is an explicit action.
--
-- 3. H-5. iam_v2.pms_reconciliation_settings_set_checked: the versioned writer the console uses. It refuses a
--    stale expected version and a missing reason, then calls the existing writer, which appends the change row.
--
-- 4. H-8. iam_v2.pms_interface_has_undrained_postings: whether a DRAINING interface still has room charges to
--    finish (the set the decommission gate refuses on). pmsd keeps a DRAINING connection's worker running while
--    it does, so wind down lets queued charges complete -- contract section 10, "outbox drains".
--
-- 5. H-15 / D-D. A PMS message waiting in MANUAL_REVIEW can now be decided, one message at a time:
--      ACKNOWLEDGE  the operator records that it needs no action, with a reason;
--      LINK         the operator records which stay it concerns, chosen from the stays it is RE-MATCHABLE to
--                   (same connection; same reservation, or -- for a message without one -- a stay that occupied
--                   that room when the message arrived).
--    A decision is a row in the existing append-only iam_v2.pms_case_resolutions (one per message), so every
--    count that already reads "unanswered" -- the dashboard, the connection row, the Property-management card --
--    stops counting it at once. Neither action changes a stay, an entitlement or the message itself: a checkout
--    is still only ever an APPLIED GO event.
--
--    And an AUTOMATIC EXPIRY setting (docs/OPERATIONAL_SETTINGS_POLICY.md): off by default; when on, a message
--    that arrived AFTER the setting was switched on and has waited the configured number of days (1-365) is
--    recorded as EXPIRED_UNDECIDED. Messages that arrived before it was switched on are never expired by it --
--    the existing backlog is never mass-updated. Versioned, reason required, append-only change log.
--
-- Nothing here deletes or rewrites a row.

BEGIN;

-- ---------------------------------------------------------------------------------------------------------
-- 1 + 2. Roster reconciliation through the checkout path; side-effect-free dry run.
-- ---------------------------------------------------------------------------------------------------------
DROP FUNCTION IF EXISTS iam_v2.pms_roster_reconcile(uuid, uuid, uuid, bigint, text, boolean, text);

CREATE OR REPLACE FUNCTION iam_v2.pms_roster_reconcile(
    p_tenant uuid, p_site uuid, p_iface uuid, p_generation bigint,
    p_operator text, p_apply boolean DEFAULT false, p_reason text DEFAULT NULL
) RETURNS TABLE (outcome text, roster_size integer, mirror_in_house integer,
                 absent_from_roster integer, stays_closed integer,
                 rooms_enumerated integer, rooms_expected integer, protected integer, run_id uuid)
LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $$
DECLARE
    v_floor int; v_cap int; v_tol int; v_look int;
    v_roster int; v_mirror int; v_absent int; v_closed int := 0; v_protected int := 0;
    v_rooms int; v_expected int := 0; v_missing int := 0; v_conflicts int := 0;
    v_have boolean := false; v_swept text[]; v_known text[]; v_added text[];
    v_published bigint; v_boundary timestamptz; v_run uuid; v_outcome text;
    v_sync text; v_cont text; v_scope int; v_ver bigint; v_norm int;
BEGIN
    IF p_operator IS NULL OR length(btrim(p_operator)) = 0 THEN
        RAISE EXCEPTION 'roster reconciliation: an operator identity is required'
            USING ERRCODE = 'invalid_parameter_value';
    END IF;
    PERFORM pg_advisory_xact_lock(hashtext('pms_roster_reconcile'), hashtext(p_iface::text));

    SELECT s.roster_trust_min, s.max_close_per_run, s.inventory_tolerance, s.inventory_lookback
      INTO v_floor, v_cap, v_tol, v_look
      FROM iam_v2.pms_reconciliation_settings_get(p_tenant, p_site) s;

    SELECT count(*) INTO v_scope FROM iam_v2.pms_interfaces i
     WHERE i.id = p_iface AND i.tenant_id = p_tenant AND i.site_id = p_site;

    SELECT r.published_resync_generation, r.sync_status, r.continuity_status,
           COALESCE((SELECT rv.normalization_version FROM iam_v2.pms_interface_revisions rv
                      WHERE rv.id = r.pinned_revision_id), 1)
      INTO v_published, v_sync, v_cont, v_norm
      FROM iam_v2.pms_interface_runtime r
     WHERE r.tenant_id = p_tenant AND r.site_id = p_site AND r.pms_interface_id = p_iface;

    -- The boundary is the roster's own: the latest record the PMS sent in this generation. The checkouts this
    -- function queues are admitted under the same generation and are excluded here, so re-running a generation
    -- measures the same roster it measured the first time.
    SELECT max(e.received_at) INTO v_boundary
      FROM iam_v2.stay_events e
     WHERE e.tenant_id = p_tenant AND e.site_id = p_site AND e.pms_interface_id = p_iface
       AND e.resync_generation = p_generation
       AND e.external_event_identity NOT LIKE 'ROSTER_RECONCILIATION:%';

    SELECT count(*) INTO v_roster
      FROM iam_v2.pms_roster_of_generation(p_tenant, p_site, p_iface, p_generation);

    SELECT count(*) INTO v_mirror FROM iam_v2.stays s
     WHERE s.tenant_id = p_tenant AND s.site_id = p_site AND s.pms_interface_id = p_iface
       AND s.status = 'IN_HOUSE';

    SELECT true, c.rooms_named, c.conflicting_rooms, c.rooms
      INTO v_have, v_rooms, v_conflicts, v_swept
      FROM iam_v2.pms_resync_coverage c
     WHERE c.tenant_id = p_tenant AND c.site_id = p_site AND c.pms_interface_id = p_iface
       AND c.resync_generation = p_generation;

    SELECT i.rooms INTO v_known FROM iam_v2.pms_room_inventory i
     WHERE i.tenant_id = p_tenant AND i.site_id = p_site AND i.pms_interface_id = p_iface;

    -- BOOTSTRAP. The first uncontradicted sweep establishes the building -- on an APPLY only. A dry run
    -- describes what a run would do and changes nothing it describes (H-13): opening a screen, or pressing
    -- "Run dry run", must never be what established the inventory every later run is judged against.
    IF p_apply AND v_known IS NULL AND v_have AND v_conflicts = 0 AND COALESCE(array_length(v_swept,1),0) > 0 THEN
        INSERT INTO iam_v2.pms_room_inventory
               (tenant_id, site_id, pms_interface_id, rooms, established_from, config_version)
        VALUES (p_tenant, p_site, p_iface, v_swept, p_generation, 1)
        RETURNING rooms, config_version INTO v_known, v_ver;
        INSERT INTO iam_v2.pms_room_inventory_changes
               (tenant_id, site_id, pms_interface_id, changed_by, reason, change_kind,
                rooms_before, rooms_after, added, new_config_version)
        VALUES (p_tenant, p_site, p_iface, btrim(p_operator),
                'established from the first uncontradicted sweep', 'ESTABLISHED',
                0, COALESCE(array_length(v_swept,1),0), v_swept, v_ver);
    END IF;

    v_expected := COALESCE(array_length(v_known,1),0);
    IF v_have AND v_known IS NOT NULL THEN
        SELECT count(*) INTO v_missing
          FROM (SELECT unnest(v_known) EXCEPT SELECT unnest(COALESCE(v_swept,'{}'))) m;
    END IF;

    DROP TABLE IF EXISTS _absent;
    CREATE TEMP TABLE _absent ON COMMIT DROP AS
    SELECT s.id, s.external_reservation_id, s.normalized_room_number
      FROM iam_v2.stays s
     WHERE s.tenant_id = p_tenant AND s.site_id = p_site AND s.pms_interface_id = p_iface
       AND s.status = 'IN_HOUSE'
       AND btrim(COALESCE(s.external_reservation_id,'')) <> ''
       AND (v_boundary IS NULL OR s.arrival IS NULL OR s.arrival <= v_boundary)
       AND NOT EXISTS (
           SELECT 1 FROM iam_v2.pms_roster_of_generation(p_tenant, p_site, p_iface, p_generation) g
            WHERE g.reservation = btrim(s.external_reservation_id))
       AND NOT EXISTS (
           SELECT 1 FROM iam_v2.stay_events e2
            WHERE e2.tenant_id = p_tenant AND e2.site_id = p_site AND e2.pms_interface_id = p_iface
              AND v_boundary IS NOT NULL AND e2.received_at > v_boundary
              AND e2.external_event_identity NOT LIKE 'ROSTER_RECONCILIATION:%'
              AND (e2.stay_id = s.id
                   OR btrim(COALESCE(e2.payload->>'reservation','')) = btrim(s.external_reservation_id)));
    SELECT count(*) INTO v_absent FROM _absent;

    SELECT count(*) INTO v_protected
      FROM iam_v2.stays s
     WHERE s.tenant_id = p_tenant AND s.site_id = p_site AND s.pms_interface_id = p_iface
       AND s.status = 'IN_HOUSE'
       AND btrim(COALESCE(s.external_reservation_id,'')) <> ''
       AND NOT EXISTS (
           SELECT 1 FROM iam_v2.pms_roster_of_generation(p_tenant, p_site, p_iface, p_generation) g
            WHERE g.reservation = btrim(s.external_reservation_id))
       AND NOT EXISTS (SELECT 1 FROM _absent a WHERE a.id = s.id);

    IF v_scope <> 1 THEN
        v_outcome := 'REFUSED_SCOPE_MISMATCH';
    ELSIF v_published IS NULL OR v_published < p_generation THEN
        v_outcome := 'REFUSED_GENERATION_UNPUBLISHED';
    ELSIF v_published <> p_generation THEN
        v_outcome := 'REFUSED_GENERATION_NOT_LATEST';
    ELSIF v_sync IS DISTINCT FROM 'IN_SYNC' OR v_cont IS DISTINCT FROM 'CONTINUOUS' THEN
        v_outcome := 'REFUSED_LINK_NOT_HEALTHY';
    ELSIF NOT v_have THEN
        v_outcome := 'REFUSED_NO_COVERAGE_EVIDENCE';
    ELSIF v_conflicts > 0 THEN
        v_outcome := 'REFUSED_CONFLICTING_OBSERVATIONS';
    ELSIF v_expected > 0 AND v_missing > v_tol THEN
        v_outcome := 'REFUSED_ROSTER_INCOMPLETE';
    ELSIF v_roster < v_floor THEN
        v_outcome := 'REFUSED_ROSTER_TOO_SMALL';
    ELSIF v_absent > v_cap THEN
        v_outcome := 'REFUSED_CAP_EXCEEDED';
    ELSE
        v_outcome := 'COMPLETED';
    END IF;

    INSERT INTO iam_v2.pms_roster_reconciliation_runs
        (tenant_id, site_id, pms_interface_id, resync_generation, mode, outcome,
         roster_size, mirror_in_house, absent_from_roster, stays_closed, boundary_at, run_by, reason,
         rooms_enumerated, rooms_expected, protected_by_newer_events)
    VALUES (p_tenant, p_site, p_iface, p_generation,
            CASE WHEN p_apply THEN 'APPLY' ELSE 'DRY_RUN' END, v_outcome,
            v_roster, v_mirror, v_absent, 0, v_boundary, btrim(p_operator),
            NULLIF(btrim(COALESCE(p_reason,'')),''), COALESCE(v_rooms,0), v_expected, v_protected)
    RETURNING id INTO v_run;

    IF v_outcome = 'COMPLETED' AND p_apply THEN
        -- GROWTH, and only on an apply (see BOOTSTRAP above).
        SELECT COALESCE(ARRAY(SELECT unnest(COALESCE(v_swept,'{}')) EXCEPT SELECT unnest(v_known)), '{}')
          INTO v_added;
        IF v_known IS NOT NULL AND COALESCE(array_length(v_added,1),0) > 0 THEN
            UPDATE iam_v2.pms_room_inventory
               SET rooms = ARRAY(SELECT DISTINCT unnest(rooms || v_added) ORDER BY 1),
                   updated_at = now(), config_version = config_version + 1
             WHERE tenant_id = p_tenant AND site_id = p_site AND pms_interface_id = p_iface
            RETURNING config_version INTO v_ver;
            INSERT INTO iam_v2.pms_room_inventory_changes
                   (tenant_id, site_id, pms_interface_id, changed_by, reason, change_kind,
                    rooms_before, rooms_after, added, new_config_version)
            VALUES (p_tenant, p_site, p_iface, btrim(p_operator),
                    'a complete sweep named rooms not previously known', 'GREW',
                    v_expected, v_expected + array_length(v_added,1), v_added, v_ver);
        END IF;

        -- THE CHECKOUT, AS A PMS CHECKOUT. One durable GO per stay, admitted under the published generation
        -- the roster came from. The Stay Engine applies it and runs the Checkout Converter in the same
        -- transaction, so the entitlement, devices, sessions, grace and audit follow exactly the rules a
        -- checkout the PMS sent follows. A stay with a checkout already waiting gets no second one.
        INSERT INTO iam_v2.stay_events
               (tenant_id, site_id, pms_interface_id, external_event_identity, event_type,
                pms_timestamp_utc, normalization_version, clock_suspect, payload,
                admission_kind, admission_runtime_generation, resync_generation)
        SELECT p_tenant, p_site, p_iface,
               'ROSTER_RECONCILIATION:' || p_generation::text || ':' || a.id::text, 'GO',
               v_boundary, v_norm, false,
               jsonb_build_object('reservation', a.external_reservation_id,
                                  'room', COALESCE(a.normalized_room_number, ''),
                                  'source', 'ROSTER_RECONCILIATION',
                                  'run_id', v_run::text,
                                  'resync_generation', p_generation),
               'RESYNC', 0, p_generation
          FROM _absent a
         WHERE NOT EXISTS (
               SELECT 1 FROM iam_v2.stay_events q
                WHERE q.tenant_id = p_tenant AND q.site_id = p_site AND q.pms_interface_id = p_iface
                  AND q.processing_status = 'PENDING' AND q.event_type = 'GO'
                  AND btrim(COALESCE(q.payload->>'reservation','')) = btrim(a.external_reservation_id))
        ON CONFLICT DO NOTHING;
        GET DIAGNOSTICS v_closed = ROW_COUNT;

        INSERT INTO iam_v2.pms_case_resolutions
            (tenant_id, site_id, pms_interface_id, stay_event_id, stay_id, disposition,
             evidence_kind, evidence_ref, run_id, resolved_by, note)
        SELECT e.tenant_id, e.site_id, e.pms_interface_id, e.id, e.stay_id,
               'DEPARTED_CONFIRMED_BY_ROSTER', 'PUBLISHED_ROSTER_GENERATION',
               'generation ' || p_generation::text, v_run, btrim(p_operator), NULL
          FROM iam_v2.stay_events e
          JOIN _absent a ON a.id = e.stay_id
         WHERE e.processing_status = 'MANUAL_REVIEW'
        ON CONFLICT (stay_event_id) DO NOTHING;

        UPDATE iam_v2.pms_roster_reconciliation_runs SET stays_closed = v_closed WHERE id = v_run;
    END IF;

    RETURN QUERY SELECT v_outcome, v_roster, v_mirror, v_absent, v_closed,
                        COALESCE(v_rooms,0), v_expected, v_protected, v_run;
END;
$$;

COMMENT ON FUNCTION iam_v2.pms_roster_reconcile(uuid,uuid,uuid,bigint,text,boolean,text) IS
  'Reconciles the mirror against a complete published roster. APPLY queues one checkout (GO) stay event per absent stay, which the Stay Engine applies through the Checkout Converter (0123); stays_closed counts the checkouts queued. A dry run changes nothing but its own run row.';

REVOKE ALL ON FUNCTION iam_v2.pms_roster_reconcile(uuid,uuid,uuid,bigint,text,boolean,text) FROM PUBLIC;

-- ---------------------------------------------------------------------------------------------------------
-- 3. H-5. The versioned settings writer.
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION iam_v2.pms_reconciliation_settings_set_checked(
    p_tenant uuid, p_site uuid, p_expected_version bigint,
    p_floor integer, p_cap integer, p_tolerance integer, p_lookback integer,
    p_operator text, p_reason text
) RETURNS bigint
LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $$
DECLARE v_cur bigint;
BEGIN
    IF p_operator IS NULL OR length(btrim(p_operator)) = 0 THEN
        RAISE EXCEPTION 'RECON_SETTINGS_ACTOR: an operator identity is required' USING ERRCODE = 'invalid_parameter_value';
    END IF;
    IF p_reason IS NULL OR length(btrim(p_reason)) < 3 OR length(p_reason) > 500 THEN
        RAISE EXCEPTION 'RECON_SETTINGS_REASON: a reason of 3 to 500 characters is required' USING ERRCODE = 'invalid_parameter_value';
    END IF;
    IF p_floor IS NULL OR p_floor < 1 OR p_floor > 100000 THEN
        RAISE EXCEPTION 'RECON_SETTINGS_BOUNDS: the minimum roster size must be between 1 and 100000' USING ERRCODE = 'check_violation';
    END IF;
    IF p_cap IS NULL OR p_cap < 1 OR p_cap > 100000 THEN
        RAISE EXCEPTION 'RECON_SETTINGS_BOUNDS: the most stays one run may close must be between 1 and 100000' USING ERRCODE = 'check_violation';
    END IF;
    IF p_tolerance IS NULL OR p_tolerance < 0 OR p_tolerance > 10000 THEN
        RAISE EXCEPTION 'RECON_SETTINGS_BOUNDS: the room tolerance must be between 0 and 10000' USING ERRCODE = 'check_violation';
    END IF;
    IF p_lookback IS NULL OR p_lookback < 1 OR p_lookback > 1000 THEN
        RAISE EXCEPTION 'RECON_SETTINGS_BOUNDS: the inventory look-back must be between 1 and 1000 sweeps' USING ERRCODE = 'check_violation';
    END IF;
    PERFORM pg_advisory_xact_lock(hashtext('pms_recon_settings'), hashtext(p_site::text));
    SELECT g.config_version INTO v_cur FROM iam_v2.pms_reconciliation_settings_get(p_tenant, p_site) g;
    IF p_expected_version IS NULL OR v_cur IS DISTINCT FROM p_expected_version THEN
        RAISE EXCEPTION 'RECON_SETTINGS_VERSION_CONFLICT: the settings changed (now version %); reload and try again', v_cur
            USING ERRCODE = 'serialization_failure';
    END IF;
    RETURN iam_v2.pms_reconciliation_settings_set(p_tenant, p_site, p_floor, p_cap, p_operator, btrim(p_reason),
                                                   p_tolerance, p_lookback);
END;
$$;
REVOKE ALL ON FUNCTION iam_v2.pms_reconciliation_settings_set_checked(uuid,uuid,bigint,integer,integer,integer,integer,text,text) FROM PUBLIC;

-- ---------------------------------------------------------------------------------------------------------
-- 4. H-8. Does a DRAINING connection still have room charges to finish?
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION iam_v2.pms_interface_has_undrained_postings(p_tenant uuid, p_site uuid, p_iface uuid)
RETURNS boolean
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $$
    -- The same set the decommission gate (p4_interface_decommission_gate) refuses on: work that was authorized
    -- and has not reached a terminal answer. While any exists, wind down keeps the link to the PMS.
    SELECT EXISTS (SELECT 1 FROM iam_v2.posting_outbox o
                    WHERE o.tenant_id = p_tenant AND o.site_id = p_site AND o.pms_interface_id = p_iface
                      AND o.state IN ('QUEUED','IN_FLIGHT','HELD_RECOVERY'))
        OR EXISTS (SELECT 1 FROM iam_v2.posting_attempts a
                    WHERE a.tenant_id = p_tenant AND a.site_id = p_site AND a.pms_interface_id = p_iface
                      AND a.outcome IN ('SENDING','UNKNOWN'));
$$;
REVOKE ALL ON FUNCTION iam_v2.pms_interface_has_undrained_postings(uuid,uuid,uuid) FROM PUBLIC;

-- ---------------------------------------------------------------------------------------------------------
-- 5. H-15 / D-D. Per-message decisions, and the automatic expiry setting.
-- ---------------------------------------------------------------------------------------------------------
-- The disposition vocabulary widens; nothing existing is rewritten. NOT VALID is not needed (every existing
-- value is still in the list), so the constraint is fully validated.
ALTER TABLE iam_v2.pms_case_resolutions DROP CONSTRAINT IF EXISTS pms_case_resolutions_disposition_check;
ALTER TABLE iam_v2.pms_case_resolutions ADD CONSTRAINT pms_case_resolutions_disposition_check CHECK (disposition = ANY (ARRAY[
    'DEPARTED_CONFIRMED_BY_ROSTER','NOT_A_DEPARTURE_ROSTER_SNAPSHOT','ALREADY_CLOSED','NEEDS_PMS_EVIDENCE',
    'ACCEPTED_STARTUP_DATA_GAP','ACKNOWLEDGED_BY_OPERATOR','LINKED_TO_STAY','EXPIRED_UNDECIDED']));
ALTER TABLE iam_v2.pms_case_resolutions DROP CONSTRAINT IF EXISTS pms_case_resolutions_evidence_kind_check;
ALTER TABLE iam_v2.pms_case_resolutions ADD CONSTRAINT pms_case_resolutions_evidence_kind_check CHECK (evidence_kind = ANY (ARRAY[
    'PUBLISHED_ROSTER_GENERATION','ADMISSION_KIND','STAY_STATE','NONE','OPERATOR_DECISION','EXPIRY_SETTING']));

-- Which stays a review message can be linked to. Ids and operational fields only -- the names are on the stay
-- detail, behind its own permission.
CREATE OR REPLACE FUNCTION iam_v2.pms_review_link_candidates(p_tenant uuid, p_site uuid, p_event uuid)
RETURNS TABLE (stay_id uuid, matched_by text, room text, status text, arrival date, departure date)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $$
    WITH ev AS (
        SELECT e.tenant_id, e.site_id, e.pms_interface_id, e.received_at,
               btrim(COALESCE(e.payload->>'reservation','')) AS res,
               upper(btrim(COALESCE(e.payload->>'room',''))) AS room
          FROM iam_v2.stay_events e
         WHERE e.id = p_event AND e.tenant_id = p_tenant AND e.site_id = p_site
           AND e.processing_status = 'MANUAL_REVIEW')
    SELECT s.id, 'RESERVATION', s.normalized_room_number, s.status, s.arrival::date, s.departure::date
      FROM ev JOIN iam_v2.stays s
        ON s.tenant_id = ev.tenant_id AND s.site_id = ev.site_id AND s.pms_interface_id = ev.pms_interface_id
     WHERE ev.res <> '' AND btrim(s.external_reservation_id) = ev.res
    UNION ALL
    -- A message that names no reservation is about a ROOM. The stays it can be about are the ones that were in
    -- that room when it arrived: arrived by then, and not checked out before it.
    SELECT s.id, 'ROOM', s.normalized_room_number, s.status, s.arrival::date, s.departure::date
      FROM ev JOIN iam_v2.stays s
        ON s.tenant_id = ev.tenant_id AND s.site_id = ev.site_id AND s.pms_interface_id = ev.pms_interface_id
     WHERE ev.res = '' AND ev.room <> '' AND s.normalized_room_number = ev.room
       AND (s.arrival IS NULL OR s.arrival <= ev.received_at)
       AND (s.effective_checkout_at IS NULL OR s.effective_checkout_at >= ev.received_at)
     ORDER BY 2, 4, 5 DESC
     LIMIT 50;
$$;
REVOKE ALL ON FUNCTION iam_v2.pms_review_link_candidates(uuid,uuid,uuid) FROM PUBLIC;

CREATE OR REPLACE FUNCTION iam_v2.pms_review_decide(
    p_tenant uuid, p_site uuid, p_event uuid, p_action text, p_stay uuid, p_operator text, p_reason text
) RETURNS uuid
LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $$
DECLARE v_iface uuid; v_status text; v_res uuid; v_match text;
BEGIN
    IF p_operator IS NULL OR length(btrim(p_operator)) = 0 THEN
        RAISE EXCEPTION 'REVIEW_ACTOR: an operator identity is required' USING ERRCODE = 'invalid_parameter_value';
    END IF;
    IF p_reason IS NULL OR length(btrim(p_reason)) < 3 OR length(p_reason) > 500 THEN
        RAISE EXCEPTION 'REVIEW_REASON: a reason of 3 to 500 characters is required' USING ERRCODE = 'invalid_parameter_value';
    END IF;
    IF p_action NOT IN ('ACKNOWLEDGE','LINK') THEN
        RAISE EXCEPTION 'REVIEW_ACTION: ACKNOWLEDGE or LINK' USING ERRCODE = 'invalid_parameter_value';
    END IF;

    SELECT e.pms_interface_id, e.processing_status INTO v_iface, v_status
      FROM iam_v2.stay_events e
     WHERE e.id = p_event AND e.tenant_id = p_tenant AND e.site_id = p_site
       FOR SHARE;
    IF v_iface IS NULL THEN
        RAISE EXCEPTION 'REVIEW_NOT_FOUND: no such PMS message on this property' USING ERRCODE = 'no_data_found';
    END IF;
    IF v_status <> 'MANUAL_REVIEW' THEN
        RAISE EXCEPTION 'REVIEW_NOT_WAITING: this message is % and needs no decision', v_status USING ERRCODE = 'check_violation';
    END IF;
    IF EXISTS (SELECT 1 FROM iam_v2.pms_case_resolutions x WHERE x.stay_event_id = p_event) THEN
        RAISE EXCEPTION 'REVIEW_ALREADY_DECIDED: this message already carries a decision' USING ERRCODE = 'unique_violation';
    END IF;

    IF p_action = 'ACKNOWLEDGE' THEN
        INSERT INTO iam_v2.pms_case_resolutions
               (tenant_id, site_id, pms_interface_id, stay_event_id, stay_id, disposition,
                evidence_kind, evidence_ref, resolved_by, note)
        VALUES (p_tenant, p_site, v_iface, p_event, NULL, 'ACKNOWLEDGED_BY_OPERATOR',
                'OPERATOR_DECISION', NULL, btrim(p_operator), btrim(p_reason))
        RETURNING id INTO v_res;
        RETURN v_res;
    END IF;

    IF p_stay IS NULL THEN
        RAISE EXCEPTION 'REVIEW_STAY_REQUIRED: choose the stay this message is about' USING ERRCODE = 'invalid_parameter_value';
    END IF;
    SELECT c.matched_by INTO v_match
      FROM iam_v2.pms_review_link_candidates(p_tenant, p_site, p_event) c WHERE c.stay_id = p_stay;
    IF v_match IS NULL THEN
        RAISE EXCEPTION 'REVIEW_NOT_MATCHABLE: this message cannot be matched to that stay (different connection, reservation or room)'
            USING ERRCODE = 'check_violation';
    END IF;
    INSERT INTO iam_v2.pms_case_resolutions
           (tenant_id, site_id, pms_interface_id, stay_event_id, stay_id, disposition,
            evidence_kind, evidence_ref, resolved_by, note)
    VALUES (p_tenant, p_site, v_iface, p_event, p_stay, 'LINKED_TO_STAY',
            'OPERATOR_DECISION', 'matched by ' || lower(v_match), btrim(p_operator), btrim(p_reason))
    RETURNING id INTO v_res;
    RETURN v_res;
END;
$$;
REVOKE ALL ON FUNCTION iam_v2.pms_review_decide(uuid,uuid,uuid,text,uuid,text,text) FROM PUBLIC;

-- The expiry setting: one row per site, read and written only through the functions below.
CREATE TABLE IF NOT EXISTS iam_v2.pms_review_expiry_settings (
    tenant_id          uuid NOT NULL,
    site_id            uuid NOT NULL,
    enabled            boolean NOT NULL DEFAULT false,
    expire_after_days  integer NOT NULL DEFAULT 30,
    -- Messages that arrived before this instant are never expired by the setting. It moves to now() each time
    -- the setting is switched on, so switching it off and on again does not reach back over the gap.
    applies_from       timestamptz,
    config_version     bigint NOT NULL DEFAULT 1,
    updated_at         timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, site_id),
    CONSTRAINT pres_days_bounded CHECK (expire_after_days BETWEEN 1 AND 365),
    CONSTRAINT pres_on_has_start CHECK (NOT enabled OR applies_from IS NOT NULL),
    CONSTRAINT pres_version_positive CHECK (config_version >= 1)
);

CREATE TABLE IF NOT EXISTS iam_v2.pms_review_expiry_settings_changes (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id          uuid NOT NULL,
    site_id            uuid NOT NULL,
    changed_at         timestamptz NOT NULL DEFAULT now(),
    changed_by         text NOT NULL CHECK (length(btrim(changed_by)) > 0),
    reason             text NOT NULL CHECK (length(btrim(reason)) BETWEEN 3 AND 500),
    old_values         jsonb,
    new_values         jsonb NOT NULL,
    new_config_version bigint NOT NULL
);
CREATE INDEX IF NOT EXISTS pres_changes_scope_idx
    ON iam_v2.pms_review_expiry_settings_changes (tenant_id, site_id, changed_at DESC);

CREATE OR REPLACE FUNCTION iam_v2.pms_review_expiry_settings_append_only() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'iam_v2.pms_review_expiry_settings_changes is append-only: % refused', TG_OP
        USING ERRCODE = 'restrict_violation';
END;
$$;
DROP TRIGGER IF EXISTS pms_review_expiry_settings_changes_no_update ON iam_v2.pms_review_expiry_settings_changes;
CREATE TRIGGER pms_review_expiry_settings_changes_no_update
    BEFORE UPDATE OR DELETE ON iam_v2.pms_review_expiry_settings_changes
    FOR EACH ROW EXECUTE FUNCTION iam_v2.pms_review_expiry_settings_append_only();

CREATE OR REPLACE FUNCTION iam_v2.pms_review_expiry_settings_get(p_tenant uuid, p_site uuid)
RETURNS TABLE (enabled boolean, expire_after_days integer, applies_from timestamptz,
               config_version bigint, is_default boolean)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $$
    SELECT COALESCE(s.enabled, false), COALESCE(s.expire_after_days, 30), s.applies_from,
           COALESCE(s.config_version, 0), (s.tenant_id IS NULL)
      FROM (SELECT p_tenant AS t, p_site AS si) k
      LEFT JOIN iam_v2.pms_review_expiry_settings s ON s.tenant_id = k.t AND s.site_id = k.si;
$$;
REVOKE ALL ON FUNCTION iam_v2.pms_review_expiry_settings_get(uuid,uuid) FROM PUBLIC;

CREATE OR REPLACE FUNCTION iam_v2.pms_review_expiry_settings_set(
    p_tenant uuid, p_site uuid, p_expected_version bigint, p_enabled boolean, p_days integer,
    p_operator text, p_reason text
) RETURNS bigint
LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $$
DECLARE v_old jsonb; v_cur bigint; v_was boolean; v_from timestamptz; v_ver bigint;
BEGIN
    IF p_operator IS NULL OR length(btrim(p_operator)) = 0 THEN
        RAISE EXCEPTION 'REVIEW_EXPIRY_ACTOR: an operator identity is required' USING ERRCODE = 'invalid_parameter_value';
    END IF;
    IF p_reason IS NULL OR length(btrim(p_reason)) < 3 OR length(p_reason) > 500 THEN
        RAISE EXCEPTION 'REVIEW_EXPIRY_REASON: a reason of 3 to 500 characters is required' USING ERRCODE = 'invalid_parameter_value';
    END IF;
    IF p_enabled IS NULL OR p_days IS NULL OR p_days < 1 OR p_days > 365 THEN
        RAISE EXCEPTION 'REVIEW_EXPIRY_BOUNDS: the waiting period must be between 1 and 365 days' USING ERRCODE = 'check_violation';
    END IF;
    PERFORM pg_advisory_xact_lock(hashtext('pms_review_expiry'), hashtext(p_site::text));
    SELECT to_jsonb(s), s.config_version, s.enabled, s.applies_from INTO v_old, v_cur, v_was, v_from
      FROM iam_v2.pms_review_expiry_settings s
     WHERE s.tenant_id = p_tenant AND s.site_id = p_site FOR UPDATE;
    IF p_expected_version IS NULL OR COALESCE(v_cur, 0) <> p_expected_version THEN
        RAISE EXCEPTION 'REVIEW_EXPIRY_VERSION_CONFLICT: the setting changed (now version %); reload and try again', COALESCE(v_cur,0)
            USING ERRCODE = 'serialization_failure';
    END IF;
    -- Switching ON starts the clock now; staying on keeps it; switching off clears it.
    IF p_enabled AND NOT COALESCE(v_was, false) THEN
        v_from := now();
    ELSIF NOT p_enabled THEN
        v_from := NULL;
    END IF;
    INSERT INTO iam_v2.pms_review_expiry_settings AS s
           (tenant_id, site_id, enabled, expire_after_days, applies_from, config_version, updated_at)
    VALUES (p_tenant, p_site, p_enabled, p_days, v_from, 1, now())
    ON CONFLICT (tenant_id, site_id) DO UPDATE
       SET enabled = EXCLUDED.enabled, expire_after_days = EXCLUDED.expire_after_days,
           applies_from = EXCLUDED.applies_from,
           config_version = s.config_version + 1, updated_at = now()
    RETURNING s.config_version INTO v_ver;
    INSERT INTO iam_v2.pms_review_expiry_settings_changes
           (tenant_id, site_id, changed_by, reason, old_values, new_values, new_config_version)
    VALUES (p_tenant, p_site, btrim(p_operator), btrim(p_reason), v_old,
            jsonb_build_object('enabled', p_enabled, 'expire_after_days', p_days, 'applies_from', v_from), v_ver);
    RETURN v_ver;
END;
$$;
REVOKE ALL ON FUNCTION iam_v2.pms_review_expiry_settings_set(uuid,uuid,bigint,boolean,integer,text,text) FROM PUBLIC;

-- The sweep pmsd runs. Bounded per call; idempotent (one disposition per message, ON CONFLICT DO NOTHING);
-- never touches a message that arrived before the setting was switched on.
CREATE OR REPLACE FUNCTION iam_v2.pms_review_expire_due(p_tenant uuid, p_site uuid, p_limit integer DEFAULT 500)
RETURNS integer
LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $$
DECLARE v_on boolean; v_days int; v_from timestamptz; v_ver bigint; v_n int := 0;
BEGIN
    SELECT s.enabled, s.expire_after_days, s.applies_from, s.config_version INTO v_on, v_days, v_from, v_ver
      FROM iam_v2.pms_review_expiry_settings s WHERE s.tenant_id = p_tenant AND s.site_id = p_site;
    IF NOT COALESCE(v_on, false) OR v_from IS NULL THEN
        RETURN 0;
    END IF;
    INSERT INTO iam_v2.pms_case_resolutions
           (tenant_id, site_id, pms_interface_id, stay_event_id, stay_id, disposition,
            evidence_kind, evidence_ref, resolved_by, note)
    SELECT e.tenant_id, e.site_id, e.pms_interface_id, e.id, NULL, 'EXPIRED_UNDECIDED',
           'EXPIRY_SETTING', 'setting version ' || v_ver::text || ', after ' || v_days::text || ' days',
           'automatic expiry', NULL
      FROM iam_v2.stay_events e
     WHERE e.tenant_id = p_tenant AND e.site_id = p_site
       AND e.processing_status = 'MANUAL_REVIEW'
       AND e.received_at >= v_from
       AND e.received_at < now() - make_interval(days => v_days)
       AND NOT EXISTS (SELECT 1 FROM iam_v2.pms_case_resolutions x WHERE x.stay_event_id = e.id)
     ORDER BY e.received_at
     LIMIT GREATEST(1, LEAST(COALESCE(p_limit, 500), 5000))
    ON CONFLICT (stay_event_id) DO NOTHING;
    GET DIAGNOSTICS v_n = ROW_COUNT;
    RETURN v_n;
END;
$$;
REVOKE ALL ON FUNCTION iam_v2.pms_review_expire_due(uuid,uuid,integer) FROM PUBLIC;

-- ---------------------------------------------------------------------------------------------------------
-- Ownership and least privilege, mirrored in deploy/gatep/svc-edged-phase345-admin-grants.sql and
-- deploy/gatep/svc-pmsd-iamv2-connector-grants.sql.
-- ---------------------------------------------------------------------------------------------------------
DO $own$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
    EXECUTE 'ALTER FUNCTION iam_v2.pms_roster_reconcile(uuid,uuid,uuid,bigint,text,boolean,text) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.pms_reconciliation_settings_set_checked(uuid,uuid,bigint,integer,integer,integer,integer,text,text) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.pms_interface_has_undrained_postings(uuid,uuid,uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.pms_review_link_candidates(uuid,uuid,uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.pms_review_decide(uuid,uuid,uuid,text,uuid,text,text) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.pms_review_expiry_settings_get(uuid,uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.pms_review_expiry_settings_set(uuid,uuid,bigint,boolean,integer,text,text) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.pms_review_expire_due(uuid,uuid,integer) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER TABLE iam_v2.pms_review_expiry_settings OWNER TO iam_v2_owner';
    EXECUTE 'ALTER TABLE iam_v2.pms_review_expiry_settings_changes OWNER TO iam_v2_owner';
  END IF;
END $own$;

DO $g$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_pmsd') THEN
    GRANT EXECUTE ON FUNCTION iam_v2.pms_roster_reconcile(uuid,uuid,uuid,bigint,text,boolean,text) TO svc_pmsd;
    GRANT EXECUTE ON FUNCTION iam_v2.pms_interface_has_undrained_postings(uuid,uuid,uuid) TO svc_pmsd;
    GRANT EXECUTE ON FUNCTION iam_v2.pms_review_expire_due(uuid,uuid,integer) TO svc_pmsd;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
    -- edged only ever passes p_apply = false (a dry run); the apply caller is pmsd.
    GRANT EXECUTE ON FUNCTION iam_v2.pms_roster_reconcile(uuid,uuid,uuid,bigint,text,boolean,text) TO svc_edged;
    GRANT EXECUTE ON FUNCTION iam_v2.pms_reconciliation_settings_set_checked(uuid,uuid,bigint,integer,integer,integer,integer,text,text) TO svc_edged;
    GRANT EXECUTE ON FUNCTION iam_v2.pms_review_link_candidates(uuid,uuid,uuid) TO svc_edged;
    GRANT EXECUTE ON FUNCTION iam_v2.pms_review_decide(uuid,uuid,uuid,text,uuid,text,text) TO svc_edged;
    GRANT EXECUTE ON FUNCTION iam_v2.pms_review_expiry_settings_get(uuid,uuid) TO svc_edged;
    GRANT EXECUTE ON FUNCTION iam_v2.pms_review_expiry_settings_set(uuid,uuid,bigint,boolean,integer,text,text) TO svc_edged;
    GRANT SELECT ON iam_v2.pms_review_expiry_settings, iam_v2.pms_review_expiry_settings_changes TO svc_edged;
  END IF;
END $g$;

COMMIT;
