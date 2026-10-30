-- 0180 -- AN ENTITLEMENT'S USAGE SURVIVES THE RAW PURGE (follow-up to 0175, audit AD-48 (a); the PR #272 review finding)
--
-- THE DEFECT. 0175 keeps a raw sample (iam_v2.accounting_records) only until it has been rolled up AND is older than
-- raw_retention_days (default 2). Three functions still compute an entitlement's usage by re-summing the RAW samples:
--
--   iam_v2.entitlement_usage_bytes(p_ent, p_at)   post-stay quota test and the frozen boundary watermark (checkout.go)
--   iam_v2.p3_entitlement_data_usage(p_ent)       the derivation entitlements.consumed_data_bytes must equal (0061/0062)
--   iam_v2.p6_data_crossing_full(p_ent)           the exact recount that decides the data-quota crossing (0122), called by
--                                                 p6_advance_data_crossing and p6_data_crossing when the checkpointed
--                                                 running total reaches the quota
--
-- so a stay longer than the raw retention read LESS than it used (measured: 600,000,000 bytes read 516,600,000). For the
-- quota this is a bypass, not a display problem: when the running total reaches the limit the exact recount decides, and a
-- recount that no longer sees the old samples returns NULL ("the checkpoint overstated"), after which
-- p6_advance_data_crossing REWRITES the per-binding checkpoints from the surviving raw samples only, so the lost bytes are
-- also removed from the running total for good. A multi-day guest with a data quota can pass it.
--
-- THE FIX. One exact per-Entitlement accumulator, iam_v2.accounting_usage_entitlement, folded INSIDE the statement that
-- deletes the raw samples (purge_accounting_detail, tier 1): DELETE ... RETURNING feeds the accumulator in the same
-- statement, so a sample is in exactly one of two places -- the raw table or the accumulator -- never both and never
-- neither, however the purge is interrupted or repeated. The readers return accumulator + raw. Every function keeps its
-- name, signature and return columns; whenever no raw sample has been purged the results are identical to before.
--
-- WHY THE FOLD IS IN THE PURGE AND NOT IN THE ROLL-UP. Attribution is by session_entitlement_bindings, and a binding can
-- be written RETROACTIVELY: rebind_session_entitlement(session, entitlement, p_at) closes the open binding at p_at and
-- opens the next at p_at, and checkout passes the PMS boundary (in the past) as p_at; ending a session closes the open
-- binding at its ended instant. A sample rolled up a minute after it was taken is attributed to the binding in force
-- THEN, and a rebind at an earlier instant moves it to another entitlement. An accumulator filled by the roll-up (within a
-- minute of the sample) would therefore be wrong after such a rebind, with no way to repair it once the sample is gone.
-- Folding at purge time attributes a sample only after raw_retention_days (1-30), by which time its bindings are settled; a
-- retroactive binding is repaired for every sample whose raw still exists because those are still counted from raw.
--
-- ATTRIBUTION is the rule the three functions share, with the two variations they have today kept as separate "arms" so
-- that every function stays exactly what it was:
--   arm 0  the half-open binding interval  bound_from <= sampled_at < bound_until   (all three functions)
--   arm 1  the closing instant: sampled_at = bound_until and no other binding covers it  (0062: p3_entitlement_data_usage only)
--   arm 2  the no-binding fallback: a session with no binding row at all, to sessions.entitlement_id (0065: usage_bytes only)
--
-- WHAT IS EXACT AND WHAT IS NOT: see docs/reviews/USAGE_AFTER_RAW_PURGE_FIX.md. In short: totals are exact; a usage-at-
-- instant read (p_at) is exact whenever the accumulator rows it needs lie wholly at or before p_at (or p_at is within the
-- raw retention); the crossing instant is exact when the crossing sample is still raw and is the latest sample time of its
-- hour when it was already folded; rows made by the one-off backfill below for samples purged BEFORE this migration are
-- marked exact = false when a binding boundary falls inside their hour.

BEGIN;

