-- RECONCILE THE ROSTER AGAINST THE MIRROR, BY RESERVATION.
--
-- 0071 and its predecessors left one real problem open: the mirror holds 827 stays as IN_HOUSE while the
-- PMS roster holds 589, and 13 579 departure events sit in MANUAL_REVIEW that no amount of operator
-- attention could ever clear. The connector fix that lands with this migration stops the backlog GROWING.
-- This migration is how the existing divergence is CLOSED, and how it stays closed.
--
-- THE RULE THIS IS BUILT ON: compare like with like. A room number is not an identity -- it is reused by the
-- next guest that afternoon -- so nothing here ever matches on one. A stay is present or absent from the
-- roster by its RESERVATION, which is what both sides actually agree on: every GI and GC carries G#, all
-- 54 671 of them.
--
-- WHAT COUNTS AS AUTHORITATIVE EVIDENCE. Exactly one thing: a COMPLETE, PUBLISHED resync generation. Not a
-- planned departure date, not an empty room, not a room somebody else now occupies. A complete DS..DE
-- generation is the PMS stating, in full, who is in the building. A stay the mirror holds IN_HOUSE that a
-- complete roster does not mention has ended -- that is a deduction from a complete statement, not a guess.
--
-- WHAT THIS REFUSES TO DO, and the refusals are the point:
--
--   * It will not act on a partial roster. A truncated DR that returned 12 records would otherwise "prove"
--     that 577 guests left at once. Below a configured floor it refuses and says so.
--   * It will not close a stay that began AFTER the roster was taken. That guest was not absent from the
--     snapshot; they had not arrived yet.
--   * It will not rewrite a single stay_event. Those rows are immutable terminal evidence of what the PMS
--     said and what this system did about it, including where it was wrong. History is not edited here;
--     dispositions are APPENDED alongside it.
--   * It will not invent a checkout time. The boundary is the moment the roster generation completed -- the
--     first instant this system can honestly say it KNEW the stay had ended.

BEGIN;

-- ---------------------------------------------------------------------------------------------------------
-- Operational settings, because these are numbers a hotel may reasonably need to change.
--
-- The floor especially: a property with 40 rooms and a property with 900 do not share a sensible definition
-- of "this roster is too small to trust". Defaults suit this site; both are bounded, audited, and reachable
-- without a deployment.
-- ---------------------------------------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS iam_v2.pms_reconciliation_settings (
    tenant_id            uuid NOT NULL,
    site_id              uuid NOT NULL,
    -- A complete roster carrying fewer than this many reservations is treated as untrustworthy and
    -- reconciliation refuses. Unit: reservations. Default sized well under this property's ~589.
    roster_trust_min     integer NOT NULL DEFAULT 50,
    -- The most stays one run may close. A blast-radius limit, not a policy: a run that wants to close more
    -- than this stops and reports, so a surprise is reviewed by a person instead of applied in full.
    max_close_per_run    integer NOT NULL DEFAULT 500,
    config_version       bigint  NOT NULL DEFAULT 1,
    updated_at           timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, site_id),
    CONSTRAINT prs_floor_sane CHECK (roster_trust_min BETWEEN 1 AND 100000),
    CONSTRAINT prs_cap_sane   CHECK (max_close_per_run BETWEEN 1 AND 100000),
    CONSTRAINT prs_version_positive CHECK (config_version >= 1)
);

COMMENT ON TABLE iam_v2.pms_reconciliation_settings IS
  'Bounds for roster/mirror reconciliation. roster_trust_min: the smallest complete roster (in reservations) that may be acted on. max_close_per_run: the most stays a single run may close before it stops for a human.';

