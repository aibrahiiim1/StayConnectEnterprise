-- AN ACCEPTED STARTUP DATA GAP IS A DECISION, NOT A REPAIR.
--
-- One departure on this appliance names a reservation it never received an arrival for. It is not a fault
-- and not a bug: the appliance was connected to a hotel that was already running, its first roster sweeps
-- did not yet cover every room, and a guest checked out inside that window. The PMS correctly announced a
-- departure for a stay this system had never been told about.
--
-- Two answers were always available. Ask the PMS whether that reservation existed, or ACCEPT it as a known
-- gap from the appliance's first days. The Product Owner has taken the second. This migration is what makes
-- that answer expressible without lying about it.
--
-- WHAT THIS IS NOT:
--
--   * It is NOT a deletion. The stay_event stays exactly as written -- MANUAL_REVIEW, its review code, its
--     payload, its timestamps. What the PMS said and what this system did about it remain readable for ever.
--   * It is NOT "resolved". The disposition says ACCEPTED_STARTUP_DATA_GAP, which is the truth: nobody
--     established what happened, somebody decided it does not need establishing.
--   * It is NOT a guest-state change. No stay is closed, opened, or touched. The guest this record concerns
--     departed in August and has no session, no entitlement and no device here.
--   * It is NOT a blanket suppression. The function takes ONE event id, refuses anything that is not an
--     unanswered live departure, and refuses an event that already carries a disposition. A gap accepted
--     today cannot silence a genuine exception that arrives tomorrow -- which is the property that matters
--     most, because an operator who accepts one thing must not thereby stop being told about others.

BEGIN;

ALTER TABLE iam_v2.pms_case_resolutions DROP CONSTRAINT IF EXISTS pms_case_resolutions_disposition_check;
ALTER TABLE iam_v2.pms_case_resolutions ADD CONSTRAINT pms_case_resolutions_disposition_check
    CHECK (disposition IN (
        'DEPARTED_CONFIRMED_BY_ROSTER',
        'NOT_A_DEPARTURE_ROSTER_SNAPSHOT',
        'ALREADY_CLOSED',
        'NEEDS_PMS_EVIDENCE',
        -- An operator decided this record is a known gap from the appliance's first days and needs no
        -- further answer. Deliberately distinct from every other value here: the rest describe what the
        -- EVIDENCE showed, this one describes what a PERSON chose.
        'ACCEPTED_STARTUP_DATA_GAP'));

ALTER TABLE iam_v2.pms_case_resolutions DROP CONSTRAINT IF EXISTS pms_case_resolutions_evidence_kind_check;
ALTER TABLE iam_v2.pms_case_resolutions ADD CONSTRAINT pms_case_resolutions_evidence_kind_check
    CHECK (evidence_kind IN ('PUBLISHED_ROSTER_GENERATION','ADMISSION_KIND','STAY_STATE','NONE',
                             -- There is no evidence. A person decided, and the record says so rather than
                             -- dressing the decision up as a finding.
                             'OPERATOR_DECISION'));

