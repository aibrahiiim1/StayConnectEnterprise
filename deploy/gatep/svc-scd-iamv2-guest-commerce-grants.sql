-- Least-privilege grants for the IAM-v2 GUEST COMMERCE surface (scd).
--
-- With guest authentication actually running on IAM-v2, the next step of the
-- accepted journey -- list eligible packages, quote, confirm -- failed with
-- "iamv2: repository: list" and, in the PostgreSQL log,
-- "permission denied for table internet_packages".
--
-- This is the fourth service-and-surface pair to need its own grant file
-- (svc_edged/commerce-admin, svc_acctd/opener, svc_scd/guest-auth, and now
-- svc_scd/guest-commerce). The pattern is consistent: the iam_v2 schema was
-- created wholesale and privileges were written only for whichever path someone
-- was actively exercising, so every newly exercised path finds its own gap.
--
-- Derived statement by statement from
-- data-plane/internal/iamv2/commerce_repo_pg.go. No GRANT ON ALL TABLES: the
-- guest path must not acquire the commerce ADMIN write surface just because it
-- needs to read what it may buy.

-- ---- what the guest may be offered (read only) ----------------------------
GRANT SELECT ON iam_v2.internet_packages           TO svc_scd;
GRANT SELECT ON iam_v2.internet_package_revisions  TO svc_scd;
GRANT SELECT ON iam_v2.service_plan_revisions      TO svc_scd;
GRANT SELECT ON iam_v2.package_eligibility_rules   TO svc_scd;
GRANT SELECT ON iam_v2.package_grant_tiers         TO svc_scd;

-- ---- the acquisition chain ------------------------------------------------
-- A quote is created and later marked consumed; a purchase and its settlement
-- are appended. No DELETE anywhere: the commercial record is append-only, and
-- that is enforced here as well as in application code.
GRANT SELECT, INSERT, UPDATE ON iam_v2.offer_quotes TO svc_scd;
GRANT SELECT, INSERT         ON iam_v2.purchases    TO svc_scd;
GRANT SELECT, INSERT         ON iam_v2.settlements  TO svc_scd;

-- ---- what the purchase becomes -------------------------------------------
-- The entitlement and the IAM-v2 session are the authoritative grant of access.
-- SELECT is needed to enforce device limits and to reconnect an existing
-- entitlement rather than issue a second one.
GRANT SELECT, INSERT, UPDATE ON iam_v2.entitlements TO svc_scd;
GRANT SELECT, INSERT, UPDATE ON iam_v2.sessions     TO svc_scd;

-- SELECT (and only SELECT) on the device bindings. Opening a session fires
-- iam_v2.p6_session_requires_authorized_binding, a trigger that runs with the WRITER's rights and reads
-- iam_v2.entitlement_devices to refuse a live session on a device that holds no authorized binding. Without
-- this the INSERT into iam_v2.sessions fails with "permission denied for table entitlement_devices" — an
-- error naming a table the handler never mentions, raised by a guard that is doing its job.
--
-- The bindings themselves are written by iam_v2.authorize_entitlement_device, granted above; scd reads them
-- and never writes them directly.
GRANT SELECT ON iam_v2.entitlement_devices TO svc_scd;

-- NOT granted: any write to internet_packages, its revisions, service plans or
-- their revisions. A guest buying a package must never be able to change what
-- the package IS -- the immutable revision pin depends on exactly that.

-- ---- the entitlement grant kernel -----------------------------------------
-- Confirming a free purchase does not write iam_v2.entitlements directly: it
-- calls a kernel function that performs the grant atomically (supersede any live
-- entitlement for the subject, insert the new one, mark the purchase granted).
-- Without EXECUTE the confirm step fails with
-- "permission denied for function p4_grant_quoted_entitlement", which names a
-- function the handler never mentions and so reads as a schema fault.
--
-- Exactly the four the guest commerce repository calls, derived from
-- commerce_repo_pg.go -- not EXECUTE ON ALL FUNCTIONS, which would hand scd the
-- financial-recovery, compliance-archive and payment-provider kernels too.
GRANT EXECUTE ON FUNCTION iam_v2.p4_grant_quoted_entitlement(uuid, uuid, uuid) TO svc_scd;
GRANT EXECUTE ON FUNCTION iam_v2.p4_mark_purchase_granted(uuid) TO svc_scd;
GRANT EXECUTE ON FUNCTION iam_v2.p4_insert_entitlement(uuid, uuid, uuid, uuid, uuid, uuid, jsonb, uuid, uuid, text, text, timestamptz, uuid) TO svc_scd;
GRANT EXECUTE ON FUNCTION iam_v2.p4_terminate_live_entitlement_for_subject(uuid, uuid, uuid, uuid, uuid) TO svc_scd;

-- ---- the Entitlement STATE MACHINE ----------------------------------------
-- iam_v2.apply_entitlement_transition is the ONLY writer of an entitlement's state, and
-- p3_entitlement_controlled_writer refuses any other route. internal/staygrant calls it to move the freshly
-- inserted entitlement to ACTIVE with reason GRANT, so a Room Login that reaches this point without EXECUTE
-- dies with "permission denied for function apply_entitlement_transition" — after the Purchase has been
-- written, which means the guest is refused at the very last step of a grant that otherwise succeeded.
--
-- svc_netd, svc_acctd and svc_pmsd already hold exactly this grant for their own transitions; the guest
-- grant path is the one that was missed, because it had never been exercised as this role.
GRANT EXECUTE ON FUNCTION iam_v2.apply_entitlement_transition(uuid, text, timestamptz, text) TO svc_scd;

-- ---- the DEVICE AUTHORIZATION writer --------------------------------------
-- iam_v2.entitlement_device_authorizations is capability-scoped in the database: nothing writes it except
-- iam_v2.authorize_entitlement_device, which enforces the per-plan device limit as it inserts. That is the
-- statement that turns "this guest has an entitlement" into "this laptop may use it", so without EXECUTE the
-- grant fails one step past the entitlement transition, again after the Purchase is durable.
GRANT EXECUTE ON FUNCTION iam_v2.authorize_entitlement_device(uuid, uuid, timestamptz) TO svc_scd;
