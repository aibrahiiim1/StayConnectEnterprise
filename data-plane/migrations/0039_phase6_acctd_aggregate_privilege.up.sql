-- PHASE 6 M3 — the exact privilege acctd needs to run the aggregate tick, and nothing beyond it.
--
-- The tick is SECURITY DEFINER, so it performs its own writes under the owner. What acctd needs is the right
-- to CALL it -- and that is all it needs, which is the point of the definer boundary: the caller gets one
-- audited operation rather than the write authority to reproduce it.
--
-- WHAT THIS DELIBERATELY DOES NOT GRANT, and each absence is load-bearing:
--
--   * no UPDATE on iam_v2.entitlements. Consumption, the crossing instant and the terminal transition are
--     written by the definer function. A role that could write consumed_online_seconds directly could give a
--     guest time back, or take it away, with no evidence anywhere.
--   * no write on iam_v2.session_online_watermarks. The watermark is what makes accrual idempotent; a caller
--     that could move it could charge the same minutes twice, or never.
--   * no write on iam_v2.online_time_skipped_intervals. It is append-only evidence, written by the tick.
--   * no EXECUTE on the device-authorization primitives or on p6_record_time_termination. Those belong to
--     admission, guest self-service and the expiry sweep's owner, not to the accounting daemon.
--   * nothing on PUBLIC, anywhere. A function's ACL starts NULL and NULL means PUBLIC EXECUTE, so the
--     revoke is explicit and is asserted by the privilege gate rather than assumed.
--
-- The SELECTs are the ones the ACCRUAL LOOP genuinely reads through the definer boundary already; they are
-- granted so that acctd's own diagnostics and the expiry sweep it runs in the same process can see the state
-- they act on, and no more. If a later reading shows one of them is not on any real query path, it should be
-- revoked -- an unused grant is not free, it is a privilege waiting for a use.
BEGIN;

DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_acctd') THEN
    CREATE ROLE svc_acctd NOLOGIN;
    RAISE NOTICE 'created role svc_acctd (NOLOGIN); the deployment grants LOGIN and a password separately';
  END IF;
END $$;

-- The one operation. Everything the tick writes, it writes as its owner.
GRANT EXECUTE ON FUNCTION iam_v2.p6_tick_online_time(uuid, uuid, timestamptz, int, uuid[], timestamptz[])
  TO svc_acctd;

-- The reads the sweep performs around it, in the same transaction: which entitlements are live and in which
-- mode, what their plan revision says, and which sessions are eligible.
GRANT SELECT ON iam_v2.entitlements            TO svc_acctd;
GRANT SELECT ON iam_v2.service_plan_revisions  TO svc_acctd;
GRANT SELECT ON iam_v2.sessions                TO svc_acctd;
GRANT SELECT ON iam_v2.session_online_watermarks TO svc_acctd;

-- PUBLIC never executes it, whatever a later migration does to the ACL by accident.
REVOKE ALL ON FUNCTION iam_v2.p6_tick_online_time(uuid, uuid, timestamptz, int, uuid[], timestamptz[])
  FROM PUBLIC;

-- SELF-ASSERTING CHECKS. A privilege migration that only granted would be a claim; these make the absences
-- facts, and they fail the migration rather than leaving a surprise for the audit.
DO $$
DECLARE bad text;
BEGIN
  IF has_function_privilege('public',
       'iam_v2.p6_tick_online_time(uuid,uuid,timestamptz,int,uuid[],timestamptz[])', 'EXECUTE') THEN
    RAISE EXCEPTION 'PUBLIC can execute the aggregate tick';
  END IF;

  SELECT string_agg(format('%s on %s', privilege_type, table_name), ', ')
    INTO bad
    FROM information_schema.role_table_grants
   WHERE table_schema = 'iam_v2' AND grantee = 'svc_acctd'
     AND privilege_type <> 'SELECT';
  IF bad IS NOT NULL THEN
    RAISE EXCEPTION 'svc_acctd holds write authority in iam_v2: %', bad;
  END IF;

  FOR bad IN
    SELECT p.proname FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
     WHERE n.nspname = 'iam_v2'
       AND p.proname IN ('authorize_entitlement_device', 'deauthorize_entitlement_device',
                         'p6_record_time_termination', 'p6_guest_release_device',
                         'p6_set_guest_device_self_service', 'terminate_entitlement_at_boundary')
       AND has_function_privilege('svc_acctd', p.oid, 'EXECUTE')
  LOOP
    RAISE EXCEPTION 'svc_acctd can execute %, which belongs to another boundary', bad;
  END LOOP;
END $$;

COMMIT;
