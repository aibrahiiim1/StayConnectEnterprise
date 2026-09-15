-- RECOVERY BELONGS TO THE CONNECTION, NOT TO THE BUILDING.
--
-- 0076 made the reconnect bounds settings instead of compile-time constants, and keyed them
-- (tenant_id, site_id). With one PMS interface per property that reads as correct, and it stayed correct
-- right up until the moment it mattered: the UI had to tell the operator, in as many words, that these
-- values "apply to the whole site, not just this connection". A setting shown on a connection page that
-- silently governs a different connection too is a trap with a caption on it.
--
-- The bounds describe how ONE link retries. Two interfaces at one property -- a second PMS, a migration
-- running old and new side by side, a test connector -- are exactly the case where a flaky link needs
-- patient backoff while a healthy one should not be slowed down to match. Under (tenant, site) that is not
-- expressible: tuning either one tunes both.
--
-- So the key gains the interface.
--
-- WHAT MUST NOT CHANGE IS THE RUNNING PROPERTY'S BEHAVIOUR. The values in force today were chosen and
-- recorded; they are carried onto every existing interface exactly as they stand, config_version and all.
-- A migration that reset an operator's tuning to defaults -- or that quietly turned a configured row back
-- into an "unconfigured, using defaults" row -- would be changing production settings under cover of a
-- storage change, which is the one thing a storage change may never do.

BEGIN;

-- ---------------------------------------------------------------------------------------------------------
-- 1. THE COLUMN, AND THE CARRY-OVER.
--
-- Added nullable, backfilled, then made NOT NULL. The backfill is a cross join of each existing site-wide
-- row with that site's interfaces, so a property with two interfaces ends with both carrying the values it
-- had -- because both of them genuinely were governed by that row a moment ago. Anything else would be
-- inventing a distinction the operator never made.
-- ---------------------------------------------------------------------------------------------------------
ALTER TABLE iam_v2.pms_connection_settings         ADD COLUMN IF NOT EXISTS pms_interface_id uuid;
ALTER TABLE iam_v2.pms_connection_settings_changes ADD COLUMN IF NOT EXISTS pms_interface_id uuid;

-- THE OLD KEY COMES OFF FIRST, and the order is not cosmetic. The carry-over below writes one row per
-- interface for a (tenant, site) that already has a row, so with the two-column key still in force the very
-- first property to be migrated fails on a duplicate key -- which is exactly what a rehearsal of this
-- migration against the live appliance produced before this line existed. The table is briefly unkeyed
-- WITHIN this transaction; the new key is added below, before anything can observe it.
ALTER TABLE iam_v2.pms_connection_settings DROP CONSTRAINT IF EXISTS pms_connection_settings_pkey;

-- The existing rows, one per (tenant, site), become one row per interface. Written as an insert of the
-- MISSING combinations followed by deletion of the now-redundant site-wide rows, so it is re-runnable and
-- never loses a value on a partial application.
INSERT INTO iam_v2.pms_connection_settings
       (tenant_id, site_id, pms_interface_id, backoff_min_ms, backoff_max_ms, stable_reset_seconds,
        link_down_alert_seconds, blocked_after_refusals, config_version, updated_at)
SELECT s.tenant_id, s.site_id, i.id,
       s.backoff_min_ms, s.backoff_max_ms, s.stable_reset_seconds,
       s.link_down_alert_seconds, s.blocked_after_refusals,
       -- THE VERSION CARRIES TOO. It is the operator's change count for these values; restarting it at 1
       -- would quietly discard the fact that they had been deliberately changed.
       s.config_version, s.updated_at
  FROM iam_v2.pms_connection_settings s
  JOIN iam_v2.pms_interfaces i ON i.tenant_id = s.tenant_id AND i.site_id = s.site_id
 WHERE s.pms_interface_id IS NULL
   AND NOT EXISTS (SELECT 1 FROM iam_v2.pms_connection_settings x
                    WHERE x.tenant_id = s.tenant_id AND x.site_id = s.site_id
                      AND x.pms_interface_id = i.id);

