-- 0175 down -- back to the single-tier purge of 0131. The rollup tables and settings are dropped (their rows are
-- summaries of samples that are no longer all present; the hourly totals of an up-migrated database are lost).

BEGIN;

CREATE OR REPLACE FUNCTION iam_v2.purge_accounting_detail(p_tenant uuid, p_site uuid, p_batch integer)
    RETURNS integer
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE v_days integer; v_cutoff timestamptz; v_n integer;
BEGIN
  IF p_batch IS NULL OR p_batch NOT BETWEEN 1 AND 100000 THEN
    RAISE EXCEPTION 'a batch of 1-100000 records is required' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  SELECT g.detail_retention_days INTO v_days FROM iam_v2.accounting_retention_get(p_tenant, p_site) g;
  IF v_days IS NULL THEN
    RETURN 0;   -- UNLIMITED (the administrator's choice): nothing is read, nothing is deleted, nothing is recorded
  END IF;
  v_cutoff := now() - make_interval(days => v_days);
  PERFORM set_config('iam_v2.accounting_purge', 'retention', true);
  WITH doomed AS (
    SELECT ar.id
      FROM iam_v2.accounting_records ar
      JOIN iam_v2.sessions se ON se.id = ar.session_id
      JOIN iam_v2.entitlements cur ON cur.id = se.entitlement_id
     WHERE ar.tenant_id = p_tenant AND ar.site_id = p_site
       AND ar.sampled_at < v_cutoff
       AND se.ended IS NOT NULL AND se.ended < v_cutoff
       AND cur.status = 'TERMINATED'
       -- every entitlement the session was ever attributed to has ended: live usage is never touched
       AND NOT EXISTS (SELECT 1 FROM iam_v2.session_entitlement_bindings b
                         JOIN iam_v2.entitlements be ON be.id = b.entitlement_id
                        WHERE b.session_id = ar.session_id AND be.status <> 'TERMINATED')
     LIMIT p_batch)
  DELETE FROM iam_v2.accounting_records ar USING doomed WHERE ar.id = doomed.id;
  GET DIAGNOSTICS v_n = ROW_COUNT;
  PERFORM set_config('iam_v2.accounting_purge', '', true);
  IF v_n > 0 THEN
    INSERT INTO iam_v2.accounting_purge_runs (tenant_id, site_id, retention_days, cutoff, records_deleted)
    VALUES (p_tenant, p_site, v_days, v_cutoff, v_n);
  END IF;
  RETURN v_n;
END $$;
COMMENT ON FUNCTION iam_v2.purge_accounting_detail(uuid, uuid, integer) IS
  'The accounting-detail retention purge (0122; 0131: a site that never chose keeps 365 days). Does nothing while '
  'the site''s administrator has chosen unlimited. Otherwise removes at most p_batch accounting records older than '
  'the retention, of sessions ended before it whose every entitlement is TERMINATED, and records the run. Totals '
  'on entitlements and sessions are not touched.';

-- the revoked grants go with the objects
DROP FUNCTION IF EXISTS iam_v2.rollup_accounting_usage(uuid, uuid, interval);
DROP FUNCTION IF EXISTS iam_v2.usage_rollup_settings_set(uuid, uuid, integer, integer, integer, text, text);
DROP FUNCTION IF EXISTS iam_v2.usage_rollup_settings_get(uuid, uuid);
DROP TABLE IF EXISTS iam_v2.site_usage_rollup_changes;
DROP TABLE IF EXISTS iam_v2.site_usage_rollup_settings;
DROP TABLE IF EXISTS iam_v2.site_usage_rollup_state;
DROP TABLE IF EXISTS iam_v2.accounting_usage_hour;
DROP TABLE IF EXISTS iam_v2.accounting_usage_minute;

COMMIT;
