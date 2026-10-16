-- Client accounts: Disable suspends the account's package and Enable resumes it (migration 0120, decision D-A).
-- Idempotent; included by gatep-grants.sql after its revoke, so a Gate-P reconcile reproduces exactly what the
-- migration granted.
--
--   svc_edged  aligns one account's packages with its enabled flag only through the definer function, which
--              moves status through apply_entitlement_transition. edged still holds no UPDATE on entitlement
--              status and no EXECUTE on the transition writer itself.
GRANT EXECUTE ON FUNCTION iam_v2.account_entitlements_follow_access(uuid,uuid,uuid) TO svc_edged;
