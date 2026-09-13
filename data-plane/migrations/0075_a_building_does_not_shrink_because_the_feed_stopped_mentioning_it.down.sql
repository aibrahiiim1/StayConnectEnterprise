-- Reverse of 0075.
--
-- Returns the known building to a rolling window over recent sweeps, which is the circularity this migration
-- removed: a feed that stops mentioning rooms would once again be able to shrink the standard it is measured
-- against, and after enough partial sweeps would close stays whose guests are still in their rooms. Proven,
-- not theorised. This rollback exists for schema recovery, not as an operating mode.

BEGIN;

DROP FUNCTION IF EXISTS iam_v2.pms_roster_reconcile(uuid, uuid, uuid, bigint, text, boolean, text);
DROP FUNCTION IF EXISTS iam_v2.pms_rebaseline_room_inventory(uuid,uuid,uuid,bigint,text,text);

CREATE OR REPLACE FUNCTION iam_v2.pms_known_room_inventory(
    p_tenant uuid, p_site uuid, p_iface uuid, p_generation bigint, p_lookback integer
) RETURNS TABLE (room text)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $$
    SELECT DISTINCT r
      FROM iam_v2.pms_resync_coverage c, LATERAL unnest(c.rooms) r
     WHERE c.tenant_id = p_tenant AND c.site_id = p_site AND c.pms_interface_id = p_iface
       AND c.conflicting_rooms = 0
       AND c.resync_generation <= p_generation
       AND c.resync_generation > p_generation - p_lookback;
$$;

DROP TRIGGER IF EXISTS pms_room_inventory_changes_no_update ON iam_v2.pms_room_inventory_changes;
DROP TABLE IF EXISTS iam_v2.pms_room_inventory_changes;
DROP TABLE IF EXISTS iam_v2.pms_room_inventory;
DROP FUNCTION IF EXISTS iam_v2.pms_room_inventory_append_only();

COMMIT;
