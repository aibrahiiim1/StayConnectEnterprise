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

-- The feed-health predicate the PMS arm evaluates at issue and again at consume, and the fail-closed config
-- reader it calls.
--
-- Migrations 0050 and 0051 grant these to svc_scd, and that is not sufficient — it is the same mistake that
-- silently disabled the expiry sweep. A reconcile revokes every iam_v2 function privilege and re-grants only
-- what these per-service files name, so a migration-only grant lasts until the next reconcile and no longer.
-- Observed directly: after a reconcile on PRE-LIVE these functions were left executable by scd only because
-- CREATE FUNCTION's default ACL grants EXECUTE to PUBLIC, which is neither least privilege nor something to
-- depend on.
--
-- So PUBLIC is withdrawn and scd is named explicitly. Withdrawing PUBLIC is the point: an authentication
-- predicate that every role in the database may evaluate is an information disclosure about occupancy, and
-- it was never intended — it is what a bare CREATE FUNCTION leaves behind when nobody says otherwise.
REVOKE ALL ON FUNCTION iam_v2.p3_feed_authorizes(uuid, uuid, uuid, uuid, timestamptz) FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.p3_cfg_secs(jsonb, text, int)                            FROM PUBLIC;
GRANT EXECUTE ON FUNCTION iam_v2.p3_feed_authorizes(uuid, uuid, uuid, uuid, timestamptz) TO svc_scd;
GRANT EXECUTE ON FUNCTION iam_v2.p3_cfg_secs(jsonb, text, int)                           TO svc_scd;

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

-- ---------------------------------------------------------------------------
-- THE TWO ROW LOCKS THE GRANT TAKES, as scoped EXECUTE rather than table UPDATE.
--
-- PostgreSQL requires UPDATE as well as SELECT for SELECT ... FOR UPDATE. Both of these rows are ones scd
-- must LOCK and must never WRITE, so each lock lives behind a SECURITY DEFINER function that takes it and
-- returns nothing else:
--
--   * iam_v2.lock_auth_context_offer (migration 0057) -- UPDATE on iam_v2.auth_context_offers would let scd
--     rewrite the matched tier, the evidence version and the expiry, which are the very fields the grant
--     validates against.
--   * iam_v2.lock_pms_interface_runtime (migration 0058) -- UPDATE on iam_v2.pms_interface_runtime would let
--     the role being authorised rewrite the PMS feed health it is authorised against.
--   * iam_v2.lock_stay and iam_v2.lock_origin_stay (migration 0058) -- the L1 lock the whole approved lock
--     order starts from. UPDATE on iam_v2.stays belongs to the stay engine, which writes it from the PMS
--     feed; a guest-facing service able to rewrite a Stay could put anybody in any room.
--
-- None of them is optional, and they fail in sequence: without the first, a real Room Login verifies and then
-- fails at the grant with a permission error reported to the operator as
-- package_not_offered_to_this_context -- which is exactly what happened to the first live guest on
-- 2026-08-26. Without the second it fails one statement later at the grant-time linearization lock, and
-- without the third one statement after that, each with no reason code at all.
GRANT EXECUTE ON FUNCTION iam_v2.lock_auth_context_offer(uuid,uuid,uuid,uuid) TO svc_scd;
GRANT EXECUTE ON FUNCTION iam_v2.lock_pms_interface_runtime(uuid,uuid,uuid)   TO svc_scd;
GRANT EXECUTE ON FUNCTION iam_v2.lock_stay(uuid,uuid,uuid)                    TO svc_scd;
GRANT EXECUTE ON FUNCTION iam_v2.lock_origin_stay(uuid,uuid,uuid)             TO svc_scd;

-- THE GUEST SIGN-IN ATTEMPT RECORD (migration 0067).
--
-- scd is the only writer: it is the authentication path, and it is the only service that holds the sealing
-- key for the credential half. INSERT records the attempt; SELECT lets it open a sealed row when edged --
-- which deliberately has no key -- asks on an authorised operator's behalf; DELETE is the thirty-day
-- retention sweep, which is a stated part of the feature rather than housekeeping.
--
-- NO UPDATE, and that is the shape of the record: an attempt describes ONE instant and is never revised.
-- A service that could rewrite it could rewrite the evidence of its own refusals.
--
-- This line exists because a grant written only in the migration is revoked by the first Gate-P reconcile:
-- attempts would record until then and afterwards stop, silently, leaving the operator screen empty with no
-- error anywhere.
GRANT SELECT, INSERT, DELETE ON iam_v2.sign_in_attempts TO svc_scd;

