-- Remove the scoped row-lock helpers the guest-auth chain takes.
--
-- Rolling this back returns the consume path to taking SELECT ... FOR UPDATE inline on
-- iam_v2.pms_interface_runtime, which svc_scd cannot do: every Room Login would again verify cleanly and then
-- fail at the grant. The correct rollback for a broken grant is the previous scd binary, not a wider grant on
-- the PMS feed's own state table.

BEGIN;

DROP FUNCTION IF EXISTS iam_v2.lock_pms_interface_runtime(uuid,uuid,uuid);
DROP FUNCTION IF EXISTS iam_v2.lock_stay(uuid,uuid,uuid);
DROP FUNCTION IF EXISTS iam_v2.lock_origin_stay(uuid,uuid,uuid);

-- ...and the predicate goes back to invoker rights, which is what it was before. Note that this alone breaks
-- the Go consume path again: it cannot read the tables the predicate touches.
ALTER FUNCTION iam_v2.p3_feed_authorizes(uuid,uuid,uuid,uuid,timestamptz) SECURITY INVOKER;
ALTER FUNCTION iam_v2.p3_feed_authorizes(uuid,uuid,uuid,uuid,timestamptz) RESET search_path;

COMMIT;
