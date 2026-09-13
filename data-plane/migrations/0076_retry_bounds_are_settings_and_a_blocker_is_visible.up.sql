-- RETRY BOUNDS ARE SETTINGS, AND A PERSISTENT BLOCKER IS VISIBLE.
--
-- The connector already recovers from connection loss on its own, and that was verified before any of this
-- was written: it reconnects with bounded exponential backoff, an interrupted sweep publishes nothing and
-- leaves the last known-good mirror serving guests, and recovery is a whole fresh DS..DE rather than a
-- resumption of the fragment that was in flight. Nothing here changes that behaviour.
--
-- What was missing is everything AROUND it.
--
-- 1. THE RETRY BOUNDS WERE COMPILE-TIME CONSTANTS. 500 ms to 30 s, reset after 60 s stable -- sensible
--    numbers, and unreachable without a deployment. A property whose PMS restarts nightly and one on a
--    flaky VPN do not want the same backoff, and §0C is explicit that an operational value a hotel may
--    reasonably need to change ships as an audited setting.
--
-- 2. A PERSISTENT BLOCKER WAS SILENT. The previous delivery made reconciliation refuse FOR EVER when the
--    feed stops describing the building -- deliberately, because the alternative was closing stays on a
--    degraded picture. But "refuses for ever" is only correct if somebody is TOLD. Left silent it is just a
--    feature that quietly stopped working, which is the worst of both.
--
-- So: the numbers become settings, and the blockers become a view derived from facts already recorded.
-- There is no new background job, no new writer and nothing to press -- a blocker is computed from the
-- transport status and the reconciliation runs that are written anyway.

BEGIN;

CREATE TABLE IF NOT EXISTS iam_v2.pms_connection_settings (
    tenant_id              uuid NOT NULL,
    site_id                uuid NOT NULL,
    -- Reconnect backoff. Unit: milliseconds. The connector retries for ever -- a hotel's PMS coming back at
    -- 3am must be picked up without anyone present -- so what is bounded is the INTERVAL, never the count.
    backoff_min_ms         integer NOT NULL DEFAULT 500,
    backoff_max_ms         integer NOT NULL DEFAULT 30000,
    -- How long a connection must hold before the backoff is considered recovered. Unit: seconds.
    stable_reset_seconds   integer NOT NULL DEFAULT 60,
    -- How long the transport may stay down before it is reported as a blocker. Unit: seconds. This is a
    -- REPORTING threshold: guests keep authenticating from the mirror throughout, so it governs when a
    -- person is told, not when anything stops.
    link_down_alert_seconds integer NOT NULL DEFAULT 900,
    -- How many consecutive refused reconciliations count as blocked. Unit: runs.
    blocked_after_refusals integer NOT NULL DEFAULT 3,
    config_version         bigint NOT NULL DEFAULT 1,
    updated_at             timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, site_id),
    CONSTRAINT pcs_backoff_sane CHECK (backoff_min_ms BETWEEN 100 AND 600000
                                   AND backoff_max_ms BETWEEN 100 AND 3600000
                                   AND backoff_max_ms >= backoff_min_ms),
    CONSTRAINT pcs_stable_sane  CHECK (stable_reset_seconds BETWEEN 1 AND 86400),
    CONSTRAINT pcs_linkdown_sane CHECK (link_down_alert_seconds BETWEEN 30 AND 604800),
    CONSTRAINT pcs_refusals_sane CHECK (blocked_after_refusals BETWEEN 1 AND 1000),
    CONSTRAINT pcs_version_positive CHECK (config_version >= 1)
);

COMMENT ON TABLE iam_v2.pms_connection_settings IS
  'Reconnect and blocker-reporting bounds for the PMS link. The connector retries indefinitely on purpose -- a PMS returning overnight must be picked up unattended -- so these bound the INTERVAL and the reporting, never the number of attempts.';

CREATE TABLE IF NOT EXISTS iam_v2.pms_connection_settings_changes (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    uuid NOT NULL,
    site_id      uuid NOT NULL,
    changed_at   timestamptz NOT NULL DEFAULT now(),
    changed_by   text NOT NULL CHECK (length(btrim(changed_by)) > 0),
    reason       text CHECK (reason IS NULL OR length(reason) <= 500),
    old_values   jsonb,
    new_values   jsonb NOT NULL,
    new_config_version bigint NOT NULL
);

