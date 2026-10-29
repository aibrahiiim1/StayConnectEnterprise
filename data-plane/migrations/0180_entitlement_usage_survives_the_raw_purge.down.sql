-- 0180 down -- back to the raw-only readers of 0065 / 0062 / 0122 and the purge of 0175.
--
-- WHAT ROLLING BACK COSTS: the accumulator table is dropped, and with it the usage of every raw sample the purge folded into it
-- since 0180. The three readers then read only the raw samples that remain (the defect 0180 fixed), so a stay longer than the
-- raw retention reads less than it used. entitlements.consumed_data_bytes and the session totals are not touched and stay true.
-- Grants are kept: CREATE OR REPLACE does not change a function's ACL.

BEGIN;

CREATE OR REPLACE FUNCTION iam_v2.entitlement_usage_bytes(p_ent uuid, p_at timestamptz)
RETURNS TABLE(bytes_up bigint, bytes_down bigint, records bigint, latest_sampled_at timestamptz)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = iam_v2, pg_temp
AS $$
  SELECT COALESCE(sum(ar.bytes_up),0)::bigint, COALESCE(sum(ar.bytes_down),0)::bigint,
         count(*)::bigint, max(ar.sampled_at)
  FROM iam_v2.accounting_records ar
  JOIN iam_v2.sessions s ON s.id = ar.session_id
  WHERE ar.sampled_at <= p_at
    AND (
      EXISTS (SELECT 1 FROM iam_v2.session_entitlement_bindings b
              WHERE b.session_id = ar.session_id AND b.entitlement_id = p_ent
                AND b.bound_from <= ar.sampled_at AND (b.bound_until IS NULL OR b.bound_until > ar.sampled_at))
      OR (s.entitlement_id = p_ent
          AND NOT EXISTS (SELECT 1 FROM iam_v2.session_entitlement_bindings b2 WHERE b2.session_id = ar.session_id))
    );
$$;

COMMENT ON FUNCTION iam_v2.entitlement_usage_bytes(uuid, timestamptz) IS
  'Bytes attributed to ONE Entitlement up to ONE instant, by binding interval, with the no-binding fallback '
  'for sessions that predate the binding table. SECURITY DEFINER so a caller can obtain the aggregate without '
  'holding SELECT on iam_v2.accounting_records -- svc_pmsd needs the number at a checkout boundary and Gate-P '
  'forbids it the table. It returns four aggregate numbers about an entitlement the caller must already be '
  'able to name; it exposes no guest, Stay, reservation, folio, payment or PMS data, and it writes nothing.';

CREATE OR REPLACE FUNCTION iam_v2.p3_entitlement_data_usage(p_entitlement uuid)
RETURNS bigint
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = iam_v2, pg_temp
AS $usage$
  SELECT COALESCE(sum(ar.bytes_up + ar.bytes_down), 0)::bigint
    FROM iam_v2.accounting_records ar
    JOIN iam_v2.session_entitlement_bindings b
      ON b.session_id = ar.session_id
     AND b.entitlement_id = p_entitlement
     AND b.bound_from <= ar.sampled_at
     AND (
           b.bound_until IS NULL
        OR b.bound_until > ar.sampled_at
        OR ( b.bound_until = ar.sampled_at
             AND NOT EXISTS (
                   SELECT 1
                     FROM iam_v2.session_entitlement_bindings b2
                    WHERE b2.session_id = ar.session_id
                      AND b2.bound_from <= ar.sampled_at
                      AND (b2.bound_until IS NULL OR b2.bound_until > ar.sampled_at)) )
         )
$usage$;

COMMENT ON FUNCTION iam_v2.p3_entitlement_data_usage(uuid) IS
  'Authoritative bytes attributed to an Entitlement across every Session and Device bound to it. Attribution '
  'follows the half-open binding interval, plus the closing instant itself when no other binding for that '
  'session covers it - which is the terminal case, where the sample that exhausted the quota would otherwise '
  'be attributed to nothing. A Phase-5 rebinding always has a successor covering the boundary, so that arm '
  'cannot fire there and no sample is counted twice. entitlements.consumed_data_bytes must equal this.';

CREATE OR REPLACE FUNCTION iam_v2.p6_data_crossing_full(p_entitlement uuid) RETURNS timestamp with time zone
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
  SELECT min(x.sampled_at)
    FROM iam_v2.entitlements e
    LEFT JOIN iam_v2.service_plan_revisions spr ON spr.id = e.service_plan_revision_id
    CROSS JOIN LATERAL (
      SELECT ar.sampled_at,
             sum(ar.bytes_up + ar.bytes_down) OVER (ORDER BY ar.sampled_at, ar.id) AS running
        FROM iam_v2.accounting_records ar
        JOIN iam_v2.session_entitlement_bindings b ON b.session_id = ar.session_id
         AND b.entitlement_id = e.id AND b.bound_from <= ar.sampled_at
         AND (b.bound_until IS NULL OR b.bound_until > ar.sampled_at)
    ) x
   WHERE e.id = p_entitlement
     AND COALESCE(e.data_quota_bytes, spr.data_quota_bytes) IS NOT NULL
     AND x.running >= COALESCE(e.data_quota_bytes, spr.data_quota_bytes);
$$;
COMMENT ON FUNCTION iam_v2.p6_data_crossing_full(uuid) IS
  'The exact data-quota crossing by full derivation over every attributed accounting record (the pre-0122 '
  'p6_data_crossing). Called only once the checkpointed running total says the quota may have been reached.';

