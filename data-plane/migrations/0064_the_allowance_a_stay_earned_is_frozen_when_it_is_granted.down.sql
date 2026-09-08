-- Reverses 0064. The crossing goes back to reading the plan revision alone, and the two columns are dropped.
--
-- DROPPING entitlements.data_quota_bytes DISCARDS SNAPSHOTS. Any entitlement granted under a PER_STAY_NIGHT
-- package while 0064 was applied will, after this, fall back to its plan revision's quota -- which is usually
-- NULL, i.e. unlimited. That is a real loss of enforcement, so this down migration belongs to a rollback
-- performed BEFORE any such package is published, not to routine operation.

BEGIN;

CREATE OR REPLACE FUNCTION iam_v2.p6_data_crossing(p_entitlement uuid)
RETURNS timestamptz
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = iam_v2, pg_temp
AS $$
  SELECT min(x.sampled_at)
    FROM iam_v2.entitlements e
    JOIN iam_v2.service_plan_revisions spr ON spr.id = e.service_plan_revision_id
    CROSS JOIN LATERAL (
      SELECT ar.sampled_at,
             sum(ar.bytes_up + ar.bytes_down) OVER (ORDER BY ar.sampled_at, ar.id) AS running
        FROM iam_v2.accounting_records ar
        JOIN iam_v2.session_entitlement_bindings b ON b.session_id = ar.session_id
         AND b.entitlement_id = e.id AND b.bound_from <= ar.sampled_at
         AND (b.bound_until IS NULL OR b.bound_until > ar.sampled_at)
    ) x
   WHERE e.id = p_entitlement
     AND spr.data_quota_bytes IS NOT NULL
     AND x.running >= spr.data_quota_bytes;
$$;

ALTER TABLE iam_v2.entitlements DROP CONSTRAINT IF EXISTS entitlements_data_quota_bytes_positive;
ALTER TABLE iam_v2.entitlements DROP COLUMN IF EXISTS data_quota_bytes;

ALTER TABLE iam_v2.internet_package_revisions
  DROP CONSTRAINT IF EXISTS internet_package_revisions_data_allocation_policy_shape;
ALTER TABLE iam_v2.internet_package_revisions DROP COLUMN IF EXISTS data_allocation_policy;

COMMIT;
