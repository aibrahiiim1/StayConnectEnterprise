-- Down for 0155. Every mapped interface stays as it is. The explicit policies are copied to iam_v2.guest_network_pms_policy_rolled_back before the table goes, so
-- applying 0155 again restores the ones that still agree with the mapping instead of leaving shared and none networks undecided.
--
-- Roll the SOFTWARE back first (scd, edged, console), then this: a release that does not know the policy treats a network with several mapped interfaces as the
-- strict set it always resolved, but without the cap, the per-candidate freshness check or the out-of-service guard.

BEGIN;

DROP TABLE IF EXISTS iam_v2.guest_network_pms_policy_rolled_back;
CREATE TABLE iam_v2.guest_network_pms_policy_rolled_back AS SELECT p.*, now() AS rolled_back_at FROM iam_v2.guest_network_pms_policy p;
DROP FUNCTION IF EXISTS iam_v2.p3_interface_can_authorise(uuid, uuid, uuid);
DROP TRIGGER IF EXISTS gnpp_map_consistent ON iam_v2.guest_network_pms_map;
DROP TABLE IF EXISTS iam_v2.guest_network_pms_policy;
DROP FUNCTION IF EXISTS iam_v2.gnpp_consistent();

COMMIT;
