-- 0093 down — the cloud telemetry STRUCTURE comes back; its DATA does not.
--
-- Restores exactly the objects 0093 dropped, as 0001, 0069 (part one) and 0071 created them: the cloud-mode
-- setting and its change log, the delivered-record retention setting and its change log, the recovery log,
-- the recovery / prune / accounting functions, and (where the applying role may create in schema public)
-- public.sync_outbox and public.sync_checkpoints. Every table comes back EMPTY: the rows were deleted by an
-- authorised decision and nothing can honestly restore them. No code in this delivery reads any of it, so a
-- rollback of the schema is not a rollback of the removal -- the previous binaries are needed for that.
--
-- The public tables are created first, because 0069's accounting function reads public.sync_outbox. Where
-- the applying role holds no CREATE on schema public (a live site applies as iam_v2_owner), they are
-- skipped with a NOTICE and must be recreated by their owner from 0001 if the previous binaries are
-- redeployed; the iam_v2 functions are plpgsql and resolve the table at call time, so they install either way.

BEGIN;

DO $public$
BEGIN
  IF has_schema_privilege(current_user, 'public', 'CREATE') THEN
    CREATE TABLE IF NOT EXISTS public.sync_outbox (
      seq             bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
      kind            text NOT NULL,
      payload         jsonb NOT NULL DEFAULT '{}'::jsonb,
      created_at      timestamptz NOT NULL DEFAULT now(),
      sent_at         timestamptz,
      attempts        int NOT NULL DEFAULT 0,
      next_attempt_at timestamptz NOT NULL DEFAULT now(),
      dead            boolean NOT NULL DEFAULT false,
      last_error      text
    );
    CREATE INDEX IF NOT EXISTS sync_outbox_pending_idx ON public.sync_outbox (next_attempt_at)
      WHERE sent_at IS NULL AND dead = false;
    CREATE TABLE IF NOT EXISTS public.sync_checkpoints (
      name       text PRIMARY KEY,
      value      jsonb NOT NULL DEFAULT '{}'::jsonb,
      updated_at timestamptz NOT NULL DEFAULT now()
    );
  ELSE
    RAISE NOTICE '0093 down: % may not create in schema public; public.sync_outbox and public.sync_checkpoints are left to their owner', current_user;
  END IF;
END $public$;

-- ==== 0069, part one: the queue's settings, recovery, retention and accounting =============================

-- ---------------------------------------------------------------------------------------------------------
-- 1. How long delivered records are kept.
--
-- Site-scoped, typed, bounded, in the idiom iam_v2.site_guest_signin_protection established: real columns
-- with real CHECK bounds, a version that increments on every change, and absence of a row meaning the
-- approved default rather than "no retention". There is no enable flag, for the reason given there.
--
-- The unit is DAYS and it is in the column name, because "30" has been read as hours.
-- ---------------------------------------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS iam_v2.site_cloud_sync_settings (
    tenant_id               uuid   NOT NULL,
    site_id                 uuid   NOT NULL,
    delivered_retention_days integer NOT NULL DEFAULT 30,
    config_version          bigint NOT NULL DEFAULT 1,
    updated_at              timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, site_id),
    CONSTRAINT scs_retention_days_bounds
        CHECK (delivered_retention_days BETWEEN 1 AND 365),
    CONSTRAINT scs_version_positive CHECK (config_version >= 1)
);

COMMENT ON TABLE iam_v2.site_cloud_sync_settings IS
  'Per-site settings for reporting to the StayConnect cloud. Absence of a row means the approved defaults (delivered records kept 30 days), never "retention is off".';
COMMENT ON COLUMN iam_v2.site_cloud_sync_settings.delivered_retention_days IS
  'How many days a SUCCESSFULLY DELIVERED sync record is kept before it is removed, in days. Records still waiting, and records the appliance gave up on, are never removed by this setting.';

-- The change log. Append-only by trigger, and the only writer is the definer function below, so a change
-- cannot be made without the row that says who made it.
CREATE TABLE IF NOT EXISTS iam_v2.cloud_sync_settings_changes (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   uuid NOT NULL,
    site_id     uuid NOT NULL,
    changed_at  timestamptz NOT NULL DEFAULT now(),
    changed_by  text NOT NULL CHECK (length(btrim(changed_by)) > 0),
    reason      text CHECK (reason IS NULL OR length(reason) <= 500),
    old_delivered_retention_days integer,
    new_delivered_retention_days integer NOT NULL,
    new_config_version bigint NOT NULL
);

CREATE INDEX IF NOT EXISTS cloud_sync_settings_changes_recent_idx
    ON iam_v2.cloud_sync_settings_changes (tenant_id, site_id, changed_at DESC);

CREATE OR REPLACE FUNCTION iam_v2.cloud_sync_settings_changes_append_only()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'iam_v2.cloud_sync_settings_changes is append-only: % refused', TG_OP
        USING ERRCODE = 'restrict_violation';
