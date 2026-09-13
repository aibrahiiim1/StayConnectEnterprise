-- A BUILDING DOES NOT SHRINK BECAUSE THE FEED STOPPED MENTIONING IT.
--
-- 0074 measured completeness against "the rooms recent uncontradicted sweeps described". That is circular,
-- and a closure review demonstrated the consequence end to end:
--
--   A 60-room property, 60 guests in house. Twelve consecutive sweeps name only rooms r001..r040 -- the PMS
--   has simply stopped reporting the other twenty rooms. As the complete sweeps age out of the lookback
--   window the inventory falls 60 -> 40. The thirteenth partial sweep then names 40 of a 40-room "building",
--   passes completeness, and reconciliation CLOSES TWENTY STAYS whose guests are still in their rooms.
--
-- Twenty guests lose their internet because a feed went quiet. Nothing malfunctioned; every individual check
-- did exactly what it was written to do. The error is that the evidence being validated was also the thing
-- defining the standard.
--
-- SO THE BUILDING BECOMES A PERSISTED FACT, not a rolling observation:
--
--   * It is ESTABLISHED once, from the first uncontradicted sweep.
--   * It GROWS automatically -- but only from a sweep that already passed completeness. A sweep that cannot
--     account for the building it claims to describe does not get to enlarge it.
--   * It SHRINKS only by an audited operator action. A property that genuinely got smaller -- a wing closed,
--     a renovation, a renumbering -- is something a person knows. A feed going quiet is not evidence of
--     demolition, and this is the entire point of the change.
--
-- The consequence is deliberate: a persistently degraded feed now refuses FOR EVER rather than eventually
-- agreeing with itself. Someone has to look. That is the correct cost.

BEGIN;

CREATE TABLE IF NOT EXISTS iam_v2.pms_room_inventory (
    tenant_id         uuid NOT NULL,
    site_id           uuid NOT NULL,
    pms_interface_id  uuid NOT NULL,
    rooms             text[] NOT NULL,
    established_at    timestamptz NOT NULL DEFAULT now(),
    established_from  bigint,
    updated_at        timestamptz NOT NULL DEFAULT now(),
    config_version    bigint NOT NULL DEFAULT 1,
    PRIMARY KEY (tenant_id, site_id, pms_interface_id),
    CONSTRAINT pri_version_positive CHECK (config_version >= 1)
);

COMMENT ON TABLE iam_v2.pms_room_inventory IS
  'The rooms this property is known to have. Grows only from sweeps that already passed completeness; shrinks only by an audited operator decision. A feed that stops mentioning rooms must never be able to shrink it.';

CREATE TABLE IF NOT EXISTS iam_v2.pms_room_inventory_changes (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    uuid NOT NULL,
    site_id      uuid NOT NULL,
    pms_interface_id uuid NOT NULL,
    changed_at   timestamptz NOT NULL DEFAULT now(),
    changed_by   text NOT NULL CHECK (length(btrim(changed_by)) > 0),
    reason       text CHECK (reason IS NULL OR length(reason) <= 500),
    change_kind  text NOT NULL CHECK (change_kind IN ('ESTABLISHED','GREW','REBASELINED')),
    rooms_before integer NOT NULL DEFAULT 0,
    rooms_after  integer NOT NULL DEFAULT 0,
    removed      text[] NOT NULL DEFAULT '{}',
    added        text[] NOT NULL DEFAULT '{}',
    new_config_version bigint NOT NULL
);

CREATE OR REPLACE FUNCTION iam_v2.pms_room_inventory_append_only()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'iam_v2.pms_room_inventory_changes is append-only: % refused', TG_OP
        USING ERRCODE = 'restrict_violation';
END;
$$;

DROP TRIGGER IF EXISTS pms_room_inventory_changes_no_update ON iam_v2.pms_room_inventory_changes;
CREATE TRIGGER pms_room_inventory_changes_no_update
    BEFORE UPDATE OR DELETE ON iam_v2.pms_room_inventory_changes
    FOR EACH ROW EXECUTE FUNCTION iam_v2.pms_room_inventory_append_only();

