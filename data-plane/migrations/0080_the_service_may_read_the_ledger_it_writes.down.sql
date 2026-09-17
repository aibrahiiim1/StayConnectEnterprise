-- Withdrawing svc_edged's read of the Checkout Grace publication ledger.
--
-- This direction is clean: it removes a privilege and nothing else. No data is written, moved or lost, and
-- no other grant is touched.
--
-- WHAT IT COSTS, so that a rollback is an informed choice rather than a surprise. Hotel Admin's Policy
-- History panel stops being able to read the ledger. It does not break and it does not lie: the endpoint
-- reports availability honestly and the page says it cannot see the record, explicitly distinguishing that
-- from "no policy has ever been published". Every other part of Checkout Grace — the policy in force,
-- publishing a new version, and the conversion that gives departing guests their grace — is unaffected,
-- because none of them reads this table. The record itself is untouched and keeps accumulating.

BEGIN;

DO $g$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
        REVOKE SELECT ON iam_v2.checkout_grace_policy_publications FROM svc_edged;
    END IF;
END;
$g$;

COMMIT;