END;
$$;

DROP TRIGGER IF EXISTS cloud_sync_settings_changes_no_update ON iam_v2.cloud_sync_settings_changes;
CREATE TRIGGER cloud_sync_settings_changes_no_update
    BEFORE UPDATE OR DELETE ON iam_v2.cloud_sync_settings_changes
    FOR EACH ROW EXECUTE FUNCTION iam_v2.cloud_sync_settings_changes_append_only();

-- Read. is_default distinguishes "never configured" from "configured to the same number", which is the
-- difference between a property that accepted the standard and one that chose it.
CREATE OR REPLACE FUNCTION iam_v2.cloud_sync_settings_get(p_tenant uuid, p_site uuid)
RETURNS TABLE (
    delivered_retention_days integer,
    config_version bigint,
    updated_at timestamptz,
    is_default boolean
)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $$
    SELECT COALESCE(s.delivered_retention_days, 30),
           COALESCE(s.config_version, 0),
           s.updated_at,
           (s.tenant_id IS NULL)
      FROM (SELECT p_tenant AS t, p_site AS s) k
      LEFT JOIN iam_v2.site_cloud_sync_settings s
             ON s.tenant_id = k.t AND s.site_id = k.s;
$$;

-- Write. Validates, takes a per-site advisory lock, upserts and writes the change row IN THE SAME
-- TRANSACTION. No runtime role holds UPDATE on the settings table or INSERT on the log, so the audit row is
-- mandatory by privilege and not by convention.
CREATE OR REPLACE FUNCTION iam_v2.cloud_sync_settings_set(
    p_tenant uuid, p_site uuid, p_days integer, p_operator text, p_reason text DEFAULT NULL
) RETURNS bigint
LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $$
DECLARE
    v_old integer;
    v_new_version bigint;
BEGIN
    IF p_operator IS NULL OR length(btrim(p_operator)) = 0 THEN
        RAISE EXCEPTION 'cloud sync settings: an operator identity is required'
            USING ERRCODE = 'invalid_parameter_value';
    END IF;
    IF p_days IS NULL OR p_days < 1 OR p_days > 365 THEN
        RAISE EXCEPTION 'delivered record retention must be between 1 and 365 days (got %)', p_days
            USING ERRCODE = 'check_violation';
    END IF;

    PERFORM pg_advisory_xact_lock(hashtext('cloud_sync_settings'), hashtext(p_site::text));

    SELECT s.delivered_retention_days INTO v_old
      FROM iam_v2.site_cloud_sync_settings s
     WHERE s.tenant_id = p_tenant AND s.site_id = p_site
       FOR UPDATE;

    INSERT INTO iam_v2.site_cloud_sync_settings AS s
           (tenant_id, site_id, delivered_retention_days, config_version, updated_at)
    VALUES (p_tenant, p_site, p_days, 1, now())
    ON CONFLICT (tenant_id, site_id) DO UPDATE
       SET delivered_retention_days = EXCLUDED.delivered_retention_days,
           config_version = s.config_version + 1,
           updated_at = now()
    RETURNING s.config_version INTO v_new_version;

    INSERT INTO iam_v2.cloud_sync_settings_changes
           (tenant_id, site_id, changed_by, reason,
            old_delivered_retention_days, new_delivered_retention_days, new_config_version)
    VALUES (p_tenant, p_site, btrim(p_operator), NULLIF(btrim(COALESCE(p_reason,'')), ''),
            v_old, p_days, v_new_version);

    RETURN v_new_version;
END;
$$;

-- ---------------------------------------------------------------------------------------------------------
-- 2. Recovering records the appliance gave up on.
--
-- THESE LIVE IN iam_v2 WHILE public.sync_outbox DOES NOT, AND THAT ASYMMETRY IS DELIBERATE.
--
-- A live-site migration is applied by a least-privilege NON-superuser -- the runner refuses anything else,
-- and it is right to. That role is iam_v2_owner, which holds no CREATE on schema public: a migration that
-- created objects there fails whole with "permission denied for schema public". The alternatives were to
-- grant iam_v2_owner CREATE on public, which widens a role permanently to solve one migration's problem,
-- or to apply as a superuser, which is the control the runner exists to enforce.
--
-- So the OBJECTS live where the migration's own role may create them, and the TABLE stays exactly where
-- 0001 put it. The functions reach it as SECURITY DEFINER, and the three privileges that requires --
-- SELECT, UPDATE and DELETE on public.sync_outbox for iam_v2_owner -- are granted by Gate-P, which runs as
-- the role that owns that table. No runtime service role gains anything: iam_v2_owner is a NOLOGIN owner.
--
-- The log is written first and is append-only, so a recovery cannot happen without the row that says who
-- asked for it, why, which sequence range moved and how old the oldest record was.
-- ---------------------------------------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS iam_v2.sync_outbox_recovery_log (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    requested_at  timestamptz NOT NULL DEFAULT now(),
    requested_by  text NOT NULL CHECK (length(btrim(requested_by)) > 0),
    reason        text NOT NULL CHECK (length(btrim(reason)) >= 3 AND length(reason) <= 500),
    rows_recovered integer NOT NULL CHECK (rows_recovered >= 0),
    seq_from      bigint,
    seq_to        bigint,
    oldest_created_at timestamptz,
    exhausted_remaining bigint NOT NULL CHECK (exhausted_remaining >= 0)
);

