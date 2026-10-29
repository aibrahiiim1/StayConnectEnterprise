-- 0175 -- USAGE DETAIL IS ROLLED UP: PER-MINUTE FOR 7 DAYS, HOURLY TOTALS FOR 12 MONTHS (audit AD-48 (a); D1-H3, D1-L7, D7-H2)
--
-- Product-Owner decision AD-48 (a), decided jointly with privacy: per-sample accounting detail
-- (iam_v2.accounting_records, one row per device per pass) outgrew the disk in weeks and nothing removed it as fast
-- as it grew. The grain the hotel needs is kept instead:
--
--   * iam_v2.accounting_usage_minute  per session per minute, kept 7 days   (setting: minute_retention_days)
--   * iam_v2.accounting_usage_hour    per session per hour, kept as long as the site's existing usage-detail
--                                     retention (iam_v2.accounting_retention_get: 365 days by default, 30-3650, or
--                                     unlimited) and under the same rule as before: only sessions that ended before the
--                                     cutoff and whose every entitlement is TERMINATED
--   * the raw samples                 kept only until they have been rolled up and are older than raw_retention_days
--                                     (default 2 days). A sample is NEVER removed before the rollup has read it.
--
-- Additive: new tables and functions; iam_v2.purge_accounting_detail keeps its name and signature (acctd, its grants
-- and the writer guard are unchanged) but now purges the three tiers within a TIME BUDGET that is a setting
-- (purge_budget_seconds, default 10, 1-120): a purge that cannot finish inside it stops and continues on the next run.
-- Entitlement and session totals are not touched. The hourly totals are summed from the same samples the totals
-- came from, so the sum of a session's hours equals what the raw samples summed to.
--
-- The rollup is advanced by a watermark on ingested_at (rows older than one minute only, so a transaction still
-- committing is never skipped). It is idempotent per window: the watermark moves in the same transaction as the
-- upserts. public.accounting_records (the retired hypertable) is NOT touched here; see docs/AUDIT_BACKLOG.md.

BEGIN;

CREATE TABLE IF NOT EXISTS iam_v2.accounting_usage_minute (
  tenant_id  uuid NOT NULL,
  site_id    uuid NOT NULL,
  session_id uuid NOT NULL,
  bucket     timestamptz NOT NULL,
  bytes_up   bigint NOT NULL DEFAULT 0 CONSTRAINT aum_up CHECK (bytes_up >= 0),
  bytes_down bigint NOT NULL DEFAULT 0 CONSTRAINT aum_down CHECK (bytes_down >= 0),
  samples    integer NOT NULL DEFAULT 0,
  PRIMARY KEY (session_id, bucket)
);
CREATE INDEX IF NOT EXISTS aum_site_bucket ON iam_v2.accounting_usage_minute (tenant_id, site_id, bucket);
COMMENT ON TABLE iam_v2.accounting_usage_minute IS
  'Per-session usage per minute, summed from iam_v2.accounting_records by iam_v2.rollup_accounting_usage (0175). Kept minute_retention_days (7).';

CREATE TABLE IF NOT EXISTS iam_v2.accounting_usage_hour (
  tenant_id  uuid NOT NULL,
  site_id    uuid NOT NULL,
  session_id uuid NOT NULL,
  bucket     timestamptz NOT NULL,
  bytes_up   bigint NOT NULL DEFAULT 0 CONSTRAINT auh_up CHECK (bytes_up >= 0),
  bytes_down bigint NOT NULL DEFAULT 0 CONSTRAINT auh_down CHECK (bytes_down >= 0),
  samples    integer NOT NULL DEFAULT 0,
  PRIMARY KEY (session_id, bucket)
);
CREATE INDEX IF NOT EXISTS auh_site_bucket ON iam_v2.accounting_usage_hour (tenant_id, site_id, bucket);
COMMENT ON TABLE iam_v2.accounting_usage_hour IS
  'Per-session usage per hour, summed from iam_v2.accounting_records by iam_v2.rollup_accounting_usage (0175). Kept as long as the site''s usage-detail retention.';

