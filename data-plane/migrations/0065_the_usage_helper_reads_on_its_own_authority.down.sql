-- Reverses 0065: the usage helper goes back to reading with the CALLER's rights.
--
-- REAPPLYING THIS REOPENS THE OUTAGE IT FIXED. svc_pmsd has no SELECT on iam_v2.accounting_records and Gate-P
-- forbids giving it any, so with SECURITY INVOKER restored its checkout-boundary call fails again with
-- "permission denied for table accounting_records", stay events stop being applied, and p3_feed_authorizes
-- then refuses every PMS Auth Context on the interface -- which presents as every Room Login failing.
--
-- This exists for completeness of the migration chain, not as an operational step.

BEGIN;

CREATE OR REPLACE FUNCTION iam_v2.entitlement_usage_bytes(p_ent uuid, p_at timestamptz)
RETURNS TABLE(bytes_up bigint, bytes_down bigint, records bigint, latest_sampled_at timestamptz)
LANGUAGE sql
STABLE
SET search_path = iam_v2, pg_temp
AS $$
  SELECT COALESCE(sum(ar.bytes_up),0)::bigint, COALESCE(sum(ar.bytes_down),0)::bigint,
         count(*)::bigint, max(ar.sampled_at)
  FROM iam_v2.accounting_records ar
  JOIN iam_v2.sessions s ON s.id = ar.session_id
  WHERE ar.sampled_at <= p_at
    AND (
      EXISTS (SELECT 1 FROM iam_v2.session_entitlement_bindings b
              WHERE b.session_id = ar.session_id AND b.entitlement_id = p_ent
                AND b.bound_from <= ar.sampled_at AND (b.bound_until IS NULL OR b.bound_until > ar.sampled_at))
      OR (s.entitlement_id = p_ent
          AND NOT EXISTS (SELECT 1 FROM iam_v2.session_entitlement_bindings b2 WHERE b2.session_id = ar.session_id))
    );
$$;

REVOKE ALL ON FUNCTION iam_v2.entitlement_usage_bytes(uuid, timestamptz) FROM PUBLIC;
DO $grant$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_acctd') THEN
    GRANT EXECUTE ON FUNCTION iam_v2.entitlement_usage_bytes(uuid, timestamptz) TO svc_acctd;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_pmsd') THEN
    GRANT EXECUTE ON FUNCTION iam_v2.entitlement_usage_bytes(uuid, timestamptz) TO svc_pmsd;
  END IF;
END
$grant$;

COMMIT;
