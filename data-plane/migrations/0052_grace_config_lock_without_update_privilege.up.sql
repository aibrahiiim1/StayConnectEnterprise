-- A row lock should not require the privilege to write the row.
--
-- THE PROBLEM THIS SOLVES IS A COLLISION BETWEEN TWO CORRECT RULES.
--
-- The Checkout Converter locks the site grace-config row with SELECT ... FOR UPDATE, so a departure and a
-- concurrent grace-policy publication cannot interleave into a mixed snapshot. PostgreSQL requires UPDATE
-- privilege to take a row lock even when the statement modifies nothing, so svc_pmsd was granted UPDATE on
-- iam_v2.site_checkout_grace_config. Without it the conversion failed mid-transaction and left GO events
-- PENDING behind an ordered per-interface stream — which is exactly what happened to five real checkouts.
--
-- Gate-P's D32 assertion, meanwhile, refuses to complete if ANY non-owner login role holds INSERT, UPDATE or
-- DELETE on that table, because grace policy must only ever change through the audited publication boundary.
-- It scans pg_roles dynamically, so svc_pmsd was covered the day it was created.
--
-- Both rules are right. Together they made the full reconcile un-runnable on any appliance where the pmsd
-- grants had been applied: the reconcile aborted on its own assertion, which meant the one mechanism that
-- converges privilege to the allowlist could not be run at all.
--
-- THE FIX IS TO STOP CONFLATING "MAY LOCK" WITH "MAY WRITE". This function takes the lock as its owner and
-- returns only the fields the converter already reads. The lock is acquired inside the CALLER'S transaction —
-- a SECURITY DEFINER function does not get its own — so it is held until that transaction ends and serialises
-- against publication exactly as the inline statement did. What changes is only the privilege the caller needs
-- to reach it: EXECUTE on one function that cannot mutate anything, instead of UPDATE on the whole table.
--
-- Deliberately NOT a general accessor. It returns one row for one tenant/site, it always locks, and it exposes
-- only the eight columns the Checkout Converter consumes. A wider function would become a way to read or lock
-- grace configuration for reasons nobody reviewed.
--
-- plpgsql rather than sql: a LANGUAGE sql function can be inlined into the calling query, and an inlined body
-- is not guaranteed to preserve FOR UPDATE. The lock is the entire purpose here, so the body must not be
-- something the planner may rewrite.

BEGIN;

CREATE OR REPLACE FUNCTION iam_v2.p3_lock_grace_config(p_tenant uuid, p_site uuid)
RETURNS TABLE (
  grace_duration_seconds    int,
  grace_down_kbps           int,
  grace_up_kbps             int,
  grace_data_quota_bytes    bigint,
  grace_device_limit        int,
  grace_device_limit_policy text,
  grace_package_revision_id uuid,
  config_version            bigint
)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = iam_v2, pg_temp
AS $fn$
BEGIN
  RETURN QUERY
    SELECT g.grace_duration_seconds, g.grace_down_kbps, g.grace_up_kbps, g.grace_data_quota_bytes,
           g.grace_device_limit, g.grace_device_limit_policy, g.grace_package_revision_id, g.config_version
      FROM iam_v2.site_checkout_grace_config g
     WHERE g.tenant_id = p_tenant AND g.site_id = p_site
     FOR UPDATE;
  -- No row is not an error: an unconfigured site takes the Emergency Grace path, and the caller distinguishes
  -- "no row" from "row present but incomplete". Returning zero rows preserves that exactly.
END
$fn$;

COMMENT ON FUNCTION iam_v2.p3_lock_grace_config(uuid, uuid) IS
  'Locks one site grace-config row FOR UPDATE inside the caller''s transaction and returns the eight fields '
  'the Checkout Converter reads. Exists so a caller needs EXECUTE on a non-mutating function rather than '
  'UPDATE on the table: PostgreSQL requires UPDATE privilege to take a row lock, and granting it to a runtime '
  'role collided with the D32 no-direct-grace-mutation invariant. Mutates nothing; the audited publication '
  'boundary remains the only way grace policy changes.';

-- PUBLIC never gets it. The default ACL on a new function grants EXECUTE to PUBLIC, and a SECURITY DEFINER
-- function that locks rows as its owner is the last thing that should be left world-executable.
REVOKE ALL ON FUNCTION iam_v2.p3_lock_grace_config(uuid, uuid) FROM PUBLIC;

-- svc_pmsd is the only runtime role that converts checkouts. The grant is repeated in
-- deploy/gatep/svc-pmsd-iamv2-connector-grants.sql, which is what makes it survive a reconcile; this one makes
-- a freshly-migrated database work before Gate-P has ever run.
DO $grant$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_pmsd') THEN
    GRANT EXECUTE ON FUNCTION iam_v2.p3_lock_grace_config(uuid, uuid) TO svc_pmsd;
  END IF;
END $grant$;

COMMIT;
