-- 0211 -- DEVICE RETENTION IS NOT BLOCKED BY THE DEVICE'S OWN NETWORK APPEARANCES (reliability audit, 2026-11-06).
--
-- AD-34 (0162) removes a device unseen for device_retention_days that nothing references. Every device that ever joined a network has
-- a row in iam_v2.device_network_appearances (ON DELETE NO ACTION), and those rows are never removed, so on the test appliance no device
-- could ever be pruned (0 of 1,191 eligible by construction). A device's appearance rows describe that device and nothing else; they
-- are now treated as part of it: removed with it, and never on their own.
--
-- Idle evidence is only what was recorded: the latest of the device's last_seen, first_seen and its appearances' last_seen and
-- first_seen. A device with no recorded timestamp at all is never removed; no timestamp is written or inferred. Every other reference
-- (a session, a slot, a credential, an authorization, an auth context, or any table added later) still protects the device.
--
-- WHAT THIS MIGRATION CHANGES, AND ONLY THAT: the body of iam_v2.prune_idle_devices. No row is rewritten. CREATE OR REPLACE keeps the
-- function's owner and its grants.

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
  -- Every foreign key to devices except the device's own appearances, single-column or composite: the referencing row matches the
  -- device on ALL the key's columns, each paired with the column it references.
  FOR r IN
    SELECT c.conrelid::regclass::text AS tbl,
           string_agg(format('x.%I = d.%I', ra.attname, pa.attname), ' AND ' ORDER BY k.ord) AS cond
      FROM pg_constraint c
      CROSS JOIN LATERAL unnest(c.conkey, c.confkey) WITH ORDINALITY AS k(refing, refed, ord)
      JOIN pg_attribute ra ON ra.attrelid = c.conrelid AND ra.attnum = k.refing
      JOIN pg_attribute pa ON pa.attrelid = c.confrelid AND pa.attnum = k.refed
     WHERE c.contype = 'f' AND c.confrelid = 'iam_v2.devices'::regclass
       AND c.conrelid <> 'iam_v2.device_network_appearances'::regclass
     GROUP BY c.oid, c.conrelid
  LOOP
    v_not := v_not || format(' AND NOT EXISTS (SELECT 1 FROM %s x WHERE %s)', r.tbl, r.cond);
  END LOOP;
  v_sql := format($q$
    WITH seen AS (
      SELECT d.id, GREATEST(d.last_seen, d.first_seen, a.last_seen, a.first_seen) AS at
        FROM iam_v2.devices d
        LEFT JOIN LATERAL (
          SELECT max(x.last_seen) AS last_seen, max(x.first_seen) AS first_seen
            FROM iam_v2.device_network_appearances x
           WHERE x.tenant_id = d.tenant_id AND x.site_id = d.site_id AND x.device_id = d.id) a ON true
       WHERE d.tenant_id = %L AND d.site_id = %L %s),
    doomed AS (
      SELECT s.id FROM seen s
       WHERE s.at < now() - make_interval(days => %s)
       ORDER BY s.at LIMIT %s),
    gone_appearances AS (
      DELETE FROM iam_v2.device_network_appearances x USING doomed
       WHERE x.tenant_id = %L AND x.site_id = %L AND x.device_id = doomed.id)
    DELETE FROM iam_v2.devices d USING doomed WHERE d.id = doomed.id$q$,
    p_tenant, p_site, v_not, v_days, p_batch, p_tenant, p_site);
  EXECUTE v_sql;
  GET DIAGNOSTICS v_n = ROW_COUNT;
  RETURN v_n;
END $fn$;
COMMENT ON FUNCTION iam_v2.prune_idle_devices(uuid,uuid,integer) IS
  'AD-34: removes devices unseen for device_retention_days (latest recorded device or network-appearance timestamp; never a device with none) '
  'that nothing but their own network appearances references (every other foreign key to iam_v2.devices is consulted from the catalogue). '
  'The appearances go with the device. Never removes a device with a session, a slot, a credential or any other dependent row.';

COMMIT;
