-- EVERY ROW LOCK THE GUEST-AUTH CHAIN TAKES, FOUND IN ONE PASS INSTEAD OF ONE PER LIVE FAILURE.
--
-- 0057 gave svc_scd the offer lock. The production privilege harness then drove the WHOLE Room Login as
-- svc_scd — verify, offer, grant, Purchase, Entitlement, Session — and the grant failed at the NEXT lock, and
-- would have failed at the one after that. Rather than ship them one live refusal at a time, this migration
-- closes every remaining lock in that chain.
--
-- The first was the PMS runtime row:
--
--     as svc_scd, SELECT 1 FROM iam_v2.pms_interface_runtime ... FOR UPDATE
--       ->  ERROR: permission denied for table pms_interface_runtime
--
-- That lock is the grant-time linearization point added by 0056: it is taken on the row a pmsd admission
-- already serializes on, so a GO admitted-but-unapplied cannot slip between the readiness check and the
-- grant. Without it the check means "true on some earlier snapshot"; without the privilege the grant simply
-- cannot happen.
--
-- Resolve did not fail the same way because it mints its context through iam_v2.issue_or_return_pms_context,
-- a SECURITY DEFINER function that reaches the row as its owner. Only the Go consume path takes the lock
-- directly, which is why a live guest could verify cleanly and still be refused at the grant.
--
-- WHY A FUNCTION AND NOT `GRANT UPDATE`. iam_v2.pms_interface_runtime is the PMS feed's own state — transport
-- status, sync status, continuity, the published resync generation. It is pmsd's to write, and the whole
-- Phase-3 authorisation rule is decided FROM it. Granting scd UPDATE would let the role being authorised
-- rewrite the health it is authorised against. The lock is the only capability needed, so it is the only one
-- granted: this function takes it, returns nothing, and cannot reach another tenant's row.

BEGIN;

CREATE OR REPLACE FUNCTION iam_v2.lock_pms_interface_runtime(
  p_tenant uuid, p_site uuid, p_interface uuid
) RETURNS void
LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE v_one int;
BEGIN
  SELECT 1 INTO v_one FROM iam_v2.pms_interface_runtime rt
   WHERE rt.tenant_id = p_tenant AND rt.site_id = p_site AND rt.pms_interface_id = p_interface
   FOR UPDATE;
  -- NOT FOUND is deliberately not an error. An interface with no runtime row has never connected, and the
  -- caller's own eligibility test — iam_v2.p3_feed_authorizes — refuses it a moment later with the reason a
  -- guest should see. Raising here would turn "the PMS has never spoken to us" into an internal fault.
END $fn$;

COMMENT ON FUNCTION iam_v2.lock_pms_interface_runtime(uuid,uuid,uuid) IS
  'Takes the grant-time linearization lock on ONE iam_v2.pms_interface_runtime row and returns nothing. '
  'Exists so svc_scd can hold that lock without holding UPDATE on the table, which would let the role being '
  'authorised rewrite the feed health it is authorised against. Tenant- and site-scoped; a missing runtime '
  'row is not an error, because the caller''s feed-health test refuses that interface on its own terms.';

REVOKE ALL ON FUNCTION iam_v2.lock_pms_interface_runtime(uuid,uuid,uuid) FROM PUBLIC;

-- Guarded on role existence: the service roles are provisioned by the Gate-P scripts, not by migrations.
DO $grant$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_scd') THEN
    GRANT EXECUTE ON FUNCTION iam_v2.lock_pms_interface_runtime(uuid,uuid,uuid) TO svc_scd;
  END IF;
END $grant$;

-- ---------------------------------------------------------------------------
-- THE L1 STAY LOCK — the same problem, on the row the whole lock order starts from.
--
-- Every authentication path locks the pinned Stay FIRST (L1 Stay → L2 Auth Context → L3 Entitlement), which
-- is what makes it serialize against Checkout, Reinstatement and occupancy-evidence replacement instead of
-- racing them. svc_scd holds SELECT on iam_v2.stays and must never hold UPDATE: the Stay is the PMS's own
-- record of who is in the room, written by the stay engine from the feed. A guest-facing service that could
-- rewrite it could put anybody in any room.
--
-- The predicate that follows each of these locks stays INLINE in Go, deliberately. Locking here and testing
-- there is not a weakening: the lock blocks until any concurrent Checkout commits, and the caller's next
-- statement takes a fresh READ COMMITTED snapshot, so it evaluates its conditions against the updated tuple
-- exactly as the combined FOR UPDATE did. What must NOT move into a function is the CONDITIONS — a STABLE
-- function reading iam_v2.stays internally would answer from the pre-Checkout snapshot and let a departed
-- guest through.
CREATE OR REPLACE FUNCTION iam_v2.lock_stay(
  p_tenant uuid, p_site uuid, p_stay uuid
) RETURNS void
LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE v_one int;
BEGIN
  SELECT 1 INTO v_one FROM iam_v2.stays st
   WHERE st.tenant_id = p_tenant AND st.site_id = p_site AND st.id = p_stay
   FOR UPDATE;
  -- A Stay that does not exist is not an error here either: the caller's own pin set refuses it immediately
  -- afterwards with the uniform "context invalid" answer, and that is the answer the guest must get.
