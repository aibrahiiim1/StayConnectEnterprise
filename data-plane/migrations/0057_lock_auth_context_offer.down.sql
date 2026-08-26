-- Remove the scoped offer-lock helper.
--
-- Rolling this back returns the grant to taking SELECT ... FOR UPDATE inline, which svc_scd cannot do: the
-- first real Room Login would again fail at the grant with a permission error. The correct rollback for a
-- broken grant is the previous scd binary, not a wider table grant.

BEGIN;

DROP FUNCTION IF EXISTS iam_v2.lock_auth_context_offer(uuid,uuid,uuid,uuid);

COMMIT;
