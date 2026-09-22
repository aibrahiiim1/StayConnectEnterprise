-- THE SERVICE MAY READ THE TRAIL IT WRITES.
--
-- public.audit_log is the appliance's record of who did what: every operator action edged performs, every
-- system action scd takes, with the actor, the target, the address it came from and the payload. Both
-- services have INSERT. Neither has SELECT, and no other service role does either — so until now the only
-- way to read this hotel's audit trail was a direct database session as the schema owner.
--
-- WHAT THAT LOOKED LIKE TO AN OPERATOR. The Audit log screen said:
--
--     No audit entries
--     Operator and system actions are recorded here.
--
-- On the PRE-LIVE appliance that sentence was false in the most expensive way available. 379 rows existed,
-- 345 of them scoped to this tenant, running from the day the appliance was commissioned on 2026-08-21 to
-- the morning this was found. Among them: the licence-enforcement attempt that the security design exists to
-- record. The screen reported that nothing had ever happened.
--
-- It said that rather than reporting an error because the handler never checked rows.Err(). pgx surfaces a
-- permission failure on the first Next(), not at Query(), so "permission denied for table audit_log" arrived
-- as an empty result set and HTTP 200. That defect is fixed in the same delivery; this migration is the
-- other half, because an honest error message is still not an audit trail.
--
-- WHY A FIXTURE COULD NOT HAVE CAUGHT THIS. The same reason recorded in 0080: the integration harness
-- connects as the schema owner, which can read everything, so a test running as owner is structurally
-- incapable of finding a least-privilege defect. Only the appliance, running as svc_edged, could produce it.
-- Two deliveries apart, the identical shape of bug. The lesson is not "remember the grant" — it is that
-- least-privilege defects are invisible to owner-connected tests by construction.
--
-- WHAT THIS GRANT IS, EXACTLY
-- ---------------------------
-- SELECT on ONE table, to ONE role. Nothing else.
--
--   * Not INSERT — svc_edged already has it, and writing is not what was missing.
--   * Not UPDATE or DELETE, to any role, ever. An audit trail that a service can rewrite is not one. The
--     absence of those privileges is the property that makes the table worth reading at all.
--   * Not a schema-wide or future-objects grant. A blanket GRANT ON ALL TABLES IN SCHEMA public would have
--     cleared today's symptom and silently handed svc_edged every table added afterwards.
--   * Not to PUBLIC, and not to svc_scd, pmsd, portald, netd or acctd. None of them serves an operator
--     screen, and a privilege granted without a reason is one nobody later dares to remove.
--
-- WHAT edged MAY THEN SHOW. Its handler already filters `WHERE tenant_id = $1` against the tenant the signed
-- assignment binds this appliance to, so the grant does not widen what an operator sees beyond their own
-- property. Rows carry operator identities, action names, targets and addresses — not guest credentials,
-- room numbers or names.
--
-- No row is written, altered, deleted or backfilled by this migration. The history shown after it is the
-- history that was already there.

BEGIN;

DO $g$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
        GRANT SELECT ON public.audit_log TO svc_edged;
    END IF;
END;
$g$;

COMMIT;
