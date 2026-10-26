-- Down for 0155. Every mapped interface stays as it is. The explicit policies are copied to iam_v2.guest_network_pms_policy_rolled_back before the table goes, so
-- applying 0155 again restores the ones that still agree with the mapping.
--
-- It REFUSES while any Shared STRICT policy exists (the guard below): no release that predates the policy protects a shared set, and scripts/binary-rollback.sh refuses
-- the same rollback of scd and edged. Roll the software back first (scd, edged, console), then run this.

BEGIN;

-- GUARD BEGIN
DO $guard$
DECLARE n int;
BEGIN
  IF to_regclass('iam_v2.guest_network_pms_policy') IS NOT NULL THEN
    SELECT count(*) INTO n FROM iam_v2.guest_network_pms_policy WHERE mode = 'SHARED_STRICT';
    IF n > 0 THEN
      RAISE EXCEPTION 'refusing to roll back 0155: % network(s) are served by a Shared STRICT set. Without the policy a release resolves them without the cap, the out-of-service guard and the per-candidate feed check. Change each to a dedicated or no-room-sign-in policy under PMS routing first.', n
        USING ERRCODE = 'P0001';
    END IF;
  END IF;
END
$guard$;
-- GUARD END

DROP TABLE IF EXISTS iam_v2.guest_network_pms_policy_rolled_back;
CREATE TABLE iam_v2.guest_network_pms_policy_rolled_back AS SELECT p.*, now() AS rolled_back_at FROM iam_v2.guest_network_pms_policy p;
DROP FUNCTION IF EXISTS iam_v2.p3_interface_can_authorise(uuid, uuid, uuid);
DROP TRIGGER IF EXISTS gnpp_map_consistent ON iam_v2.guest_network_pms_map;
DROP TABLE IF EXISTS iam_v2.guest_network_pms_policy;
DROP FUNCTION IF EXISTS iam_v2.gnpp_consistent();

COMMIT;
