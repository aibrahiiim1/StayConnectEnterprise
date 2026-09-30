-- ROOM-CHARGE FRESHNESS IS FEED HEALTH PLUS A PROVEN MIRROR AGE, NOT "AN EVENT IN THE LAST 15 MINUTES".
-- Product-Owner decision of 2026-09-30 (recorded as D48), on PR #202.
--
-- THE RULE BEING REPLACED. p4_interface_freshness_block (0012) required a LIVE guest event within the
-- revision's feed_freshness_ms. last_valid_event_at is stamped only when Protel sends a guest record, and a hotel
-- is quiet for long stretches: on PRE-LIVE, over seven days, that rule blocked room charge 47% of the time on a
-- connected, heartbeat-healthy, IN_SYNC, CONTINUOUS interface. It contradicted the FINAL Phase-0 contract (§9 axis
-- 2: "never a naive 'no events for N minutes' rule") and the accepted room sign-in rule (0050: silence from a
-- healthy feed is confirmation, not decay).
--
-- WHAT A ROOM CHARGE NOW REQUIRES, at every stage (offer, posting creation, worker claim, abort-before-send,
-- attempt creation, pmsd pre-wire authorisation -- all of which call this one function):
--
--   transport    CONNECTED, and Protel's own link-alive within heartbeat_timeout_ms (default 5 min if the
--                revision states none: fail closed, never "unchecked")
--   continuity   CONTINUOUS -- no detected gap since the last complete resync on this connection
--   sync         IN_SYNC, and a complete sync within complete_sync_ms where the revision states one
--   MIRROR AGE   a SUCCESSFUL COMPLETE RESYNC within the interface's financial mirror maximum age. A live guest
--                event updates its own stay but is NOT proof that the whole mirror has missed nothing, so it
--                does not reset this age. (FINANCIAL_MIRROR_STALE)
--   pin          the runtime is pinned to the posting's interface revision, and no resync generation is
--                part-published
--
-- The financial mirror maximum age is a per-interface OPERATIONAL SETTING (CLAUDE.md §0C): default 4 hours,
-- bounded 1..24 hours, changed only through an audited definer by an active operator with a reason; every change
-- is kept in an append-only log. pmsd keeps a quiet link proving itself by requesting a read-only full resync
-- once half the bound has passed (p4_financial_resync_due); if no complete resync succeeds before the bound,
-- room charge fails closed until one does.
--
-- Unchanged: every stay/G#/RN/posting-block check (Amendment A1, D46), UNKNOWN and no automatic retry.
BEGIN;

-- ---------------------------------------------------------------------------------------------------------
-- 1. The setting and its audit log
-- ---------------------------------------------------------------------------------------------------------
CREATE TABLE iam_v2.pms_interface_financial_settings (
  tenant_id              uuid NOT NULL,
  site_id                uuid NOT NULL,
  pms_interface_id       uuid PRIMARY KEY REFERENCES iam_v2.pms_interfaces(id) ON DELETE CASCADE,
  mirror_max_age_seconds integer NOT NULL CHECK (mirror_max_age_seconds BETWEEN 3600 AND 86400),
  updated_at             timestamptz NOT NULL DEFAULT now(),
  updated_by             uuid NOT NULL
);

