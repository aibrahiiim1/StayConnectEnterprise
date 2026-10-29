-- ---------------------------------------------------------------------------------------------------------
-- 0174: a retention period can be previewed before it is shortened (audit AD-67, P9 L14).
-- ---------------------------------------------------------------------------------------------------------
-- Shortening a period anonymises history on the next hourly run. The operator is now shown what the new periods WOULD remove
-- before they confirm, and the count comes from the retention functions themselves, so it can never drift from what they do:
-- public.data_retention_preview applies the proposed periods inside a sub-transaction, runs iam_v2.run_data_retention,
-- public.run_data_retention_public and iam_v2.run_staff_note_retention until they find nothing more to do (and counts the
-- sign-in attempts with the rule scd's purge uses), adds up what each reported per category, and then ROLLS EVERYTHING BACK by
-- raising inside the block. It runs twice -- with the periods in force, and with the proposed ones -- so the caller can tell
-- what the change adds to a backlog the hourly run has not reached yet.
--
-- Nothing is written: not the periods, not the run record, not the checkpoints, not an anonymised row. System logs (log_days)
-- are removed by scd from the journal, not by these functions, and are not counted.
BEGIN;

CREATE OR REPLACE FUNCTION public.data_retention_dry_run(p_tenant uuid, p_site uuid, p_override boolean,
        p_pms integer, p_activity integer, p_inactive integer, p_expired integer, p_audit integer, p_attempts integer,
        p_logs integer, p_notes integer) RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public', 'pg_temp'
    AS $function$
DECLARE r record; n integer; i integer; v_counts jsonb := '{}'::jsonb; v_complete boolean := false; v_days integer; v_k integer;
        v_detail text;
BEGIN
  BEGIN
    PERFORM set_config('statement_timeout', '45s', true);
    IF p_override THEN
      INSERT INTO iam_v2.site_data_retention AS s
        (tenant_id, site_id, pms_stay_days, client_activity_days, inactive_identity_days, expired_record_days, audit_log_days,
         sign_in_attempt_days, log_days, staff_note_days, updated_by)
      VALUES (p_tenant, p_site, p_pms, p_activity, p_inactive, p_expired, p_audit, p_attempts, p_logs, p_notes, 'preview')
      ON CONFLICT (tenant_id, site_id) DO UPDATE
         SET pms_stay_days = EXCLUDED.pms_stay_days, client_activity_days = EXCLUDED.client_activity_days,
             inactive_identity_days = EXCLUDED.inactive_identity_days, expired_record_days = EXCLUDED.expired_record_days,
             audit_log_days = EXCLUDED.audit_log_days, sign_in_attempt_days = EXCLUDED.sign_in_attempt_days,
             log_days = EXCLUDED.log_days, staff_note_days = EXCLUDED.staff_note_days;
    END IF;
    FOR i IN 1 .. 40 LOOP
      n := 0;
      FOR r IN SELECT x.o_category, x.o_anonymised FROM iam_v2.run_data_retention(p_tenant, p_site, 10000) x LOOP
        v_counts := jsonb_set(v_counts, ARRAY[r.o_category], to_jsonb(COALESCE((v_counts ->> r.o_category)::integer, 0) + r.o_anonymised));
        n := n + r.o_anonymised;
      END LOOP;
      FOR r IN SELECT x.o_category, x.o_anonymised FROM public.run_data_retention_public(p_tenant, p_site, 10000) x LOOP
        v_counts := jsonb_set(v_counts, ARRAY[r.o_category], to_jsonb(COALESCE((v_counts ->> r.o_category)::integer, 0) + r.o_anonymised));
        n := n + r.o_anonymised;
      END LOOP;
      FOR r IN SELECT x.o_category, x.o_anonymised FROM iam_v2.run_staff_note_retention(p_tenant, p_site, 10000) x LOOP
        v_counts := jsonb_set(v_counts, ARRAY[r.o_category], to_jsonb(COALESCE((v_counts ->> r.o_category)::integer, 0) + r.o_anonymised));
        n := n + r.o_anonymised;
      END LOOP;
      IF n = 0 THEN
        v_complete := true;
        EXIT;
      END IF;
    END LOOP;
    -- Sign-in attempts are deleted by scd's purge with exactly this rule (internal/signinattempt/store.go).
    SELECT g.sign_in_attempt_days INTO v_days FROM iam_v2.data_retention_get(p_tenant, p_site) g;
    SELECT count(*) INTO v_k FROM iam_v2.sign_in_attempts a
     WHERE a.tenant_id = p_tenant AND a.site_id = p_site AND a.occurred_at < now() - make_interval(days => v_days);
    v_counts := jsonb_set(v_counts, ARRAY['sign_in_attempts'], to_jsonb(v_k));
    RAISE EXCEPTION 'retention dry run' USING ERRCODE = 'DRYRN', DETAIL = jsonb_build_object('counts', v_counts, 'complete', v_complete)::text;
  EXCEPTION WHEN SQLSTATE 'DRYRN' THEN
    GET STACKED DIAGNOSTICS v_detail = PG_EXCEPTION_DETAIL;
    RETURN v_detail::jsonb;
  END;
END $function$;

-- What the proposed periods would remove, beside what the periods in force would remove. Rolled back; nothing is written.
CREATE OR REPLACE FUNCTION public.data_retention_preview(p_tenant uuid, p_site uuid,
        p_pms integer, p_activity integer, p_inactive integer, p_expired integer, p_audit integer, p_attempts integer,
        p_logs integer, p_notes integer) RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public', 'pg_temp'
    AS $function$
DECLARE v_now jsonb; v_new jsonb;
BEGIN
  -- The same lock the setter takes: a preview never runs against a half-made change.
  PERFORM pg_advisory_xact_lock(hashtext('data_retention'), hashtext(p_site::text));
  v_now := public.data_retention_dry_run(p_tenant, p_site, false, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL);
  v_new := public.data_retention_dry_run(p_tenant, p_site, true, p_pms, p_activity, p_inactive, p_expired, p_audit, p_attempts, p_logs, p_notes);
  RETURN jsonb_build_object('current', v_now -> 'counts', 'proposed', v_new -> 'counts',
                            'complete', COALESCE((v_now ->> 'complete')::boolean, false) AND COALESCE((v_new ->> 'complete')::boolean, false),
                            'not_counted', jsonb_build_array('log_days'));
END $function$;
COMMENT ON FUNCTION public.data_retention_preview(uuid, uuid, integer, integer, integer, integer, integer, integer, integer, integer) IS
  'What the proposed retention periods would remove, by category, counted by the retention functions themselves inside a rolled-back sub-transaction (0174, audit AD-67). Writes nothing.';

REVOKE ALL ON FUNCTION public.data_retention_dry_run(uuid, uuid, boolean, integer, integer, integer, integer, integer, integer, integer, integer) FROM PUBLIC;
REVOKE ALL ON FUNCTION public.data_retention_preview(uuid, uuid, integer, integer, integer, integer, integer, integer, integer, integer) FROM PUBLIC;

DO $g$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
    EXECUTE 'GRANT EXECUTE ON FUNCTION public.data_retention_preview(uuid, uuid, integer, integer, integer, integer, integer, integer, integer, integer) TO svc_edged';
  END IF;
END $g$;

COMMIT;
