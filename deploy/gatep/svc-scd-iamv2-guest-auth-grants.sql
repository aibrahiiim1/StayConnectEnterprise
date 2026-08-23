-- Least-privilege grants for the IAM-v2 GUEST AUTHENTICATION surface (scd).
--
-- WHY THIS FILE EXISTS
-- --------------------
-- With IAM-v2 wired as the authority for VOUCHER and ACCOUNT at the real guest
-- entry points, both refused with IAMV2_UNAVAILABLE and scd logged
-- "iamv2: repository: account" / "iamv2: repository: voucher". The cause was
-- the same one that hit svc_edged (commerce) and svc_acctd (the controlled-
-- operation opener): svc_scd had never been granted anything on the iam_v2
-- authentication tables.
--
--     guest_access_accounts  select=f insert=f update=f
--     vouchers               select=f insert=f update=f
--     auth_contexts          select=f insert=f update=f
--     devices                select=t insert=t update=f
--
-- Three services, three separate discoveries of the same omission: the IAM-v2
-- schema was created and the per-service grants were written only for whichever
-- service was being debugged at the time. This file closes the guest-auth half.
--
-- SCOPE DISCIPLINE
-- ----------------
-- Every grant is derived from the statements in
-- data-plane/internal/iamv2/repo_pg.go, table by table and verb by verb. There
-- is deliberately no GRANT ... ON ALL TABLES IN SCHEMA iam_v2: scd must not be
-- able to read the financial ledger or the commerce admin tables just because
-- it needs to authenticate a guest.
--
-- Idempotent: re-granting an existing privilege is a no-op in PostgreSQL.

GRANT USAGE ON SCHEMA iam_v2 TO svc_scd;

-- ---- credential lookup (read only) ----------------------------------------
-- ResolveVoucherByHMAC reads the blind index; LookupAccount reads the argon2id
-- hash and the validity/lockout columns. Neither path writes the credential.
GRANT SELECT ON iam_v2.vouchers               TO svc_scd;
GRANT SELECT ON iam_v2.guest_access_accounts  TO svc_scd;

-- ---- identity resolution (OTP / SOCIAL principals) ------------------------
-- ResolvePrincipalByIdentity upserts a principal for a verified factor. Granted
-- now so enabling OTP or SOCIAL later is a flag change rather than another
-- round of this same discovery; both methods remain OFF on this appliance.
GRANT SELECT, INSERT ON iam_v2.guest_principals           TO svc_scd;
GRANT SELECT, INSERT ON iam_v2.guest_principal_identities TO svc_scd;

-- ---- device identity ------------------------------------------------------
-- UpsertDevice resolves a device by (tenant, mac) with INSERT ... ON CONFLICT
-- DO UPDATE, so it needs UPDATE as well as INSERT, and records the network
-- appearance as an append-only row.
GRANT SELECT, INSERT, UPDATE ON iam_v2.devices                    TO svc_scd;
-- The appearance row is an UPSERT (ON CONFLICT DO UPDATE SET last_seen), so INSERT alone is not enough:
-- PostgreSQL reports the missing UPDATE as a bare "permission denied for table
-- device_network_appearances", which reads like a missing INSERT and sent the first diagnosis the wrong way.
GRANT SELECT, INSERT, UPDATE ON iam_v2.device_network_appearances TO svc_scd;

-- ---- the auth context -----------------------------------------------------
-- CreateAuthContext writes a one-time context; ConsumeAuthContext marks it
-- consumed, so UPDATE is required. SELECT supports the pinned lookup.
GRANT SELECT, INSERT, UPDATE ON iam_v2.auth_contexts TO svc_scd;

-- ---- the resolution audit ---------------------------------------------------
-- Every PMS authentication attempt, successful or not, records one auth_resolutions row: the outcome code,
-- the guest network, the resolved Stay on success. It is the only durable record that an attempt happened,
-- and the resolver writes it BEFORE returning — so without INSERT the resolver fails, and because a resolver
-- failure is (correctly) folded into the uniform NOT_VERIFIED envelope, the visible symptom is every guest
-- being rejected with no reason on screen. The live commissioning of the Protel interface hit exactly that:
-- link healthy, 502 Stays ingested, a real in-house guest refused, and
--   "resolver: ERROR: permission denied for table auth_resolutions (SQLSTATE 42501)"
-- only in the daemon log.
--
-- SELECT as well as INSERT: the resolver reads back a prior row for the same resolution_request_id so a
-- retried request returns its original outcome instead of resolving twice.
--
-- No UPDATE and no DELETE. A resolution is a record of something that happened; it is never revised.
GRANT SELECT, INSERT ON iam_v2.auth_resolutions TO svc_scd;