COMMENT ON TABLE iam_v2.sync_outbox_recovery_log IS
  'Append-only record of every time exhausted-retry sync records were returned to the queue. Payloads are never copied here.';

CREATE OR REPLACE FUNCTION iam_v2.sync_outbox_recovery_log_append_only()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'iam_v2.sync_outbox_recovery_log is append-only: % refused', TG_OP
        USING ERRCODE = 'restrict_violation';
END;
$$;

DROP TRIGGER IF EXISTS sync_outbox_recovery_log_no_update ON iam_v2.sync_outbox_recovery_log;
CREATE TRIGGER sync_outbox_recovery_log_no_update
    BEFORE UPDATE OR DELETE ON iam_v2.sync_outbox_recovery_log
    FOR EACH ROW EXECUTE FUNCTION iam_v2.sync_outbox_recovery_log_append_only();

-- Recover a BOUNDED batch of exhausted records, oldest sequence first.
--
-- Bounded on purpose. 9 395 records released at once would be drained by a loop that publishes 100 at a time
-- against a Central that is also serving every other appliance; and if something is wrong with the link, a
-- small batch discovers it cheaply. The caller repeats until exhausted_remaining reaches zero.
--
-- OLDEST FIRST, AND THAT MATTERS HERE. The exhausted records on this appliance are seq 1..9 395 — they are
-- the head of the sequence, older than everything still pending. Central's consumer keys on (appliance, seq)
-- and the appliance drains in seq order, so recovering them before the pending tail drains is what keeps the
-- delivered order the same as the recorded order.
--
-- The payload is not read, not copied and not modified. attempts is reset so the row gets a full retry
-- budget rather than dying again on its next failure; last_error is kept, because why it died the first time
-- is the most useful thing about it.
CREATE OR REPLACE FUNCTION iam_v2.sync_outbox_recover_exhausted(
    p_operator text, p_reason text, p_limit integer DEFAULT 1000
) RETURNS TABLE (rows_recovered integer, seq_from bigint, seq_to bigint, exhausted_remaining bigint)
LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, public, pg_temp AS $$
DECLARE
    v_count integer := 0;
    v_from bigint;
    v_to bigint;
    v_oldest timestamptz;
    v_remaining bigint;
BEGIN
    IF p_operator IS NULL OR length(btrim(p_operator)) = 0 THEN
        RAISE EXCEPTION 'sync outbox recovery: an operator identity is required'
            USING ERRCODE = 'invalid_parameter_value';
    END IF;
    IF p_reason IS NULL OR length(btrim(p_reason)) < 3 THEN
        RAISE EXCEPTION 'sync outbox recovery: a reason of at least 3 characters is required'
            USING ERRCODE = 'invalid_parameter_value';
    END IF;
    IF p_limit IS NULL OR p_limit < 1 OR p_limit > 20000 THEN
        RAISE EXCEPTION 'sync outbox recovery: batch size must be between 1 and 20000 (got %)', p_limit
            USING ERRCODE = 'check_violation';
    END IF;

    -- One recovery at a time. Two concurrent callers would otherwise each release an overlapping batch and
    -- both report having moved it.
    PERFORM pg_advisory_xact_lock(hashtext('sync_outbox_recover'));

    WITH picked AS (
        SELECT o.seq
          FROM public.sync_outbox o
         WHERE o.sent_at IS NULL AND o.dead = true
         ORDER BY o.seq ASC
         LIMIT p_limit
         FOR UPDATE
    ), moved AS (
        UPDATE public.sync_outbox o
           SET dead = false, attempts = 0, next_attempt_at = now()
          FROM picked
         WHERE o.seq = picked.seq
        RETURNING o.seq, o.created_at
    )
    SELECT count(*)::integer, min(seq), max(seq), min(created_at)
      INTO v_count, v_from, v_to, v_oldest
      FROM moved;

    SELECT count(*) INTO v_remaining
      FROM public.sync_outbox o
     WHERE o.sent_at IS NULL AND o.dead = true;

    INSERT INTO iam_v2.sync_outbox_recovery_log
           (requested_by, reason, rows_recovered, seq_from, seq_to, oldest_created_at, exhausted_remaining)
    VALUES (btrim(p_operator), btrim(p_reason), v_count, v_from, v_to, v_oldest, v_remaining);

    RETURN QUERY SELECT v_count, v_from, v_to, v_remaining;
