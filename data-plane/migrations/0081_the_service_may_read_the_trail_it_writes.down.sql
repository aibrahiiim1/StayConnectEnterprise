-- Withdrawing svc_edged's read of the audit trail.
--
-- This direction is clean: it removes a privilege and nothing else. No row is written, moved or lost, and no
-- other grant is touched. svc_edged keeps INSERT, so the trail goes on being recorded exactly as before —
-- rolling this back stops operators READING the history, it does not stop the appliance writing it.
--
-- WHAT IT COSTS, so that a rollback is an informed choice rather than a surprise. The Audit log screen stops
-- being able to read the table. It does not go back to claiming there are no entries: the handler checks
-- rows.Err() now and reports that the trail could not be read, which is a different sentence from "nothing
-- has happened" and the only honest one available without this grant. Every other screen is unaffected —
-- nothing else reads this table.

BEGIN;

DO $g$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
        REVOKE SELECT ON public.audit_log FROM svc_edged;
    END IF;
END;
$g$;

COMMIT;