CREATE OR REPLACE FUNCTION iam_v2.p6_advance_data_crossing(p_tenant uuid, p_site uuid) RETURNS integer
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE e record; b record; v_new bigint; v_max bigint; v_est numeric; v_exact timestamptz; n int := 0;
BEGIN
  FOR e IN
    SELECT en.id, COALESCE(en.data_quota_bytes, spr.data_quota_bytes) AS quota
      FROM iam_v2.entitlements en
      LEFT JOIN iam_v2.service_plan_revisions spr ON spr.id = en.service_plan_revision_id
     WHERE en.tenant_id = p_tenant AND en.site_id = p_site
       AND en.status IN ('ACTIVE', 'PENDING', 'SUSPENDED')
       AND COALESCE(en.data_quota_bytes, spr.data_quota_bytes) IS NOT NULL
       AND NOT EXISTS (SELECT 1 FROM iam_v2.data_crossing_state s
                        WHERE s.entitlement_id = en.id AND s.crossed_at IS NOT NULL
                          AND s.quota_bytes = COALESCE(en.data_quota_bytes, spr.data_quota_bytes))
     ORDER BY en.id
  LOOP
    n := n + 1;
    FOR b IN
      SELECT sb.id, sb.session_id, sb.bound_from, sb.bound_until, se.ended,
             COALESCE(pr.examined_seq, 0) AS seq, COALESCE(pr.final, false) AS final
        FROM iam_v2.session_entitlement_bindings sb
        JOIN iam_v2.sessions se ON se.id = sb.session_id
        LEFT JOIN iam_v2.data_crossing_progress pr ON pr.binding_id = sb.id
       WHERE sb.entitlement_id = e.id
    LOOP
      IF b.final THEN
        CONTINUE;   -- an ended session's records are complete; there is nothing new to read
      END IF;
      -- ONLY the records after the checkpoint: the unique (session_id, sample_seq) index answers this directly.
      SELECT COALESCE(sum(CASE WHEN b.bound_from <= ar.sampled_at
                                    AND (b.bound_until IS NULL OR b.bound_until > ar.sampled_at)
                               THEN ar.bytes_up + ar.bytes_down ELSE 0 END), 0),
             max(ar.sample_seq)
        INTO v_new, v_max
        FROM iam_v2.accounting_records ar
       WHERE ar.session_id = b.session_id AND ar.sample_seq > b.seq;
      INSERT INTO iam_v2.data_crossing_progress AS pr
        (binding_id, tenant_id, site_id, entitlement_id, session_id, examined_seq, counted_bytes, final)
      VALUES (b.id, p_tenant, p_site, e.id, b.session_id, COALESCE(v_max, b.seq), v_new,
              -- ingestion refuses an ended session outright, so once it has been ended for a while (longer than
              -- any in-flight observation could take) its record set is closed for good
              b.ended IS NOT NULL AND b.ended < now() - interval '5 minutes')
      ON CONFLICT (binding_id) DO UPDATE
        SET examined_seq = GREATEST(pr.examined_seq, EXCLUDED.examined_seq),
            counted_bytes = pr.counted_bytes + EXCLUDED.counted_bytes,
            final = EXCLUDED.final, updated_at = now();
    END LOOP;

    SELECT COALESCE(sum(pr.counted_bytes), 0) INTO v_est
      FROM iam_v2.data_crossing_progress pr WHERE pr.entitlement_id = e.id;
    IF v_est >= e.quota THEN
      -- The running total says the quota may have been reached: establish the exact instant ONCE.
      v_exact := iam_v2.p6_data_crossing_full(e.id);
      IF v_exact IS NULL THEN
        -- The checkpoint overstated (an attribution interval shrank after it counted). Recount every binding of
        -- this entitlement exactly, so the estimate is honest again and this branch is not taken every tick.
        UPDATE iam_v2.data_crossing_progress pr
           SET counted_bytes = COALESCE((
                 SELECT sum(ar.bytes_up + ar.bytes_down)
                   FROM iam_v2.accounting_records ar
                   JOIN iam_v2.session_entitlement_bindings sb ON sb.id = pr.binding_id
                  WHERE ar.session_id = pr.session_id AND ar.sample_seq <= pr.examined_seq
                    AND sb.bound_from <= ar.sampled_at
                    AND (sb.bound_until IS NULL OR sb.bound_until > ar.sampled_at)), 0),
               updated_at = now()
         WHERE pr.entitlement_id = e.id;
      END IF;
      INSERT INTO iam_v2.data_crossing_state AS s (entitlement_id, tenant_id, site_id, quota_bytes, crossed_at)
      VALUES (e.id, p_tenant, p_site, e.quota, v_exact)
      ON CONFLICT (entitlement_id) DO UPDATE
        SET quota_bytes = EXCLUDED.quota_bytes, crossed_at = EXCLUDED.crossed_at, verified_at = now();
    END IF;
  END LOOP;
  RETURN n;
END $$;
COMMENT ON FUNCTION iam_v2.p6_advance_data_crossing(uuid, uuid) IS
  'Advances the data-crossing checkpoint of every live, quota-bearing entitlement of a site by the accounting '
  'records that arrived since the last call (0122), and establishes the exact crossing instant once the running '
  'total reaches the quota. Called by acctd before the expiry sweep. Deletes nothing.';

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

DROP TABLE IF EXISTS iam_v2.accounting_usage_entitlement;

COMMIT;
