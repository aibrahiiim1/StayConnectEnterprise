-- CENTRAL SERVES THIS APPLIANCE FOR LICENSING ONLY.
--
-- A Product-Owner decision, and a reversal of a WORKING feature rather than a repair of a broken one: the
-- cloud telemetry link was completed and verified two deliveries ago, and is switched off here because the
-- hotel's operations belong on the hotel's appliance. Anyone reading this later should know the link worked.
-- It was not removed because it failed.
--
-- WHAT THE APPLIANCE MAY STILL SAY TO CENTRAL, and nothing else:
--
--   /v1/appliances/register, /v1/appliances/enroll      appliance identity
--   /v1/appliance/csr, /v1/appliance/certificate        the certificate that authenticates the rest
--   /v1/appliance/license, /v1/appliance/offline-reconcile   the licence itself
--   /v1/appliance/hello                                 licence ENFORCEMENT, not chat: it is how a deleted
--                                                       appliance discovers it is orphaned and STOPS serving
--                                                       on a stale cached licence
--   /v1/appliance/assignment, /assignment-registry, /assignment/ack
--                                                       the signed tenant/site binding the licence is scoped
--                                                       to, re-verified locally on every boot
--
-- All of it is HTTPS to ctrlapi. NONE of it is NATS.
--
-- WHY THE NATS TRANSPORT GOES ENTIRELY. The only licensing-shaped thing on it was license_ack, and that is
-- not a licensing mechanism: Central accepts it as a telemetry kind and stores a row. Nothing consumes it and
-- no licence operation depends on it. It is a REPORT ABOUT licensing -- precisely what this decision
-- excludes. With the transport go four subscriptions that were never licensing at all: remote guest-session
-- revocation, remote PMS test/cache/health, the signed command channel, the software-update agent, and the
-- tenant PMS config broadcast.
--
-- WHAT DOES NOT CHANGE. Signed licence validation, certificate verification, and the offline licence and
-- grace rules. Every local subsystem: PMS ingestion, guest authentication, sign-in attempt records, packages,
-- allowances, sessions, accounting, enforcement, Hotel Admin. Nothing here makes Central reachability a
-- condition of a guest getting online.
--
-- NO RECORD IS DELETED BY THIS MIGRATION, on either side. Stopping a producer is not a retention policy.

BEGIN;

-- ---------------------------------------------------------------------------------------------------------
-- The mode, as a persisted setting rather than a build-time constant.
--
-- DEFAULT IS LICENSING_ONLY, AND THAT IS THE WHOLE POINT. The requirement is that non-licensing
-- communication "must not silently reactivate through defaults, service restart or configuration
-- reconciliation". A default of FULL would do exactly that the first time the row was missing -- a restored
-- database, a fresh install, a read that failed. So absence of a row MEANS licensing-only, and running any
-- other way requires somebody to have written that choice down with their name on it.
--
-- There is deliberately NO UI SWITCH. The mode is shown, never offered.
-- ---------------------------------------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS iam_v2.site_cloud_mode (
    tenant_id      uuid NOT NULL,
    site_id        uuid NOT NULL,
    mode           text NOT NULL DEFAULT 'LICENSING_ONLY',
    config_version bigint NOT NULL DEFAULT 1,
    updated_at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, site_id),
    CONSTRAINT scm_mode_known CHECK (mode IN ('LICENSING_ONLY','FULL')),
    CONSTRAINT scm_version_positive CHECK (config_version >= 1)
);

COMMENT ON TABLE iam_v2.site_cloud_mode IS
  'What this site may say to Central. LICENSING_ONLY (the default, and what absence of a row means): licence, appliance identity, certificate lifecycle and the signed assignment, over HTTPS only. FULL additionally opens the cloud telemetry transport. Changing it takes an audited write; there is no UI switch.';

CREATE TABLE IF NOT EXISTS iam_v2.cloud_mode_changes (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   uuid NOT NULL,
    site_id     uuid NOT NULL,
    changed_at  timestamptz NOT NULL DEFAULT now(),
    changed_by  text NOT NULL CHECK (length(btrim(changed_by)) > 0),
    reason      text CHECK (reason IS NULL OR length(reason) <= 500),
    old_mode    text,
    new_mode    text NOT NULL,
    new_config_version bigint NOT NULL
);

CREATE INDEX IF NOT EXISTS cloud_mode_changes_recent_idx
    ON iam_v2.cloud_mode_changes (tenant_id, site_id, changed_at DESC);

CREATE OR REPLACE FUNCTION iam_v2.cloud_mode_changes_append_only()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'iam_v2.cloud_mode_changes is append-only: % refused', TG_OP
        USING ERRCODE = 'restrict_violation';
END;
$$;

DROP TRIGGER IF EXISTS cloud_mode_changes_no_update ON iam_v2.cloud_mode_changes;
CREATE TRIGGER cloud_mode_changes_no_update
    BEFORE UPDATE OR DELETE ON iam_v2.cloud_mode_changes
    FOR EACH ROW EXECUTE FUNCTION iam_v2.cloud_mode_changes_append_only();