END $fn$;

COMMENT ON FUNCTION iam_v2.lock_stay(uuid,uuid,uuid) IS
  'Takes the L1 Stay lock on ONE iam_v2.stays row and returns nothing. Exists so the guest-auth services can '
  'hold the lock their lock order starts from without holding UPDATE on iam_v2.stays, which is the stay '
  'engine''s to write. Tenant- and site-scoped; a missing Stay is not an error, because the caller''s pin set '
  'refuses it on its own terms.';

-- The post-stay arms reach the Stay through the profile, and the caller never learns the origin Stay id — it
-- is lineage, not the subject. Keying the lock by profile keeps it that way.
CREATE OR REPLACE FUNCTION iam_v2.lock_origin_stay(
  p_tenant uuid, p_site uuid, p_profile uuid
) RETURNS void
LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE v_one int;
BEGIN
  SELECT 1 INTO v_one
    FROM iam_v2.post_stay_profiles psp
    JOIN iam_v2.stays st
      ON st.tenant_id = psp.tenant_id AND st.site_id = psp.site_id AND st.id = psp.origin_stay_id
   WHERE psp.tenant_id = p_tenant AND psp.site_id = p_site AND psp.id = p_profile
   FOR UPDATE OF st;
END $fn$;

COMMENT ON FUNCTION iam_v2.lock_origin_stay(uuid,uuid,uuid) IS
  'Takes the L1 Stay lock on the origin Stay of ONE post-stay profile and returns nothing. Same reason as '
  'iam_v2.lock_stay, keyed by profile because a post-stay caller holds the profile, not the Stay.';

REVOKE ALL ON FUNCTION iam_v2.lock_stay(uuid,uuid,uuid)        FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.lock_origin_stay(uuid,uuid,uuid) FROM PUBLIC;

DO $grant$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_scd') THEN
    GRANT EXECUTE ON FUNCTION iam_v2.lock_stay(uuid,uuid,uuid)        TO svc_scd;
    GRANT EXECUTE ON FUNCTION iam_v2.lock_origin_stay(uuid,uuid,uuid) TO svc_scd;
  END IF;
END $grant$;

-- ---------------------------------------------------------------------------
-- THE FEED-HEALTH PREDICATE MUST RUN AS ITS OWNER, not as whoever asks.
--
-- iam_v2.p3_feed_authorizes is the single definition of the Phase-3 authorisation rule, and it is a plain
-- STABLE (SECURITY INVOKER) function that reads iam_v2.pms_interface_runtime and — since 0056 — iam_v2.
-- stay_events. svc_scd holds EXECUTE on it and no privilege on either table, so every path that evaluates it
-- OUTSIDE a SECURITY DEFINER wrapper fails with "permission denied for table pms_interface_runtime". Resolve
-- survived only because it mints its context through iam_v2.issue_or_return_pms_context, which reaches both
-- tables as its owner; the Go consume path evaluates the predicate directly and cannot.
--
-- The alternative — granting svc_scd SELECT on iam_v2.stay_events — was rejected. That table is the whole PMS
-- event stream: every arrival, departure, room move and name in the hotel. The guest-auth service needs ONE
-- BOOLEAN about one interface, so it gets the boolean and not the stream. Definer rights are the narrower
-- grant here, not the wider one.
--
-- ALTER rather than CREATE OR REPLACE on purpose: the body is whatever the latest migration made it (0050,
-- 0051, 0053, 0056), and restating it here would fork the one definition this design insists on having only
-- once. NOTE for anyone redefining it later: CREATE OR REPLACE resets these properties to invoker rights, so
-- a future migration that rewrites the body must restate both lines below.
ALTER FUNCTION iam_v2.p3_feed_authorizes(uuid,uuid,uuid,uuid,timestamptz) SECURITY DEFINER;
ALTER FUNCTION iam_v2.p3_feed_authorizes(uuid,uuid,uuid,uuid,timestamptz) SET search_path = iam_v2, pg_temp;

COMMIT;
