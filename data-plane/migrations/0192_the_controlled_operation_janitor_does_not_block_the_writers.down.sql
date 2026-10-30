-- edge-migrate: no-transaction
-- 0192 down -- restore the previous begin_controlled_operation (unbounded janitor) and drop the index.
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
  -- without limit on an appliance that runs for years.
  DELETE FROM iam_v2.controlled_operation_scope WHERE opened_at < now() - interval '1 hour';
  v_token := gen_random_uuid();
  INSERT INTO iam_v2.controlled_operation_scope (txid, family, token)
  VALUES (txid_current(), p_family, v_token)
  ON CONFLICT (txid, family) DO UPDATE SET token = EXCLUDED.token, opened_at = now();
  -- is_local = true: the setting dies with the transaction, so a scope cannot outlive the operation that
  -- opened it and be reused by the next statement on a pooled connection.
  PERFORM set_config('iam_v2.op_' || p_family, v_token::text, true);
END $$;

DROP INDEX CONCURRENTLY IF EXISTS iam_v2.controlled_operation_scope_opened_idx;