CREATE TABLE IF NOT EXISTS iam_v2.accounting_usage_entitlement (
  tenant_id         uuid NOT NULL,
  site_id           uuid NOT NULL,
  entitlement_id    uuid NOT NULL,
  arm               smallint NOT NULL CONSTRAINT aue_arm CHECK (arm IN (0, 1, 2)),
  bucket            timestamptz NOT NULL,
  bytes_up          bigint NOT NULL DEFAULT 0 CONSTRAINT aue_up CHECK (bytes_up >= 0),
  bytes_down        bigint NOT NULL DEFAULT 0 CONSTRAINT aue_down CHECK (bytes_down >= 0),
  records           bigint NOT NULL DEFAULT 0 CONSTRAINT aue_records CHECK (records >= 0),
  latest_sampled_at timestamptz NOT NULL,
  exact             boolean NOT NULL DEFAULT true,
  PRIMARY KEY (entitlement_id, bucket, arm)
);
CREATE INDEX IF NOT EXISTS aue_site_bucket ON iam_v2.accounting_usage_entitlement (tenant_id, site_id, bucket);
COMMENT ON TABLE iam_v2.accounting_usage_entitlement IS
  'Per Entitlement and hour of sampled_at: the usage of raw accounting samples that purge_accounting_detail has DELETED, folded in the '
  'same statement as the delete (0180). Raw samples still present are NOT in here; readers add the two. arm 0 = half-open binding '
  'interval, arm 1 = the closing-instant sample (0062), arm 2 = session without any binding row (0065). exact = false only for rows '
  'backfilled by 0180 from the per-session hour tier where a binding boundary fell inside the hour. Kept as long as the entitlement''s '
  'sessions'' hourly totals (never while the entitlement is not TERMINATED).';
COMMENT ON COLUMN iam_v2.accounting_usage_entitlement.exact IS
  'false = best effort (backfilled by 0180 from accounting_usage_hour, attributed by the first binding overlapping the hour); true = folded sample by sample.';

-- ---------------------------------------------------------------------------------------------------------
-- 1. Readers: accumulator + raw.
-- ---------------------------------------------------------------------------------------------------------
-- A folded bucket row is counted by entitlement_usage_bytes(p_ent, p_at) when ALL its samples are at or before p_at: its hour
-- ended by p_at, or its latest sample is at or before p_at. A bucket straddling p_at that holds folded samples after p_at cannot be
-- split (that raw is gone) and is left out: the result is then a lower bound, short by at most the usage of that one hour.
-- Raw rows are attributed per binding, driving from the entitlement's bindings so the lookup uses the indexes on
-- session_entitlement_bindings (entitlement_id, bound_from) and accounting_records (session_id, sample_seq).
CREATE OR REPLACE FUNCTION iam_v2.entitlement_usage_bytes(p_ent uuid, p_at timestamptz)
RETURNS TABLE(bytes_up bigint, bytes_down bigint, records bigint, latest_sampled_at timestamptz)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = iam_v2, pg_temp
AS $$
  WITH folded AS (
    SELECT sum(a.bytes_up) AS up, sum(a.bytes_down) AS down, sum(a.records) AS n, max(a.latest_sampled_at) AS latest
      FROM iam_v2.accounting_usage_entitlement a
     WHERE a.entitlement_id = p_ent AND a.arm IN (0, 2)
       AND (a.bucket + interval '1 hour' <= p_at OR a.latest_sampled_at <= p_at)),
  live AS (
    SELECT sum(r.bytes_up) AS up, sum(r.bytes_down) AS down, count(*) AS n, max(r.sampled_at) AS latest
      FROM (
        SELECT ar.bytes_up, ar.bytes_down, ar.sampled_at
          FROM iam_v2.session_entitlement_bindings b
          JOIN iam_v2.accounting_records ar ON ar.session_id = b.session_id
           AND ar.sampled_at >= b.bound_from AND (b.bound_until IS NULL OR ar.sampled_at < b.bound_until)
          JOIN iam_v2.sessions s ON s.id = ar.session_id
         WHERE b.entitlement_id = p_ent AND ar.sampled_at <= p_at
        UNION ALL
        SELECT ar.bytes_up, ar.bytes_down, ar.sampled_at
          FROM iam_v2.sessions s
          JOIN iam_v2.accounting_records ar ON ar.session_id = s.id
         WHERE s.entitlement_id = p_ent AND ar.sampled_at <= p_at
           AND NOT EXISTS (SELECT 1 FROM iam_v2.session_entitlement_bindings b2 WHERE b2.session_id = s.id)
      ) r)
  SELECT (COALESCE(f.up, 0) + COALESCE(l.up, 0))::bigint,
         (COALESCE(f.down, 0) + COALESCE(l.down, 0))::bigint,
         (COALESCE(f.n, 0) + COALESCE(l.n, 0))::bigint,
         greatest(f.latest, l.latest)
    FROM folded f, live l;
