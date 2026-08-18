-- iam_v2_owner: SELECT on public.operators.
--
-- iam_v2.publish_checkout_grace_policy is SECURITY DEFINER owned by iam_v2_owner, and it validates the ACTOR
-- against public.operators ("an existing, active operator of this tenant -- a policy nobody can be held to is
-- not governed"). A SECURITY DEFINER function runs as its OWNER, so the privilege that matters is
-- iam_v2_owner's, not the caller's: granting the calling service SELECT changes nothing and the publication
-- still fails with "permission denied for table operators".
--
-- Read-only, and only this table. The actor check reads; it never writes an operator.
GRANT SELECT ON public.operators TO iam_v2_owner;
