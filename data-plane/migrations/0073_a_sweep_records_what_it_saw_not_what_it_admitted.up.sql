-- A SWEEP RECORDS WHAT IT SAW, NOT WHAT IT ADMITTED.
--
-- 0072 proved a roster complete by counting the distinct rooms a generation named: occupied rooms arrive as
-- GI/GC, vacant rooms as GO, and the two together are the building. That worked exactly until the connector
-- fix landed -- because the vacant-room GO records are precisely the ones it now refuses to admit.
--
--     generation 247 (before)   439 roster + 149 vacant = 587 rooms
--     generation 248 (after)    424 roster +   0 vacant = 423 rooms
--
-- Nothing was wrong with either change on its own. Together they left reconciliation measuring completeness
-- against evidence its sibling had stopped storing, so it refused REFUSED_ROSTER_INCOMPLETE forever -- fail
-- safe, and permanently useless.
--
-- THE ERROR WAS INFERRING THE OBSERVATION FROM THE ADMISSION. What the connector SEES is the whole sweep;
-- what it ADMITS is only the part that keys a Stay. Those are different facts and only one of them was
-- being recorded. So the connector now records the observation directly, at the moment it makes it, and
-- reconciliation reads that.
--
-- THE ALTERNATIVE THAT MUST NOT BE TAKEN: re-admitting vacant-room GO records would restore the room count
-- and reopen the 14 125-record defect that created this backlog. Coverage is evidence about a sweep, not a
-- statement about a guest, and it is stored as such -- one row per generation, no stay, no event, no guest.

BEGIN;

CREATE TABLE IF NOT EXISTS iam_v2.pms_resync_coverage (
    tenant_id         uuid   NOT NULL,
    site_id           uuid   NOT NULL,
    pms_interface_id  uuid   NOT NULL,
    resync_generation bigint NOT NULL,
    -- DISTINCT rooms the sweep named, whether or not the record was admissible. This is the completeness
    -- measure: it answers "did the PMS describe the building?", which is the only question that licenses a
    -- deduction from the roster's silence.
    rooms_named       integer NOT NULL,
    -- The occupied half (admitted GI/GC) and the vacant half (observed, deliberately not admitted), kept
    -- apart so an operator can see WHY a total moved.
    roster_records    integer NOT NULL,
    vacant_rooms      integer NOT NULL,
    observed_at       timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, site_id, pms_interface_id, resync_generation),
    CONSTRAINT prc_counts_sane CHECK (rooms_named >= 0 AND roster_records >= 0 AND vacant_rooms >= 0)
);

COMMENT ON TABLE iam_v2.pms_resync_coverage IS
  'What each completed resync OBSERVED: distinct rooms named, split into occupied (admitted) and vacant (observed but never admitted as departures). Evidence about a sweep, never about a guest.';

CREATE OR REPLACE FUNCTION iam_v2.pms_record_resync_coverage(
    p_tenant uuid, p_site uuid, p_iface uuid, p_generation bigint,
    p_rooms integer, p_roster integer, p_vacant integer
) RETURNS void
LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $$
BEGIN
    INSERT INTO iam_v2.pms_resync_coverage AS c
        (tenant_id, site_id, pms_interface_id, resync_generation, rooms_named, roster_records, vacant_rooms)
    VALUES (p_tenant, p_site, p_iface, p_generation, p_rooms, p_roster, p_vacant)
    ON CONFLICT (tenant_id, site_id, pms_interface_id, resync_generation) DO UPDATE
       SET rooms_named = EXCLUDED.rooms_named,
           roster_records = EXCLUDED.roster_records,
           vacant_rooms = EXCLUDED.vacant_rooms,
           observed_at = now();
END;
$$;

-- ---------------------------------------------------------------------------------------------------------
-- Reconciliation now reads OBSERVED coverage, and defers rather than guessing when it has none.
--
-- REFUSED_NO_COVERAGE_EVIDENCE is not an error state and not something an operator clears. Every generation
-- before this migration has no coverage row and never will; every generation after it has one, written by
-- the connector at the moment the sweep completes. So the first complete resync after deployment supplies
-- the evidence and the next automatic attempt proceeds. Deferring until the evidence exists is the whole
-- behaviour.
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
    v_rooms int; v_expected int := 0;
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

    -- OBSERVED coverage for this generation, and the building as recent sweeps have described it.
    SELECT c.rooms_named INTO v_rooms FROM iam_v2.pms_resync_coverage c
     WHERE c.tenant_id = p_tenant AND c.site_id = p_site AND c.pms_interface_id = p_iface
       AND c.resync_generation = p_generation;

    SELECT COALESCE(max(c.rooms_named), 0) INTO v_expected FROM iam_v2.pms_resync_coverage c
     WHERE c.tenant_id = p_tenant AND c.site_id = p_site AND c.pms_interface_id = p_iface
       AND c.resync_generation <= p_generation
       AND c.resync_generation > p_generation - v_look;

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
    ELSIF v_rooms IS NULL THEN
        -- No observation recorded for this sweep. Defer; the next complete resync writes one.
        v_outcome := 'REFUSED_NO_COVERAGE_EVIDENCE';
    ELSIF v_expected > 0 AND v_rooms < v_expected - v_tol THEN
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
                       'REFUSED_ROSTER_INCOMPLETE','REFUSED_SCOPE_MISMATCH','REFUSED_NO_COVERAGE_EVIDENCE'));

REVOKE ALL ON iam_v2.pms_resync_coverage FROM PUBLIC;

DO $grant$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_pmsd') THEN
        -- pmsd records what each sweep observed, and runs reconciliation when one completes. It gets no
        -- direct write to the coverage table: the definer function is the only way in, so a coverage row
        -- with no sweep behind it is not expressible.
        GRANT EXECUTE ON FUNCTION iam_v2.pms_record_resync_coverage(uuid,uuid,uuid,bigint,integer,integer,integer) TO svc_pmsd;
        GRANT EXECUTE ON FUNCTION iam_v2.pms_roster_reconcile(uuid,uuid,uuid,bigint,text,boolean,text) TO svc_pmsd;
        GRANT SELECT ON iam_v2.pms_resync_coverage TO svc_pmsd;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
        GRANT EXECUTE ON FUNCTION iam_v2.pms_roster_reconcile(uuid,uuid,uuid,bigint,text,boolean,text) TO svc_edged;
        GRANT SELECT ON iam_v2.pms_resync_coverage TO svc_edged;
    END IF;
END;
$grant$;

DO $own$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
        ALTER TABLE iam_v2.pms_resync_coverage OWNER TO iam_v2_owner;
        ALTER FUNCTION iam_v2.pms_record_resync_coverage(uuid,uuid,uuid,bigint,integer,integer,integer) OWNER TO iam_v2_owner;
        ALTER FUNCTION iam_v2.pms_roster_reconcile(uuid,uuid,uuid,bigint,text,boolean,text) OWNER TO iam_v2_owner;
    END IF;
END;
$own$;

DO $verify$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_pmsd') THEN
        IF has_table_privilege('svc_pmsd','iam_v2.pms_resync_coverage','INSERT')
           OR has_table_privilege('svc_pmsd','iam_v2.pms_resync_coverage','UPDATE') THEN
            RAISE EXCEPTION '0073: svc_pmsd can write coverage directly; a sweep could be claimed without being observed';
        END IF;
    END IF;
END;
$verify$;

COMMIT;