$$;

COMMENT ON FUNCTION iam_v2.entitlement_usage_bytes(uuid, timestamptz) IS
  'Bytes attributed to ONE Entitlement up to ONE instant, by binding interval, with the no-binding fallback '
  'for sessions that predate the binding table. SECURITY DEFINER so a caller can obtain the aggregate without '
  'holding SELECT on iam_v2.accounting_records -- svc_pmsd needs the number at a checkout boundary and Gate-P '
  'forbids it the table. Since 0180 it adds the samples the purge has already folded into '
  'iam_v2.accounting_usage_entitlement to the raw samples still present, so the answer does not shrink when raw is purged. '
  'It returns four aggregate numbers about an entitlement the caller must already be '
  'able to name; it exposes no guest, Stay, reservation, folio, payment or PMS data, and it writes nothing.';

CREATE OR REPLACE FUNCTION iam_v2.p3_entitlement_data_usage(p_entitlement uuid)
RETURNS bigint
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = iam_v2, pg_temp
AS $usage$
  SELECT (COALESCE((SELECT sum(a.bytes_up + a.bytes_down) FROM iam_v2.accounting_usage_entitlement a
                     WHERE a.entitlement_id = p_entitlement AND a.arm IN (0, 1)), 0)
        + COALESCE((SELECT sum(ar.bytes_up + ar.bytes_down)
                      FROM iam_v2.session_entitlement_bindings b
                      JOIN iam_v2.accounting_records ar
                        ON ar.session_id = b.session_id
                       AND b.bound_from <= ar.sampled_at
                       AND (
                             -- the ordinary half-open interval, unchanged
                             b.bound_until IS NULL
                          OR b.bound_until > ar.sampled_at
                             -- ...and the closing instant, ONLY when nothing else owns the sample (0062)
                          OR ( b.bound_until = ar.sampled_at
                               AND NOT EXISTS (
                                     SELECT 1
                                       FROM iam_v2.session_entitlement_bindings b2
                                      WHERE b2.session_id = ar.session_id
                                        AND b2.bound_from <= ar.sampled_at
                                        AND (b2.bound_until IS NULL OR b2.bound_until > ar.sampled_at)) )
                           )
                     WHERE b.entitlement_id = p_entitlement), 0))::bigint
$usage$;

COMMENT ON FUNCTION iam_v2.p3_entitlement_data_usage(uuid) IS
  'Authoritative bytes attributed to an Entitlement across every Session and Device bound to it. Attribution '
  'follows the half-open binding interval, plus the closing instant itself when no other binding for that '
  'session covers it - which is the terminal case, where the sample that exhausted the quota would otherwise '
  'be attributed to nothing. A Phase-5 rebinding always has a successor covering the boundary, so that arm '
  'cannot fire there and no sample is counted twice. entitlements.consumed_data_bytes must equal this. Since 0180 '
  'the samples the raw purge has already folded into iam_v2.accounting_usage_entitlement are added to the raw samples still present.';

