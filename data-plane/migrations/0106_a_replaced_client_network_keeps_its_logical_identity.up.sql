-- 0106 — A REPLACED CLIENT NETWORK KEEPS ITS LOGICAL IDENTITY.
--
-- THE DEFECT THIS CLOSES. 0105 made a topology change staged and safe, and the apply replaces the client
-- network ROW: the successor is a new id. Internet Package eligibility names client networks BY ID
-- (SITE_NETWORK, {guest_network_ids: [...]}), and a package REVISION is immutable. So the moment a hotel moved
-- a port from untagged to a tagged trunk -- a cabling decision, nothing to do with who may buy what -- every
-- eligibility rule in every EXISTING revision stopped matching the guests sitting on that very network.
--
-- Republishing the CURRENT revision forward onto the successor (edged does that at confirm) fixes the offer a
-- future guest sees. It cannot fix a revision that is PINNED and must stay pinned:
--
--   * an UNUSED PRINTED VOUCHER is pinned to the revision it was issued against, and redeems that revision
--     directly -- deliberately, so republishing or deactivating a package never invalidates printed codes.
--     Its SITE_NETWORK rule names the retired id forever. The voucher became unredeemable, and the operator's
--     only remedy would have been to reissue valid unused codes after an ordinary cabling change.
--   * a guest HOLDING an entitlement keeps the revision they were granted on, and a second device joining
--     that access re-evaluates the same pinned revision.
--
-- History must not be rewritten to fix this, and the retired id must not be re-pointed. What was missing is
-- the LOGICAL IDENTITY: that the successor network *is* the continuation of the network it replaced, and that
-- a rule naming the predecessor is therefore satisfied by a device on the successor.
--
-- WHAT THIS ADDS. A view over the replacement chain 0105 already records, so there is exactly one source of
-- truth and no second copy to drift:
--
--   iam_v2.guest_network_lineage (network_id, ancestor_id, depth)
--
-- one row per (live network, each logical predecessor it continues), walked transitively. Only replacements
-- that actually took effect count: APPLIED (the successor is live and carrying devices inside the
-- confirmation window -- a guest redeeming a voucher in those 120 seconds must not be refused) and CONFIRMED.
-- A CANCELLED, REVERTED or still-PENDING request never happened on the wire and contributes nothing.
--
-- WHAT IT DELIBERATELY DOES NOT DO. It does not widen eligibility to any network that was not a predecessor
-- of the one the device is actually on; the device's own network is still server-derived, never guest-supplied.
-- It grants no path from a predecessor FORWARD -- a rule naming the successor is not satisfied by a device on
-- some other network. And it is a view, not a column: nothing in public.guest_networks is touched, which it
-- could not be anyway (live-site migrations apply as iam_v2_owner, which owns nothing in public -- see 0099).

BEGIN;

CREATE OR REPLACE VIEW iam_v2.guest_network_lineage AS
WITH RECURSIVE chain AS (
  -- Step one: a network and the network it directly replaced.
  SELECT r.tenant_id,
         r.site_id,
         r.successor_network_id AS network_id,
         r.original_network_id  AS ancestor_id,
         1                      AS depth
    FROM iam_v2.guest_network_replacements r
   WHERE r.successor_network_id IS NOT NULL
     AND r.state IN ('APPLIED', 'CONFIRMED')

  UNION ALL

  -- ...and transitively: a network replaced three times still continues the first one.
  SELECT c.tenant_id,
         c.site_id,
         c.network_id,
         r.original_network_id,
         c.depth + 1
    FROM chain c
    JOIN iam_v2.guest_network_replacements r
      ON r.successor_network_id = c.ancestor_id
     AND r.tenant_id = c.tenant_id
     AND r.site_id = c.site_id
     AND r.state IN ('APPLIED', 'CONFIRMED')
   WHERE c.depth < 32 -- a cycle cannot arise (a successor id is freshly generated), but a view must terminate
)
SELECT tenant_id, site_id, network_id, ancestor_id, min(depth) AS depth
  FROM chain
 WHERE ancestor_id <> network_id
 GROUP BY tenant_id, site_id, network_id, ancestor_id;

DO $own$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
    EXECUTE 'ALTER VIEW iam_v2.guest_network_lineage OWNER TO iam_v2_owner';
  END IF;
END $own$;

-- Least privilege, mirrored in deploy/gatep/svc-scd-iamv2-guest-commerce-grants.sql and
-- deploy/gatep/svc-edged-phase345-admin-grants.sql so both survive a Gate-P reconcile.
--
-- SELECT on the VIEW only. The underlying guest_network_replacements table carries the operator who asked, the
-- reason they typed and the staged addressing; the guest commerce path needs none of that, and the view is
-- owned by iam_v2_owner so reading it does not require reading the table.
DO $g$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_scd') THEN
    GRANT SELECT ON iam_v2.guest_network_lineage TO svc_scd;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
    GRANT SELECT ON iam_v2.guest_network_lineage TO svc_edged;
  END IF;
END;
$g$;

COMMIT;
