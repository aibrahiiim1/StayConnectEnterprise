-- 0089's reverse changes nothing, deliberately, and that is not laziness.
--
-- TWO REASONS, and the second one is the load-bearing one.
--
-- 1. IT IS NOT REVERSIBLE BY THE ROLE THAT PERFORMS ROLLBACKS. Handing the three tables back to
--    iam_v2_migrator requires membership in iam_v2_migrator, and iam_v2_rollback is a member of
--    iam_v2_owner and of nothing else. Measured: the first version of this file was refused with
--    `ERROR: must be able to SET ROLE "iam_v2_migrator"`. An unrunnable down migration is worse than an
--    empty one, because it blocks the rollback of every migration ABOVE it -- the guarded runner walks the
--    ledger downwards one at a time, so a step that cannot run stops the whole descent.
--
-- 2. OWNERSHIP IS NOT THIS MIGRATION'S TO OWN ANY MORE. deploy/gatep/gatep-iam-roles.sql sweeps every
--    iam_v2 table to iam_v2_owner on each reconcile, because only the administrative role can reassign a
--    table away from an arbitrary prior owner. So even a successful reverse here would be undone by the
--    next deployment -- a migration fighting a deployment artefact over the same fact, with the deployment
--    winning. 0090 and Gate-P assert the invariant; this file recorded the one-time correction of three
--    tables that 0085 and 0087 created under the applying role.
--
-- What rolling 0089 back therefore means is exactly this: the ledger stops claiming the correction was
-- applied. The correction itself stands, and Gate-P keeps it standing.

BEGIN;
SELECT 1;  -- deliberately nothing; see above
COMMIT;