-- THE AUDIT TRAIL IS NOT TOUCHED, in either direction, and both halves of that are deliberate.
--
-- It is not COPIED onto each interface. A first draft did exactly that, reasoning that a change to the
-- site-wide row really did change every interface, so attributing it to each was the true reading. It is
-- not: it would manufacture two recorded events where one person made one change, and an operator reading
-- an interface's history would see a change nobody made to that connection.
--
-- It is not DELETED either -- the table refuses, by an append-only trigger that exists for this exact
-- reason, and a rehearsal against the live appliance duly hit it. Those rows keep a NULL interface because
-- a NULL interface is precisely what they were: changes made while the setting was site-wide. A history
-- that quietly rewrote them to look per-interface would be the one thing an audit trail may never do.

-- A site-wide row with NO interface yet would lose its values here, so it is kept until one exists: the
-- delete only removes rows whose values have actually been carried onto at least one interface.
DELETE FROM iam_v2.pms_connection_settings s
 WHERE s.pms_interface_id IS NULL
   AND EXISTS (SELECT 1 FROM iam_v2.pms_connection_settings x
                WHERE x.tenant_id = s.tenant_id AND x.site_id = s.site_id
                  AND x.pms_interface_id IS NOT NULL);

-- ---------------------------------------------------------------------------------------------------------
-- 2. THE KEY.
--
-- Only once every surviving row carries an interface. A leftover site-wide row at this point would mean the
-- carry-over above did not cover it, and the migration must fail rather than key it to a fabricated
-- interface -- so the NOT NULL is the check, not a formality.
-- ---------------------------------------------------------------------------------------------------------
DO $$
DECLARE orphan int;
BEGIN
    SELECT count(*) INTO orphan FROM iam_v2.pms_connection_settings WHERE pms_interface_id IS NULL;
    IF orphan > 0 THEN
        RAISE EXCEPTION 'refusing to re-key: % connection-settings row(s) belong to a site with no PMS '
                        'interface. Their values would be lost. Create the interface, or delete the row '
                        'deliberately, then re-run.', orphan
            USING ERRCODE = 'restrict_violation';
    END IF;
END $$;

ALTER TABLE iam_v2.pms_connection_settings  ALTER COLUMN pms_interface_id SET NOT NULL;

ALTER TABLE iam_v2.pms_connection_settings  ADD  CONSTRAINT pms_connection_settings_pkey
      PRIMARY KEY (tenant_id, site_id, pms_interface_id);

-- The settings follow the interface out of existence. Leaving orphaned bounds behind would let a later
-- interface that happened to reuse an id inherit a stranger's tuning.
ALTER TABLE iam_v2.pms_connection_settings  DROP CONSTRAINT IF EXISTS pms_connection_settings_iface_fk;
ALTER TABLE iam_v2.pms_connection_settings  ADD  CONSTRAINT pms_connection_settings_iface_fk
      FOREIGN KEY (pms_interface_id) REFERENCES iam_v2.pms_interfaces(id) ON DELETE CASCADE;

CREATE INDEX IF NOT EXISTS pms_connection_settings_changes_iface_idx
    ON iam_v2.pms_connection_settings_changes (tenant_id, site_id, pms_interface_id, changed_at DESC);

COMMENT ON COLUMN iam_v2.pms_connection_settings.pms_interface_id IS
  'The ONE interface these bounds govern. Recovery is a property of a link, not of a building: changing one interface must leave every other interface at the same site exactly as it was.';

-- ---------------------------------------------------------------------------------------------------------
-- 3. THE ACCESSORS.
--
-- The two-argument forms are DROPPED rather than kept as wrappers. A wrapper would have to answer "what are
-- the site's bounds?", a question that no longer has one answer, and it would answer it plausibly -- by
-- picking some interface -- on every call site anybody forgot to update. A missing function is a loud
-- failure at deploy time; a wrapper is a quiet wrong number for ever.
-- ---------------------------------------------------------------------------------------------------------
DROP FUNCTION IF EXISTS iam_v2.pms_connection_settings_get(uuid, uuid);
DROP FUNCTION IF EXISTS iam_v2.pms_connection_settings_set(uuid, uuid, text, text, integer, integer, integer, integer, integer);

