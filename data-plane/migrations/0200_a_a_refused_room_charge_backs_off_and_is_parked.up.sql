-- AD-41 (audit P6 M1): a refused room charge at the head of a posting lane.
--
-- Before: NOT_SENT and a pre-send decline both returned the row to QUEUED, and the worker claimed it again on the
-- next two-second tick. A PMS outage burned about 43,000 P# values and attempt rows a day, and one row the gate
-- refuses blocked every later room charge on its interface.
--
-- After:
--   * posting_outbox carries not_before (the earliest next claim), not_sent_count and decline_count. A NOT_SENT and a
--     decline both set not_before with exponential backoff, 2 s doubling to 5 min. The lane's order is unchanged: while
--     its head row backs off, nothing behind it is claimed.
--   * After N declines of the same row (iam_v2.posting_lane_settings.park_after_declines, default 10, 1..1000, per
--     site, audited) the row is parked in HELD_RECOVERY with the reason, so the lane moves on. NOT_SENT never parks:
--     the PMS link being down is not the row's fault, and the backoff alone stops the P# burn.
--   * A parked row leaves HELD_RECOVERY only through iam_v2.posting_outbox_requeue_parked, which needs an operator and
--     a reason, refuses any posting that has a possibly-transmitted attempt, and writes the park ledger. Nothing here
--     touches a row that was HELD_RECOVERY for any other reason (an UNKNOWN outcome stays on the audited review path).
--
-- Additive and idempotent. Existing rows get not_before = the migration time (claimable at once) and zero counts.

ALTER TABLE iam_v2.posting_outbox
    ADD COLUMN IF NOT EXISTS not_before      timestamptz NOT NULL DEFAULT now(),
    ADD COLUMN IF NOT EXISTS not_sent_count  integer     NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS decline_count   integer     NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS parked_reason   text;

DO $$ BEGIN
    ALTER TABLE iam_v2.posting_outbox
        ADD CONSTRAINT posting_outbox_counts_nonnegative CHECK (not_sent_count >= 0 AND decline_count >= 0);
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

CREATE INDEX IF NOT EXISTS outbox_queued_by_lane
    ON iam_v2.posting_outbox (pms_interface_id, enqueued_at, id) WHERE state = 'QUEUED';

-- ---------------------------------------------------------------------------------------------------------
-- The setting (CLAUDE.md section 0C): per site, a default, a unit, a bound, a versioned audited writer.
-- ---------------------------------------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS iam_v2.posting_lane_settings (
    tenant_id            uuid NOT NULL,
    site_id              uuid NOT NULL,
    park_after_declines  integer NOT NULL DEFAULT 10,
    config_version       bigint  NOT NULL DEFAULT 1,
    updated_at           timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, site_id),
    CONSTRAINT pls_park_bounded CHECK (park_after_declines BETWEEN 1 AND 1000),
    CONSTRAINT pls_version_positive CHECK (config_version >= 1)
);

CREATE TABLE IF NOT EXISTS iam_v2.posting_lane_settings_changes (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id          uuid NOT NULL,
    site_id            uuid NOT NULL,
    changed_at         timestamptz NOT NULL DEFAULT now(),
    changed_by         text NOT NULL CHECK (length(btrim(changed_by)) > 0),
    reason             text NOT NULL CHECK (length(btrim(reason)) BETWEEN 3 AND 500),
    old_values         jsonb,
    new_values         jsonb NOT NULL,
    new_config_version bigint NOT NULL
);
CREATE INDEX IF NOT EXISTS pls_changes_scope_idx
    ON iam_v2.posting_lane_settings_changes (tenant_id, site_id, changed_at DESC);

