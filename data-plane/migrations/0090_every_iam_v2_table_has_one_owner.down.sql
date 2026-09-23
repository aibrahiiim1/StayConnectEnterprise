-- 0090 asserts an invariant and changes nothing, so its reverse changes nothing either.
--
-- The assertion is a statement that ran once and either passed or refused the migration. There is no object
-- to drop and no privilege to withdraw. The ownership itself is established by
-- deploy/gatep/gatep-iam-roles.sql, which is a deployment artefact rather than schema, and is not touched
-- by rolling a migration back.
--
-- This file exists because every numbered migration has a reverse, and a missing one would make the
-- guarded down runner refuse the whole range above it.

BEGIN;
SELECT 1;  -- deliberately nothing
COMMIT;