CREATE TABLE IF NOT EXISTS iam_v2.site_usage_rollup_state (
  tenant_id  uuid NOT NULL,
  site_id    uuid NOT NULL,
  watermark  timestamptz NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, site_id)
);
COMMENT ON TABLE iam_v2.site_usage_rollup_state IS
  'How far the usage rollup has read iam_v2.accounting_records, by ingested_at (0175). Raw samples are never purged beyond it.';

-- The settings (CLAUDE.md section 0C): default, unit, bounds, audited.
CREATE TABLE IF NOT EXISTS iam_v2.site_usage_rollup_settings (
  tenant_id             uuid NOT NULL,
  site_id               uuid NOT NULL,
  minute_retention_days integer NOT NULL DEFAULT 7   CONSTRAINT surs_minute CHECK (minute_retention_days BETWEEN 1 AND 90),
  raw_retention_days    integer NOT NULL DEFAULT 2   CONSTRAINT surs_raw CHECK (raw_retention_days BETWEEN 1 AND 30),
  purge_budget_seconds  integer NOT NULL DEFAULT 10  CONSTRAINT surs_budget CHECK (purge_budget_seconds BETWEEN 1 AND 120),
  config_version        bigint NOT NULL DEFAULT 1,
  updated_at            timestamptz NOT NULL DEFAULT now(),
  updated_by            text,
  PRIMARY KEY (tenant_id, site_id)
);
COMMENT ON TABLE iam_v2.site_usage_rollup_settings IS
  'minute_retention_days: days of per-minute usage kept (default 7). raw_retention_days: days of per-sample detail kept once rolled up (default 2). purge_budget_seconds: the most time one purge run may spend deleting (default 10). A site with no row reads the defaults. Written only through iam_v2.usage_rollup_settings_set (0175).';

