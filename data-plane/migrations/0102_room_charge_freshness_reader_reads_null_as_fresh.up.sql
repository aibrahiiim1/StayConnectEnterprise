-- THE ROOM-CHARGE FRESHNESS READER READS "NO BLOCK" CORRECTLY.
--
-- p4_interface_freshness_block returns NULL when the interface is fresh and a reason code when it is not. The
-- reader added by 0101 (and the statement it replaced) compared COALESCE(block, 'X') = '', which is never true
-- for a fresh interface, so Room charge was still never offered once svc_scd could ask. Found on PRE-LIVE
-- straight after 0101. The answer is now: the interface exists in the caller's scope AND its block is NULL.
BEGIN;

CREATE OR REPLACE FUNCTION iam_v2.p4_room_charge_interface_fresh(p_tenant uuid, p_site uuid, p_iface uuid)
  RETURNS boolean
  LANGUAGE sql STABLE SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
  SELECT EXISTS (SELECT 1 FROM iam_v2.pms_interfaces i
                  WHERE i.tenant_id = p_tenant AND i.site_id = p_site AND i.id = p_iface
                    AND iam_v2.p4_interface_freshness_block(p_tenant, p_site, p_iface, i.current_revision_id, now()) IS NULL);
$fn$;

COMMIT;
