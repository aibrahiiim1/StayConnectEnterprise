-- Withdrawing svc_edged's read of the usage evidence.
--
-- This direction is clean: it removes two privileges and nothing else. No row is written, moved or lost, and
-- no other grant is touched. acctd goes on writing the records and scd goes on enforcing quota against them
-- exactly as before — rolling this back stops an operator READING the evidence, it does not stop the
-- appliance recording it.
--
-- WHAT IT COSTS, so that a rollback is an informed choice rather than a surprise. The Usage Explorer loses
-- the per-sample timeline and can no longer name a device by its MAC. It does not go back to guessing or
-- estimating: the endpoint reports that the evidence cannot be read, which is the only honest answer
-- available without these grants. Session and stay totals, which come from tables svc_edged could already
-- read, are unaffected.

BEGIN;

DO $g$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
        REVOKE SELECT ON iam_v2.accounting_records FROM svc_edged;
        REVOKE SELECT ON iam_v2.devices FROM svc_edged;
    END IF;
END;
$g$;

COMMIT;