-- The park ledger: every park and every operator release, append-only.
CREATE TABLE IF NOT EXISTS iam_v2.posting_outbox_park_events (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   uuid NOT NULL,
    site_id     uuid NOT NULL,
    outbox_id   uuid NOT NULL,
    posting_id  uuid NOT NULL,
    event       text NOT NULL CHECK (event IN ('PARKED','REQUEUED')),
    decline_count integer NOT NULL,
    reason      text,
    actor       text NOT NULL,
    at          timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS pope_scope_idx ON iam_v2.posting_outbox_park_events (tenant_id, site_id, at DESC);

CREATE OR REPLACE FUNCTION iam_v2.posting_lane_append_only() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION '% is append-only: % refused', TG_TABLE_NAME, TG_OP USING ERRCODE = 'restrict_violation';
END;
$$;
DROP TRIGGER IF EXISTS posting_lane_settings_changes_append_only ON iam_v2.posting_lane_settings_changes;
CREATE TRIGGER posting_lane_settings_changes_append_only
    BEFORE UPDATE OR DELETE ON iam_v2.posting_lane_settings_changes
    FOR EACH ROW EXECUTE FUNCTION iam_v2.posting_lane_append_only();
DROP TRIGGER IF EXISTS posting_outbox_park_events_append_only ON iam_v2.posting_outbox_park_events;
CREATE TRIGGER posting_outbox_park_events_append_only
    BEFORE UPDATE OR DELETE ON iam_v2.posting_outbox_park_events
    FOR EACH ROW EXECUTE FUNCTION iam_v2.posting_lane_append_only();

CREATE OR REPLACE FUNCTION iam_v2.posting_lane_settings_get(p_tenant uuid, p_site uuid)
RETURNS TABLE (park_after_declines integer, config_version bigint, is_default boolean)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $$
    SELECT COALESCE(s.park_after_declines, 10), COALESCE(s.config_version, 0), (s.tenant_id IS NULL)
      FROM (SELECT p_tenant AS t, p_site AS si) k
      LEFT JOIN iam_v2.posting_lane_settings s ON s.tenant_id = k.t AND s.site_id = k.si;
$$;

CREATE OR REPLACE FUNCTION iam_v2.posting_lane_settings_set(
    p_tenant uuid, p_site uuid, p_expected_version bigint, p_park_after integer, p_operator text, p_reason text
) RETURNS bigint
LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $$
DECLARE v_old jsonb; v_cur bigint; v_ver bigint;
BEGIN
    IF p_operator IS NULL OR length(btrim(p_operator)) = 0 THEN
        RAISE EXCEPTION 'LANE_SETTING_ACTOR: an operator identity is required' USING ERRCODE = 'invalid_parameter_value';
    END IF;
    IF p_reason IS NULL OR length(btrim(p_reason)) < 3 OR length(p_reason) > 500 THEN
        RAISE EXCEPTION 'LANE_SETTING_REASON: a reason of 3 to 500 characters is required' USING ERRCODE = 'invalid_parameter_value';
    END IF;
    IF p_park_after IS NULL OR p_park_after < 1 OR p_park_after > 1000 THEN
        RAISE EXCEPTION 'LANE_SETTING_BOUNDS: the number of refusals must be between 1 and 1000' USING ERRCODE = 'check_violation';
    END IF;
    PERFORM pg_advisory_xact_lock(hashtext('posting_lane_settings'), hashtext(p_site::text));
    SELECT to_jsonb(s), s.config_version INTO v_old, v_cur
      FROM iam_v2.posting_lane_settings s WHERE s.tenant_id = p_tenant AND s.site_id = p_site FOR UPDATE;
    IF p_expected_version IS NULL OR COALESCE(v_cur, 0) <> p_expected_version THEN
        RAISE EXCEPTION 'LANE_SETTING_VERSION_CONFLICT: the setting changed (now version %); reload and try again', COALESCE(v_cur,0)
            USING ERRCODE = 'serialization_failure';
    END IF;
    INSERT INTO iam_v2.posting_lane_settings AS s (tenant_id, site_id, park_after_declines, config_version, updated_at)
    VALUES (p_tenant, p_site, p_park_after, 1, now())
    ON CONFLICT (tenant_id, site_id) DO UPDATE
       SET park_after_declines = EXCLUDED.park_after_declines, config_version = s.config_version + 1, updated_at = now()
    RETURNING s.config_version INTO v_ver;
    INSERT INTO iam_v2.posting_lane_settings_changes
           (tenant_id, site_id, changed_by, reason, old_values, new_values, new_config_version)
    VALUES (p_tenant, p_site, btrim(p_operator), btrim(p_reason), v_old,
            jsonb_build_object('park_after_declines', p_park_after), v_ver);
    RETURN v_ver;
END;
$$;

-- ---------------------------------------------------------------------------------------------------------
-- The worker's single deferral entry point. Definer, so the posting role needs no UPDATE on the new columns.
--   p_kind 'NOT_SENT'  -> backoff only (the link is down; never parks)
--   p_kind 'DECLINED'  -> backoff; the Nth decline of the same row parks it in HELD_RECOVERY
-- Returns 'QUEUED' (backing off), 'PARKED', or the row's current state when it is no longer ours to move (a
-- financial-recovery hold took it first), in which case nothing changes.
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION iam_v2.posting_outbox_defer(p_outbox uuid, p_kind text, p_reason text)
RETURNS text
LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $$
DECLARE o record; v_n integer; v_delay integer; v_park integer;
BEGIN
    IF p_kind NOT IN ('NOT_SENT','DECLINED') THEN
        RAISE EXCEPTION 'LANE_DEFER_KIND: unknown kind %', p_kind USING ERRCODE = 'invalid_parameter_value';
    END IF;
    SELECT * INTO o FROM iam_v2.posting_outbox WHERE id = p_outbox FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'LANE_DEFER_UNKNOWN: no such outbox row' USING ERRCODE = 'no_data_found';
    END IF;
    IF o.state NOT IN ('IN_FLIGHT','QUEUED') THEN
        RETURN o.state;
    END IF;
    IF p_kind = 'NOT_SENT' THEN
        v_n := o.not_sent_count + 1;
    ELSE
        v_n := o.decline_count + 1;
    END IF;
    v_delay := LEAST(300, 2 * (2 ^ LEAST(v_n - 1, 10))::integer);

    IF p_kind = 'DECLINED' THEN
        SELECT COALESCE(s.park_after_declines, 10) INTO v_park
          FROM (SELECT 1) one LEFT JOIN iam_v2.posting_lane_settings s
            ON s.tenant_id = o.tenant_id AND s.site_id = o.site_id;
        IF v_n >= v_park THEN
            UPDATE iam_v2.posting_outbox
               SET state = 'HELD_RECOVERY', decline_count = v_n,
                   parked_reason = left(COALESCE(p_reason, 'declined'), 200)
             WHERE id = o.id;
            INSERT INTO iam_v2.posting_outbox_park_events
                   (tenant_id, site_id, outbox_id, posting_id, event, decline_count, reason, actor)
            VALUES (o.tenant_id, o.site_id, o.id, o.posting_id, 'PARKED', v_n, left(COALESCE(p_reason,'declined'), 200), 'posting worker');
            RETURN 'PARKED';
        END IF;
        UPDATE iam_v2.posting_outbox
           SET state = 'QUEUED', decline_count = v_n, not_before = now() + make_interval(secs => v_delay)
         WHERE id = o.id;
    ELSE
        UPDATE iam_v2.posting_outbox
           SET state = 'QUEUED', not_sent_count = v_n, not_before = now() + make_interval(secs => v_delay)
         WHERE id = o.id;
    END IF;
    RETURN 'QUEUED';
