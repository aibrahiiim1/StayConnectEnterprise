BEGIN;
-- Restores the 0012 financial freshness definition (a live event within feed_freshness_ms) and removes the
-- financial mirror age setting.
CREATE OR REPLACE FUNCTION iam_v2.p4_interface_freshness_block(p_tenant uuid, p_site uuid, p_interface uuid, p_revision uuid, p_at timestamp with time zone) RETURNS text
    LANGUAGE plpgsql STABLE
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE r record; hb_ms bigint; fresh_ms bigint; sync_ms bigint;
BEGIN
  SELECT * INTO r FROM iam_v2.pms_interface_runtime
   WHERE tenant_id = p_tenant AND site_id = p_site AND pms_interface_id = p_interface;
  IF NOT FOUND THEN
    RETURN 'RUNTIME_UNKNOWN';           -- no runtime state at all: fail closed, never assume healthy
  END IF;

  SELECT (config->>'heartbeat_timeout_ms')::bigint, (config->>'feed_freshness_ms')::bigint,
         (config->>'complete_sync_ms')::bigint
    INTO hb_ms, fresh_ms, sync_ms
    FROM iam_v2.pms_interface_revisions
   WHERE tenant_id = p_tenant AND site_id = p_site AND pms_interface_id = p_interface AND id = p_revision;

  -- axis 1: transport
  IF r.transport_status <> 'CONNECTED' THEN RETURN 'TRANSPORT_' || r.transport_status; END IF;
  IF hb_ms IS NOT NULL AND (r.last_heartbeat_at IS NULL
       OR r.last_heartbeat_at < p_at - make_interval(secs => hb_ms / 1000.0)) THEN
    RETURN 'TRANSPORT_HEARTBEAT_STALE';
  END IF;

  -- axis 2: feed continuity
  IF r.continuity_status <> 'CONTINUOUS' THEN RETURN 'CONTINUITY_' || r.continuity_status; END IF;
  IF fresh_ms IS NOT NULL AND (r.last_valid_event_at IS NULL
       OR r.last_valid_event_at < p_at - make_interval(secs => fresh_ms / 1000.0)) THEN
    RETURN 'CONTINUITY_FEED_STALE';
  END IF;

  -- axis 3: complete sync
  IF r.sync_status <> 'IN_SYNC' THEN RETURN 'SYNC_' || r.sync_status; END IF;
  IF sync_ms IS NOT NULL AND (r.last_complete_sync_at IS NULL
       OR r.last_complete_sync_at < p_at - make_interval(secs => sync_ms / 1000.0)) THEN
    RETURN 'SYNC_STALE';
  END IF;

  -- axis 4: pin coherence
  IF r.pinned_revision_id IS DISTINCT FROM p_revision THEN RETURN 'PIN_REVISION_MISMATCH'; END IF;
  IF r.published_resync_generation <> r.resync_generation_seq THEN RETURN 'PIN_RESYNC_IN_FLIGHT'; END IF;

  RETURN NULL;
END $$;

DROP FUNCTION IF EXISTS iam_v2.p4_financial_resync_due(uuid,uuid,uuid);
DROP FUNCTION IF EXISTS iam_v2.p4_set_financial_mirror_max_age(uuid,uuid,uuid,integer,text,uuid);
DROP FUNCTION IF EXISTS iam_v2.p4_financial_mirror_max_age_seconds(uuid,uuid,uuid);
DROP TABLE IF EXISTS iam_v2.pms_interface_financial_setting_changes;
DROP TABLE IF EXISTS iam_v2.pms_interface_financial_settings;
COMMIT;