CREATE OR REPLACE FUNCTION iam_v2.pms_connection_settings_get(p_tenant uuid, p_site uuid, p_interface uuid)
RETURNS TABLE (backoff_min_ms integer, backoff_max_ms integer, stable_reset_seconds integer,
               link_down_alert_seconds integer, blocked_after_refusals integer,
               config_version bigint, is_default boolean)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $$
    -- Absence of a row still yields the approved defaults, so a NEWLY CREATED interface reconnects sensibly
    -- before anyone has configured it -- and is_default says truthfully that nobody has.
    SELECT COALESCE(s.backoff_min_ms, 500),
           COALESCE(s.backoff_max_ms, 30000),
           COALESCE(s.stable_reset_seconds, 60),
           COALESCE(s.link_down_alert_seconds, 900),
           COALESCE(s.blocked_after_refusals, 3),
           COALESCE(s.config_version, 0),
           (s.tenant_id IS NULL)
      FROM (SELECT p_tenant AS t, p_site AS s, p_interface AS i) k
      LEFT JOIN iam_v2.pms_connection_settings s
             ON s.tenant_id = k.t AND s.site_id = k.s AND s.pms_interface_id = k.i;
$$;

CREATE OR REPLACE FUNCTION iam_v2.pms_connection_settings_set(
    p_tenant uuid, p_site uuid, p_interface uuid, p_operator text, p_reason text DEFAULT NULL,
    p_backoff_min_ms integer DEFAULT NULL, p_backoff_max_ms integer DEFAULT NULL,
    p_stable_reset_seconds integer DEFAULT NULL, p_link_down_alert_seconds integer DEFAULT NULL,
    p_blocked_after_refusals integer DEFAULT NULL
) RETURNS bigint
LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $$
DECLARE v_old jsonb; v_ver bigint; c record; v_belongs boolean;
BEGIN
    IF p_operator IS NULL OR length(btrim(p_operator)) = 0 THEN
        RAISE EXCEPTION 'connection settings: an operator identity is required'
            USING ERRCODE = 'invalid_parameter_value';
    END IF;

    -- THE INTERFACE MUST BE THIS SITE'S. Without this an operator scoped to one property could write bounds
    -- onto another property's connection by passing its id, and the foreign key alone would permit it.
    SELECT EXISTS (SELECT 1 FROM iam_v2.pms_interfaces i
                    WHERE i.id = p_interface AND i.tenant_id = p_tenant AND i.site_id = p_site)
      INTO v_belongs;
    IF NOT v_belongs THEN
        RAISE EXCEPTION 'connection settings: interface % does not belong to this site', p_interface
            USING ERRCODE = 'invalid_parameter_value';
    END IF;

    -- Serialised PER INTERFACE, which is the point of the whole change: two operators tuning two different
    -- connections no longer queue behind each other.
    PERFORM pg_advisory_xact_lock(hashtext('pms_conn_settings'), hashtext(p_interface::text));
    SELECT to_jsonb(s) INTO v_old FROM iam_v2.pms_connection_settings s
     WHERE s.tenant_id = p_tenant AND s.site_id = p_site AND s.pms_interface_id = p_interface FOR UPDATE;
    SELECT * INTO c FROM iam_v2.pms_connection_settings_get(p_tenant, p_site, p_interface);

    INSERT INTO iam_v2.pms_connection_settings AS s
           (tenant_id, site_id, pms_interface_id, backoff_min_ms, backoff_max_ms, stable_reset_seconds,
            link_down_alert_seconds, blocked_after_refusals, config_version, updated_at)
    VALUES (p_tenant, p_site, p_interface,
            COALESCE(p_backoff_min_ms, c.backoff_min_ms),
            COALESCE(p_backoff_max_ms, c.backoff_max_ms),
            COALESCE(p_stable_reset_seconds, c.stable_reset_seconds),
            COALESCE(p_link_down_alert_seconds, c.link_down_alert_seconds),
            COALESCE(p_blocked_after_refusals, c.blocked_after_refusals), 1, now())
    ON CONFLICT (tenant_id, site_id, pms_interface_id) DO UPDATE
       SET backoff_min_ms = EXCLUDED.backoff_min_ms,
           backoff_max_ms = EXCLUDED.backoff_max_ms,
           stable_reset_seconds = EXCLUDED.stable_reset_seconds,
           link_down_alert_seconds = EXCLUDED.link_down_alert_seconds,
           blocked_after_refusals = EXCLUDED.blocked_after_refusals,
           config_version = s.config_version + 1, updated_at = now()
    RETURNING s.config_version INTO v_ver;

    INSERT INTO iam_v2.pms_connection_settings_changes
           (tenant_id, site_id, pms_interface_id, changed_by, reason, old_values, new_values,
            new_config_version)
    SELECT p_tenant, p_site, p_interface, btrim(p_operator),
           NULLIF(btrim(COALESCE(p_reason,'')),''), v_old,
           to_jsonb(n) - 'tenant_id' - 'site_id' - 'pms_interface_id', v_ver
      FROM iam_v2.pms_connection_settings n
     WHERE n.tenant_id = p_tenant AND n.site_id = p_site AND n.pms_interface_id = p_interface;
    RETURN v_ver;