-- REBASELINE: the only way the known building may get smaller.
--
-- It takes a generation to adopt rather than a room list, so an operator confirms a sweep the system has
-- actually seen instead of typing a building from memory. What it removes is recorded by name, because
-- "which twenty rooms stopped being part of this hotel" is the question somebody will ask later.
CREATE OR REPLACE FUNCTION iam_v2.pms_rebaseline_room_inventory(
    p_tenant uuid, p_site uuid, p_iface uuid, p_generation bigint, p_operator text, p_reason text
) RETURNS integer
LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $$
DECLARE v_rooms text[]; v_before text[]; v_ver bigint; v_conflicts int;
BEGIN
    IF p_operator IS NULL OR length(btrim(p_operator)) = 0 THEN
        RAISE EXCEPTION 'room inventory: an operator identity is required'
            USING ERRCODE = 'invalid_parameter_value';
    END IF;
    IF p_reason IS NULL OR length(btrim(p_reason)) = 0 THEN
        RAISE EXCEPTION 'room inventory: shrinking the known building requires a recorded reason'
            USING ERRCODE = 'invalid_parameter_value';
    END IF;

    SELECT c.rooms, c.conflicting_rooms INTO v_rooms, v_conflicts
      FROM iam_v2.pms_resync_coverage c
     WHERE c.tenant_id = p_tenant AND c.site_id = p_site AND c.pms_interface_id = p_iface
       AND c.resync_generation = p_generation;
    IF v_rooms IS NULL THEN
        RAISE EXCEPTION 'room inventory: generation % has no recorded observation to adopt', p_generation
            USING ERRCODE = 'no_data_found';
    END IF;
    IF v_conflicts > 0 THEN
        RAISE EXCEPTION 'room inventory: generation % contradicted itself and cannot define the building',
            p_generation USING ERRCODE = 'check_violation';
    END IF;

    SELECT i.rooms INTO v_before FROM iam_v2.pms_room_inventory i
     WHERE i.tenant_id = p_tenant AND i.site_id = p_site AND i.pms_interface_id = p_iface FOR UPDATE;

    INSERT INTO iam_v2.pms_room_inventory AS i
           (tenant_id, site_id, pms_interface_id, rooms, established_from, config_version)
    VALUES (p_tenant, p_site, p_iface, v_rooms, p_generation, 1)
    ON CONFLICT (tenant_id, site_id, pms_interface_id) DO UPDATE
       SET rooms = EXCLUDED.rooms, established_from = p_generation,
           updated_at = now(), config_version = i.config_version + 1
    RETURNING i.config_version INTO v_ver;

    INSERT INTO iam_v2.pms_room_inventory_changes
           (tenant_id, site_id, pms_interface_id, changed_by, reason, change_kind,
            rooms_before, rooms_after, removed, added, new_config_version)
    VALUES (p_tenant, p_site, p_iface, btrim(p_operator), btrim(p_reason), 'REBASELINED',
            COALESCE(array_length(v_before,1),0), COALESCE(array_length(v_rooms,1),0),
            COALESCE(ARRAY(SELECT unnest(v_before) EXCEPT SELECT unnest(v_rooms)), '{}'),
            COALESCE(ARRAY(SELECT unnest(v_rooms) EXCEPT SELECT unnest(COALESCE(v_before,'{}'))), '{}'),
            v_ver);
    RETURN COALESCE(array_length(v_rooms,1),0);
END;
$$;

-- The known building, replacing the rolling-window view. Kept under the old name so every caller that asked
-- "what rooms does this property have" now gets the persisted answer.
CREATE OR REPLACE FUNCTION iam_v2.pms_known_room_inventory(
    p_tenant uuid, p_site uuid, p_iface uuid, p_generation bigint, p_lookback integer
) RETURNS TABLE (room text)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $$
    SELECT unnest(i.rooms) FROM iam_v2.pms_room_inventory i
     WHERE i.tenant_id = p_tenant AND i.site_id = p_site AND i.pms_interface_id = p_iface;
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
    v_have boolean := false; v_swept text[]; v_known text[]; v_added text[];
    v_published bigint; v_boundary timestamptz; v_run uuid; v_outcome text;
    v_sync text; v_cont text; v_scope int; v_ver bigint;
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

    SELECT true, c.rooms_named, c.conflicting_rooms, c.rooms
      INTO v_have, v_rooms, v_conflicts, v_swept
      FROM iam_v2.pms_resync_coverage c
     WHERE c.tenant_id = p_tenant AND c.site_id = p_site AND c.pms_interface_id = p_iface
       AND c.resync_generation = p_generation;

    SELECT i.rooms INTO v_known FROM iam_v2.pms_room_inventory i
     WHERE i.tenant_id = p_tenant AND i.site_id = p_site AND i.pms_interface_id = p_iface;

    -- BOOTSTRAP. The first uncontradicted sweep establishes the building; there is nothing else it could be
    -- established from, and refusing for ever would make the feature unreachable on a new appliance.
    IF v_known IS NULL AND v_have AND v_conflicts = 0 AND COALESCE(array_length(v_swept,1),0) > 0 THEN
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

    IF v_outcome = 'COMPLETED' THEN
        -- GROWTH, and only here: a sweep that accounted for the building may add rooms to it. A sweep that
        -- could not is refused above and never reaches this point, so a degraded feed cannot enlarge the
        -- building any more than it can shrink it.
        SELECT COALESCE(ARRAY(SELECT unnest(COALESCE(v_swept,'{}')) EXCEPT SELECT unnest(v_known)), '{}')
          INTO v_added;
        IF COALESCE(array_length(v_added,1),0) > 0 THEN
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
    END IF;

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

