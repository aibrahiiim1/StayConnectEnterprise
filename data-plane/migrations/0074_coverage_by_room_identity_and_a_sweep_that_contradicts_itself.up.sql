-- COVERAGE BY ROOM IDENTITY, AND A SWEEP THAT CONTRADICTS ITSELF.
--
-- 0073 recorded how MANY rooms a sweep named. That closed the gap it was written for, and left two it was
-- not:
--
--   1. A COUNT IS NOT A SET. A sweep naming 587 rooms passes a count test even if they are a DIFFERENT 587 --
--      a wing swapped for a wing, a renumbering, a feed that answered for the wrong property. The count says
--      the building is the right SIZE, never that it is the right BUILDING.
--
--   2. A SWEEP CAN CONTRADICT ITSELF. The same room reported occupied and empty inside one generation is not
--      a building anything can reason about. Under a count it is invisible: one room, two observations, and
--      the totals still add up. It must not be possible to close a guest's stay on that.
--
-- So coverage stores the room IDENTITIES, completeness is set difference against the inventory recent sweeps
-- have described, and a generation carrying any contradiction refuses outright.
--
-- WHY THE IDENTITIES ARE SAFE TO STORE. A room number is not guest data: it identifies a space, not a person,
-- and it is already in every record this system handles. What is NOT stored here is who was in it -- there is
-- no reservation, no name, no stay, no event. One row describes one sweep of a building.

BEGIN;

ALTER TABLE iam_v2.pms_resync_coverage
    ADD COLUMN IF NOT EXISTS rooms            text[]  NOT NULL DEFAULT '{}',
    ADD COLUMN IF NOT EXISTS conflicting_rooms integer NOT NULL DEFAULT 0;

ALTER TABLE iam_v2.pms_resync_coverage
    DROP CONSTRAINT IF EXISTS prc_conflicts_sane;
ALTER TABLE iam_v2.pms_resync_coverage
    ADD CONSTRAINT prc_conflicts_sane CHECK (conflicting_rooms >= 0);

COMMENT ON COLUMN iam_v2.pms_resync_coverage.rooms IS
  'The room identities this sweep named, normalized and sorted. Completeness is a set difference against recent sweeps, not a count. Rooms are spaces, not people: no reservation, name, stay or event is stored here.';
COMMENT ON COLUMN iam_v2.pms_resync_coverage.conflicting_rooms IS
  'Rooms this sweep reported BOTH occupied and empty. Any value above zero makes the generation unusable for reconciliation.';

CREATE OR REPLACE FUNCTION iam_v2.pms_record_resync_coverage(
    p_tenant uuid, p_site uuid, p_iface uuid, p_generation bigint,
    p_rooms text[], p_roster integer, p_vacant integer, p_conflicts integer DEFAULT 0
) RETURNS void
LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $$
BEGIN
    INSERT INTO iam_v2.pms_resync_coverage AS c
        (tenant_id, site_id, pms_interface_id, resync_generation,
         rooms_named, roster_records, vacant_rooms, rooms, conflicting_rooms)
    VALUES (p_tenant, p_site, p_iface, p_generation,
            COALESCE(array_length(p_rooms, 1), 0), p_roster, p_vacant,
            COALESCE(p_rooms, '{}'), COALESCE(p_conflicts, 0))
    ON CONFLICT (tenant_id, site_id, pms_interface_id, resync_generation) DO UPDATE
       SET rooms_named       = COALESCE(array_length(EXCLUDED.rooms, 1), 0),
           roster_records    = EXCLUDED.roster_records,
           vacant_rooms      = EXCLUDED.vacant_rooms,
           rooms             = EXCLUDED.rooms,
           conflicting_rooms = EXCLUDED.conflicting_rooms,
           observed_at       = now();

    -- Bounded history. One row per sweep and this property resyncs constantly, so without this the table
    -- grows for ever to answer a question that only ever looks at recent generations.
    DELETE FROM iam_v2.pms_resync_coverage d
     WHERE d.tenant_id = p_tenant AND d.site_id = p_site AND d.pms_interface_id = p_iface
       AND d.resync_generation <= p_generation - 500;
END;
$$;

-- The inventory recent sweeps agree the property HAS, as a set of identities.
CREATE OR REPLACE FUNCTION iam_v2.pms_known_room_inventory(
    p_tenant uuid, p_site uuid, p_iface uuid, p_generation bigint, p_lookback integer
) RETURNS TABLE (room text)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $$
    SELECT DISTINCT r
      FROM iam_v2.pms_resync_coverage c, LATERAL unnest(c.rooms) r
     WHERE c.tenant_id = p_tenant AND c.site_id = p_site AND c.pms_interface_id = p_iface
       AND c.conflicting_rooms = 0
       AND c.resync_generation <= p_generation
       AND c.resync_generation > p_generation - p_lookback;