-- Read. Returns the approved default when no row exists, and says which of the two it was.
CREATE OR REPLACE FUNCTION iam_v2.cloud_mode_get(p_tenant uuid, p_site uuid)
RETURNS TABLE (mode text, config_version bigint, updated_at timestamptz, is_default boolean)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $$
    SELECT COALESCE(m.mode, 'LICENSING_ONLY'),
           COALESCE(m.config_version, 0),
           m.updated_at,
           (m.tenant_id IS NULL)
      FROM (SELECT p_tenant AS t, p_site AS s) k
      LEFT JOIN iam_v2.site_cloud_mode m ON m.tenant_id = k.t AND m.site_id = k.s;
$$;

-- Write. Audited in the same transaction, like every other operational setting here. No runtime role holds
-- UPDATE on the table or INSERT on the log, so a mode change with no record of who made it is not
-- expressible. That matters more for this setting than most: what FULL re-enables is outbound traffic about
-- a hotel's guests.
CREATE OR REPLACE FUNCTION iam_v2.cloud_mode_set(
    p_tenant uuid, p_site uuid, p_mode text, p_operator text, p_reason text DEFAULT NULL
) RETURNS bigint
LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $$
DECLARE
    v_old text;
    v_new_version bigint;
BEGIN
    IF p_operator IS NULL OR length(btrim(p_operator)) = 0 THEN
        RAISE EXCEPTION 'cloud mode: an operator identity is required'
            USING ERRCODE = 'invalid_parameter_value';
    END IF;
    IF p_mode IS NULL OR p_mode NOT IN ('LICENSING_ONLY','FULL') THEN
        RAISE EXCEPTION 'cloud mode must be LICENSING_ONLY or FULL (got %)', p_mode
            USING ERRCODE = 'check_violation';
    END IF;

    PERFORM pg_advisory_xact_lock(hashtext('cloud_mode'), hashtext(p_site::text));

    SELECT m.mode INTO v_old FROM iam_v2.site_cloud_mode m
     WHERE m.tenant_id = p_tenant AND m.site_id = p_site FOR UPDATE;

    INSERT INTO iam_v2.site_cloud_mode AS m (tenant_id, site_id, mode, config_version, updated_at)
    VALUES (p_tenant, p_site, p_mode, 1, now())
    ON CONFLICT (tenant_id, site_id) DO UPDATE
       SET mode = EXCLUDED.mode, config_version = m.config_version + 1, updated_at = now()
    RETURNING m.config_version INTO v_new_version;

    INSERT INTO iam_v2.cloud_mode_changes
           (tenant_id, site_id, changed_by, reason, old_mode, new_mode, new_config_version)
    VALUES (p_tenant, p_site, btrim(p_operator), NULLIF(btrim(COALESCE(p_reason,'')), ''),
            v_old, p_mode, v_new_version);

    RETURN v_new_version;
END;
$$;

REVOKE ALL ON iam_v2.site_cloud_mode FROM PUBLIC;
REVOKE ALL ON iam_v2.cloud_mode_changes FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.cloud_mode_get(uuid, uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.cloud_mode_set(uuid, uuid, text, text, text) FROM PUBLIC;

DO $grant$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_scd') THEN
        -- scd reads the mode at boot to decide whether to open the telemetry transport at all.
        GRANT EXECUTE ON FUNCTION iam_v2.cloud_mode_get(uuid, uuid) TO svc_scd;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
        -- edged reads it to show the mode and to refuse to enqueue service-health. It gets NO write:
        -- there is no UI switch, so nothing needs one.
        GRANT EXECUTE ON FUNCTION iam_v2.cloud_mode_get(uuid, uuid) TO svc_edged;
        GRANT SELECT ON iam_v2.cloud_mode_changes TO svc_edged;
    END IF;
END;
$grant$;

DO $own$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
        ALTER TABLE iam_v2.site_cloud_mode     OWNER TO iam_v2_owner;
        ALTER TABLE iam_v2.cloud_mode_changes  OWNER TO iam_v2_owner;
        ALTER FUNCTION iam_v2.cloud_mode_get(uuid, uuid) OWNER TO iam_v2_owner;
        ALTER FUNCTION iam_v2.cloud_mode_set(uuid, uuid, text, text, text) OWNER TO iam_v2_owner;
        ALTER FUNCTION iam_v2.cloud_mode_changes_append_only() OWNER TO iam_v2_owner;
    END IF;
END;
$own$;

DO $verify$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
        IF has_function_privilege('svc_edged','iam_v2.cloud_mode_set(uuid,uuid,text,text,text)','EXECUTE') THEN
            RAISE EXCEPTION '0071: svc_edged can change the cloud mode; this decision has no UI switch';
        END IF;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_scd') THEN
        IF has_table_privilege('svc_scd','iam_v2.site_cloud_mode','UPDATE') THEN
            RAISE EXCEPTION '0071: svc_scd can rewrite the mode it is itself subject to';
        END IF;
    END IF;
END;
$verify$;

COMMIT;