CREATE TABLE IF NOT EXISTS iam_v2.site_usage_rollup_changes (
  id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id     uuid NOT NULL,
  site_id       uuid NOT NULL,
  changed_by    text NOT NULL CONSTRAINT surc_actor CHECK (length(btrim(changed_by)) > 0),
  change_reason text NOT NULL CONSTRAINT surc_reason CHECK (length(btrim(change_reason)) BETWEEN 4 AND 500),
  old_values    jsonb NOT NULL,
  new_values    jsonb NOT NULL,
  new_config_version bigint NOT NULL,
  changed_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS surc_site ON iam_v2.site_usage_rollup_changes (tenant_id, site_id, changed_at DESC);
DROP TRIGGER IF EXISTS site_usage_rollup_changes_append_only ON iam_v2.site_usage_rollup_changes;
CREATE TRIGGER site_usage_rollup_changes_append_only BEFORE UPDATE OR DELETE ON iam_v2.site_usage_rollup_changes
  FOR EACH ROW EXECUTE FUNCTION iam_v2.accounting_retention_append_only();

CREATE OR REPLACE FUNCTION iam_v2.usage_rollup_settings_get(p_tenant uuid, p_site uuid)
    RETURNS TABLE(minute_retention_days integer, raw_retention_days integer, purge_budget_seconds integer,
                  config_version bigint, updated_at timestamptz, updated_by text)
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
  SELECT COALESCE(s.minute_retention_days, 7), COALESCE(s.raw_retention_days, 2), COALESCE(s.purge_budget_seconds, 10),
         COALESCE(s.config_version, 0), s.updated_at, s.updated_by
    FROM (SELECT 1) one
    LEFT JOIN iam_v2.site_usage_rollup_settings s ON s.tenant_id = p_tenant AND s.site_id = p_site;
$$;

CREATE OR REPLACE FUNCTION iam_v2.usage_rollup_settings_set(p_tenant uuid, p_site uuid, p_minute_days integer,
        p_raw_days integer, p_budget_seconds integer, p_operator text, p_reason text) RETURNS bigint
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE v_old jsonb; v_version bigint;
BEGIN
  IF p_operator IS NULL OR btrim(p_operator) = '' THEN
    RAISE EXCEPTION 'an operator label is required' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  IF p_reason IS NULL OR length(btrim(p_reason)) < 4 OR length(btrim(p_reason)) > 500 THEN
    RAISE EXCEPTION 'a reason of 4-500 characters is required to change the usage rollup settings'
      USING ERRCODE = 'invalid_parameter_value';
  END IF;
  IF p_minute_days NOT BETWEEN 1 AND 90 OR p_raw_days NOT BETWEEN 1 AND 30 OR p_budget_seconds NOT BETWEEN 1 AND 120 THEN
    RAISE EXCEPTION 'minute_retention_days 1-90, raw_retention_days 1-30 and purge_budget_seconds 1-120 are required'
      USING ERRCODE = 'invalid_parameter_value';
  END IF;
  PERFORM pg_advisory_xact_lock(hashtext('usage_rollup_settings'), hashtext(p_site::text));
  SELECT to_jsonb(g) - 'config_version' - 'updated_at' - 'updated_by' INTO v_old
    FROM iam_v2.usage_rollup_settings_get(p_tenant, p_site) g;
  INSERT INTO iam_v2.site_usage_rollup_settings AS s
         (tenant_id, site_id, minute_retention_days, raw_retention_days, purge_budget_seconds, updated_by)
  VALUES (p_tenant, p_site, p_minute_days, p_raw_days, p_budget_seconds, btrim(p_operator))
  ON CONFLICT (tenant_id, site_id) DO UPDATE
     SET minute_retention_days = EXCLUDED.minute_retention_days, raw_retention_days = EXCLUDED.raw_retention_days,
         purge_budget_seconds = EXCLUDED.purge_budget_seconds, updated_by = EXCLUDED.updated_by,
         config_version = s.config_version + 1, updated_at = now()
  RETURNING s.config_version INTO v_version;
  INSERT INTO iam_v2.site_usage_rollup_changes
         (tenant_id, site_id, changed_by, change_reason, old_values, new_values, new_config_version)
  VALUES (p_tenant, p_site, btrim(p_operator), btrim(p_reason), v_old,
          jsonb_build_object('minute_retention_days', p_minute_days, 'raw_retention_days', p_raw_days,
                             'purge_budget_seconds', p_budget_seconds), v_version);
  RETURN v_version;
END $$;

-- The rollup: reads one window of raw samples (by ingested_at) and adds it to the minute and hour tables, then moves
-- the watermark, all in one transaction. Returns the number of raw samples it read. Safe to call at any rate.
CREATE OR REPLACE FUNCTION iam_v2.rollup_accounting_usage(p_tenant uuid, p_site uuid,
        p_max_window interval DEFAULT interval '1 day') RETURNS bigint
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE v_from timestamptz; v_next timestamptz; v_cap timestamptz; v_to timestamptz; v_n bigint;
BEGIN
  IF p_max_window IS NULL OR p_max_window < interval '1 minute' OR p_max_window > interval '7 days' THEN
    RAISE EXCEPTION 'a rollup window of 1 minute to 7 days is required' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  IF NOT pg_try_advisory_xact_lock(hashtext('usage_rollup'), hashtext(p_site::text)) THEN
    RETURN 0;   -- another run is rolling this site up
  END IF;
  SELECT watermark INTO v_from FROM iam_v2.site_usage_rollup_state WHERE tenant_id = p_tenant AND site_id = p_site;
  IF v_from IS NULL THEN
    SELECT min(ar.ingested_at) INTO v_from FROM iam_v2.accounting_records ar
     WHERE ar.tenant_id = p_tenant AND ar.site_id = p_site;
  END IF;
  IF v_from IS NULL THEN RETURN 0; END IF;
  v_cap := now() - interval '1 minute';        -- a row still being committed is never skipped
  IF v_from >= v_cap THEN RETURN 0; END IF;
  -- Skip an empty stretch: start at the first sample at or after the watermark, so a window is never wasted on
  -- nothing and a return of 0 means "nothing left below the cap".
  SELECT min(ar.ingested_at) INTO v_next FROM iam_v2.accounting_records ar
   WHERE ar.tenant_id = p_tenant AND ar.site_id = p_site AND ar.ingested_at >= v_from AND ar.ingested_at < v_cap;
  IF v_next IS NULL THEN
    INSERT INTO iam_v2.site_usage_rollup_state (tenant_id, site_id, watermark)
    VALUES (p_tenant, p_site, v_cap)
    ON CONFLICT (tenant_id, site_id) DO UPDATE SET watermark = EXCLUDED.watermark, updated_at = now();
    RETURN 0;
  END IF;
  v_from := v_next;
  v_to := least(v_cap, v_from + p_max_window);
  WITH src AS MATERIALIZED (
         SELECT ar.session_id, date_trunc('minute', ar.sampled_at) AS mb,
                sum(ar.bytes_up)::bigint AS up, sum(ar.bytes_down)::bigint AS down, count(*)::integer AS n
           FROM iam_v2.accounting_records ar
          WHERE ar.tenant_id = p_tenant AND ar.site_id = p_site
            AND ar.ingested_at >= v_from AND ar.ingested_at < v_to
          GROUP BY 1, 2),
       m AS (
         INSERT INTO iam_v2.accounting_usage_minute AS t (tenant_id, site_id, session_id, bucket, bytes_up, bytes_down, samples)
         SELECT p_tenant, p_site, session_id, mb, up, down, n FROM src
         ON CONFLICT (session_id, bucket) DO UPDATE
            SET bytes_up = t.bytes_up + EXCLUDED.bytes_up, bytes_down = t.bytes_down + EXCLUDED.bytes_down,
                samples = t.samples + EXCLUDED.samples
         RETURNING 1),
       h AS (
         INSERT INTO iam_v2.accounting_usage_hour AS t (tenant_id, site_id, session_id, bucket, bytes_up, bytes_down, samples)
         SELECT p_tenant, p_site, session_id, date_trunc('hour', mb), sum(up)::bigint, sum(down)::bigint, sum(n)::integer
           FROM src GROUP BY session_id, date_trunc('hour', mb)
         ON CONFLICT (session_id, bucket) DO UPDATE
            SET bytes_up = t.bytes_up + EXCLUDED.bytes_up, bytes_down = t.bytes_down + EXCLUDED.bytes_down,
                samples = t.samples + EXCLUDED.samples
         RETURNING 1)
  SELECT COALESCE(sum(n), 0) INTO v_n FROM src;
  INSERT INTO iam_v2.site_usage_rollup_state (tenant_id, site_id, watermark)
  VALUES (p_tenant, p_site, v_to)
  ON CONFLICT (tenant_id, site_id) DO UPDATE SET watermark = EXCLUDED.watermark, updated_at = now();
  RETURN v_n;
END $$;
COMMENT ON FUNCTION iam_v2.rollup_accounting_usage(uuid, uuid, interval) IS
  'Adds one window (at most p_max_window) of raw accounting samples, read by ingested_at, to the per-minute and per-hour usage tables and advances the watermark (0175, AD-48). Returns the samples read.';

-- The purge keeps its name and signature. Three tiers, one TIME BUDGET (purge_budget_seconds):
--   1. raw samples older than raw_retention_days AND already rolled up (never past the watermark);
--   2. per-minute rows older than minute_retention_days;
--   3. hourly rows older than the usage-detail retention (0131), under 0122's rule: ended sessions whose every
--      entitlement is TERMINATED. Unlimited retention (NULL) removes nothing from this tier.
-- p_batch bounds each DELETE. The return value is the rows removed across the tiers; every tier that removed
-- something is recorded in accounting_purge_runs with the retention it applied.
CREATE OR REPLACE FUNCTION iam_v2.purge_accounting_detail(p_tenant uuid, p_site uuid, p_batch integer)
    RETURNS integer
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE
  v_set record; v_wm timestamptz; v_days integer; v_cut timestamptz;
  v_t0 timestamptz := clock_timestamp(); v_budget interval;
  v_n integer; v_tier integer; v_total integer := 0;
BEGIN
  IF p_batch IS NULL OR p_batch NOT BETWEEN 1 AND 100000 THEN
    RAISE EXCEPTION 'a batch of 1-100000 records is required' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  SELECT * INTO v_set FROM iam_v2.usage_rollup_settings_get(p_tenant, p_site);
  v_budget := make_interval(secs => v_set.purge_budget_seconds);
  SELECT watermark INTO v_wm FROM iam_v2.site_usage_rollup_state WHERE tenant_id = p_tenant AND site_id = p_site;
  PERFORM set_config('iam_v2.accounting_purge', 'retention', true);

  -- Tier 1: raw samples that have been rolled up.
  IF v_wm IS NOT NULL THEN
    v_cut := least(now() - make_interval(days => v_set.raw_retention_days), v_wm);
    v_tier := 0;
    LOOP
      WITH doomed AS (
        SELECT ar.id FROM iam_v2.accounting_records ar
         WHERE ar.tenant_id = p_tenant AND ar.site_id = p_site AND ar.ingested_at < v_cut
         LIMIT p_batch)
      DELETE FROM iam_v2.accounting_records ar USING doomed WHERE ar.id = doomed.id;
      GET DIAGNOSTICS v_n = ROW_COUNT;
      v_tier := v_tier + v_n;
      EXIT WHEN v_n < p_batch OR clock_timestamp() - v_t0 > v_budget;
    END LOOP;
    IF v_tier > 0 THEN
      INSERT INTO iam_v2.accounting_purge_runs (tenant_id, site_id, retention_days, cutoff, records_deleted)
      VALUES (p_tenant, p_site, v_set.raw_retention_days, v_cut, v_tier);
      v_total := v_total + v_tier;
    END IF;
  END IF;

  -- Tier 2: per-minute rows.
  IF clock_timestamp() - v_t0 <= v_budget THEN
    v_cut := now() - make_interval(days => v_set.minute_retention_days);
    v_tier := 0;
    LOOP
      DELETE FROM iam_v2.accounting_usage_minute m
       USING (SELECT x.session_id, x.bucket FROM iam_v2.accounting_usage_minute x
               WHERE x.tenant_id = p_tenant AND x.site_id = p_site AND x.bucket < v_cut LIMIT p_batch) d
       WHERE m.session_id = d.session_id AND m.bucket = d.bucket;
      GET DIAGNOSTICS v_n = ROW_COUNT;
      v_tier := v_tier + v_n;
      EXIT WHEN v_n < p_batch OR clock_timestamp() - v_t0 > v_budget;
    END LOOP;
    IF v_tier > 0 THEN
      INSERT INTO iam_v2.accounting_purge_runs (tenant_id, site_id, retention_days, cutoff, records_deleted)
      VALUES (p_tenant, p_site, v_set.minute_retention_days, v_cut, v_tier);
      v_total := v_total + v_tier;
    END IF;
  END IF;

  -- Tier 3: hourly totals, under the site's usage-detail retention.
  SELECT g.detail_retention_days INTO v_days FROM iam_v2.accounting_retention_get(p_tenant, p_site) g;
  IF v_days IS NOT NULL AND clock_timestamp() - v_t0 <= v_budget THEN
    v_cut := now() - make_interval(days => v_days);
    v_tier := 0;
    LOOP
      DELETE FROM iam_v2.accounting_usage_hour h
       USING (SELECT x.session_id, x.bucket
                FROM iam_v2.accounting_usage_hour x
                JOIN iam_v2.sessions se ON se.id = x.session_id
                JOIN iam_v2.entitlements cur ON cur.id = se.entitlement_id
               WHERE x.tenant_id = p_tenant AND x.site_id = p_site
                 AND x.bucket < v_cut
                 AND se.ended IS NOT NULL AND se.ended < v_cut
                 AND cur.status = 'TERMINATED'
                 AND NOT EXISTS (SELECT 1 FROM iam_v2.session_entitlement_bindings b
                                   JOIN iam_v2.entitlements be ON be.id = b.entitlement_id
                                  WHERE b.session_id = x.session_id AND be.status <> 'TERMINATED')
               LIMIT p_batch) d
       WHERE h.session_id = d.session_id AND h.bucket = d.bucket;
      GET DIAGNOSTICS v_n = ROW_COUNT;
      v_tier := v_tier + v_n;
      EXIT WHEN v_n < p_batch OR clock_timestamp() - v_t0 > v_budget;
    END LOOP;
    IF v_tier > 0 THEN
      INSERT INTO iam_v2.accounting_purge_runs (tenant_id, site_id, retention_days, cutoff, records_deleted)
      VALUES (p_tenant, p_site, v_days, v_cut, v_tier);
      v_total := v_total + v_tier;
    END IF;
  END IF;

  PERFORM set_config('iam_v2.accounting_purge', '', true);
  RETURN v_total;
END $$;
COMMENT ON FUNCTION iam_v2.purge_accounting_detail(uuid, uuid, integer) IS
  'The accounting retention purge (0122; 0131; 0175 AD-48): rolled-up raw samples past raw_retention_days, per-minute rows past minute_retention_days, and hourly totals past the usage-detail retention (ended sessions, every entitlement TERMINATED), within purge_budget_seconds. Totals on entitlements and sessions are not touched.';

DO $own$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
    EXECUTE 'ALTER TABLE iam_v2.accounting_usage_minute OWNER TO iam_v2_owner';
    EXECUTE 'ALTER TABLE iam_v2.accounting_usage_hour OWNER TO iam_v2_owner';
    EXECUTE 'ALTER TABLE iam_v2.site_usage_rollup_state OWNER TO iam_v2_owner';
    EXECUTE 'ALTER TABLE iam_v2.site_usage_rollup_settings OWNER TO iam_v2_owner';
    EXECUTE 'ALTER TABLE iam_v2.site_usage_rollup_changes OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.usage_rollup_settings_get(uuid,uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.usage_rollup_settings_set(uuid,uuid,integer,integer,integer,text,text) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.rollup_accounting_usage(uuid,uuid,interval) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.purge_accounting_detail(uuid,uuid,integer) OWNER TO iam_v2_owner';
  END IF;
END $own$;

-- A new function is executable by PUBLIC until that is revoked (cf. 0122, 0161): only the roles named below may call these.
REVOKE ALL ON FUNCTION iam_v2.usage_rollup_settings_get(uuid,uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.usage_rollup_settings_set(uuid,uuid,integer,integer,integer,text,text) FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.rollup_accounting_usage(uuid,uuid,interval) FROM PUBLIC;

-- Grants (mirrored in deploy/gatep/svc-usage-rollup-grants.sql, which survives a Gate-P reconcile).
DO $g$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_acctd') THEN
    GRANT EXECUTE ON FUNCTION iam_v2.rollup_accounting_usage(uuid,uuid,interval) TO svc_acctd;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
    GRANT EXECUTE ON FUNCTION iam_v2.usage_rollup_settings_get(uuid,uuid) TO svc_edged;
    GRANT EXECUTE ON FUNCTION iam_v2.usage_rollup_settings_set(uuid,uuid,integer,integer,integer,text,text) TO svc_edged;
    GRANT SELECT ON iam_v2.accounting_usage_minute, iam_v2.accounting_usage_hour TO svc_edged;
    GRANT SELECT ON iam_v2.site_usage_rollup_changes TO svc_edged;
  END IF;
END $g$;

COMMIT;
