-- 0163 -- THE DURABLE THROTTLE CHARGE IS ONE ROUND TRIP (audit decision AD-51, finding D2-L1).
-- edge-migrate: platform
--
-- public.auth_throttle_charge applies one attempt to every rule of the durable guest throttle inside a single
-- statement: per rule, the standing block check, the window upsert and, past the limit, the block. It is a faithful port
-- of internal/throttle.Store.Allow (same order, same results), so a sign-in costs one round trip instead of 6-9
-- statements in a client-driven transaction, and the shared appliance-wide row, which Go sorts last, is updated last.
--
-- The caller (Go) still computes the HMAC of every scope value, so the database never sees an identity, and still sorts
-- the rules (client-scoped first, the appliance-wide damper last, otherwise by kind, key, method) so concurrent charges
-- take row locks in one order. SECURITY INVOKER: it writes only public.auth_throttle_buckets, which svc_scd may already
-- write; it adds no privilege.
--
-- Additive and idempotent. The previous multi-statement path stays in Go as the fallback for a database that predates this.

BEGIN;

CREATE OR REPLACE FUNCTION public.auth_throttle_charge(
    p_kinds text[], p_keys text[], p_methods text[], p_limits integer[], p_blocks_seconds integer[],
    p_globals boolean[], p_now timestamptz, p_window_start timestamptz, p_window_seconds integer)
  RETURNS TABLE (allowed boolean, retry_milliseconds bigint, reason text)
  LANGUAGE plpgsql
  SET search_path = public, pg_temp AS $fn$
DECLARE
  i integer; n integer := COALESCE(array_length(p_kinds, 1), 0);
  v_allowed boolean := true; v_worst_ms bigint := 0; v_reason text := NULL;
  v_max_block timestamptz; v_count integer; v_retry_ms bigint;
BEGIN
  IF n <> COALESCE(array_length(p_keys, 1), 0) OR n <> COALESCE(array_length(p_methods, 1), 0)
     OR n <> COALESCE(array_length(p_limits, 1), 0) OR n <> COALESCE(array_length(p_blocks_seconds, 1), 0)
     OR n <> COALESCE(array_length(p_globals, 1), 0) THEN
    RAISE EXCEPTION 'throttle rule arrays must have the same length' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  FOR i IN 1..n LOOP
    -- A global damper is charged only when every client-scoped rule allowed the attempt.
    IF p_globals[i] AND NOT v_allowed THEN CONTINUE; END IF;

    SELECT max(b.blocked_until) INTO v_max_block FROM public.auth_throttle_buckets b
     WHERE b.scope_kind = p_kinds[i] AND b.scope_key = p_keys[i] AND b.method = p_methods[i]
       AND b.blocked_until > p_now;
    IF v_max_block IS NOT NULL AND v_max_block > p_now THEN
      v_allowed := false;
      v_retry_ms := (EXTRACT(EPOCH FROM (v_max_block - p_now)) * 1000)::bigint;
      IF v_retry_ms > v_worst_ms OR v_reason IS NULL THEN
        v_worst_ms := GREATEST(v_retry_ms, v_worst_ms); v_reason := p_kinds[i];
      END IF;
      CONTINUE;
    END IF;

    INSERT INTO public.auth_throttle_buckets (scope_kind, scope_key, method, window_start, window_len_s, attempt_count, updated_at)
    VALUES (p_kinds[i], p_keys[i], p_methods[i], p_window_start, p_window_seconds, 1, now())
    ON CONFLICT (scope_kind, scope_key, method, window_start)
    DO UPDATE SET attempt_count = public.auth_throttle_buckets.attempt_count + 1, updated_at = now()
    RETURNING attempt_count INTO v_count;

    IF v_count > p_limits[i] THEN
      v_allowed := false;
      v_retry_ms := (EXTRACT(EPOCH FROM (p_window_start + make_interval(secs => p_window_seconds) - p_now)) * 1000)::bigint;
      IF p_blocks_seconds[i] > 0 THEN
        UPDATE public.auth_throttle_buckets SET blocked_until = p_now + make_interval(secs => p_blocks_seconds[i]), updated_at = now()
         WHERE scope_kind = p_kinds[i] AND scope_key = p_keys[i] AND method = p_methods[i] AND window_start = p_window_start;
        v_retry_ms := p_blocks_seconds[i]::bigint * 1000;
      END IF;
      IF v_retry_ms > v_worst_ms OR v_reason IS NULL THEN
        v_worst_ms := GREATEST(v_retry_ms, v_worst_ms); v_reason := p_kinds[i];
      END IF;
    END IF;
  END LOOP;
  RETURN QUERY SELECT v_allowed, v_worst_ms, v_reason;
END $fn$;
REVOKE EXECUTE ON FUNCTION public.auth_throttle_charge(text[],text[],text[],integer[],integer[],boolean[],timestamptz,timestamptz,integer) FROM PUBLIC;
COMMENT ON FUNCTION public.auth_throttle_charge(text[],text[],text[],integer[],integer[],boolean[],timestamptz,timestamptz,integer) IS
  'AD-51: one attempt against every rule of the durable guest throttle in one round trip; a port of internal/throttle Store.Allow. Stores only the HMAC scope keys the caller supplies.';

DO $g$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_scd') THEN
    GRANT EXECUTE ON FUNCTION public.auth_throttle_charge(text[],text[],text[],integer[],integer[],boolean[],timestamptz,timestamptz,integer) TO svc_scd;
  END IF;
END $g$;

COMMIT;
