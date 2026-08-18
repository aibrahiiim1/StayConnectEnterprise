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
-- Derived from the iam_v2 objects referenced in cmd/netd. Functions, not tables, carry the writes: netd
-- moves no session state by raw UPDATE any more than anything else does.

GRANT USAGE ON SCHEMA iam_v2 TO svc_netd;

-- Read the session/device rows it enforces for.
GRANT SELECT ON iam_v2.sessions TO svc_netd;
GRANT SELECT ON iam_v2.devices  TO svc_netd;

-- The controlled-operation opener and its guards, same minimum already given to the other writers.
GRANT EXECUTE ON FUNCTION iam_v2.begin_controlled_operation(text)    TO svc_netd;
GRANT EXECUTE ON FUNCTION iam_v2.p3_controlled_operation_open(text)  TO svc_netd;
GRANT EXECUTE ON FUNCTION iam_v2.p5_controlled_operation_open(text)  TO svc_netd;

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

-- Accounting ingest: netd samples the accountable class and submits absolute counters.
GRANT EXECUTE ON FUNCTION iam_v2.ingest_absolute_counters(uuid, uuid, uuid, uuid, text, integer, bigint, bigint, bigint, timestamptz) TO svc_netd;

-- Checkout-grace publication and offer recording, both reached by the same Phase-3 surface.
GRANT EXECUTE ON FUNCTION iam_v2.publish_checkout_grace_config(uuid, uuid, uuid, integer, integer, integer, bigint, integer, text, integer) TO svc_netd;
GRANT EXECUTE ON FUNCTION iam_v2.record_auth_context_offer(uuid, uuid, uuid, uuid, integer, bigint, timestamptz) TO svc_netd;

-- The boundary self-check inspects this before starting; entitlement transitions are driven by the
-- enforcement lifecycle above.
GRANT EXECUTE ON FUNCTION iam_v2.apply_entitlement_transition(uuid, text, timestamptz, text) TO svc_netd;

-- NOT granted: INSERT/UPDATE/DELETE on any iam_v2 table. netd's authority is exercised entirely through the
-- controlled-writer functions above, so a bug in netd cannot move session or entitlement state directly.