END;
$$;

-- ---------------------------------------------------------------------------------------------------------
-- 4. THE BLOCKERS READ THE BOUNDS OF THE INTERFACE THEY ARE ABOUT.
--
-- It already iterated the runtime rows, one per interface; it just resolved the threshold site-wide. With
-- per-interface bounds, a patient connection and an impatient one must be reported on their own terms --
-- otherwise one interface's alert threshold silently governs when the other is called down.
-- ---------------------------------------------------------------------------------------------------------
-- THE SITE'S REFUSAL THRESHOLD, once there can be several.
--
-- Named rather than inlined because it encodes a judgement, and a judgement buried in a subquery is one
-- nobody can find later: where a property's interfaces disagree, the MOST PATIENT threshold governs a
-- site-level alarm. An unconfigured site still answers with the approved default rather than NULL, which is
-- what would otherwise silently disable the blocker entirely on a fresh appliance.
CREATE OR REPLACE FUNCTION iam_v2.pms_site_blocked_after_refusals(p_tenant uuid, p_site uuid)
RETURNS integer
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $$
    SELECT COALESCE(max(blocked_after_refusals), 3)
      FROM iam_v2.pms_connection_settings
     WHERE tenant_id = p_tenant AND site_id = p_site;
$$;

-- EVERYTHING BELOW IS 0077'S FUNCTION, CHANGED ONLY WHERE IT RESOLVED A THRESHOLD. The blocker names, the
-- wording an operator reads, the tables, the filters and the ordering are carried across unaltered: this
-- migration is about which row a threshold comes from, and a re-keying that also quietly reworded an alert
-- would be two changes wearing one name.
CREATE OR REPLACE FUNCTION iam_v2.pms_integration_blockers(p_tenant uuid, p_site uuid)
RETURNS TABLE (blocker text, since timestamptz, detail text, guests_affected boolean)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $$
    -- LINK_DOWN is now judged per interface. It already iterated the runtime rows, one per interface, and
    -- then compared every one of them against a single site-wide threshold -- so a patient connection's
    -- tolerance decided when an impatient one was called down. The lateral now takes the interface from the
    -- runtime row it is judging, which is the only reading that survives a second connection.
    SELECT 'LINK_DOWN'::text,
           r.disconnected_since,
           'The PMS link has been down since then. Guests continue to be authorised from the last good '
             || 'roster; new arrivals and departures are not being seen.',
           false
      FROM iam_v2.pms_interface_runtime r,
           LATERAL iam_v2.pms_connection_settings_get(r.tenant_id, r.site_id, r.pms_interface_id) s
     WHERE r.tenant_id = p_tenant AND r.site_id = p_site
       AND r.transport_status = 'DISCONNECTED'
       AND r.disconnected_since IS NOT NULL
       AND r.disconnected_since < now() - make_interval(secs => s.link_down_alert_seconds)

    UNION ALL

    -- RECONCILIATION_BLOCKED stays a SITE-level fact, deliberately. Reconciliation reconciles the property's
    -- mirror against the roster; pms_roster_reconciliation_runs is keyed by site and has no interface column,
    -- so there is no per-interface run to threshold. Where several interfaces disagree on how many refusals
    -- are too many, the most patient one wins: reporting at the strictest threshold would raise a blocker
    -- about the whole property on the say-so of its twitchiest connection.
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
             LIMIT (SELECT iam_v2.pms_site_blocked_after_refusals(p_tenant, p_site))
           ) x
     HAVING count(*) = (SELECT iam_v2.pms_site_blocked_after_refusals(p_tenant, p_site))
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

COMMIT;