$$;

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
    v_have boolean := false;
    v_published bigint; v_boundary timestamptz; v_run uuid; v_outcome text;
    v_sync text; v_cont text; v_scope int;
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

    SELECT r.published_resync_generation, r.sync_status, r.continuity_status
      INTO v_published, v_sync, v_cont
      FROM iam_v2.pms_interface_runtime r
     WHERE r.tenant_id = p_tenant AND r.site_id = p_site AND r.pms_interface_id = p_iface;

    SELECT max(e.received_at) INTO v_boundary
      FROM iam_v2.stay_events e
     WHERE e.tenant_id = p_tenant AND e.site_id = p_site AND e.pms_interface_id = p_iface
       AND e.resync_generation = p_generation;

    SELECT count(*) INTO v_roster
      FROM iam_v2.pms_roster_of_generation(p_tenant, p_site, p_iface, p_generation);

    SELECT count(*) INTO v_mirror FROM iam_v2.stays s
     WHERE s.tenant_id = p_tenant AND s.site_id = p_site AND s.pms_interface_id = p_iface
       AND s.status = 'IN_HOUSE';

    SELECT true, c.rooms_named, c.conflicting_rooms INTO v_have, v_rooms, v_conflicts
      FROM iam_v2.pms_resync_coverage c
     WHERE c.tenant_id = p_tenant AND c.site_id = p_site AND c.pms_interface_id = p_iface
       AND c.resync_generation = p_generation;

    -- COMPLETENESS BY IDENTITY. Not "did it name enough rooms" but "which rooms of the building did it fail
    -- to name". A wing swapped for a wing keeps the count and fails this.
    IF v_have THEN
        SELECT count(*) INTO v_expected
          FROM iam_v2.pms_known_room_inventory(p_tenant, p_site, p_iface, p_generation, v_look);
        SELECT count(*) INTO v_missing FROM (
            SELECT room FROM iam_v2.pms_known_room_inventory(p_tenant, p_site, p_iface, p_generation, v_look)
            EXCEPT
            SELECT r FROM iam_v2.pms_resync_coverage c, LATERAL unnest(c.rooms) r
             WHERE c.tenant_id = p_tenant AND c.site_id = p_site AND c.pms_interface_id = p_iface
               AND c.resync_generation = p_generation) m;
    END IF;

    CREATE TEMP TABLE _absent ON COMMIT DROP AS
    SELECT s.id, s.external_reservation_id
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
        -- The sweep disagreed with itself about the state of a room. Nothing here is safe to act on, and no
        -- operator can adjudicate it -- only the next uncontradicted sweep can.
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
        UPDATE iam_v2.stays s
           SET status = 'CHECKED_OUT', effective_checkout_at = v_boundary
          FROM _absent a
         WHERE s.id = a.id AND s.status = 'IN_HOUSE';
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

ALTER TABLE iam_v2.pms_roster_reconciliation_runs DROP CONSTRAINT IF EXISTS pms_roster_reconciliation_runs_outcome_check;
ALTER TABLE iam_v2.pms_roster_reconciliation_runs ADD CONSTRAINT pms_roster_reconciliation_runs_outcome_check
    CHECK (outcome IN ('COMPLETED','REFUSED_ROSTER_TOO_SMALL','REFUSED_GENERATION_UNPUBLISHED',
                       'REFUSED_CAP_EXCEEDED','REFUSED_GENERATION_NOT_LATEST','REFUSED_LINK_NOT_HEALTHY',
                       'REFUSED_ROSTER_INCOMPLETE','REFUSED_SCOPE_MISMATCH','REFUSED_NO_COVERAGE_EVIDENCE',
                       'REFUSED_CONFLICTING_OBSERVATIONS'));

DO $grant$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_pmsd') THEN
        GRANT EXECUTE ON FUNCTION iam_v2.pms_record_resync_coverage(uuid,uuid,uuid,bigint,text[],integer,integer,integer) TO svc_pmsd;
        GRANT EXECUTE ON FUNCTION iam_v2.pms_roster_reconcile(uuid,uuid,uuid,bigint,text,boolean,text) TO svc_pmsd;
        GRANT EXECUTE ON FUNCTION iam_v2.pms_known_room_inventory(uuid,uuid,uuid,bigint,integer) TO svc_pmsd;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
        GRANT EXECUTE ON FUNCTION iam_v2.pms_roster_reconcile(uuid,uuid,uuid,bigint,text,boolean,text) TO svc_edged;
        GRANT EXECUTE ON FUNCTION iam_v2.pms_known_room_inventory(uuid,uuid,uuid,bigint,integer) TO svc_edged;
    END IF;
END;
$grant$;

DO $own$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
        ALTER FUNCTION iam_v2.pms_record_resync_coverage(uuid,uuid,uuid,bigint,text[],integer,integer,integer) OWNER TO iam_v2_owner;
        ALTER FUNCTION iam_v2.pms_known_room_inventory(uuid,uuid,uuid,bigint,integer) OWNER TO iam_v2_owner;
        ALTER FUNCTION iam_v2.pms_roster_reconcile(uuid,uuid,uuid,bigint,text,boolean,text) OWNER TO iam_v2_owner;
    END IF;
END;
$own$;

DROP FUNCTION IF EXISTS iam_v2.pms_record_resync_coverage(uuid,uuid,uuid,bigint,integer,integer,integer);

COMMIT;
