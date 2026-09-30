BEGIN;
CREATE OR REPLACE FUNCTION iam_v2.p4_room_charge_interface_fresh(p_tenant uuid, p_site uuid, p_iface uuid)
  RETURNS boolean
  LANGUAGE sql STABLE SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
  SELECT COALESCE((SELECT iam_v2.p4_interface_freshness_block(p_tenant, p_site, p_iface, i.current_revision_id, now())
                     FROM iam_v2.pms_interfaces i
                    WHERE i.tenant_id = p_tenant AND i.site_id = p_site AND i.id = p_iface), 'X') = '';
$fn$;
COMMIT;
