-- THE GRANT COULD NOT LOCK THE OFFER IT HAD JUST WRITTEN.
--
-- The first real guest Room Login on the PRE-LIVE appliance verified correctly, minted an Auth Context and
-- recorded exactly one offer — then refused at the grant with package_not_offered_to_this_context. The offer
-- row satisfied every condition the grant checks. What failed was the LOCK:
--
--     as svc_scd, SELECT ... FOR UPDATE  ->  ERROR: permission denied for table auth_context_offers
--     as svc_scd, the same SELECT        ->  returns the row
--
-- PostgreSQL requires SELECT *and* UPDATE for SELECT ... FOR UPDATE. svc_scd holds SELECT only, so the row
-- lock the grant takes to stop a concurrent consumption slipping between check and grant was refused. The
-- path had never been exercised here — the earlier acceptance stopped at the offer stage, and CI runs as a
-- superuser, so the requirement was invisible until a real guest tried.
--
-- WHY A FUNCTION AND NOT `GRANT UPDATE`. Granting UPDATE on iam_v2.auth_context_offers would let scd rewrite
-- any column of any offer: the tier it matched, the evidence version it was decided under, its expiry. Those
-- are the fields the grant then VALIDATES against, so the role being checked would gain the ability to edit
-- the check. The lock is the only capability needed, so the lock is the only capability granted.
--
-- The function is deliberately not a general-purpose locker. It takes the exact four-part key the grant uses,
-- returns only the two values the grant reads, and cannot be asked to lock anything else or to return a row
-- from another tenant. Being SECURITY DEFINER buys the caller a row lock and nothing more.

BEGIN;

CREATE OR REPLACE FUNCTION iam_v2.lock_auth_context_offer(
  p_tenant uuid, p_site uuid, p_context uuid, p_package_revision uuid
) RETURNS TABLE (matched_tier_order int, evidence_version bigint)
LANGUAGE sql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
  SELECT o.matched_tier_order, o.evidence_version
    FROM iam_v2.auth_context_offers o
   WHERE o.tenant_id = p_tenant
     AND o.site_id = p_site
     AND o.auth_context_id = p_context
     AND o.package_revision_id = p_package_revision
     -- The expiry test stays INSIDE the locked read, exactly as the inline query had it: an offer that
     -- expires between the check and the grant must not be redeemable.
     AND o.expires_at > now()
   FOR UPDATE;
$fn$;

COMMENT ON FUNCTION iam_v2.lock_auth_context_offer(uuid,uuid,uuid,uuid) IS
  'Locks ONE auth_context_offer row for the grant and returns the two values the grant validates against. '
  'Exists so svc_scd can take the row lock without holding UPDATE on iam_v2.auth_context_offers, which would '
  'let it rewrite the matched tier, the evidence version and the expiry — the very fields the grant checks. '
  'Returns no row when the offer does not exist, belongs to another tenant or site, or has expired; the '
  'caller must treat no-row as "not offered to this context" and any ERROR as an internal failure.';

REVOKE ALL ON FUNCTION iam_v2.lock_auth_context_offer(uuid,uuid,uuid,uuid) FROM PUBLIC;

-- Guarded on role existence: the service roles are provisioned by the Gate-P scripts, not by migrations.
DO $grant$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_scd') THEN
    GRANT EXECUTE ON FUNCTION iam_v2.lock_auth_context_offer(uuid,uuid,uuid,uuid) TO svc_scd;
  END IF;
END $grant$;

COMMIT;