CREATE TABLE iam_v2.pms_interface_financial_setting_changes (
  id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id         uuid NOT NULL,
  site_id           uuid NOT NULL,
  pms_interface_id  uuid NOT NULL REFERENCES iam_v2.pms_interfaces(id) ON DELETE CASCADE,
  setting           text NOT NULL CHECK (setting = 'mirror_max_age_seconds'),
  old_value         integer,
  new_value         integer NOT NULL,
  reason            text NOT NULL CHECK (length(btrim(reason)) BETWEEN 4 AND 500),
  changed_by        uuid NOT NULL,
  changed_at        timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX pms_interface_financial_setting_changes_iface ON iam_v2.pms_interface_financial_setting_changes (pms_interface_id, changed_at DESC);
CREATE TRIGGER pms_interface_financial_setting_changes_append_only BEFORE UPDATE OR DELETE
  ON iam_v2.pms_interface_financial_setting_changes FOR EACH ROW EXECUTE FUNCTION iam_v2.p4_append_only_refuse();

-- The effective value: the interface's own setting, or the 4-hour default.
CREATE FUNCTION iam_v2.p4_financial_mirror_max_age_seconds(p_tenant uuid, p_site uuid, p_iface uuid)
  RETURNS integer
  LANGUAGE sql STABLE SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
  SELECT COALESCE((SELECT s.mirror_max_age_seconds FROM iam_v2.pms_interface_financial_settings s
                    WHERE s.tenant_id = p_tenant AND s.site_id = p_site AND s.pms_interface_id = p_iface), 14400);
$fn$;

-- The only writer. Returns the new value.
CREATE FUNCTION iam_v2.p4_set_financial_mirror_max_age(p_tenant uuid, p_site uuid, p_iface uuid,
    p_seconds integer, p_reason text, p_actor uuid)
  RETURNS integer
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE v_status text; v_old integer;
BEGIN
  IF p_seconds IS NULL OR p_seconds < 3600 OR p_seconds > 86400 THEN
    RAISE EXCEPTION 'MIRROR_AGE_OUT_OF_RANGE: the financial mirror maximum age is between 1 and 24 hours'
      USING ERRCODE = 'check_violation';
  END IF;
  IF p_reason IS NULL OR length(btrim(p_reason)) < 4 OR length(p_reason) > 500 THEN
    RAISE EXCEPTION 'MIRROR_AGE_REASON: a reason is required' USING ERRCODE = 'check_violation';
  END IF;
  SELECT status INTO v_status FROM public.operators WHERE id = p_actor AND tenant_id = p_tenant;
  IF v_status IS DISTINCT FROM 'active' THEN
    RAISE EXCEPTION 'MIRROR_AGE_ACTOR: an active operator of this tenant is required' USING ERRCODE = 'check_violation';
  END IF;
  IF NOT EXISTS (SELECT 1 FROM iam_v2.pms_interfaces WHERE tenant_id = p_tenant AND site_id = p_site AND id = p_iface) THEN
    RAISE EXCEPTION 'MIRROR_AGE_INTERFACE: no such interface at this site' USING ERRCODE = 'check_violation';
  END IF;
  SELECT mirror_max_age_seconds INTO v_old FROM iam_v2.pms_interface_financial_settings WHERE pms_interface_id = p_iface;
  INSERT INTO iam_v2.pms_interface_financial_settings (tenant_id, site_id, pms_interface_id, mirror_max_age_seconds, updated_at, updated_by)
  VALUES (p_tenant, p_site, p_iface, p_seconds, now(), p_actor)
  ON CONFLICT (pms_interface_id) DO UPDATE SET mirror_max_age_seconds = EXCLUDED.mirror_max_age_seconds,
    updated_at = EXCLUDED.updated_at, updated_by = EXCLUDED.updated_by;
  INSERT INTO iam_v2.pms_interface_financial_setting_changes
    (tenant_id, site_id, pms_interface_id, setting, old_value, new_value, reason, changed_by)
  VALUES (p_tenant, p_site, p_iface, 'mirror_max_age_seconds', v_old, p_seconds, btrim(p_reason), p_actor);
  RETURN p_seconds;
END $fn$;

-- ---------------------------------------------------------------------------------------------------------
-- 2. The one authoritative financial freshness definition
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION iam_v2.p4_interface_freshness_block(p_tenant uuid, p_site uuid, p_interface uuid,
    p_revision uuid, p_at timestamptz)
  RETURNS text
  LANGUAGE plpgsql STABLE SET search_path = iam_v2, pg_temp AS $fn$
DECLARE r record; hb_ms bigint; sync_ms bigint; v_age integer;
BEGIN
  SELECT * INTO r FROM iam_v2.pms_interface_runtime
   WHERE tenant_id = p_tenant AND site_id = p_site AND pms_interface_id = p_interface;
  IF NOT FOUND THEN
    RETURN 'RUNTIME_UNKNOWN';           -- no runtime state at all: fail closed, never assume healthy
  END IF;

  SELECT (config->>'heartbeat_timeout_ms')::bigint, (config->>'complete_sync_ms')::bigint
    INTO hb_ms, sync_ms
    FROM iam_v2.pms_interface_revisions
   WHERE tenant_id = p_tenant AND site_id = p_site AND pms_interface_id = p_interface AND id = p_revision;

  -- axis 1: transport -- connected, and Protel's own link-alive recently observed
  IF r.transport_status <> 'CONNECTED' THEN RETURN 'TRANSPORT_' || r.transport_status; END IF;
  IF r.last_heartbeat_at IS NULL
     OR r.last_heartbeat_at < p_at - make_interval(secs => COALESCE(hb_ms, 300000) / 1000.0) THEN
    RETURN 'TRANSPORT_HEARTBEAT_STALE';
  END IF;

  -- axis 2: continuity -- no detected gap. Silence from a healthy feed is NOT a gap.
  IF r.continuity_status <> 'CONTINUOUS' THEN RETURN 'CONTINUITY_' || r.continuity_status; END IF;

  -- axis 3: complete sync
  IF r.sync_status <> 'IN_SYNC' THEN RETURN 'SYNC_' || r.sync_status; END IF;
  IF sync_ms IS NOT NULL AND (r.last_complete_sync_at IS NULL
       OR r.last_complete_sync_at < p_at - make_interval(secs => sync_ms / 1000.0)) THEN
    RETURN 'SYNC_STALE';
  END IF;

  -- axis 3b: the financial mirror age, proven only by a successful COMPLETE resync (a live event does not reset it)
  v_age := iam_v2.p4_financial_mirror_max_age_seconds(p_tenant, p_site, p_interface);
  IF r.last_complete_sync_at IS NULL OR r.last_complete_sync_at < p_at - make_interval(secs => v_age) THEN
    RETURN 'FINANCIAL_MIRROR_STALE';
  END IF;

  -- axis 4: pin coherence
  IF r.pinned_revision_id IS DISTINCT FROM p_revision THEN RETURN 'PIN_REVISION_MISMATCH'; END IF;
  IF r.published_resync_generation <> r.resync_generation_seq THEN RETURN 'PIN_RESYNC_IN_FLIGHT'; END IF;

  RETURN NULL;
END $fn$;

-- ---------------------------------------------------------------------------------------------------------
-- 3. When pmsd should prove the mirror again (half the bound), asked by pmsd itself
-- ---------------------------------------------------------------------------------------------------------
-- True only for an interface that is financially onboarded and otherwise healthy, whose last complete resync
-- is older than half its financial mirror maximum age, with no resync already in flight. pmsd adds its own
-- serialization (never while a DS..DE window or a DR is outstanding) and backoff between attempts.
CREATE FUNCTION iam_v2.p4_financial_resync_due(p_tenant uuid, p_site uuid, p_iface uuid)
  RETURNS boolean
  LANGUAGE sql STABLE SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
  SELECT COALESCE((
    SELECT rt.transport_status = 'CONNECTED'
       AND rt.sync_status = 'IN_SYNC'
       AND rt.resync_started_at IS NULL
       AND rt.published_resync_generation = rt.resync_generation_seq
       AND rt.last_complete_sync_at IS NOT NULL
       AND rt.last_complete_sync_at < now() - make_interval(secs =>
             iam_v2.p4_financial_mirror_max_age_seconds(p_tenant, p_site, p_iface) / 2.0)
       AND COALESCE((SELECT r.ready FROM iam_v2.pms_interface_financially_ready(p_tenant, p_site, p_iface) r), false)
      FROM iam_v2.pms_interface_runtime rt
     WHERE rt.tenant_id = p_tenant AND rt.site_id = p_site AND rt.pms_interface_id = p_iface), false);
$fn$;

-- ---------------------------------------------------------------------------------------------------------
-- 4. Privileges and ownership
-- ---------------------------------------------------------------------------------------------------------
REVOKE ALL ON iam_v2.pms_interface_financial_settings FROM PUBLIC;
REVOKE ALL ON iam_v2.pms_interface_financial_setting_changes FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.p4_financial_mirror_max_age_seconds(uuid,uuid,uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.p4_set_financial_mirror_max_age(uuid,uuid,uuid,integer,text,uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.p4_financial_resync_due(uuid,uuid,uuid) FROM PUBLIC;
-- The freshness function is invoker and runs as the posting worker's role when an attempt is inserted.
GRANT EXECUTE ON FUNCTION iam_v2.p4_financial_mirror_max_age_seconds(uuid,uuid,uuid) TO sc_posting_runtime;

DO $grant$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
    GRANT SELECT ON iam_v2.pms_interface_financial_settings, iam_v2.pms_interface_financial_setting_changes TO svc_edged;
    GRANT EXECUTE ON FUNCTION iam_v2.p4_financial_mirror_max_age_seconds(uuid,uuid,uuid) TO svc_edged;
    GRANT EXECUTE ON FUNCTION iam_v2.p4_set_financial_mirror_max_age(uuid,uuid,uuid,integer,text,uuid) TO svc_edged;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_pmsd') THEN
    GRANT EXECUTE ON FUNCTION iam_v2.p4_financial_resync_due(uuid,uuid,uuid) TO svc_pmsd;
  END IF;
END
$grant$;

DO $own$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
    EXECUTE 'ALTER TABLE iam_v2.pms_interface_financial_settings OWNER TO iam_v2_owner';
    EXECUTE 'ALTER TABLE iam_v2.pms_interface_financial_setting_changes OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.p4_financial_mirror_max_age_seconds(uuid,uuid,uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.p4_set_financial_mirror_max_age(uuid,uuid,uuid,integer,text,uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.p4_financial_resync_due(uuid,uuid,uuid) OWNER TO iam_v2_owner';
  END IF;
END
$own$;

COMMIT;