REVOKE ALL ON iam_v2.pms_room_inventory FROM PUBLIC;
REVOKE ALL ON iam_v2.pms_room_inventory_changes FROM PUBLIC;
-- EXECUTE defaults to PUBLIC on a new function, so the revoke is not tidiness: without it the connector --
-- the very thing being measured against this building -- could shrink it. The assertion at the end of this
-- migration caught exactly that, which is why it is written as an assertion and not a comment.
REVOKE ALL ON FUNCTION iam_v2.pms_rebaseline_room_inventory(uuid,uuid,uuid,bigint,text,text) FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.pms_known_room_inventory(uuid,uuid,uuid,bigint,integer) FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.pms_roster_reconcile(uuid,uuid,uuid,bigint,text,boolean,text) FROM PUBLIC;

DO $grant$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_pmsd') THEN
        GRANT EXECUTE ON FUNCTION iam_v2.pms_roster_reconcile(uuid,uuid,uuid,bigint,text,boolean,text) TO svc_pmsd;
        GRANT EXECUTE ON FUNCTION iam_v2.pms_known_room_inventory(uuid,uuid,uuid,bigint,integer) TO svc_pmsd;
        GRANT SELECT ON iam_v2.pms_room_inventory TO svc_pmsd;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
        GRANT EXECUTE ON FUNCTION iam_v2.pms_roster_reconcile(uuid,uuid,uuid,bigint,text,boolean,text) TO svc_edged;
        GRANT EXECUTE ON FUNCTION iam_v2.pms_known_room_inventory(uuid,uuid,uuid,bigint,integer) TO svc_edged;
        -- Shrinking the known building is an operator decision, so edged may offer it -- audited, with a
        -- mandatory reason, through the definer function and never as a table write.
        GRANT EXECUTE ON FUNCTION iam_v2.pms_rebaseline_room_inventory(uuid,uuid,uuid,bigint,text,text) TO svc_edged;
        GRANT SELECT ON iam_v2.pms_room_inventory TO svc_edged;
        GRANT SELECT ON iam_v2.pms_room_inventory_changes TO svc_edged;
    END IF;
END;
$grant$;

DO $own$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
        ALTER TABLE iam_v2.pms_room_inventory         OWNER TO iam_v2_owner;
        ALTER TABLE iam_v2.pms_room_inventory_changes OWNER TO iam_v2_owner;
        ALTER FUNCTION iam_v2.pms_room_inventory_append_only() OWNER TO iam_v2_owner;
        ALTER FUNCTION iam_v2.pms_rebaseline_room_inventory(uuid,uuid,uuid,bigint,text,text) OWNER TO iam_v2_owner;
        ALTER FUNCTION iam_v2.pms_known_room_inventory(uuid,uuid,uuid,bigint,integer) OWNER TO iam_v2_owner;
        ALTER FUNCTION iam_v2.pms_roster_reconcile(uuid,uuid,uuid,bigint,text,boolean,text) OWNER TO iam_v2_owner;
    END IF;
END;
$own$;

DO $verify$
DECLARE r text;
BEGIN
    FOREACH r IN ARRAY ARRAY['svc_edged','svc_pmsd','svc_scd'] LOOP
        IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = r) THEN
            IF has_table_privilege(r,'iam_v2.pms_room_inventory','UPDATE')
               OR has_table_privilege(r,'iam_v2.pms_room_inventory','DELETE') THEN
                RAISE EXCEPTION '0075: % can rewrite the known building directly', r;
            END IF;
        END IF;
    END LOOP;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_pmsd') THEN
        IF has_function_privilege('svc_pmsd','iam_v2.pms_rebaseline_room_inventory(uuid,uuid,uuid,bigint,text,text)','EXECUTE') THEN
            RAISE EXCEPTION '0075: the connector can shrink the building it is being measured against';
        END IF;
    END IF;
END;
$verify$;

COMMIT;
