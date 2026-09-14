-- Reverse of 0077. The blocker returns to its shorter wording, which was accurate but did not say that this
-- one never clears by itself. No record is touched either way.

BEGIN;

CREATE OR REPLACE FUNCTION iam_v2.pms_integration_blockers(p_tenant uuid, p_site uuid)
RETURNS TABLE (blocker text, since timestamptz, detail text, guests_affected boolean)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $$
    SELECT 'DEPARTURE_FOR_UNKNOWN_STAY'::text, min(e.received_at),
           count(*)::text || ' departure(s) name a reservation this appliance has no arrival for. Only the '
             || 'PMS can say whether those stays existed; nothing local can place them.',
           false
      FROM iam_v2.stay_events e
     WHERE e.tenant_id = p_tenant AND e.site_id = p_site
       AND e.processing_status = 'MANUAL_REVIEW' AND e.event_type = 'GO' AND e.admission_kind = 'LIVE'
       AND NOT EXISTS (SELECT 1 FROM iam_v2.pms_case_resolutions x WHERE x.stay_event_id = e.id)
     HAVING count(*) > 0;
$$;

COMMIT;