-- ---- the Phase-3 PMS resolution read surface --------------------------------
-- STRICT resolution fans out across every PMS Interface mapped to the guest's network and asks each one
-- whether it holds exactly one matching in-house Stay. That is the whole of the PMS authentication method,
-- and every table it reads is listed here — derived from internal/pmsresolve and cmd/scd/phase3_auth.go.
--
-- None of it was granted. Commissioning the Protel interface surfaced it one table at a time, each as a
-- uniform NOT_VERIFIED on screen and a "permission denied for table ..." line in the daemon log, because a
-- resolver error is deliberately indistinguishable from a failed match to the guest. Read privileges only:
-- the resolver decides, it does not modify the Stay domain.
GRANT SELECT ON iam_v2.guest_network_pms_map      TO svc_scd; -- which interfaces this network resolves against
GRANT SELECT ON iam_v2.pms_interfaces             TO svc_scd; -- candidate interfaces and their lifecycle
GRANT SELECT ON iam_v2.pms_interface_revisions    TO svc_scd; -- the revision each decision is pinned to
GRANT SELECT ON iam_v2.stays                      TO svc_scd; -- the in-house Stay being matched
GRANT SELECT ON iam_v2.stay_guests                TO svc_scd; -- the surname evidence the match is made on
GRANT SELECT ON iam_v2.auth_context_offers        TO svc_scd; -- offers already recorded against a context

-- Minting the one-time Auth Context on success is an operation, not a raw INSERT: issue_or_return_pms_context
-- is idempotent per resolution request, so a retried resolution returns the SAME context instead of minting a
-- second one for the same guest.
GRANT EXECUTE ON FUNCTION iam_v2.issue_or_return_pms_context(
  uuid, uuid, uuid, uuid, uuid, uuid, uuid, uuid, integer) TO svc_scd;

-- Recording WHAT the verified guest was offered is part of the same resolution, not a later step: the
-- response scd returns names those offers, and the record is the audit trail of what was on the screen.
--
-- This grant was missing, and only svc_netd had it. Nothing noticed for as long as it was unreachable —
-- every resolution failed earlier, at CONTEXT_INVALID, so the offer stage was never entered. The first
-- resolution that ever got this far died on "permission denied for function record_auth_context_offer",
-- which the guest saw as the same uniform failure as a wrong surname.
--
-- The feed-health and evidence rules that let a guest reach this point are eligibility. This is not: it is
-- scd writing down its own answer, and refusing it turns a successful authentication into a failed one.
GRANT EXECUTE ON FUNCTION iam_v2.record_auth_context_offer(
  uuid, uuid, uuid, uuid, integer, bigint, timestamptz) TO svc_scd;

-- The auth_context family is guarded in the DATABASE: a trigger refuses any
-- write not inside a transaction that has opened a controlled operation. This
-- is the same minimum already granted to svc_edged and svc_acctd -- it lets scd
-- OPEN an operation; it does not let it bypass one.
GRANT EXECUTE ON FUNCTION iam_v2.begin_controlled_operation(text) TO svc_scd;

-- ...and the CHECKER the trigger itself calls. Opening an operation and having the guard verify it open are
-- two different functions, and granting only the opener leaves the write failing with
-- "permission denied for function p3_controlled_operation_open" -- an error that names a function the
-- application never calls directly, which is why it reads as a schema fault rather than a missing grant.
-- Both Phase-3 and Phase-5 checkers exist and the auth_context family may be guarded by either.
GRANT EXECUTE ON FUNCTION iam_v2.p3_controlled_operation_open(text) TO svc_scd;
GRANT EXECUTE ON FUNCTION iam_v2.p5_controlled_operation_open(text) TO svc_scd;
GRANT EXECUTE ON FUNCTION iam_v2.p5_begin_controlled_operation(text) TO svc_scd;

-- NOT granted, on purpose:
--   * DELETE on anything -- no authentication path deletes;
--   * UPDATE on vouchers or guest_access_accounts from THIS file. Redemption
--     and lockout accounting are writes the accepted domain performs through
--     its own guarded paths; if a future adapter needs them they belong here as
--     their own lines, with the failure that prompted them recorded;
--   * any privilege on the Phase-4 financial ledger or the commerce admin
--     tables -- authenticating a guest is not a reason to reach either.
