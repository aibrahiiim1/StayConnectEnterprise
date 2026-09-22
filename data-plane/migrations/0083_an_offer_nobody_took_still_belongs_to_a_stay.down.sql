-- Withdrawing the five-column read that lets an untaken offer name its stay.
--
-- Clean in this direction: it removes column privileges and nothing else. No row is written, moved or lost,
-- no other grant is touched, and the write-guard trigger on iam_v2.auth_contexts was never involved.
--
-- WHAT IT COSTS, so rolling back is an informed choice rather than a surprise. Guest Activity goes back to
-- naming a room only for offers that were TAKEN, where the purchase carries the stay. An offer that expired
-- unused shows what was offered, when, at what price and that nobody took it — with the room blank. It does
-- not start guessing: attribution comes from the recorded link or it does not happen.

BEGIN;

DO $g$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
        REVOKE SELECT (id, tenant_id, site_id, stay_id, pms_interface_id)
            ON iam_v2.auth_contexts FROM svc_edged;
    END IF;
END;
$g$;

COMMIT;
