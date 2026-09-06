-- Put the attribution helper back to the strictly half-open form 0061 defined.
--
-- WHAT ROLLING BACK COSTS: after a terminal DATA crossing the derived usage will again omit the sample that
-- caused it, so iam_v2.entitlements.consumed_data_bytes - which is not changed here, and remains correct -
-- will read higher than this function derives, by exactly that sample. Enforcement is unaffected either way:
-- iam_v2.p6_data_crossing is untouched by 0062 and reads bindings while they are OPEN.
--
-- No counter is rewritten on the way back. The counters are true; only the derivation moves.

BEGIN;

CREATE OR REPLACE FUNCTION iam_v2.p3_entitlement_data_usage(p_entitlement uuid)
RETURNS bigint
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = iam_v2, pg_temp
AS $usage$
  SELECT COALESCE(sum(ar.bytes_up + ar.bytes_down), 0)::bigint
    FROM iam_v2.accounting_records ar
    JOIN iam_v2.session_entitlement_bindings b ON b.session_id = ar.session_id
     AND b.entitlement_id = p_entitlement AND b.bound_from <= ar.sampled_at
     AND (b.bound_until IS NULL OR b.bound_until > ar.sampled_at)
$usage$;

COMMENT ON FUNCTION iam_v2.p3_entitlement_data_usage(uuid) IS
  'Authoritative bytes attributed to an Entitlement across every Session and Device bound to it, by the same '
  'binding intervals p6_data_crossing uses. The maintained counter entitlements.consumed_data_bytes must '
  'equal this.';

COMMIT;