CREATE OR REPLACE FUNCTION iam_v2.pms_connection_settings_append_only()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'iam_v2.pms_connection_settings_changes is append-only: % refused', TG_OP
        USING ERRCODE = 'restrict_violation';
END;
$$;

DROP TRIGGER IF EXISTS pms_connection_settings_changes_no_update ON iam_v2.pms_connection_settings_changes;
CREATE TRIGGER pms_connection_settings_changes_no_update
    BEFORE UPDATE OR DELETE ON iam_v2.pms_connection_settings_changes
    FOR EACH ROW EXECUTE FUNCTION iam_v2.pms_connection_settings_append_only();

CREATE OR REPLACE FUNCTION iam_v2.pms_connection_settings_get(p_tenant uuid, p_site uuid)
RETURNS TABLE (backoff_min_ms integer, backoff_max_ms integer, stable_reset_seconds integer,
               link_down_alert_seconds integer, blocked_after_refusals integer,
               config_version bigint, is_default boolean)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $$
    SELECT COALESCE(s.backoff_min_ms, 500),
           COALESCE(s.backoff_max_ms, 30000),
           COALESCE(s.stable_reset_seconds, 60),
           COALESCE(s.link_down_alert_seconds, 900),
           COALESCE(s.blocked_after_refusals, 3),
           COALESCE(s.config_version, 0),
           (s.tenant_id IS NULL)
      FROM (SELECT p_tenant AS t, p_site AS s) k
      LEFT JOIN iam_v2.pms_connection_settings s ON s.tenant_id = k.t AND s.site_id = k.s;
$$;

CREATE OR REPLACE FUNCTION iam_v2.pms_connection_settings_set(
    p_tenant uuid, p_site uuid, p_operator text, p_reason text DEFAULT NULL,
    p_backoff_min_ms integer DEFAULT NULL, p_backoff_max_ms integer DEFAULT NULL,
    p_stable_reset_seconds integer DEFAULT NULL, p_link_down_alert_seconds integer DEFAULT NULL,
    p_blocked_after_refusals integer DEFAULT NULL
) RETURNS bigint
LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $$
DECLARE v_old jsonb; v_ver bigint; c record;
BEGIN
    IF p_operator IS NULL OR length(btrim(p_operator)) = 0 THEN
        RAISE EXCEPTION 'connection settings: an operator identity is required'
            USING ERRCODE = 'invalid_parameter_value';
    END IF;
    PERFORM pg_advisory_xact_lock(hashtext('pms_conn_settings'), hashtext(p_site::text));
    SELECT to_jsonb(s) INTO v_old FROM iam_v2.pms_connection_settings s
     WHERE s.tenant_id = p_tenant AND s.site_id = p_site FOR UPDATE;
    SELECT * INTO c FROM iam_v2.pms_connection_settings_get(p_tenant, p_site);

    INSERT INTO iam_v2.pms_connection_settings AS s
           (tenant_id, site_id, backoff_min_ms, backoff_max_ms, stable_reset_seconds,
            link_down_alert_seconds, blocked_after_refusals, config_version, updated_at)
    VALUES (p_tenant, p_site,
            COALESCE(p_backoff_min_ms, c.backoff_min_ms),
            COALESCE(p_backoff_max_ms, c.backoff_max_ms),
            COALESCE(p_stable_reset_seconds, c.stable_reset_seconds),
            COALESCE(p_link_down_alert_seconds, c.link_down_alert_seconds),
            COALESCE(p_blocked_after_refusals, c.blocked_after_refusals), 1, now())
    ON CONFLICT (tenant_id, site_id) DO UPDATE
       SET backoff_min_ms = EXCLUDED.backoff_min_ms,
           backoff_max_ms = EXCLUDED.backoff_max_ms,
           stable_reset_seconds = EXCLUDED.stable_reset_seconds,
           link_down_alert_seconds = EXCLUDED.link_down_alert_seconds,
           blocked_after_refusals = EXCLUDED.blocked_after_refusals,
           config_version = s.config_version + 1, updated_at = now()
    RETURNING s.config_version INTO v_ver;

    INSERT INTO iam_v2.pms_connection_settings_changes
           (tenant_id, site_id, changed_by, reason, old_values, new_values, new_config_version)
    SELECT p_tenant, p_site, btrim(p_operator), NULLIF(btrim(COALESCE(p_reason,'')),''), v_old,
           to_jsonb(n) - 'tenant_id' - 'site_id', v_ver
      FROM iam_v2.pms_connection_settings n
     WHERE n.tenant_id = p_tenant AND n.site_id = p_site;
    RETURN v_ver;
