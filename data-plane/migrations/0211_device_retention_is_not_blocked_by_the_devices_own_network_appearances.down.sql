-- 0211 down -- device retention again excludes every referenced device, its own network appearances included (the 0162 body).
-- Devices already removed under 0211 are not restored: they were idle past the retention setting and nothing else referenced them.

BEGIN;

CREATE OR REPLACE FUNCTION iam_v2.prune_idle_devices(p_tenant uuid, p_site uuid, p_batch integer)
  RETURNS integer
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE v_days integer; v_sql text; v_not text := ''; r record; v_n integer;
BEGIN
  IF p_batch IS NULL OR p_batch < 1 OR p_batch > 5000 THEN
    RAISE EXCEPTION 'batch must be 1-5000' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  SELECT g.device_retention_days INTO v_days FROM iam_v2.signin_capacity_settings_get(p_tenant, p_site) g;
  -- Every foreign key to devices, single-column or composite ((tenant_id, site_id, device_id) is the usual shape): the
  -- referencing row matches the device on ALL the key's columns, each paired with the column it references.
  FOR r IN
    SELECT c.conrelid::regclass::text AS tbl,
           string_agg(format('x.%I = d.%I', ra.attname, pa.attname), ' AND ' ORDER BY k.ord) AS cond
      FROM pg_constraint c
      CROSS JOIN LATERAL unnest(c.conkey, c.confkey) WITH ORDINALITY AS k(refing, refed, ord)
      JOIN pg_attribute ra ON ra.attrelid = c.conrelid AND ra.attnum = k.refing
      JOIN pg_attribute pa ON pa.attrelid = c.confrelid AND pa.attnum = k.refed
     WHERE c.contype = 'f' AND c.confrelid = 'iam_v2.devices'::regclass
     GROUP BY c.oid, c.conrelid
  LOOP
    v_not := v_not || format(' AND NOT EXISTS (SELECT 1 FROM %s x WHERE %s)', r.tbl, r.cond);
  END LOOP;
  v_sql := format($q$
    WITH doomed AS (
      SELECT d.id FROM iam_v2.devices d
       WHERE d.tenant_id = %L AND d.site_id = %L
         AND COALESCE(d.last_seen, d.first_seen) < now() - make_interval(days => %s)
         %s
       ORDER BY COALESCE(d.last_seen, d.first_seen) LIMIT %s)
    DELETE FROM iam_v2.devices d USING doomed WHERE d.id = doomed.id$q$,
    p_tenant, p_site, v_days, v_not, p_batch);
  EXECUTE v_sql;
  GET DIAGNOSTICS v_n = ROW_COUNT;
  RETURN v_n;
END $fn$;
COMMENT ON FUNCTION iam_v2.prune_idle_devices(uuid,uuid,integer) IS
  'AD-34: removes devices unseen for device_retention_days that nothing references (every foreign key to '
  'iam_v2.devices is consulted from the catalogue). Never removes a device with a session, a slot, a credential or any other dependent row.';

COMMIT;
