-- THE USAGE HELPER READS ON ITS OWN AUTHORITY.
--
-- iam_v2.entitlement_usage_bytes answers one question -- how many bytes were attributed to one Entitlement up
-- to one instant -- and to answer it, it reads iam_v2.accounting_records. It was NOT security definer, so it
-- read with the CALLER's rights.
--
-- svc_acctd holds SELECT on accounting_records, so it never noticed. svc_pmsd does not, deliberately: Gate-P
-- names accounting_records in the list of financial/metering tables the PMS connector must never hold, and
-- asserts that prohibition on every reconcile. So the moment pmsd's checkout-boundary path called this helper
-- it failed with "permission denied for table accounting_records", and because that path runs while applying
-- a stay event, EVERY event after it stayed PENDING.
--
-- WHAT THAT COST, AND WHY IT LOOKED LIKE SOMETHING ELSE. p3_feed_authorizes refuses to issue a PMS Auth
-- Context while LIVE stay_events are pending -- correctly, because the Stay projection is then known to be
-- behind the admitted feed, and authorising a guest against a stale Stay is exactly what that guard exists to
-- prevent. So a privilege error inside the applier surfaced, hours later and on a different screen, as every
-- Room Login on the interface being refused with the uniform "we could not verify your stay". The guard was
-- not wrong. It was reporting a real staleness whose cause was three layers away.
--
-- THE FIX IS THE ONE THE SCHEMA ALREADY USES ELSEWHERE. iam_v2.p3_entitlement_data_usage answers a
-- near-identical question over the same table and is SECURITY DEFINER for precisely this reason. Granting
-- svc_pmsd SELECT on accounting_records would be the other way to make the call succeed, and it is the wrong
-- one: it is the privilege Gate-P forbids, it would fail the reconcile assertion, and it would hand the PMS
-- connector the whole metering table when what it needs is one aggregate about one entitlement.
--
-- WHAT DOES NOT CHANGE. The function body is byte-for-byte the one that was there: the same attribution over
-- session_entitlement_bindings with the same fallback for sessions that have no binding row, the same
-- <= p_at bound, the same four return columns. This migration changes WHO the query runs as and nothing else.
-- No caller changes, no accounting semantics change, no quota semantics change.

BEGIN;

-- The definer, restated rather than assumed: the function must run as the schema owner, not as whoever
-- happened to create it, and its search_path must be fixed so a caller cannot shadow iam_v2 with a temp
-- schema and feed it different tables.
ALTER FUNCTION iam_v2.entitlement_usage_bytes(uuid, timestamptz) OWNER TO iam_v2_owner;

CREATE OR REPLACE FUNCTION iam_v2.entitlement_usage_bytes(p_ent uuid, p_at timestamptz)
RETURNS TABLE(bytes_up bigint, bytes_down bigint, records bigint, latest_sampled_at timestamptz)
LANGUAGE sql
STABLE
SECURITY DEFINER
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

COMMENT ON FUNCTION iam_v2.entitlement_usage_bytes(uuid, timestamptz) IS
  'Bytes attributed to ONE Entitlement up to ONE instant, by binding interval, with the no-binding fallback '
  'for sessions that predate the binding table. SECURITY DEFINER so a caller can obtain the aggregate without '
  'holding SELECT on iam_v2.accounting_records -- svc_pmsd needs the number at a checkout boundary and Gate-P '
  'forbids it the table. It returns four aggregate numbers about an entitlement the caller must already be '
  'able to name; it exposes no guest, Stay, reservation, folio, payment or PMS data, and it writes nothing.';

-- EXECUTE is not public. The two service roles that call it are named, and nobody else gets it by default.
REVOKE ALL ON FUNCTION iam_v2.entitlement_usage_bytes(uuid, timestamptz) FROM PUBLIC;
DO $grant$
BEGIN
  -- svc_acctd: the accounting owner, which already held SELECT and loses nothing by going through here.
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_acctd') THEN
    GRANT EXECUTE ON FUNCTION iam_v2.entitlement_usage_bytes(uuid, timestamptz) TO svc_acctd;
  END IF;
  -- svc_pmsd: the caller this migration exists for. EXECUTE only; the table stays forbidden.
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_pmsd') THEN
    GRANT EXECUTE ON FUNCTION iam_v2.entitlement_usage_bytes(uuid, timestamptz) TO svc_pmsd;
  END IF;
END
$grant$;

-- ---------------------------------------------------------------------------
-- PROVE THE PROPERTIES, on the database this just changed.
DO $verify$
DECLARE
  p        pg_proc%ROWTYPE;
  cfg      text[];
  n_direct int;
BEGIN
  -- Located by OID through to_regprocedure, not by comparing a rendered signature string.
  -- pg_get_function_identity_arguments includes PARAMETER NAMES ("p_ent uuid, p_at timestamp with time
  -- zone"), so matching it against "uuid, timestamp with time zone" found nothing and this block reported a
  -- function that was plainly present as missing. The OID cannot be spelled two ways.
  SELECT pr.* INTO p FROM pg_proc pr
   WHERE pr.oid = to_regprocedure('iam_v2.entitlement_usage_bytes(uuid, timestamptz)');
  IF NOT FOUND THEN
    RAISE EXCEPTION 'migration 0065 lost iam_v2.entitlement_usage_bytes';
  END IF;

  IF NOT p.prosecdef THEN
    RAISE EXCEPTION 'entitlement_usage_bytes is not SECURITY DEFINER, so it still reads with caller rights';
  END IF;
  IF pg_get_userbyid(p.proowner) <> 'iam_v2_owner' THEN
    RAISE EXCEPTION 'entitlement_usage_bytes is owned by %, not iam_v2_owner -- a definer function runs as its '
                    'owner, so the owner IS the privilege boundary', pg_get_userbyid(p.proowner);
  END IF;

  -- A definer function without a pinned search_path is the classic hijack: the caller sets search_path to a
  -- schema of their own and the body reads their tables with the owner's rights.
  cfg := p.proconfig;
  IF cfg IS NULL OR NOT EXISTS (SELECT 1 FROM unnest(cfg) c WHERE c LIKE 'search_path=%') THEN
    RAISE EXCEPTION 'entitlement_usage_bytes has no pinned search_path';
  END IF;

  IF has_function_privilege('public', p.oid, 'EXECUTE') THEN
    RAISE EXCEPTION 'PUBLIC can execute entitlement_usage_bytes';
  END IF;

  -- THE WHOLE POINT: the connector gets the answer, never the table. If a future change hands svc_pmsd the
  -- table anyway, this migration's reason for existing has been undone and Gate-P would fail too.
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_pmsd') THEN
    IF NOT has_function_privilege('svc_pmsd', p.oid, 'EXECUTE') THEN
      RAISE EXCEPTION 'svc_pmsd cannot execute entitlement_usage_bytes, which is the call this migration fixes';
    END IF;
    SELECT count(*) INTO n_direct FROM information_schema.role_table_grants
     WHERE grantee = 'svc_pmsd' AND table_schema = 'iam_v2' AND table_name = 'accounting_records';
    IF n_direct <> 0 THEN
      RAISE EXCEPTION 'svc_pmsd holds % direct privilege(s) on iam_v2.accounting_records; the definer boundary '
                      'exists so that it holds none', n_direct;
    END IF;
  END IF;
END
$verify$;

COMMIT;