END;
$$;

-- ---------------------------------------------------------------------------------------------------------
-- What is standing between this property and a reconciled mirror, derived from facts already recorded.
--
-- DERIVED, NOT RAISED. There is no job, no writer, no state machine and nothing to acknowledge. A blocker
-- exists exactly as long as the condition does and disappears the moment it stops -- which is the only
-- honest way to report something the appliance cannot itself fix.
--
-- Each row says what is wrong, how long it has been wrong, and -- the part that matters -- whether guests
-- are affected. They usually are not: the mirror keeps authorising guests while the feed is down, and
-- saying so in the same breath stops a link alarm being read as an outage.
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION iam_v2.pms_integration_blockers(p_tenant uuid, p_site uuid)
RETURNS TABLE (blocker text, since timestamptz, detail text, guests_affected boolean)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $$
    -- The link has been down longer than this property tolerates before telling somebody.
    SELECT 'LINK_DOWN'::text,
           r.disconnected_since,
           'The PMS link has been down since then. Guests continue to be authorised from the last good '
             || 'roster; new arrivals and departures are not being seen.',
           false
      FROM iam_v2.pms_interface_runtime r,
           LATERAL iam_v2.pms_connection_settings_get(p_tenant, p_site) s
     WHERE r.tenant_id = p_tenant AND r.site_id = p_site
       AND r.transport_status = 'DISCONNECTED'
       AND r.disconnected_since IS NOT NULL
       AND r.disconnected_since < now() - make_interval(secs => s.link_down_alert_seconds)

    UNION ALL

    -- Reconciliation has refused the same way repeatedly. This is the visible counterpart of the deliberate
    -- decision that a degraded feed refuses rather than eventually agreeing with itself.
    SELECT 'RECONCILIATION_BLOCKED'::text,
           min(x.run_at),
           'Roster reconciliation has refused ' || count(*)::text || ' times in a row ('
             || max(x.outcome) || '). Departures are not being closed automatically until the PMS sends a '
             || 'complete, uncontradicted roster.',
           false
      FROM (SELECT run_at, outcome
              FROM iam_v2.pms_roster_reconciliation_runs
             WHERE tenant_id = p_tenant AND site_id = p_site AND mode = 'APPLY'
             ORDER BY run_at DESC
             LIMIT (SELECT blocked_after_refusals FROM iam_v2.pms_connection_settings_get(p_tenant, p_site))
           ) x
     HAVING count(*) = (SELECT blocked_after_refusals FROM iam_v2.pms_connection_settings_get(p_tenant, p_site))
        AND count(*) FILTER (WHERE x.outcome = 'COMPLETED') = 0

    UNION ALL

    -- A departure the PMS announced for a stay this appliance never saw. It cannot be resolved here without
    -- inventing the arrival, so it is reported as what it is: an external question.
    SELECT 'DEPARTURE_FOR_UNKNOWN_STAY'::text,
           min(e.received_at),
           count(*)::text || ' departure(s) name a reservation this appliance has no arrival for. Only the '
             || 'PMS can say whether those stays existed; nothing local can place them.',
           false
      FROM iam_v2.stay_events e
     WHERE e.tenant_id = p_tenant AND e.site_id = p_site
       AND e.processing_status = 'MANUAL_REVIEW'
       AND e.event_type = 'GO'
       AND e.admission_kind = 'LIVE'
       AND NOT EXISTS (SELECT 1 FROM iam_v2.pms_case_resolutions x WHERE x.stay_event_id = e.id)
     HAVING count(*) > 0;
$$;

