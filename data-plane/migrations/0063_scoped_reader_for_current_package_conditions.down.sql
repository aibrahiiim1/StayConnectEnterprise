-- Remove the scoped package-conditions reader.
--
-- WHAT ROLLING BACK COSTS: Hotel Admin can no longer load a package's eligibility rules and grant tiers, so
-- package EDIT refuses to open again — deliberately, because a form that cannot read the existing conditions
-- would republish without them. The package list, Add, Enable/Disable and History are unaffected.
--
-- Nothing is granted back in compensation. In particular this does NOT fall back to giving svc_edged direct
-- SELECT on the protected tables: the whole point of the function was to avoid that, and a rollback that
-- widened the privilege it replaced would be worse than the state it returns to.

BEGIN;

DROP FUNCTION IF EXISTS iam_v2.p2_package_current_conditions(uuid, uuid, uuid);

COMMIT;
