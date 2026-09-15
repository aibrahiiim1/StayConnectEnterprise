-- Reverting per-interface recovery bounds to the site-wide key.
--
-- THIS DIRECTION LOSES INFORMATION AND SAYS SO. Going up, one site-wide row fans out to N interfaces and
-- nothing is lost. Coming back down, N rows must collapse into one, and if two interfaces were tuned
-- differently -- which is the entire reason the up migration exists -- there is no correct single answer.
--
-- Rather than silently picking one, the collapse takes the most patient values field by field: the longest
-- backoff, the longest stable-hold, the longest tolerance before alerting, the most refusals before
-- reporting. A rollback that quietly made a link retry HARDER than the operator configured could hammer a
-- PMS that was already struggling; erring toward patience can at worst delay a report, which is recoverable
-- by looking. The audit rows are left untouched -- they record changes that really happened, and a rollback
-- of the storage model is not grounds to rewrite history.

BEGIN;

DROP FUNCTION IF EXISTS iam_v2.pms_connection_settings_get(uuid, uuid, uuid);
DROP FUNCTION IF EXISTS iam_v2.pms_connection_settings_set(uuid, uuid, uuid, text, text, integer, integer, integer, integer, integer);

-- Collapse to one row per site, most-patient wins, before the key can be narrowed again.
CREATE TEMP TABLE _collapsed ON COMMIT DROP AS
SELECT tenant_id, site_id,
       max(backoff_min_ms)          AS backoff_min_ms,
       max(backoff_max_ms)          AS backoff_max_ms,
       max(stable_reset_seconds)    AS stable_reset_seconds,
       max(link_down_alert_seconds) AS link_down_alert_seconds,
       max(blocked_after_refusals)  AS blocked_after_refusals,
       max(config_version)          AS config_version,
       max(updated_at)              AS updated_at
  FROM iam_v2.pms_connection_settings
 GROUP BY tenant_id, site_id;

DELETE FROM iam_v2.pms_connection_settings;

ALTER TABLE iam_v2.pms_connection_settings DROP CONSTRAINT IF EXISTS pms_connection_settings_iface_fk;
ALTER TABLE iam_v2.pms_connection_settings DROP CONSTRAINT IF EXISTS pms_connection_settings_pkey;
ALTER TABLE iam_v2.pms_connection_settings ALTER COLUMN pms_interface_id DROP NOT NULL;
ALTER TABLE iam_v2.pms_connection_settings ADD  CONSTRAINT pms_connection_settings_pkey
      PRIMARY KEY (tenant_id, site_id);
ALTER TABLE iam_v2.pms_connection_settings DROP COLUMN IF EXISTS pms_interface_id;

INSERT INTO iam_v2.pms_connection_settings
       (tenant_id, site_id, backoff_min_ms, backoff_max_ms, stable_reset_seconds,
        link_down_alert_seconds, blocked_after_refusals, config_version, updated_at)
SELECT tenant_id, site_id, backoff_min_ms, backoff_max_ms, stable_reset_seconds,
       link_down_alert_seconds, blocked_after_refusals, config_version, updated_at
  FROM _collapsed;

DROP INDEX IF EXISTS iam_v2.pms_connection_settings_changes_iface_idx;
ALTER TABLE iam_v2.pms_connection_settings_changes DROP COLUMN IF EXISTS pms_interface_id;

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

DROP FUNCTION IF EXISTS iam_v2.pms_site_blocked_after_refusals(uuid, uuid);

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

COMMIT;
