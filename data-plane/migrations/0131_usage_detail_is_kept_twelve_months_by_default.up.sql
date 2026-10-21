-- 0131 -- USAGE DETAIL IS KEPT TWELVE MONTHS BY DEFAULT
-- (GDPR remediation plan C2; Product-Owner decision D-C2, 2026-10-17: the new default applies everywhere, because
--  no hotel is live yet; docs/privacy/GDPR_REMEDIATION_PLAN.md section 4.)
--
-- Until now the per-sample accounting detail (iam_v2.accounting_records) was kept for ever unless a site
-- administrator chose a retention (0122). Keeping it with no limit has no purpose a hotel could name, so:
--
--   * a site that never chose reads 365 days (iam_v2.accounting_retention_default);
--   * a site whose saved choice is "unlimited" moves to 365 days, logged in accounting_retention_changes as a
--     change by this migration, so the history says why;
--   * a site that chose a number of days keeps it.
--
-- It stays the hotel's setting (docs/OPERATIONAL_SETTINGS_POLICY.md): the site administrator may still choose
-- 30-3650 days or unlimited, with the password and a reason, audited, exactly as before. Only the default moves.
--
-- Destructive in effect: once applied, acctd's hourly purge removes detail older than a year, under 0122's rules
-- (ended sessions only, every entitlement TERMINATED, totals untouched, every run recorded). D-C2 authorised it.

BEGIN;

CREATE OR REPLACE FUNCTION iam_v2.accounting_retention_default() RETURNS integer
    LANGUAGE sql IMMUTABLE
    AS $$ SELECT 365 $$;
COMMENT ON FUNCTION iam_v2.accounting_retention_default() IS
  'The accounting-detail retention of a site that never chose one, in days (0131, GDPR plan C2, D-C2).';

-- No row: the default. A row with NULL: the administrator chose unlimited.
CREATE OR REPLACE FUNCTION iam_v2.accounting_retention_get(p_tenant uuid, p_site uuid)
    RETURNS TABLE(detail_retention_days integer, config_version bigint, updated_at timestamptz, updated_by text)
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
  SELECT CASE WHEN s.site_id IS NULL THEN iam_v2.accounting_retention_default() ELSE s.detail_retention_days END,
         COALESCE(s.config_version, 0), s.updated_at, s.updated_by
    FROM (SELECT 1) one
    LEFT JOIN iam_v2.site_accounting_retention s ON s.tenant_id = p_tenant AND s.site_id = p_site;
$$;

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

COMMENT ON TABLE iam_v2.site_accounting_retention IS
  'How long per-sample accounting detail (iam_v2.accounting_records) is kept (0122). A site with no row keeps '
  'iam_v2.accounting_retention_default() days (365, 0131); NULL in a row is the administrator''s choice of '
  'UNLIMITED: nothing is purged. Otherwise 30-3650 days. Written only through iam_v2.accounting_retention_set.';

-- D-C2: saved "unlimited" choices move to the default too, each logged as a change by this migration.
WITH moved AS (
  UPDATE iam_v2.site_accounting_retention s
     SET detail_retention_days = iam_v2.accounting_retention_default(),
         config_version = s.config_version + 1, updated_at = now(), updated_by = 'migration 0131'
   WHERE s.detail_retention_days IS NULL
  RETURNING s.tenant_id, s.site_id, s.config_version)
INSERT INTO iam_v2.accounting_retention_changes
  (tenant_id, site_id, changed_by, change_reason, old_retention_days, new_retention_days, new_config_version)
SELECT m.tenant_id, m.site_id, 'migration 0131',
       'GDPR plan C2 (D-C2): the 12-month default replaces unlimited on every site', NULL,
       iam_v2.accounting_retention_default(), m.config_version
  FROM moved m;

DO $own$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
    EXECUTE 'ALTER FUNCTION iam_v2.accounting_retention_default() OWNER TO iam_v2_owner';
  END IF;
END $own$;

COMMIT;
