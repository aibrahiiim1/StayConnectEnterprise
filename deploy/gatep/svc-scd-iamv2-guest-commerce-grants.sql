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