END;
$$;

-- Retention for DELIVERED records only. The WHERE clause names sent_at IS NOT NULL and nothing else: there is
-- no parameter, flag or code path in this function that can reach a record which has not been delivered.
CREATE OR REPLACE FUNCTION iam_v2.sync_outbox_prune_delivered(p_days integer)
RETURNS bigint
LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, public, pg_temp AS $$
DECLARE
    v_deleted bigint;
BEGIN
    IF p_days IS NULL OR p_days < 1 OR p_days > 365 THEN
        RAISE EXCEPTION 'delivered record retention must be between 1 and 365 days (got %)', p_days
            USING ERRCODE = 'check_violation';
    END IF;
    WITH gone AS (
        DELETE FROM public.sync_outbox
         WHERE sent_at IS NOT NULL
           AND sent_at < now() - make_interval(days => p_days)
        RETURNING 1
    )
    SELECT count(*) INTO v_deleted FROM gone;
    RETURN v_deleted;
END;
$$;

-- Every record in exactly one bucket, plus the ages and the size. This exists so "the backlog is recovered"
-- can be CHECKED: delivered + pending + exhausted = total, and an operator or a report can say which of the
-- three a record ended up in rather than inferring it from a falling number.
CREATE OR REPLACE FUNCTION iam_v2.sync_outbox_accounting()
RETURNS TABLE (
    delivered bigint, pending bigint, exhausted bigint, total bigint,
    oldest_pending timestamptz, newest_created timestamptz,
    oldest_exhausted timestamptz, bytes bigint
)
-- plpgsql rather than sql, and not for style. A LANGUAGE sql body is fully resolved when the function is
-- CREATED, so it would refuse to be created anywhere public.sync_outbox is absent — which is every schema
-- fixture that builds iam_v2 alone, including the one the gates run these migrations against. A plpgsql body
-- resolves at call time, so the migration applies on an iam_v2-only schema and the function works wherever
-- the queue actually exists. The same reasoning already applies to the two functions above it.
LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = iam_v2, public, pg_temp AS $$
BEGIN
    RETURN QUERY
    SELECT count(*) FILTER (WHERE o.sent_at IS NOT NULL),
           count(*) FILTER (WHERE o.sent_at IS NULL AND o.dead = false),
           count(*) FILTER (WHERE o.sent_at IS NULL AND o.dead = true),
           count(*),
           min(o.created_at) FILTER (WHERE o.sent_at IS NULL AND o.dead = false),
           max(o.created_at),
           min(o.created_at) FILTER (WHERE o.sent_at IS NULL AND o.dead = true),
           pg_total_relation_size('public.sync_outbox')
      FROM public.sync_outbox o;
END;
$$;

REVOKE ALL ON iam_v2.site_cloud_sync_settings FROM PUBLIC;
REVOKE ALL ON iam_v2.cloud_sync_settings_changes FROM PUBLIC;
REVOKE ALL ON iam_v2.sync_outbox_recovery_log FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.cloud_sync_settings_get(uuid, uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.cloud_sync_settings_set(uuid, uuid, integer, text, text) FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.sync_outbox_recover_exhausted(text, text, integer) FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.sync_outbox_prune_delivered(integer) FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.sync_outbox_accounting() FROM PUBLIC;

DO $own$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
        ALTER TABLE iam_v2.site_cloud_sync_settings   OWNER TO iam_v2_owner;
        ALTER TABLE iam_v2.cloud_sync_settings_changes OWNER TO iam_v2_owner;
        ALTER TABLE iam_v2.sync_outbox_recovery_log   OWNER TO iam_v2_owner;
        ALTER FUNCTION iam_v2.cloud_sync_settings_get(uuid, uuid) OWNER TO iam_v2_owner;
        ALTER FUNCTION iam_v2.cloud_sync_settings_set(uuid, uuid, integer, text, text) OWNER TO iam_v2_owner;
        ALTER FUNCTION iam_v2.cloud_sync_settings_changes_append_only() OWNER TO iam_v2_owner;
        ALTER FUNCTION iam_v2.sync_outbox_recovery_log_append_only() OWNER TO iam_v2_owner;
        ALTER FUNCTION iam_v2.sync_outbox_recover_exhausted(text, text, integer) OWNER TO iam_v2_owner;
        ALTER FUNCTION iam_v2.sync_outbox_prune_delivered(integer) OWNER TO iam_v2_owner;
        ALTER FUNCTION iam_v2.sync_outbox_accounting() OWNER TO iam_v2_owner;
    END IF;
END;
$own$;

-- ==== 0071: the cloud mode ==================================================================================

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
