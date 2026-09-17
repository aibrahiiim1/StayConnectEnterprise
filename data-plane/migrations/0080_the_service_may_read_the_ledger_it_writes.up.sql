-- THE SERVICE MAY READ THE LEDGER IT WRITES.
--
-- iam_v2.checkout_grace_policy_publications is the append-only record of every Checkout Grace policy this
-- hotel has published: who published it, why, and the exact terms that version put in force. It is WRITTEN
-- by iam_v2.publish_checkout_grace_policy, a SECURITY DEFINER function, which is why svc_edged has never
-- needed a privilege on the table itself — the function carries its own.
--
-- Reading it is a new requirement. Hotel Admin now offers Policy History, and an operator auditing who
-- changed what departing guests receive has to be able to see the record. Without this grant the endpoint
-- answered 500 on the appliance and the page said, correctly but uselessly, that it could not read the
-- ledger.
--
-- WHY A FIXTURE COULD NOT HAVE CAUGHT THIS, recorded because it is the general lesson rather than an excuse:
-- the integration harness connects as the schema owner, which can read everything. A test running as owner is
-- structurally incapable of finding a least-privilege defect. Only the appliance, running as svc_edged,
-- could produce it — and it did, on the first page load.
--
-- WHAT THIS GRANT IS, EXACTLY
-- ---------------------------
-- SELECT on ONE table, to ONE role. Nothing else.
--
--   * Not INSERT, UPDATE or DELETE. The table is append-only and enforced so by the
--     p3_grace_publication_appendonly trigger; the only legitimate writer is the controlled operation, which
--     already has what it needs. A service role that could write here could forge the provenance of a policy
--     change, which is the one thing this table exists to make impossible.
--   * Not a schema-wide or future-objects grant. A blanket GRANT ON ALL TABLES would have fixed today's
--     symptom and silently handed svc_edged every table added afterwards.
--   * Not to PUBLIC, and not to any other service role. pmsd, portald, netd, acctd and scd have no reason to
--     read an operator's policy history, and a privilege granted without a reason is one nobody later dares
--     to remove.
--
-- The rows are not guest data: a publication row carries the operator's id, a bounded reason code and the
-- policy scalars. No guest, stay, room or credential appears in it.

BEGIN;

DO $g$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
        GRANT SELECT ON iam_v2.checkout_grace_policy_publications TO svc_edged;
    END IF;
END;
$g$;

COMMIT;