-- The exact recount. Samples are ordered by (sampled_at, id) as before. The folded part sits BEFORE the raw samples in that
-- order (it is the part purged for being old), so the running total of the raw samples starts at the folded total; if the quota is
-- already reached inside the folded part, the crossing is in one of its hours and the answer is that hour's latest sample time
-- (the crossing sample is gone; the instant is bounded within the hour, and the latest sample of that hour is the earliest instant
-- by which the quota is certainly crossed).
CREATE OR REPLACE FUNCTION iam_v2.p6_data_crossing_full(p_entitlement uuid) RETURNS timestamp with time zone
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
  WITH q AS (
    SELECT COALESCE(e.data_quota_bytes, spr.data_quota_bytes) AS quota
      FROM iam_v2.entitlements e
      LEFT JOIN iam_v2.service_plan_revisions spr ON spr.id = e.service_plan_revision_id
     WHERE e.id = p_entitlement
       AND COALESCE(e.data_quota_bytes, spr.data_quota_bytes) IS NOT NULL),
  fb AS (
    SELECT a.latest_sampled_at AS t,
           sum(a.bytes_up + a.bytes_down) OVER (ORDER BY a.bucket) AS cum
      FROM iam_v2.accounting_usage_entitlement a
     WHERE a.entitlement_id = p_entitlement AND a.arm = 0),
  base AS (SELECT COALESCE(max(fb.cum), 0) AS folded FROM fb),
  lr AS (
    SELECT ar.sampled_at,
           base.folded + sum(ar.bytes_up + ar.bytes_down) OVER (ORDER BY ar.sampled_at, ar.id) AS running
      FROM base,
           iam_v2.session_entitlement_bindings b
      JOIN iam_v2.accounting_records ar ON ar.session_id = b.session_id AND b.bound_from <= ar.sampled_at
       AND (b.bound_until IS NULL OR b.bound_until > ar.sampled_at)
     WHERE b.entitlement_id = p_entitlement)
  SELECT min(c.t)
    FROM (SELECT fb.t FROM fb, q WHERE fb.cum >= q.quota
          UNION ALL
          SELECT lr.sampled_at FROM lr, q WHERE lr.running >= q.quota) c;
$$;
COMMENT ON FUNCTION iam_v2.p6_data_crossing_full(uuid) IS
  'The exact data-quota crossing by full derivation over every attributed accounting record (the pre-0122 '
  'p6_data_crossing). Since 0180 it also counts the samples the raw purge folded into iam_v2.accounting_usage_entitlement: exact '
  'while the crossing sample is raw, the latest sample time of its hour once it has been folded. Called only once the checkpointed '
  'running total says the quota may have been reached.';

-- p6_advance_data_crossing: identical to 0122 except the "overstated checkpoint" recount, which rewrote every binding's
-- counted_bytes from the RAW samples only and so removed the folded bytes from the running total for good. The folded
-- arm-0 total of the entitlement is now added to the entitlement's first checkpoint row (the decision sums the entitlement's rows).
CREATE OR REPLACE FUNCTION iam_v2.p6_advance_data_crossing(p_tenant uuid, p_site uuid) RETURNS integer
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE e record; b record; v_new bigint; v_max bigint; v_est numeric; v_exact timestamptz; n int := 0;
        v_folded bigint; v_first uuid;
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
        -- The samples the purge has already folded are no longer raw: they are carried by the first checkpoint row.
        SELECT COALESCE(sum(a.bytes_up + a.bytes_down), 0) INTO v_folded
          FROM iam_v2.accounting_usage_entitlement a WHERE a.entitlement_id = e.id AND a.arm = 0;
        SELECT p2.binding_id INTO v_first FROM iam_v2.data_crossing_progress p2
         WHERE p2.entitlement_id = e.id ORDER BY p2.binding_id LIMIT 1;
        UPDATE iam_v2.data_crossing_progress pr
           SET counted_bytes = COALESCE((
                 SELECT sum(ar.bytes_up + ar.bytes_down)
                   FROM iam_v2.accounting_records ar
                   JOIN iam_v2.session_entitlement_bindings sb ON sb.id = pr.binding_id
                  WHERE ar.session_id = pr.session_id AND ar.sample_seq <= pr.examined_seq
                    AND sb.bound_from <= ar.sampled_at
                    AND (sb.bound_until IS NULL OR sb.bound_until > ar.sampled_at)), 0)
                 + CASE WHEN pr.binding_id = v_first THEN v_folded ELSE 0 END,
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
  'total reaches the quota. Since 0180 the recount of an overstated checkpoint keeps the bytes the raw purge has folded. '
  'Called by acctd before the expiry sweep. Deletes nothing.';

