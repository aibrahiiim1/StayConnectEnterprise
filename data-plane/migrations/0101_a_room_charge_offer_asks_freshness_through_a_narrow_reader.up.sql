-- A ROOM-CHARGE OFFER ASKS WHETHER THE INTERFACE IS FRESH THROUGH A NARROW READER.
--
-- scd decides, when a verified room guest's packages are offered, whether Room charge may be one of them. One of
-- the preconditions is the interface's freshness (heartbeat, sync age -- p4_interface_freshness_block). That
-- function reads iam_v2.pms_interface_runtime with the caller's rights, and svc_scd deliberately holds NO
-- privilege on that table: the role being authorised must not read the feed health it is authorised against
-- (deploy/gatep/svc-scd-iamv2-guest-auth-grants.sql). So on an appliance the check failed with "permission
-- denied", which the offer treats as "no", and Room charge was never offered to anybody -- found on PRE-LIVE.
--
-- This is the same answer through a definer that returns one boolean for one interface of the caller's scope,
-- exactly as p3_guest_network_mirror_state does for sign-in. Additive: one function, no data change.
BEGIN;

CREATE FUNCTION iam_v2.p4_room_charge_interface_fresh(p_tenant uuid, p_site uuid, p_iface uuid)
  RETURNS boolean
  LANGUAGE sql STABLE SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
  SELECT COALESCE((SELECT iam_v2.p4_interface_freshness_block(p_tenant, p_site, p_iface, i.current_revision_id, now())
                     FROM iam_v2.pms_interfaces i
                    WHERE i.tenant_id = p_tenant AND i.site_id = p_site AND i.id = p_iface), 'X') = '';
$fn$;

REVOKE ALL ON FUNCTION iam_v2.p4_room_charge_interface_fresh(uuid,uuid,uuid) FROM PUBLIC;

DO $grant$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_scd') THEN
    GRANT EXECUTE ON FUNCTION iam_v2.p4_room_charge_interface_fresh(uuid,uuid,uuid) TO svc_scd;
  END IF;
END
$grant$;

DO $own$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
    EXECUTE 'ALTER FUNCTION iam_v2.p4_room_charge_interface_fresh(uuid,uuid,uuid) OWNER TO iam_v2_owner';
  END IF;
END
$own$;

COMMIT;
