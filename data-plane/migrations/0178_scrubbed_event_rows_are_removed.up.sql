-- 0178 -- ROWS THE RETENTION RUN HAS ALREADY SCRUBBED ARE REMOVED (audit AD-49 (a); D1-L5)
--
-- Purpose and retention per per-event table (the table is in docs/AUDIT_BACKLOG.md under AD-49):
--
--   public.auth_otps           A one-time code. Needed until it expires. Its address, IP and browser are scrubbed after
--                              expired_record_days (public.run_data_retention_public); a scrubbed row records nothing a
--                              hotel could use. REMOVED here, in the same pass that scrubs it, one period later.
--   public.pms_attempts        A room sign-in attempt, counted by the throttle over minutes. Scrubbed the same way. REMOVED
--                              once scrubbed.
--   public.social_oauth_states A sign-in redirect's one-time state; spent or expired, scrubbed the same way. REMOVED
--                              once scrubbed.
--
-- The other tables AD-49 names are KEPT, with their purpose, because something reads them or a guard owns their writes:
--   iam_v2.device_network_appearances  which networks a device was seen on (the device record's history; goes with the device)
--   iam_v2.auth_contexts / auth_resolutions   evidence of how a sign-in was decided (controlled writers; anonymised, not removed)
--   iam_v2.accounting_checkpoints      the counter baseline of a session (controlled writer; small)
--   iam_v2.session_entitlement_bindings / entitlement_state_transitions   the append-only record of what a session spent
--   iam_v2.sessions                    the guest-visible history and the totals of the Usage screens (anonymised, not removed)
--   public.audit_log                   the activity log; anonymised by period, chunk-drop is the audit-log owner's decision
--
-- The rule is deliberately narrow: only a row the existing retention has ALREADY scrubbed ('anonymised') and whose
-- expiry is older than the same period is deleted, so nothing is removed that was not first rendered content-free, and the
-- period stays the existing expired_record_days setting (no new setting is needed). Same shape and caller as
-- run_data_retention_public: scd's hourly loop. Additive: a new function.

BEGIN;

CREATE OR REPLACE FUNCTION public.run_event_table_trim(p_tenant uuid, p_site uuid, p_batch integer)
    RETURNS TABLE(o_category text, o_cutoff timestamptz, o_anonymised integer)
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public', 'pg_temp'
    AS $$
DECLARE r record; v_cut timestamptz; v_n integer; v_k integer;
BEGIN
  IF p_batch IS NULL OR p_batch NOT BETWEEN 1 AND 10000 THEN
    RAISE EXCEPTION 'a batch of 1-10000 is required' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  SELECT * INTO r FROM iam_v2.data_retention_get(p_tenant, p_site);
  v_cut := now() - make_interval(days => r.expired_record_days); v_n := 0;

  DELETE FROM public.auth_otps o
   WHERE o.id IN (SELECT x.id FROM public.auth_otps x
                   WHERE x.tenant_id = p_tenant AND x.expires_at < v_cut
                     AND x.destination = 'anonymised' AND x.ip IS NULL AND x.user_agent IS NULL
                   LIMIT p_batch);
  GET DIAGNOSTICS v_k = ROW_COUNT; v_n := v_n + v_k;

  DELETE FROM public.pms_attempts a
   WHERE a.id IN (SELECT x.id FROM public.pms_attempts x
                   WHERE x.tenant_id = p_tenant AND x.attempted_at < v_cut
                     AND x.room_number = 'anonymised' AND x.ip IS NULL
                   LIMIT p_batch);
  GET DIAGNOSTICS v_k = ROW_COUNT; v_n := v_n + v_k;

  DELETE FROM public.social_oauth_states s
   WHERE s.state IN (SELECT x.state FROM public.social_oauth_states x
                      WHERE x.tenant_id = p_tenant AND x.expires_at < v_cut
                        AND num_nonnulls(x.client_ip, x.client_mac, x.user_agent, x.code_verifier, x.nonce) = 0
                      LIMIT p_batch);
  GET DIAGNOSTICS v_k = ROW_COUNT; v_n := v_n + v_k;

  IF v_n > 0 THEN
    -- Not written to data_retention_runs: that record is of anonymisation (its category check admits nothing else), and
    -- what is removed here was already recorded there when it was scrubbed. scd logs this removal.
    o_category := 'expired_records_removed'; o_cutoff := v_cut; o_anonymised := v_n; RETURN NEXT;
  END IF;
END $$;
COMMENT ON FUNCTION public.run_event_table_trim(uuid, uuid, integer) IS
  'Removes one-time-code, room-attempt and sign-in-redirect rows that run_data_retention_public has already scrubbed and whose expiry is older than expired_record_days (0178, AD-49).';
REVOKE ALL ON FUNCTION public.run_event_table_trim(uuid, uuid, integer) FROM PUBLIC;

DO $g$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_scd') THEN
    GRANT EXECUTE ON FUNCTION public.run_event_table_trim(uuid, uuid, integer) TO svc_scd;
  END IF;
END $g$;

COMMIT;