CREATE TABLE IF NOT EXISTS iam_v2.pms_reconciliation_settings_changes (
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

CREATE OR REPLACE FUNCTION iam_v2.pms_reconciliation_settings_append_only()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'iam_v2.pms_reconciliation_settings_changes is append-only: % refused', TG_OP
        USING ERRCODE = 'restrict_violation';
END;
$$;

DROP TRIGGER IF EXISTS pms_reconciliation_settings_changes_no_update
    ON iam_v2.pms_reconciliation_settings_changes;
CREATE TRIGGER pms_reconciliation_settings_changes_no_update
    BEFORE UPDATE OR DELETE ON iam_v2.pms_reconciliation_settings_changes
    FOR EACH ROW EXECUTE FUNCTION iam_v2.pms_reconciliation_settings_append_only();

CREATE OR REPLACE FUNCTION iam_v2.pms_reconciliation_settings_get(p_tenant uuid, p_site uuid)
RETURNS TABLE (roster_trust_min integer, max_close_per_run integer, config_version bigint, is_default boolean)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $$
    SELECT COALESCE(s.roster_trust_min, 50),
           COALESCE(s.max_close_per_run, 500),
           COALESCE(s.config_version, 0),
           (s.tenant_id IS NULL)
      FROM (SELECT p_tenant AS t, p_site AS s) k
      LEFT JOIN iam_v2.pms_reconciliation_settings s ON s.tenant_id = k.t AND s.site_id = k.s;
$$;

CREATE OR REPLACE FUNCTION iam_v2.pms_reconciliation_settings_set(
    p_tenant uuid, p_site uuid, p_floor integer, p_cap integer, p_operator text, p_reason text DEFAULT NULL
) RETURNS bigint
LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $$
DECLARE v_old jsonb; v_ver bigint;
BEGIN
    IF p_operator IS NULL OR length(btrim(p_operator)) = 0 THEN
        RAISE EXCEPTION 'reconciliation settings: an operator identity is required'
            USING ERRCODE = 'invalid_parameter_value';
    END IF;
    PERFORM pg_advisory_xact_lock(hashtext('pms_recon_settings'), hashtext(p_site::text));
    SELECT to_jsonb(s) INTO v_old FROM iam_v2.pms_reconciliation_settings s
     WHERE s.tenant_id = p_tenant AND s.site_id = p_site FOR UPDATE;

    INSERT INTO iam_v2.pms_reconciliation_settings AS s
           (tenant_id, site_id, roster_trust_min, max_close_per_run, config_version, updated_at)
    VALUES (p_tenant, p_site, p_floor, p_cap, 1, now())
    ON CONFLICT (tenant_id, site_id) DO UPDATE
       SET roster_trust_min = EXCLUDED.roster_trust_min,
           max_close_per_run = EXCLUDED.max_close_per_run,
           config_version = s.config_version + 1, updated_at = now()
    RETURNING s.config_version INTO v_ver;

    INSERT INTO iam_v2.pms_reconciliation_settings_changes
           (tenant_id, site_id, changed_by, reason, old_values, new_values, new_config_version)
    VALUES (p_tenant, p_site, btrim(p_operator), NULLIF(btrim(COALESCE(p_reason,'')),''), v_old,
            jsonb_build_object('roster_trust_min', p_floor, 'max_close_per_run', p_cap), v_ver);
    RETURN v_ver;
END;
$$;

-- ---------------------------------------------------------------------------------------------------------
-- The run ledger. One row per reconciliation, DRY_RUN or APPLY, successful or refused.
--
-- A refused run is recorded too, and that is deliberate: "the roster was too small to trust, so nothing was
-- closed" is exactly the kind of thing somebody needs to find six weeks later when they ask why the
-- divergence is still there.
-- ---------------------------------------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS iam_v2.pms_roster_reconciliation_runs (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id         uuid NOT NULL,
    site_id           uuid NOT NULL,
    pms_interface_id  uuid NOT NULL,
    resync_generation bigint NOT NULL,
    mode              text NOT NULL CHECK (mode IN ('DRY_RUN','APPLY')),
    outcome           text NOT NULL CHECK (outcome IN ('COMPLETED','REFUSED_ROSTER_TOO_SMALL',
                                                       'REFUSED_GENERATION_UNPUBLISHED','REFUSED_CAP_EXCEEDED')),
    roster_size       integer NOT NULL DEFAULT 0,
    mirror_in_house   integer NOT NULL DEFAULT 0,
    absent_from_roster integer NOT NULL DEFAULT 0,
    stays_closed      integer NOT NULL DEFAULT 0,
    boundary_at       timestamptz,
    run_at            timestamptz NOT NULL DEFAULT now(),
    run_by            text NOT NULL CHECK (length(btrim(run_by)) > 0),
    reason            text CHECK (reason IS NULL OR length(reason) <= 500)
);

CREATE INDEX IF NOT EXISTS pms_roster_recon_runs_recent_idx
    ON iam_v2.pms_roster_reconciliation_runs (tenant_id, site_id, run_at DESC);

-- ---------------------------------------------------------------------------------------------------------
-- The disposition ledger: how each historical case was answered, and on what evidence.
--
-- This is the answer to "resolve the historical cases without rewriting history". The stay_events rows stay
-- exactly as they are -- immutable, terminal, including the 13 579 that record this system's own mistake.
-- What gets appended here is the DISPOSITION: what the case turned out to be, decided from evidence, with a
-- name against it.
-- ---------------------------------------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS iam_v2.pms_case_resolutions (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id         uuid NOT NULL,
    site_id           uuid NOT NULL,
    pms_interface_id  uuid NOT NULL,
    stay_event_id     uuid NOT NULL REFERENCES iam_v2.stay_events(id),
    stay_id           uuid REFERENCES iam_v2.stays(id),
    disposition       text NOT NULL CHECK (disposition IN (
        -- the roster proved the stay ended; the stay was closed by this run
        'DEPARTED_CONFIRMED_BY_ROSTER',
        -- a resync snapshot artifact: never a departure announcement, nothing to act on
        'NOT_A_DEPARTURE_ROSTER_SNAPSHOT',
        -- the stay had already been closed by other evidence before this case was examined
        'ALREADY_CLOSED',
        -- genuinely unanswerable here: the PMS must settle it
        'NEEDS_PMS_EVIDENCE')),
    evidence_kind     text NOT NULL CHECK (evidence_kind IN ('PUBLISHED_ROSTER_GENERATION','ADMISSION_KIND','STAY_STATE','NONE')),
    evidence_ref      text CHECK (evidence_ref IS NULL OR length(evidence_ref) <= 200),
    run_id            uuid REFERENCES iam_v2.pms_roster_reconciliation_runs(id),
    resolved_at       timestamptz NOT NULL DEFAULT now(),
    resolved_by       text NOT NULL CHECK (length(btrim(resolved_by)) > 0),
    note              text CHECK (note IS NULL OR length(note) <= 500),
    UNIQUE (stay_event_id)
);

CREATE INDEX IF NOT EXISTS pms_case_resolutions_scope_idx
    ON iam_v2.pms_case_resolutions (tenant_id, site_id, pms_interface_id, disposition);

CREATE OR REPLACE FUNCTION iam_v2.pms_case_resolutions_append_only()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'iam_v2.pms_case_resolutions is append-only: % refused', TG_OP
        USING ERRCODE = 'restrict_violation';
END;
$$;

DROP TRIGGER IF EXISTS pms_case_resolutions_no_update ON iam_v2.pms_case_resolutions;
CREATE TRIGGER pms_case_resolutions_no_update
    BEFORE UPDATE OR DELETE ON iam_v2.pms_case_resolutions
    FOR EACH ROW EXECUTE FUNCTION iam_v2.pms_case_resolutions_append_only();

-- ---------------------------------------------------------------------------------------------------------
-- The roster of a published generation: the reservations the PMS named as in-house, for one complete DS..DE.
--
-- GI and GC only. A GO is not a statement about who is present, and including one would let a snapshot
-- artifact vote on its own case.
-- ---------------------------------------------------------------------------------------------------------
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

-- ---------------------------------------------------------------------------------------------------------
-- Reconcile one published generation. DRY_RUN by default; APPLY closes.
--
-- The order of the refusals matters. Each one is a way this could quietly do something catastrophic, and
-- each is checked before anything is written:
--
--   1. the generation must have been PUBLISHED -- a half-received roster is not a statement of anything
--   2. the roster must clear the configured floor -- a truncated DR must never "prove" a mass departure
--   3. the number of stays to close must be within the per-run cap -- a surprise stops for a person
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION iam_v2.pms_roster_reconcile(
    p_tenant uuid, p_site uuid, p_iface uuid, p_generation bigint,
    p_operator text, p_apply boolean DEFAULT false, p_reason text DEFAULT NULL
) RETURNS TABLE (outcome text, roster_size integer, mirror_in_house integer,
                 absent_from_roster integer, stays_closed integer, run_id uuid)
LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $$
DECLARE
    v_floor int; v_cap int;
    v_roster int; v_mirror int; v_absent int; v_closed int := 0;
    v_published bigint; v_boundary timestamptz; v_run uuid; v_outcome text;
BEGIN
    IF p_operator IS NULL OR length(btrim(p_operator)) = 0 THEN
        RAISE EXCEPTION 'roster reconciliation: an operator identity is required'
            USING ERRCODE = 'invalid_parameter_value';
    END IF;
    PERFORM pg_advisory_xact_lock(hashtext('pms_roster_reconcile'), hashtext(p_iface::text));

    SELECT s.roster_trust_min, s.max_close_per_run INTO v_floor, v_cap
      FROM iam_v2.pms_reconciliation_settings_get(p_tenant, p_site) s;

    SELECT r.published_resync_generation INTO v_published
      FROM iam_v2.pms_interface_runtime r
     WHERE r.tenant_id = p_tenant AND r.site_id = p_site AND r.pms_interface_id = p_iface;

    -- THE BOUNDARY, and the reason it is this and not something friendlier: the last moment of the roster
    -- generation is the first instant this system can honestly say it knew the stay had ended. A planned
    -- departure date would be a guess about the past, and an empty room proves only that somebody left,
    -- never who.
    SELECT max(e.received_at) INTO v_boundary
      FROM iam_v2.stay_events e
     WHERE e.tenant_id = p_tenant AND e.site_id = p_site AND e.pms_interface_id = p_iface
       AND e.resync_generation = p_generation;

    SELECT count(*) INTO v_roster
      FROM iam_v2.pms_roster_of_generation(p_tenant, p_site, p_iface, p_generation);

    SELECT count(*) INTO v_mirror FROM iam_v2.stays s
     WHERE s.tenant_id = p_tenant AND s.site_id = p_site AND s.pms_interface_id = p_iface
       AND s.status = 'IN_HOUSE';

    -- Candidates, by RESERVATION and never by room, and never a stay that began after the snapshot.
    CREATE TEMP TABLE _absent ON COMMIT DROP AS
    SELECT s.id, s.external_reservation_id
      FROM iam_v2.stays s
     WHERE s.tenant_id = p_tenant AND s.site_id = p_site AND s.pms_interface_id = p_iface
       AND s.status = 'IN_HOUSE'
       AND btrim(COALESCE(s.external_reservation_id,'')) <> ''
       AND (v_boundary IS NULL OR s.arrival IS NULL OR s.arrival <= v_boundary)
       AND NOT EXISTS (
           SELECT 1 FROM iam_v2.pms_roster_of_generation(p_tenant, p_site, p_iface, p_generation) g
            WHERE g.reservation = btrim(s.external_reservation_id));
    SELECT count(*) INTO v_absent FROM _absent;

    IF v_published IS NULL OR v_published < p_generation THEN
        v_outcome := 'REFUSED_GENERATION_UNPUBLISHED';
    ELSIF v_roster < v_floor THEN
        v_outcome := 'REFUSED_ROSTER_TOO_SMALL';
    ELSIF v_absent > v_cap THEN
        v_outcome := 'REFUSED_CAP_EXCEEDED';
    ELSE
        v_outcome := 'COMPLETED';
    END IF;

    INSERT INTO iam_v2.pms_roster_reconciliation_runs
        (tenant_id, site_id, pms_interface_id, resync_generation, mode, outcome,
         roster_size, mirror_in_house, absent_from_roster, stays_closed, boundary_at, run_by, reason)
    VALUES (p_tenant, p_site, p_iface, p_generation,
            CASE WHEN p_apply THEN 'APPLY' ELSE 'DRY_RUN' END, v_outcome,
            v_roster, v_mirror, v_absent, 0, v_boundary, btrim(p_operator),
            NULLIF(btrim(COALESCE(p_reason,'')),''))
    RETURNING id INTO v_run;

    IF v_outcome = 'COMPLETED' AND p_apply THEN
        UPDATE iam_v2.stays s
           SET status = 'CHECKED_OUT', effective_checkout_at = v_boundary
          FROM _absent a
         WHERE s.id = a.id AND s.status = 'IN_HOUSE';
        GET DIAGNOSTICS v_closed = ROW_COUNT;

        UPDATE iam_v2.pms_roster_reconciliation_runs SET stays_closed = v_closed WHERE id = v_run;
    END IF;

    RETURN QUERY SELECT v_outcome, v_roster, v_mirror, v_absent, v_closed, v_run;
END;
$$;

-- ---------------------------------------------------------------------------------------------------------
-- Dispose of the historical cases that are provably not departures.
--
-- Every GO admitted as RESYNC with no reservation is, by the protocol, a roster snapshot mentioning a room.
-- The connector no longer admits these at all; the ones already recorded are answered here from that same
-- fact, in bulk, with the evidence named. No stay changes state, nothing is deleted, and the events remain
-- exactly as written.
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION iam_v2.pms_dispose_snapshot_cases(
    p_tenant uuid, p_site uuid, p_iface uuid, p_operator text, p_note text DEFAULT NULL
) RETURNS integer
LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $$
DECLARE v_n integer;
BEGIN
    IF p_operator IS NULL OR length(btrim(p_operator)) = 0 THEN
        RAISE EXCEPTION 'case disposition: an operator identity is required'
            USING ERRCODE = 'invalid_parameter_value';
    END IF;
    INSERT INTO iam_v2.pms_case_resolutions
        (tenant_id, site_id, pms_interface_id, stay_event_id, disposition, evidence_kind, evidence_ref,
         resolved_by, note)
    SELECT e.tenant_id, e.site_id, e.pms_interface_id, e.id,
           'NOT_A_DEPARTURE_ROSTER_SNAPSHOT', 'ADMISSION_KIND',
           'RESYNC generation ' || e.resync_generation::text,
           btrim(p_operator), NULLIF(btrim(COALESCE(p_note,'')),'')
      FROM iam_v2.stay_events e
     WHERE e.tenant_id = p_tenant AND e.site_id = p_site AND e.pms_interface_id = p_iface
       AND e.event_type = 'GO'
       AND e.processing_status = 'MANUAL_REVIEW'
       AND e.admission_kind = 'RESYNC'
       AND btrim(COALESCE(e.payload->>'reservation','')) = ''
    ON CONFLICT (stay_event_id) DO NOTHING;
    GET DIAGNOSTICS v_n = ROW_COUNT;
    RETURN v_n;
END;
$$;

REVOKE ALL ON iam_v2.pms_reconciliation_settings FROM PUBLIC;
REVOKE ALL ON iam_v2.pms_reconciliation_settings_changes FROM PUBLIC;
REVOKE ALL ON iam_v2.pms_roster_reconciliation_runs FROM PUBLIC;
REVOKE ALL ON iam_v2.pms_case_resolutions FROM PUBLIC;

DO $grant$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
        -- edged shows the settings, the runs and the dispositions, and lets a permitted operator run a
        -- reconciliation. It gets no direct table write anywhere: every change goes through a definer
        -- function that audits it.
        GRANT EXECUTE ON FUNCTION iam_v2.pms_reconciliation_settings_get(uuid, uuid) TO svc_edged;
        GRANT EXECUTE ON FUNCTION iam_v2.pms_reconciliation_settings_set(uuid, uuid, integer, integer, text, text) TO svc_edged;
        GRANT EXECUTE ON FUNCTION iam_v2.pms_roster_reconcile(uuid, uuid, uuid, bigint, text, boolean, text) TO svc_edged;
        GRANT EXECUTE ON FUNCTION iam_v2.pms_dispose_snapshot_cases(uuid, uuid, uuid, text, text) TO svc_edged;
        GRANT EXECUTE ON FUNCTION iam_v2.pms_roster_of_generation(uuid, uuid, uuid, bigint) TO svc_edged;
        GRANT SELECT ON iam_v2.pms_reconciliation_settings TO svc_edged;
        GRANT SELECT ON iam_v2.pms_reconciliation_settings_changes TO svc_edged;
        GRANT SELECT ON iam_v2.pms_roster_reconciliation_runs TO svc_edged;
        GRANT SELECT ON iam_v2.pms_case_resolutions TO svc_edged;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_pmsd') THEN
        -- pmsd reconciles automatically when it publishes a complete generation. Read + reconcile only.
        GRANT EXECUTE ON FUNCTION iam_v2.pms_reconciliation_settings_get(uuid, uuid) TO svc_pmsd;
        GRANT EXECUTE ON FUNCTION iam_v2.pms_roster_reconcile(uuid, uuid, uuid, bigint, text, boolean, text) TO svc_pmsd;
        GRANT EXECUTE ON FUNCTION iam_v2.pms_roster_of_generation(uuid, uuid, uuid, bigint) TO svc_pmsd;
    END IF;
END;
$grant$;

DO $own$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
        ALTER TABLE iam_v2.pms_reconciliation_settings          OWNER TO iam_v2_owner;
        ALTER TABLE iam_v2.pms_reconciliation_settings_changes  OWNER TO iam_v2_owner;
        ALTER TABLE iam_v2.pms_roster_reconciliation_runs       OWNER TO iam_v2_owner;
        ALTER TABLE iam_v2.pms_case_resolutions                 OWNER TO iam_v2_owner;
        ALTER FUNCTION iam_v2.pms_reconciliation_settings_get(uuid, uuid) OWNER TO iam_v2_owner;
        ALTER FUNCTION iam_v2.pms_reconciliation_settings_set(uuid, uuid, integer, integer, text, text) OWNER TO iam_v2_owner;
        ALTER FUNCTION iam_v2.pms_reconciliation_settings_append_only() OWNER TO iam_v2_owner;
        ALTER FUNCTION iam_v2.pms_case_resolutions_append_only() OWNER TO iam_v2_owner;
        ALTER FUNCTION iam_v2.pms_roster_of_generation(uuid, uuid, uuid, bigint) OWNER TO iam_v2_owner;
        ALTER FUNCTION iam_v2.pms_roster_reconcile(uuid, uuid, uuid, bigint, text, boolean, text) OWNER TO iam_v2_owner;
        ALTER FUNCTION iam_v2.pms_dispose_snapshot_cases(uuid, uuid, uuid, text, text) OWNER TO iam_v2_owner;
    END IF;
END;
$own$;

-- The guarantee that matters most here, asserted rather than described: no runtime role may write any of
-- these tables directly. Every closure and every disposition carries a name because there is no path that
-- does not go through a definer function that records one.
DO $verify$
DECLARE r text; t text;
BEGIN
    FOREACH r IN ARRAY ARRAY['svc_edged','svc_pmsd','svc_scd'] LOOP
        IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = r) THEN
            FOREACH t IN ARRAY ARRAY['pms_case_resolutions','pms_roster_reconciliation_runs',
                                     'pms_reconciliation_settings'] LOOP
                IF has_table_privilege(r, 'iam_v2.' || t, 'INSERT')
                   OR has_table_privilege(r, 'iam_v2.' || t, 'UPDATE')
                   OR has_table_privilege(r, 'iam_v2.' || t, 'DELETE') THEN
                    RAISE EXCEPTION '0072: % can write iam_v2.% directly; audit would be optional', r, t;
                END IF;
            END LOOP;
        END IF;
    END LOOP;
END;
$verify$;

COMMIT;