-- ---------------------------------------------------------------------------------------------------------
-- 2. The purge: tier 1 folds what it deletes; tier 3 also removes the accumulator rows of finished entitlements.
-- ---------------------------------------------------------------------------------------------------------
-- Everything else is 0175: the same tiers, budget, cut-offs and purge-run records.
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

  -- Tier 1: raw samples that have been rolled up. The delete and the fold into the per-entitlement accumulator are ONE statement:
  -- the rows that are deleted are exactly the rows folded (RETURNING), so a repeat or an interrupted run cannot count a sample
  -- twice or lose one.
  IF v_wm IS NOT NULL THEN
    v_cut := least(now() - make_interval(days => v_set.raw_retention_days), v_wm);
    v_tier := 0;
    LOOP
      WITH doomed AS (
        SELECT ar.id FROM iam_v2.accounting_records ar
         WHERE ar.tenant_id = p_tenant AND ar.site_id = p_site AND ar.ingested_at < v_cut
         LIMIT p_batch),
      del AS (
        DELETE FROM iam_v2.accounting_records ar USING doomed WHERE ar.id = doomed.id
        RETURNING ar.session_id, ar.sampled_at, ar.bytes_up, ar.bytes_down),
      att AS (
        -- arm 0: the half-open binding interval
        SELECT b.entitlement_id, 0::smallint AS arm, d.sampled_at, d.bytes_up, d.bytes_down
          FROM del d JOIN iam_v2.session_entitlement_bindings b
            ON b.session_id = d.session_id AND b.bound_from <= d.sampled_at
           AND (b.bound_until IS NULL OR b.bound_until > d.sampled_at)
        UNION ALL
        -- arm 1: the closing instant, only when no other binding of the session covers it (0062)
        SELECT b.entitlement_id, 1::smallint, d.sampled_at, d.bytes_up, d.bytes_down
          FROM del d JOIN iam_v2.session_entitlement_bindings b
            ON b.session_id = d.session_id AND b.bound_from <= d.sampled_at AND b.bound_until = d.sampled_at
         WHERE NOT EXISTS (SELECT 1 FROM iam_v2.session_entitlement_bindings b2
                            WHERE b2.session_id = d.session_id AND b2.bound_from <= d.sampled_at
                              AND (b2.bound_until IS NULL OR b2.bound_until > d.sampled_at))
        UNION ALL
        -- arm 2: a session with no binding row at all, to its current entitlement (0065)
        SELECT s.entitlement_id, 2::smallint, d.sampled_at, d.bytes_up, d.bytes_down
          FROM del d JOIN iam_v2.sessions s ON s.id = d.session_id
         WHERE NOT EXISTS (SELECT 1 FROM iam_v2.session_entitlement_bindings b3 WHERE b3.session_id = d.session_id)),
      fold AS (
        INSERT INTO iam_v2.accounting_usage_entitlement AS t
               (tenant_id, site_id, entitlement_id, arm, bucket, bytes_up, bytes_down, records, latest_sampled_at)
        SELECT p_tenant, p_site, a.entitlement_id, a.arm, date_trunc('hour', a.sampled_at),
               sum(a.bytes_up)::bigint, sum(a.bytes_down)::bigint, count(*), max(a.sampled_at)
          FROM att a GROUP BY a.entitlement_id, a.arm, date_trunc('hour', a.sampled_at)
        ON CONFLICT (entitlement_id, bucket, arm) DO UPDATE
           SET bytes_up = t.bytes_up + EXCLUDED.bytes_up, bytes_down = t.bytes_down + EXCLUDED.bytes_down,
               records = t.records + EXCLUDED.records,
               latest_sampled_at = greatest(t.latest_sampled_at, EXCLUDED.latest_sampled_at)
        RETURNING 1)
      SELECT count(*) INTO v_n FROM del;
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

  -- Tier 3: hourly totals, under the site's usage-detail retention; and the per-entitlement accumulator rows (0180) under the
  -- same cut-off: only an entitlement that is TERMINATED and whose every session (by binding or by current pointer) ended before
  -- the cut-off loses them, so an entitlement that can still be charged, quota-tested or checked out never loses its total.
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
    IF clock_timestamp() - v_t0 <= v_budget THEN
      LOOP
        DELETE FROM iam_v2.accounting_usage_entitlement a
         USING (SELECT x.entitlement_id, x.bucket, x.arm
                  FROM iam_v2.accounting_usage_entitlement x
                  JOIN iam_v2.entitlements en ON en.id = x.entitlement_id
                 WHERE x.tenant_id = p_tenant AND x.site_id = p_site
                   AND x.bucket < v_cut
                   AND en.status = 'TERMINATED'
                   AND NOT EXISTS (SELECT 1 FROM iam_v2.session_entitlement_bindings b
                                     JOIN iam_v2.sessions se ON se.id = b.session_id
                                    WHERE b.entitlement_id = x.entitlement_id
                                      AND (se.ended IS NULL OR se.ended >= v_cut))
                   AND NOT EXISTS (SELECT 1 FROM iam_v2.sessions se
                                    WHERE se.entitlement_id = x.entitlement_id
                                      AND (se.ended IS NULL OR se.ended >= v_cut))
                 LIMIT p_batch) d
         WHERE a.entitlement_id = d.entitlement_id AND a.bucket = d.bucket AND a.arm = d.arm;
        GET DIAGNOSTICS v_n = ROW_COUNT;
        v_tier := v_tier + v_n;
        EXIT WHEN v_n < p_batch OR clock_timestamp() - v_t0 > v_budget;
      END LOOP;
    END IF;
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
  'The accounting retention purge (0122; 0131; 0175 AD-48; 0180): rolled-up raw samples past raw_retention_days (folded into '
  'iam_v2.accounting_usage_entitlement in the same statement), per-minute rows past minute_retention_days, and hourly totals past the '
  'usage-detail retention (ended sessions, every entitlement TERMINATED) with the accumulator rows of finished entitlements, within '
  'purge_budget_seconds. Totals on entitlements and sessions are not touched.';

