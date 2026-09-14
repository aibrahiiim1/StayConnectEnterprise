-- NAME THE HISTORICAL EXCEPTION FOR WHAT IT IS.
--
-- The blocker for a departure naming a reservation this appliance never saw an arrival for was accurate and
-- unhelpfully anonymous. It sat beside two blockers that describe conditions which CLEAR THEMSELVES -- a link
-- that comes back, a feed that starts describing the building again -- and read as though it were another of
-- those, waiting on something.
--
-- It is not. It is a fixed historical fact from before this appliance had a complete picture of the property,
-- it will never clear on its own, and nothing local can answer it without inventing the arrival it never
-- received. Saying so in the text is the difference between an operator watching for it to resolve and an
-- operator knowing to ask the PMS -- or to leave it alone, which is also a legitimate answer.
--
-- Nothing is deleted and no stay changes state. Only the wording changes.

BEGIN;

CREATE OR REPLACE FUNCTION iam_v2.pms_integration_blockers(p_tenant uuid, p_site uuid)
RETURNS TABLE (blocker text, since timestamptz, detail text, guests_affected boolean)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $$
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

    -- The historical exception. Unlike the two above it does NOT clear itself, and it is the only one an
    -- operator may reasonably decide to leave exactly where it is.
    SELECT 'DEPARTURE_FOR_UNKNOWN_STAY'::text,
           min(e.received_at),
           'HISTORICAL EXCEPTION, not a fault and not something that will clear on its own. '
             || count(*)::text || ' departure(s) from before this appliance had a complete picture of the '
             || 'property name a reservation it never received an arrival for. Guests are unaffected. Only '
             || 'the PMS can say whether those stays existed; nothing local can place them without inventing '
             || 'the arrival, so this stays on the list until somebody asks Protel or records a decision to '
             || 'leave it.',
           false
      FROM iam_v2.stay_events e
     WHERE e.tenant_id = p_tenant AND e.site_id = p_site
       AND e.processing_status = 'MANUAL_REVIEW'
       AND e.event_type = 'GO'
       AND e.admission_kind = 'LIVE'
       AND NOT EXISTS (SELECT 1 FROM iam_v2.pms_case_resolutions x WHERE x.stay_event_id = e.id)
     HAVING count(*) > 0;
$$;

REVOKE ALL ON FUNCTION iam_v2.pms_integration_blockers(uuid,uuid) FROM PUBLIC;
DO $g$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
        GRANT EXECUTE ON FUNCTION iam_v2.pms_integration_blockers(uuid,uuid) TO svc_edged;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
        ALTER FUNCTION iam_v2.pms_integration_blockers(uuid,uuid) OWNER TO iam_v2_owner;
    END IF;
END;
$g$;

COMMIT;
