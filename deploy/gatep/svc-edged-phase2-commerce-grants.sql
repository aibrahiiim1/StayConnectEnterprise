-- Least-privilege grants for the Phase-2 commercial-packages ADMIN surface (edged).
--
-- WHY THIS FILE EXISTS
-- --------------------
-- With the Phase-2 admin surface enabled, GET /edge/v1/commercial-packages and
-- .../plans returned HTTP 500 "list failed". The handler swallows the driver
-- error, so the 500 looked like an application bug; running the repository's
-- own query as the real service role gave the actual answer in one line:
--
--     ERROR: permission denied for table service_plans
--
-- svc_edged had never been granted anything on the iam_v2 commerce tables. The
-- boundary was working exactly as designed -- the grants for this surface were
-- simply never written.
--
-- SCOPE DISCIPLINE
-- ----------------
-- Every grant below is derived from the statements in
-- data-plane/internal/iamv2/commerce_admin_repo_pg.go, table by table and verb
-- by verb. There is deliberately NO
--   GRANT ... ON ALL TABLES IN SCHEMA iam_v2
-- and no DEFAULT PRIVILEGES: a blanket grant would have fixed the 500 in one
-- line while silently handing edged read/write over the whole IAM-v2 domain,
-- including guest credentials and the financial ledger. If a future statement
-- needs another table, it belongs in this file as its own line, and the 500 it
-- produces first is the feature, not the bug.
--
-- Idempotent: re-granting an existing privilege is a no-op in PostgreSQL.

-- The schema itself must be reachable before any table grant means anything.
GRANT USAGE ON SCHEMA iam_v2 TO svc_edged;

-- ---- read paths -----------------------------------------------------------
-- listCommercialPackages / listServicePlans / listCommercialPackageRevisions /
-- listServicePlanRevisions / listCommerceQuotes / listCommercePurchases /
-- getGraceConfig.
GRANT SELECT ON iam_v2.internet_packages           TO svc_edged;
GRANT SELECT ON iam_v2.internet_package_revisions  TO svc_edged;
GRANT SELECT ON iam_v2.service_plans               TO svc_edged;
GRANT SELECT ON iam_v2.service_plan_revisions      TO svc_edged;
GRANT SELECT ON iam_v2.offer_quotes                TO svc_edged;
GRANT SELECT ON iam_v2.purchases                   TO svc_edged;
GRANT SELECT ON iam_v2.site_checkout_grace_config  TO svc_edged;

-- ---- write paths ----------------------------------------------------------
-- publishCommercialPackage and publishServicePlan create the parent row with
-- INSERT ... ON CONFLICT DO UPDATE, which requires INSERT *and* UPDATE, then
-- append an immutable revision.
GRANT INSERT, UPDATE ON iam_v2.internet_packages   TO svc_edged;
GRANT INSERT, UPDATE ON iam_v2.service_plans       TO svc_edged;

-- Revisions are append-only by design: INSERT, and deliberately no UPDATE and
-- no DELETE, so the immutability of a published revision is enforced by the
-- database and not only by application code.
GRANT INSERT ON iam_v2.internet_package_revisions  TO svc_edged;
GRANT INSERT ON iam_v2.service_plan_revisions      TO svc_edged;

-- Child rows written as part of publishing a package revision. INSERT only,
-- for the same append-only reason.
GRANT INSERT ON iam_v2.package_eligibility_rules   TO svc_edged;
GRANT INSERT ON iam_v2.package_grant_tiers         TO svc_edged;

-- Grace policy is NOT written directly, and the absence of a table grant here is the point. See the D32
-- section at the end of this file.

-- NOT granted, on purpose, and each absence is load-bearing:
--   * DELETE on anything -- no admin path deletes commerce rows;
--   * any privilege on iam_v2 vouchers, sessions or devices -- the admin
--     commerce surface never reads guest secrets or session state, and the 500
--     it would raise if it tried is the boundary reporting itself.
--     (guest_access_accounts is the ONE exception, granted at the end of this
--     file for credential ISSUANCE only, and INSERT/SELECT only -- see there;)
--   * any privilege on the Phase-4 financial ledger tables.