-- ---------------------------------------------------------------------------------------------------------
-- 3. Ownership and grants (the readers keep the privileges 0061/0065/0122/0124 gave them: CREATE OR REPLACE keeps the ACL).
-- ---------------------------------------------------------------------------------------------------------
DO $own$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
    EXECUTE 'ALTER TABLE iam_v2.accounting_usage_entitlement OWNER TO iam_v2_owner';
  END IF;
END $own$;
-- No role holds a table privilege on the accumulator: only the definer functions above read or write it.
REVOKE ALL ON TABLE iam_v2.accounting_usage_entitlement FROM PUBLIC;

-- ---------------------------------------------------------------------------------------------------------
-- 4. One-off backfill for samples purged BEFORE this migration (0175 has been in use on the test appliance only).
-- ---------------------------------------------------------------------------------------------------------
-- Blocks writers of accounting_records (inserts and purges) for the length of this transaction, so no purge that does not fold
-- can run between the backfill's snapshot and its commit.
LOCK TABLE iam_v2.accounting_records IN SHARE ROW EXCLUSIVE MODE;

-- What the per-session hour tier holds beyond the raw samples still present (and already rolled up) is what was purged. It is
-- attributed to the first binding that overlaps the hour (the one in force at the start of the hour whenever one is), or to the
-- session's current entitlement when the session has no binding row. exact = true only when the attribution cannot depend on
-- the instants inside the hour: the binding covers the whole hour, or the session has no binding row.
INSERT INTO iam_v2.accounting_usage_entitlement AS t
       (tenant_id, site_id, entitlement_id, arm, bucket, bytes_up, bytes_down, records, latest_sampled_at, exact)