END;
$$;

-- ---------------------------------------------------------------------------------------------------------
-- The operator's way back for a parked row. It is the only thing that moves a parked row, and it refuses anything
-- the PMS may have seen.
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION iam_v2.posting_outbox_requeue_parked(
    p_tenant uuid, p_site uuid, p_outbox uuid, p_operator text, p_reason text
) RETURNS text
LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $$
DECLARE o record; v_sent integer;
BEGIN
    IF p_operator IS NULL OR length(btrim(p_operator)) = 0 THEN
        RAISE EXCEPTION 'LANE_REQUEUE_ACTOR: an operator identity is required' USING ERRCODE = 'invalid_parameter_value';
    END IF;
    IF p_reason IS NULL OR length(btrim(p_reason)) < 3 OR length(p_reason) > 500 THEN
        RAISE EXCEPTION 'LANE_REQUEUE_REASON: a reason of 3 to 500 characters is required' USING ERRCODE = 'invalid_parameter_value';
    END IF;
    SELECT * INTO o FROM iam_v2.posting_outbox
     WHERE id = p_outbox AND tenant_id = p_tenant AND site_id = p_site FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'LANE_REQUEUE_UNKNOWN: no such outbox row in this site' USING ERRCODE = 'no_data_found';
    END IF;
    IF o.state <> 'HELD_RECOVERY' OR o.parked_reason IS NULL THEN
        RAISE EXCEPTION 'LANE_REQUEUE_NOT_PARKED: this row was not parked by repeated refusals' USING ERRCODE = 'check_violation';
    END IF;
    SELECT count(*) INTO v_sent FROM iam_v2.posting_attempts
     WHERE internal_posting_id = o.posting_id AND outcome <> 'NOT_SENT';
    IF v_sent > 0 THEN
        RAISE EXCEPTION 'LANE_REQUEUE_MAY_HAVE_SENT: an attempt on this posting may have reached the PMS; it needs the audited review decision'
            USING ERRCODE = 'check_violation';
    END IF;
    -- The recovery gate allows leaving HELD_RECOVERY under this transaction-local flag, which only the definer sets.
    PERFORM set_config('iam_v2.p4_recovery_reconciling', 'on', true);
    UPDATE iam_v2.posting_outbox
       SET state = 'QUEUED', decline_count = 0, parked_reason = NULL, not_before = now()
     WHERE id = o.id;
    PERFORM set_config('iam_v2.p4_recovery_reconciling', 'off', true);
    INSERT INTO iam_v2.posting_outbox_park_events
           (tenant_id, site_id, outbox_id, posting_id, event, decline_count, reason, actor)
    VALUES (o.tenant_id, o.site_id, o.id, o.posting_id, 'REQUEUED', o.decline_count, btrim(p_reason), btrim(p_operator));
    RETURN 'QUEUED';
