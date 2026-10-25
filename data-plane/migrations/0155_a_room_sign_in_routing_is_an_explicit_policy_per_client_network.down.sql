-- Down for 0155. The explicit policies are lost; the interfaces each network is mapped to stay as they are, so a network keeps resolving against exactly the
-- interfaces it was mapped to. A network deliberately set to NONE becomes unconfigured, which also offers room sign-in to nobody.

BEGIN;

DROP FUNCTION IF EXISTS iam_v2.p3_interface_can_authorise(uuid, uuid, uuid);
DROP TRIGGER IF EXISTS gnpp_map_consistent ON iam_v2.guest_network_pms_map;
DROP TABLE IF EXISTS iam_v2.guest_network_pms_policy;
DROP FUNCTION IF EXISTS iam_v2.gnpp_consistent();

COMMIT;