SELECT p.tenant_id, p.site_id, p.entitlement_id, p.arm, p.bucket,
       sum(p.up)::bigint, sum(p.down)::bigint, sum(p.n)::bigint,
       max(p.bucket + interval '1 hour' - interval '1 microsecond'), bool_and(p.exact)
  FROM (
    SELECT h.tenant_id, h.site_id, h.bucket,
           COALESCE(ob.entitlement_id, se.entitlement_id) AS entitlement_id,
           CASE WHEN ob.entitlement_id IS NULL THEN 2 ELSE 0 END::smallint AS arm,
           h.bytes_up - COALESCE(r.up, 0) AS up, h.bytes_down - COALESCE(r.down, 0) AS down,
           h.samples - COALESCE(r.n, 0) AS n,
           (ob.entitlement_id IS NULL
            OR (ob.bound_from <= h.bucket AND (ob.bound_until IS NULL OR ob.bound_until >= h.bucket + interval '1 hour'))) AS exact
      FROM iam_v2.accounting_usage_hour h
      JOIN iam_v2.site_usage_rollup_state w ON w.tenant_id = h.tenant_id AND w.site_id = h.site_id
      JOIN iam_v2.sessions se ON se.id = h.session_id
      LEFT JOIN LATERAL (
        SELECT sum(ar.bytes_up)::bigint AS up, sum(ar.bytes_down)::bigint AS down, count(*) AS n
          FROM iam_v2.accounting_records ar
         WHERE ar.session_id = h.session_id AND ar.sampled_at >= h.bucket
           AND ar.sampled_at < h.bucket + interval '1 hour' AND ar.ingested_at < w.watermark) r ON true
      LEFT JOIN LATERAL (
        SELECT b.entitlement_id, b.bound_from, b.bound_until
          FROM iam_v2.session_entitlement_bindings b
         WHERE b.session_id = h.session_id AND b.bound_from < h.bucket + interval '1 hour'
           AND (b.bound_until IS NULL OR b.bound_until > h.bucket)
         ORDER BY b.bound_from LIMIT 1) ob ON true
     WHERE h.samples - COALESCE(r.n, 0) > 0
       AND (ob.entitlement_id IS NOT NULL
            OR NOT EXISTS (SELECT 1 FROM iam_v2.session_entitlement_bindings bx WHERE bx.session_id = h.session_id))
  ) p
 GROUP BY p.tenant_id, p.site_id, p.entitlement_id, p.arm, p.bucket
ON CONFLICT (entitlement_id, bucket, arm) DO NOTHING;

-- ---------------------------------------------------------------------------------------------------------
-- PROVE THE PROPERTIES on the database this just changed.
-- ---------------------------------------------------------------------------------------------------------
DO $verify$
DECLARE p pg_proc%ROWTYPE; f text;
BEGIN
  FOREACH f IN ARRAY ARRAY['iam_v2.entitlement_usage_bytes(uuid, timestamptz)', 'iam_v2.p3_entitlement_data_usage(uuid)',
                           'iam_v2.p6_data_crossing_full(uuid)', 'iam_v2.p6_advance_data_crossing(uuid, uuid)',
                           'iam_v2.purge_accounting_detail(uuid, uuid, integer)'] LOOP
    SELECT pr.* INTO p FROM pg_proc pr WHERE pr.oid = to_regprocedure(f);
    IF NOT FOUND THEN RAISE EXCEPTION 'migration 0180 lost %', f; END IF;
    IF NOT p.prosecdef THEN RAISE EXCEPTION '% is not SECURITY DEFINER', f; END IF;
    IF p.proconfig IS NULL OR NOT EXISTS (SELECT 1 FROM unnest(p.proconfig) c WHERE c LIKE 'search_path=%') THEN
      RAISE EXCEPTION '% has no pinned search_path', f;
    END IF;
    IF has_function_privilege('public', p.oid, 'EXECUTE') THEN RAISE EXCEPTION 'PUBLIC can execute %', f; END IF;
  END LOOP;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_pmsd')
     AND NOT has_function_privilege('svc_pmsd', 'iam_v2.entitlement_usage_bytes(uuid, timestamptz)', 'EXECUTE') THEN
    RAISE EXCEPTION 'svc_pmsd lost EXECUTE on entitlement_usage_bytes';
  END IF;
END $verify$;

COMMIT;
