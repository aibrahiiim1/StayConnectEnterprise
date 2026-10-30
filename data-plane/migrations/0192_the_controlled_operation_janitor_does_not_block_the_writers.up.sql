-- edge-migrate: no-transaction
-- 0192 -- THE JANITOR INSIDE begin_controlled_operation NO LONGER SCANS, LOCKS AND DEADLOCKS EVERY WRITER (capacity acceptance, 2026-10-30)
--
-- iam_v2.begin_controlled_operation opens a controlled-writer family for the transaction that calls it, and every guest sign-in, every accounting
-- write and every enforcement write calls it: about 140 times a second at 300 sign-ins a minute. Each call began with
--
--     DELETE FROM iam_v2.controlled_operation_scope WHERE opened_at < now() - interval '1 hour';
--
-- on a table with no index on opened_at that holds one hour of rows (143,000 at that rate). Measured on the lab appliance:
--   * every call sequentially scanned the whole table: it was the largest single consumer of PostgreSQL CPU on the sign-in path;
--   * every row that crossed the one-hour mark was a row ALL concurrent callers tried to delete in their own order. The writers queued behind each
--     other's tuple locks, and PostgreSQL detected 148 deadlocks in one 30-minute run, cancelling sign-ins, accounting writes and enforcement writes at
--     the moment the oldest rows expired (log: "process ... detected deadlock while waiting for AccessExclusiveLock on tuple ... of relation
--     iam_v2.controlled_operation_scope", the victims were begin_controlled_operation, authorize_entitlement_device and ingest_absolute_counters).
--
-- What this changes, and what it does not:
--   * An index on opened_at, so finding expired rows is an index probe (the table is UNLOGGED and small; built CONCURRENTLY).
--   * The janitor deletes at most 100 expired rows per call, takes only rows nobody else holds (FOR UPDATE SKIP LOCKED) and never waits for a lock, so
--     two callers can never block or deadlock on each other's rows. The retention is unchanged (one hour: nothing reads a stale row, a transaction id does
--     not recur inside it) and the table is still bounded: at 140 calls a second a batch of 100 outruns the rate at which rows expire.
--   * The authorisation decision is untouched: the allowlist of families, the token, the row that iam_v2.*_controlled_operation_open reads, the
--     transaction-local setting. The function keeps its owner, SECURITY DEFINER, search_path and grants (CREATE OR REPLACE keeps them).
--   * iam_v2.p5_begin_controlled_operation (post-stay) carries the same janitor line; it is called rarely and is left as it is.
-- Idempotent. The down migration restores the previous body and drops the index.

CREATE INDEX CONCURRENTLY IF NOT EXISTS controlled_operation_scope_opened_idx
    ON iam_v2.controlled_operation_scope (opened_at);

CREATE OR REPLACE FUNCTION iam_v2.begin_controlled_operation(p_family text) RETURNS void
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE v_token uuid;
BEGIN
  -- The allowlist is the whole authorization decision, and it is also what keeps the GUC name below safe:
  -- the family is never caller-shaped text by the time it is concatenated.
  IF p_family NOT IN ('stay','auth_resolution','commerce_intent','checkout_conversion','source_conflict',
                      'auth_context','device_auth','session_binding','grace_publication','alert') THEN
    RAISE EXCEPTION 'no approved capability-scoped controlled-writer family %', p_family;
  END IF;
  -- Bounded janitor. A transaction that rolled back or a backend that died leaves its row behind; nothing
  -- reads a stale row (the txid will not recur inside the retention window) but the table should not grow
  -- without limit on an appliance that runs for years. A small batch of the oldest expired rows, taken only if no
  -- other transaction holds them: this call never waits for, and can never deadlock with, another caller's cleanup.
  DELETE FROM iam_v2.controlled_operation_scope
   WHERE ctid = ANY (ARRAY(
           SELECT s.ctid FROM iam_v2.controlled_operation_scope s
            WHERE s.opened_at < now() - interval '1 hour'
            ORDER BY s.opened_at
            LIMIT 100
              FOR UPDATE SKIP LOCKED));
  v_token := gen_random_uuid();
  INSERT INTO iam_v2.controlled_operation_scope (txid, family, token)
  VALUES (txid_current(), p_family, v_token)
  ON CONFLICT (txid, family) DO UPDATE SET token = EXCLUDED.token, opened_at = now();
  -- is_local = true: the setting dies with the transaction, so a scope cannot outlive the operation that
  -- opened it and be reused by the next statement on a pooled connection.
  PERFORM set_config('iam_v2.op_' || p_family, v_token::text, true);
END $$;
