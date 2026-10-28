-- Restores the 0072 and 0123 bodies (without the admission_kind predicate).

BEGIN;

CREATE OR REPLACE FUNCTION iam_v2.pms_roster_of_generation(
    p_tenant uuid, p_site uuid, p_iface uuid, p_generation bigint
) RETURNS TABLE (reservation text)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $$
    SELECT DISTINCT btrim(e.payload->>'reservation')
      FROM iam_v2.stay_events e
     WHERE e.tenant_id = p_tenant AND e.site_id = p_site AND e.pms_interface_id = p_iface
       AND e.resync_generation = p_generation
       AND e.event_type IN ('GI','GC')
       AND btrim(COALESCE(e.payload->>'reservation','')) <> '';
$$;

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

COMMIT;