-- ...and the ONE narrow write it is allowed beyond INSERT: stamping the entitlement and session a proved
-- identity ended in, one call later. EXECUTE on the scoped function rather than UPDATE on the table, for the
-- same reason the offer lock is a function: UPDATE would also let the role rewrite the result and the
-- comparison an operator reads.
GRANT EXECUTE ON FUNCTION iam_v2.complete_sign_in_attempt(uuid,uuid,uuid,text,uuid,uuid) TO svc_scd;

-- ...and the scoped reader that tells scd whether the local mirror can authorise ANYBODY, so that it can say
-- "we cannot check right now" truthfully instead of telling a guest with correct details to re-check them.
-- EXECUTE on the function, and NOT SELECT on iam_v2.pms_interface_runtime: the role being authorised must not
-- be able to read the feed health it is authorised against. The first version of this read the table directly
-- and the Gate-P privilege suite caught it refusing every guest on the property.
GRANT EXECUTE ON FUNCTION iam_v2.p3_guest_network_mirror_state(uuid,uuid,uuid) TO svc_scd;

-- GUEST SIGN-IN PROTECTION (migration 0068) — THREE FUNCTIONS, AND NOT ONE TABLE PRIVILEGE.
--
-- scd is the service the control acts upon. It asks the gate whether a device may submit, and it reports
-- what the submission turned out to be. That is the whole of its relationship with this feature.
--
-- WHAT IT DELIBERATELY CANNOT DO, and each absence is the point:
--   * SELECT on iam_v2.site_guest_signin_protection — it cannot read the policy table directly, so there is
--     no path by which it reads one set of numbers while the operator screen shows another; both go through
--     guest_signin_protection_get, which the enforcement functions call internally;
--   * anything at all on iam_v2.guest_signin_protection_set — a service that could rewrite its own
--     thresholds could switch off the control it is subject to, quietly and without an operator;
--   * SELECT on iam_v2.guest_signin_restrictions — scd needs one answer about one device, not the property's
--     list of who is currently locked out;
--   * EXECUTE on iam_v2.guest_signin_release — releasing is a named human's decision, recorded with a reason.
--
-- These lines exist because a grant written only in the migration is revoked by the first Gate-P reconcile.
-- The failure that would produce is severe and silent in the wrong direction: the gate check would start
-- erroring, every guest submission would answer "we cannot verify right now", and room sign-in would be down
-- for the whole property with nothing in the migration history to explain it.
GRANT EXECUTE ON FUNCTION iam_v2.guest_signin_gate(uuid,uuid,macaddr)                        TO svc_scd;
GRANT EXECUTE ON FUNCTION iam_v2.guest_signin_note_failure(uuid,uuid,macaddr,uuid,text,text) TO svc_scd;
GRANT EXECUTE ON FUNCTION iam_v2.guest_signin_note_success(uuid,uuid,macaddr)                TO svc_scd;

-- NOT granted, on purpose:
--   * DELETE on anything EXCEPT iam_v2.sign_in_attempts above -- no authentication path deletes
--     authoritative state; the one DELETE granted is the attempts table's own retention sweep;
--   * UPDATE on vouchers or guest_access_accounts from THIS file. Redemption
--     and lockout accounting are writes the accepted domain performs through
--     its own guarded paths; if a future adapter needs them they belong here as
--     their own lines, with the failure that prompted them recorded;
--   * any privilege on the Phase-4 financial ledger or the commerce admin
--     tables -- authenticating a guest is not a reason to reach either;
--   * any privilege on iam_v2.site_guest_signin_protection,
--     iam_v2.guest_signin_protection_changes or iam_v2.guest_signin_restrictions -- see the block above.

-- CLOUD SYNC RETENTION (migration 0069). scd owns the queue and runs its retention pass; it reads the site's
-- configured period and calls the prune function.
--
-- NO RECOVERY HERE, deliberately. Releasing records the appliance gave up on is an operator decision with a
-- far end that has to absorb the result, and a daemon that could do it on its own could do it on a loop. scd
-- gets the two read-and-prune operations and not the third.
--
-- The prune function reaches DELIVERED records only — its WHERE clause names sent_at IS NOT NULL and takes no
-- parameter that could widen it — so this grant cannot remove a record that has not reached the cloud.
GRANT EXECUTE ON FUNCTION iam_v2.cloud_sync_settings_get(uuid,uuid)        TO svc_scd;
GRANT EXECUTE ON FUNCTION public.sync_outbox_prune_delivered(integer)      TO svc_scd;
GRANT EXECUTE ON FUNCTION public.sync_outbox_accounting()                  TO svc_scd;