REVOKE ALL ON iam_v2.pms_connection_settings FROM PUBLIC;
REVOKE ALL ON iam_v2.pms_connection_settings_changes FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.pms_connection_settings_set(uuid,uuid,text,text,integer,integer,integer,integer,integer) FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.pms_connection_settings_get(uuid,uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.pms_integration_blockers(uuid,uuid) FROM PUBLIC;

DO $grant$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_pmsd') THEN
        -- The connector READS its own bounds and never writes them: a daemon that could widen its own
        -- backoff is a daemon whose configuration means nothing.
        GRANT EXECUTE ON FUNCTION iam_v2.pms_connection_settings_get(uuid,uuid) TO svc_pmsd;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
        GRANT EXECUTE ON FUNCTION iam_v2.pms_connection_settings_get(uuid,uuid) TO svc_edged;
        GRANT EXECUTE ON FUNCTION iam_v2.pms_connection_settings_set(uuid,uuid,text,text,integer,integer,integer,integer,integer) TO svc_edged;
        GRANT EXECUTE ON FUNCTION iam_v2.pms_integration_blockers(uuid,uuid) TO svc_edged;
        GRANT SELECT ON iam_v2.pms_connection_settings TO svc_edged;
        GRANT SELECT ON iam_v2.pms_connection_settings_changes TO svc_edged;
    END IF;
END;
$grant$;

DO $own$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
        ALTER TABLE iam_v2.pms_connection_settings         OWNER TO iam_v2_owner;
        ALTER TABLE iam_v2.pms_connection_settings_changes OWNER TO iam_v2_owner;
        ALTER FUNCTION iam_v2.pms_connection_settings_append_only() OWNER TO iam_v2_owner;
        ALTER FUNCTION iam_v2.pms_connection_settings_get(uuid,uuid) OWNER TO iam_v2_owner;
        ALTER FUNCTION iam_v2.pms_connection_settings_set(uuid,uuid,text,text,integer,integer,integer,integer,integer) OWNER TO iam_v2_owner;
        ALTER FUNCTION iam_v2.pms_integration_blockers(uuid,uuid) OWNER TO iam_v2_owner;
    END IF;
END;
$own$;

DO $verify$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_pmsd') THEN
        IF has_function_privilege('svc_pmsd',
             'iam_v2.pms_connection_settings_set(uuid,uuid,text,text,integer,integer,integer,integer,integer)','EXECUTE') THEN
            RAISE EXCEPTION '0076: the connector can rewrite the bounds it is governed by';
        END IF;
        IF has_table_privilege('svc_pmsd','iam_v2.pms_connection_settings','UPDATE') THEN
            RAISE EXCEPTION '0076: the connector can write its own settings table';
        END IF;
    END IF;
END;
$verify$;

-- ---------------------------------------------------------------------------------------------------------
-- A ROBUSTNESS FIX FOUND BY THE FAILURE-TO-RECOVERY TEST, carried here because it is in the automatic path.
--
-- pms_roster_reconcile built a TEMP TABLE ... ON COMMIT DROP. That lives until the transaction commits, not
-- until the function returns, so calling it twice in one transaction failed outright. pmsd calls it once per
-- transaction so production never saw it; the first controlled test that drove three generations in a single
-- block hit it on the second call. The function is re-emitted below with a DROP first, and nothing else about
-- it changes.
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

    -- ON COMMIT DROP keeps the table alive until the TRANSACTION ends, not until the function returns, so a
    -- second call in the same transaction failed with "relation _absent already exists". pmsd calls this once
    -- per transaction and never hit it; a controlled failure-to-recovery test that drove three generations in
    -- one DO block did, immediately. Dropping first makes the function re-entrant within a transaction, which
    -- is what any caller would reasonably assume.
    DROP TABLE IF EXISTS _absent;
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


REVOKE ALL ON FUNCTION iam_v2.pms_roster_reconcile(uuid,uuid,uuid,bigint,text,boolean,text) FROM PUBLIC;
DO $g2$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_pmsd') THEN
        GRANT EXECUTE ON FUNCTION iam_v2.pms_roster_reconcile(uuid,uuid,uuid,bigint,text,boolean,text) TO svc_pmsd;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
        GRANT EXECUTE ON FUNCTION iam_v2.pms_roster_reconcile(uuid,uuid,uuid,bigint,text,boolean,text) TO svc_edged;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
        ALTER FUNCTION iam_v2.pms_roster_reconcile(uuid,uuid,uuid,bigint,text,boolean,text) OWNER TO iam_v2_owner;
    END IF;
END;
$g2$;

COMMIT;
