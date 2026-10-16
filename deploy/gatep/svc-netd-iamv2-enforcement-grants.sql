-- svc_netd: the Phase-3 ENFORCEMENT writer boundary.
--
-- netd is the only process that can see whether enforcement actually took, so it is the only one allowed to
-- promote a session from PENDING_ENFORCEMENT to active. It refused to run with:
--
--   "phase3 writer boundary: cannot inspect
--    iam_v2.apply_entitlement_transition(uuid,text,timestamptz,text): permission denied for schema iam_v2"
--
-- svc_netd had no USAGE on iam_v2 at all. That refusal is the boundary working -- netd checks up front that
-- it can perform every authoritative operation it will need, rather than discovering mid-flight that it can
-- authorize a guest at the packet gate but not record it, which is the state that produces a guest online
-- forever whose session still says PENDING_ENFORCEMENT.
--
-- DERIVED STATEMENT BY STATEMENT from cmd/netd (audit B-7, migration 0124). netd executes exactly five iam_v2
-- statements -- activate_session_enforcement, end_session_enforcement, allocate_class_generation,
-- register_class_origin and one read of iam_v2.sessions -- and its startup self-check
-- (writerguard.Verify(Phase3Requirements)) reads pg_proc / pg_trigger, which needs schema USAGE and, for the
-- capability-scoped families, EXECUTE on begin_controlled_operation. Every writer it calls is SECURITY DEFINER,
-- so what those writers call in turn (apply_entitlement_transition, the controlled-operation checks) runs as
-- their owner and needs no grant here.

GRANT USAGE ON SCHEMA iam_v2 TO svc_netd;

-- The one table read: Confirm() asks whether the promotion it just made is durable.
GRANT SELECT ON iam_v2.sessions TO svc_netd;

-- The startup capability check (writerguard.verifyCapability) requires that this role CAN open a controlled
-- operation; netd opens none itself.
GRANT EXECUTE ON FUNCTION iam_v2.begin_controlled_operation(text)    TO svc_netd;

-- Enforcement lifecycle: promote a session once the tc class and the nft gate are both proven, and record
-- that enforcement has gone away so durable state stops claiming a guest is active.
GRANT EXECUTE ON FUNCTION iam_v2.activate_session_enforcement(uuid, uuid, uuid, text, integer, bigint) TO svc_netd;
GRANT EXECUTE ON FUNCTION iam_v2.end_session_enforcement(uuid, uuid, uuid, text) TO svc_netd;

-- Class generations and origins: netd allocates the accountable tc class and registers where it came from,
-- which is what makes the later promotion meaningful.
-- Signatures taken from pg_proc, not guessed: an earlier draft used (uuid,uuid,text) for
-- allocate_class_generation and the GRANT failed with "function does not exist", which under
-- ON_ERROR_STOP would have aborted every grant after it.
GRANT EXECUTE ON FUNCTION iam_v2.allocate_class_generation(uuid, uuid, uuid) TO svc_netd;
GRANT EXECUTE ON FUNCTION iam_v2.register_class_origin(uuid, uuid, uuid, uuid, text, integer, bigint, bigint, bigint, timestamptz) TO svc_netd;

-- ---- what netd used to hold and does not call (removed by audit B-7) ------------------------------------
--   SELECT on iam_v2.devices                            -- no netd statement reads it
--   EXECUTE on iam_v2.apply_entitlement_transition      -- "the boundary self-check inspects this": it reads
--                                                          pg_proc and needs no EXECUTE; netd moves no
--                                                          entitlement state, and the writers it calls reach
--                                                          the transition as their owner
--   EXECUTE on iam_v2.ingest_absolute_counters          -- acctd is the caller (see the acctd file)
--   EXECUTE on iam_v2.record_auth_context_offer         -- scd is the caller
--   EXECUTE on iam_v2.p3_controlled_operation_open,
--              iam_v2.p5_controlled_operation_open      -- called by the controlled-writer TRIGGERS in the
--                                                          writer's context; netd writes no guarded table
-- gatep-grants.sql asserts after every reconcile that netd holds no entitlement writer and no iam_v2 table
-- write, so re-adding one of these fails the reconcile rather than widening netd silently.

-- ---- and the grace grant that was never netd's to hold --------------------
-- This file previously granted svc_netd EXECUTE on iam_v2.publish_checkout_grace_config, under the rationale
-- "checkout-grace publication and offer recording, both reached by the same Phase-3 surface". They are not the
-- same surface. Publishing grace policy is a Hotel-Admin commercial action performed by edged; netd is the
-- network-enforcement owner and has never called that function -- it only ever REFERENCED it, in the startup
-- boundary self-check, which reads pg_proc and needs no EXECUTE at all.
--
-- Unconditional and idempotent: revoking a privilege that was never granted is a no-op.
REVOKE EXECUTE ON FUNCTION iam_v2.publish_checkout_grace_config(uuid, uuid, uuid, integer, integer, integer, bigint, integer, text, integer) FROM svc_netd;

-- NOT granted: INSERT/UPDATE/DELETE on any iam_v2 table. netd's authority is exercised entirely through the
-- controlled-writer functions above, so a bug in netd cannot move session or entitlement state directly.