-- ---------------------------------------------------------------------------
-- IAM-v2 GUEST-ACCOUNT ISSUANCE (added when the issuance gap was found)
--
-- edged had no IAM-v2 issuance path at all, so an appliance with ACCOUNT
-- enabled authenticated against iam_v2.guest_access_accounts while nothing in
-- the runtime could ever put a row there. With issuance switched to IAM-v2,
-- edged needs exactly this and nothing more.
--
-- INSERT and SELECT only: no UPDATE and no DELETE. Editing or removing a
-- credential is a separate admin capability and must be granted deliberately
-- when it is implemented, not acquired as a side effect of being able to create.
GRANT INSERT, SELECT ON iam_v2.guest_access_accounts TO svc_edged;

-- ---- and the rest of the credential LIFECYCLE ------------------------------
-- Granting only INSERT/SELECT left a split authority that was worse than either
-- side alone: create wrote iam_v2 while list, get, patch, set-password,
-- disconnect and delete still used public.guest_accounts, so an IAM-v2 account
-- was invisible to the API that had just created it. With every operation now
-- routed to the configured authority, edged needs UPDATE (patch, password
-- reset, lockout clear) and DELETE (revoke) on the credential itself.
GRANT UPDATE, DELETE ON iam_v2.guest_access_accounts TO svc_edged;

-- Disconnect ends the account's live IAM-v2 sessions and the list/get views
-- report how many devices are currently online for it. Read-only on
-- entitlements: edged resolves which entitlement belongs to the account but
-- never alters the grant itself.
GRANT SELECT, UPDATE ON iam_v2.sessions     TO svc_edged;
GRANT SELECT         ON iam_v2.entitlements TO svc_edged;

-- ---------------------------------------------------------------------------
-- D32: the grace POLICY path (Hotel Admin controls policy only)
--
-- WHAT CHANGED, AND WHY THE EARLIER GRANTS ARE NOW REVOKED
-- --------------------------------------------------------
-- This file first granted edged INSERT/UPDATE on iam_v2.site_checkout_grace_config and EXECUTE on the RAW
-- writer iam_v2.publish_checkout_grace_config. Both are now withdrawn.
--
-- The raw writer records no actor, no reason code and no version, performs no optimistic-concurrency check and
-- appends no audit row. Under D32 every one of those is what makes a change to what departing guests receive
-- attributable, and safe when two operators edit at once. Leaving a working raw path beside the canonical
-- audited one would mean those guarantees hold only for callers who happened to pick the right door -- and the
-- Go-side retirement of that path is only as strong as the privilege behind it. So the privilege goes too.
--
-- The direct table grant is withdrawn for the same reason. site_checkout_grace_config resolves to NO
-- capability family under p3_controlled_writer_only, i.e. it is writable only by the owner of the publication
-- function; the grant never actually enabled a direct write, it merely made one look permitted. A privilege
-- that cannot be exercised but reads as authorisation is worse than no privilege at all, because it invites
-- the next person to write the bypass and discover the refusal at runtime.
--
-- REVOKEs are unconditional and idempotent: revoking a privilege that was never granted is a no-op, so a
-- freshly built environment converges to the same effective set as an upgraded one.
REVOKE INSERT, UPDATE ON iam_v2.site_checkout_grace_config FROM svc_edged;
DO $$
BEGIN
  -- Guarded only because the raw function may not exist in a database built after it was retired; the REVOKE
  -- must not turn that into a failure of the whole bootstrap.
  IF EXISTS (
    SELECT 1 FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
     WHERE n.nspname = 'iam_v2' AND p.proname = 'publish_checkout_grace_config'
  ) THEN
    EXECUTE 'REVOKE EXECUTE ON FUNCTION iam_v2.publish_checkout_grace_config('
         || 'uuid, uuid, uuid, integer, integer, integer, bigint, integer, text, integer) FROM svc_edged';
  END IF;
END $$;

-- The EXECUTE grants for the canonical audited boundary, and the SELECT that iam_v2_owner needs to validate
-- the actor, live in gatep-grants.sql -- which includes this file and therefore applies them after it.

-- Provisioning creates the hidden system package row (D32). INSERT only: the revisions it publishes are
-- append-only and it never deletes.
GRANT INSERT ON iam_v2.internet_packages TO svc_edged;