END;
$$;

REVOKE ALL ON FUNCTION iam_v2.posting_lane_settings_get(uuid,uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.posting_lane_settings_set(uuid,uuid,bigint,integer,text,text) FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.posting_outbox_defer(uuid,text,text) FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.posting_outbox_requeue_parked(uuid,uuid,uuid,text,text) FROM PUBLIC;

DO $own$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
    EXECUTE 'ALTER TABLE iam_v2.posting_lane_settings OWNER TO iam_v2_owner';
    EXECUTE 'ALTER TABLE iam_v2.posting_lane_settings_changes OWNER TO iam_v2_owner';
    EXECUTE 'ALTER TABLE iam_v2.posting_outbox_park_events OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.posting_lane_append_only() OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.posting_lane_settings_get(uuid,uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.posting_lane_settings_set(uuid,uuid,bigint,integer,text,text) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.posting_outbox_defer(uuid,text,text) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.posting_outbox_requeue_parked(uuid,uuid,uuid,text,text) OWNER TO iam_v2_owner';
  END IF;
END $own$;

-- Grants are mirrored in deploy/gatep (svc_edged) so a reconcile keeps them; sc_posting_runtime is a group role the
-- numbered migrations grant directly (as 0097 did).
DO $g$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'sc_posting_runtime') THEN
    GRANT EXECUTE ON FUNCTION iam_v2.posting_outbox_defer(uuid,text,text) TO sc_posting_runtime;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
    GRANT EXECUTE ON FUNCTION iam_v2.posting_lane_settings_get(uuid,uuid) TO svc_edged;
    GRANT EXECUTE ON FUNCTION iam_v2.posting_lane_settings_set(uuid,uuid,bigint,integer,text,text) TO svc_edged;
    GRANT EXECUTE ON FUNCTION iam_v2.posting_outbox_requeue_parked(uuid,uuid,uuid,text,text) TO svc_edged;
  END IF;
END $g$;