-- ---------------------------------------------------------------------------------------------------------
-- Accept ONE recorded departure as a startup data gap.
--
-- Every refusal here exists because the alternative is a control that quietly grows into a way of clearing
-- warnings. It takes a single event id, checks that the event is what it claims to be, and will not touch
-- anything that already has an answer.
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION iam_v2.pms_accept_startup_data_gap(
    p_tenant uuid, p_site uuid, p_event uuid, p_operator text, p_reason text
) RETURNS uuid
LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $$
DECLARE v_iface uuid; v_kind text; v_type text; v_status text; v_res uuid;
BEGIN
    IF p_operator IS NULL OR length(btrim(p_operator)) = 0 THEN
        RAISE EXCEPTION 'accepting a startup data gap requires an operator identity'
            USING ERRCODE = 'invalid_parameter_value';
    END IF;
    IF p_reason IS NULL OR length(btrim(p_reason)) = 0 THEN
        RAISE EXCEPTION 'accepting a startup data gap requires a recorded reason'
            USING ERRCODE = 'invalid_parameter_value';
    END IF;

    SELECT e.pms_interface_id, e.admission_kind, e.event_type, e.processing_status
      INTO v_iface, v_kind, v_type, v_status
      FROM iam_v2.stay_events e
     WHERE e.id = p_event AND e.tenant_id = p_tenant AND e.site_id = p_site;

    IF v_iface IS NULL THEN
        RAISE EXCEPTION 'no such recorded event for this property' USING ERRCODE = 'no_data_found';
    END IF;
    -- Only an unanswered LIVE departure can be a startup gap. A roster snapshot is answered by what it is,
    -- and an applied event was never outstanding.
    IF v_status <> 'MANUAL_REVIEW' OR v_type <> 'GO' OR v_kind <> 'LIVE' THEN
        RAISE EXCEPTION 'only an unanswered live departure can be accepted as a startup data gap '
                        '(this one is % / % / %)', v_status, v_type, v_kind
            USING ERRCODE = 'check_violation';
    END IF;
    IF EXISTS (SELECT 1 FROM iam_v2.pms_case_resolutions x WHERE x.stay_event_id = p_event) THEN
        RAISE EXCEPTION 'this record already carries a disposition; it is not outstanding'
            USING ERRCODE = 'unique_violation';
    END IF;

    INSERT INTO iam_v2.pms_case_resolutions
           (tenant_id, site_id, pms_interface_id, stay_event_id, stay_id, disposition,
            evidence_kind, evidence_ref, resolved_by, note)
    VALUES (p_tenant, p_site, v_iface, p_event, NULL, 'ACCEPTED_STARTUP_DATA_GAP',
            'OPERATOR_DECISION', NULL, btrim(p_operator), btrim(p_reason))
    RETURNING id INTO v_res;

    RETURN v_res;
END;
$$;

REVOKE ALL ON FUNCTION iam_v2.pms_accept_startup_data_gap(uuid,uuid,uuid,text,text) FROM PUBLIC;

DO $grant$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
        GRANT EXECUTE ON FUNCTION iam_v2.pms_accept_startup_data_gap(uuid,uuid,uuid,text,text) TO svc_edged;
    END IF;
    -- The connector never accepts anything on a person's behalf.
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
        ALTER FUNCTION iam_v2.pms_accept_startup_data_gap(uuid,uuid,uuid,text,text) OWNER TO iam_v2_owner;
    END IF;
END;
$grant$;

DO $verify$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_pmsd') THEN
        IF has_function_privilege('svc_pmsd','iam_v2.pms_accept_startup_data_gap(uuid,uuid,uuid,text,text)','EXECUTE') THEN
            RAISE EXCEPTION '0078: the connector can accept gaps on an operator''s behalf';
        END IF;
    END IF;
END;
$verify$;

-- ---------------------------------------------------------------------------------------------------------
-- ONE DEFINITION OF "OUTSTANDING", because three different screens invented their own.
--
-- The dashboard, the reconciliation page, the PMS connection row and the Property-management card each
-- counted review events their own way. Two were corrected and two were missed, so an operator saw 1 on one
-- screen and 13 717 on another and reasonably concluded there were thirteen thousand decisions waiting.
-- There were none: 13 716 were answered and the last one is accepted below.
--
-- A number an operator acts on must have exactly one definition. This is it, and every caller now reads it.
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE VIEW iam_v2.pms_unanswered_review_events AS
    SELECT e.*
      FROM iam_v2.stay_events e
     WHERE e.processing_status = 'MANUAL_REVIEW'
       AND NOT EXISTS (SELECT 1 FROM iam_v2.pms_case_resolutions x WHERE x.stay_event_id = e.id);

COMMENT ON VIEW iam_v2.pms_unanswered_review_events IS
  'Recorded events that still need an answer: MANUAL_REVIEW with no disposition. THE single definition of outstanding PMS work -- every operator-facing count reads this, so no two screens can disagree about how much there is to do.';

DO $grant$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
        GRANT SELECT ON iam_v2.pms_unanswered_review_events TO svc_edged;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
        ALTER VIEW iam_v2.pms_unanswered_review_events OWNER TO iam_v2_owner;
    END IF;
END;
$grant$;

COMMIT;
