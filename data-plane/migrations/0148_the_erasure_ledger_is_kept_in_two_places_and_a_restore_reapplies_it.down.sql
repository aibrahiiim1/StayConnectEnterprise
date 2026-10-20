-- 0148 down -- the earlier definitions come back; the functions 0148 added go. The ledger tables STAY: they are the
-- proof that an erasure ran, and a re-applied 0148 finds them. The ledger holds no guest data.

BEGIN;

-- restored from the previous definition
CREATE OR REPLACE FUNCTION public.erasure_journal_replay(p_journal jsonb) RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public', 'pg_temp'
    AS $function$
DECLARE e record; v_reapplied integer := 0; v_gone integer := 0; v_known integer := 0; v_failed jsonb := '[]'::jsonb;
        v_res jsonb; v_changed integer := 0;
BEGIN
  IF jsonb_typeof(p_journal) IS DISTINCT FROM 'array' THEN
    RAISE EXCEPTION 'a journal is a list of entries' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  SELECT count(*) INTO v_known FROM jsonb_to_recordset(p_journal) AS x(id uuid)
   WHERE EXISTS (SELECT 1 FROM iam_v2.erasure_journal j WHERE j.id = x.id);
  FOR e IN SELECT x.* FROM jsonb_to_recordset(p_journal)
             AS x(id uuid, tenant_id uuid, site_id uuid, erasure_ref text, kind text, subject_id uuid, detail text, erased_at timestamptz)
            WHERE NOT EXISTS (SELECT 1 FROM iam_v2.erasure_journal j WHERE j.id = x.id)
            ORDER BY x.erased_at, x.id LOOP
    BEGIN
      IF e.kind = 'assisted_record' THEN
        v_res := iam_v2.assisted_apply_records(e.tenant_id, e.site_id, ARRAY[e.detail], ARRAY[e.subject_id]);
        v_changed := v_changed + COALESCE((v_res ->> 'rows_changed')::integer, 0);
        PERFORM public.audit_clean_macs(e.tenant_id, ARRAY(SELECT jsonb_array_elements_text(v_res -> 'macs_cleared')), e.erasure_ref, '-infinity');
      ELSE
        v_res := public.guest_data_erase_core(e.tenant_id, e.site_id, e.kind, e.subject_id, 'restore', 'erasure replayed after a restore', true, e.erasure_ref);
      END IF;
      INSERT INTO iam_v2.erasure_journal (id, tenant_id, site_id, erasure_ref, kind, subject_id, detail, erased_at)
      VALUES (e.id, e.tenant_id, e.site_id, e.erasure_ref, e.kind, e.subject_id, e.detail, COALESCE(e.erased_at, now()))
      ON CONFLICT DO NOTHING;
      v_reapplied := v_reapplied + 1;
    EXCEPTION
      WHEN no_data_found THEN
        INSERT INTO iam_v2.erasure_journal (id, tenant_id, site_id, erasure_ref, kind, subject_id, detail, erased_at)
        VALUES (e.id, e.tenant_id, e.site_id, e.erasure_ref, e.kind, e.subject_id, e.detail, COALESCE(e.erased_at, now()))
        ON CONFLICT DO NOTHING;
        v_gone := v_gone + 1;
      WHEN OTHERS THEN
        v_failed := v_failed || jsonb_build_array(jsonb_build_object('erasure_ref', e.erasure_ref, 'kind', e.kind, 'error', SQLERRM));
    END;
  END LOOP;
  RETURN jsonb_build_object('entries', jsonb_array_length(p_journal), 'already_known', v_known, 'reapplied', v_reapplied,
                            'records_gone', v_gone, 'assisted_rows_changed', v_changed, 'failed', v_failed);
END $function$;

-- restored from the previous definition
CREATE OR REPLACE FUNCTION public.erasure_journal_export(p_tenant uuid, p_site uuid) RETURNS jsonb
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public', 'pg_temp'
    AS $$
  SELECT COALESCE(jsonb_agg(to_jsonb(j) - 'seq' ORDER BY j.erased_at, j.id), '[]'::jsonb)
    FROM iam_v2.erasure_journal j WHERE j.tenant_id = p_tenant AND j.site_id = p_site
$$;

-- restored from the previous definition
CREATE OR REPLACE FUNCTION iam_v2.request_register_answer(p_tenant uuid, p_site uuid, p_id uuid, p_actor text, p_status text,
        p_outcome text, p_note text, p_erasure_ref text, p_assisted_ref text) RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE q record;
BEGIN
  IF p_actor IS NULL OR length(btrim(p_actor)) NOT BETWEEN 1 AND 200 THEN
    RAISE EXCEPTION 'an operator is required' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  IF p_status IS NULL OR p_status NOT IN ('ANSWERED', 'PARTLY_REFUSED', 'REFUSED', 'WITHDRAWN') THEN
    RAISE EXCEPTION 'the outcome is answered, partly refused, refused or withdrawn' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  -- A refusal, or a part-refusal, names its ground: the person is told it, and told they may complain (Art. 12(4)).
  IF p_status IN ('PARTLY_REFUSED', 'REFUSED') AND COALESCE(p_outcome, 'NONE') = 'NONE' THEN
    RAISE EXCEPTION 'a refusal names its ground' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  IF p_note IS NOT NULL AND length(p_note) > 500 THEN
    RAISE EXCEPTION 'the note is at most 500 characters' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  SELECT * INTO q FROM guest_data_request_register x WHERE x.id = p_id AND x.tenant_id = p_tenant AND x.site_id = p_site FOR UPDATE;
  IF NOT FOUND THEN RAISE EXCEPTION 'request not found' USING ERRCODE = 'no_data_found'; END IF;
  IF q.status <> 'OPEN' THEN
    RAISE EXCEPTION 'register_refused' USING ERRCODE = 'object_in_use', DETAIL = 'closed';
  END IF;
  UPDATE guest_data_request_register
     SET status = p_status, outcome_code = COALESCE(NULLIF(p_outcome, ''), 'NONE'), answered_by = btrim(p_actor), answered_at = now(),
         erasure_ref = NULLIF(p_erasure_ref, ''), assisted_ref = NULLIF(p_assisted_ref, ''),
         requester_ref = '[removed]', outcome_note = NULL, extension_reason = NULL
   WHERE id = p_id;
  RETURN jsonb_build_object('reference', q.ref, 'kind', q.kind, 'requester', q.requester_ref, 'received_at', q.received_at,
                            'due_at', COALESCE(q.extended_until, q.due_at), 'answered_at', now(), 'status', p_status,
                            'outcome', COALESCE(NULLIF(p_outcome, ''), 'NONE'), 'note', p_note,
                            'verification_method', q.verification_method, 'erasure_ref', NULLIF(p_erasure_ref, ''),
                            'assisted_ref', NULLIF(p_assisted_ref, ''), 'answered_in_time', iam_v2.site_today(p_site) <= COALESCE(q.extended_until, q.due_at));
END $$;

-- restored from the previous definition
CREATE OR REPLACE FUNCTION iam_v2.assisted_request_close(p_tenant uuid, p_site uuid, p_request uuid, p_actor text, p_outcome text, p_reason text)
    RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $function$
DECLARE q record; v_exports integer; v_disclosed integer;
BEGIN
  IF p_actor IS NULL OR length(btrim(p_actor)) NOT BETWEEN 1 AND 200 THEN
    RAISE EXCEPTION 'an operator is required' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  IF p_outcome IS NULL OR p_outcome NOT IN ('COMPLETED', 'CANCELLED') THEN
    RAISE EXCEPTION 'the outcome must be COMPLETED or CANCELLED' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  IF p_reason IS NULL OR length(btrim(p_reason)) NOT BETWEEN 4 AND 500 THEN
    RAISE EXCEPTION 'a reason of 4-500 characters is required' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  SELECT * INTO q FROM assisted_guest_requests x WHERE x.id = p_request AND x.tenant_id = p_tenant AND x.site_id = p_site FOR UPDATE;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'assisted request not found' USING ERRCODE = 'no_data_found';
  END IF;
  IF q.status <> 'OPEN' THEN
    RAISE EXCEPTION 'assisted_request_refused' USING ERRCODE = 'object_in_use', DETAIL = 'closed';
  END IF;
  SELECT count(*) INTO v_exports FROM assisted_guest_request_events e WHERE e.request_id = p_request AND e.action = 'EXPORTED';
  SELECT count(*) INTO v_disclosed FROM assisted_guest_request_records r WHERE r.request_id = p_request AND r.action = 'DISCLOSED';
  -- An erasure completes only by running; an access request completes only after something was handed over.
  IF p_outcome = 'COMPLETED' AND (q.purpose <> 'ACCESS' OR v_exports = 0) THEN
    RAISE EXCEPTION 'assisted_request_refused' USING ERRCODE = 'object_in_use', DETAIL = 'nothing_done';
  END IF;
  PERFORM assisted_close(p_tenant, p_site, p_request, p_actor, p_outcome, p_reason,
                         jsonb_build_object('exports', v_exports, 'records_disclosed', v_disclosed),
                         CASE p_outcome WHEN 'COMPLETED' THEN 'COMPLETED' ELSE 'CANCELLED' END);
  -- The hotel's file of this request, handed back once: closing it removes the addresses and the free text.
  RETURN jsonb_build_object('ref', q.ref, 'status', p_outcome,
    'receipt', jsonb_build_object('reference', q.ref, 'purpose', q.purpose, 'requester', q.requester_ref,
                                  'verification_method', q.verification_method, 'evidence_note', q.evidence_note,
                                  'range_from', q.range_from, 'range_to', q.range_to,
                                  'statements', jsonb_build_object('identity_verified', q.attest_identity, 'device_shown', q.attest_device,
                                                                   'sole_use', q.attest_sole_use, 'private_address_confirmed', q.attest_private_address),
                                  'prepared_by', q.created_by, 'opened_at', q.created_at,
                                  'exports', v_exports, 'records_disclosed', v_disclosed));
END $function$;

-- restored from the previous definition
CREATE OR REPLACE FUNCTION public.assisted_request_erase(p_tenant uuid, p_site uuid, p_request uuid, p_actor text, p_reason text,
                                                         p_confirm text, p_signature text, p_selected jsonb)
    RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public', 'pg_temp'
    AS $$
DECLARE v jsonb; v_macs text[]; v_pats text[]; v_ref text; v_since timestamptz; n_audit integer := 0;
BEGIN
  PERFORM public.assisted_reason_check(p_tenant, p_site, iam_v2.assisted_request_anchors_json(p_tenant, p_site, p_request), p_reason);
  v := iam_v2.assisted_request_erase_run(p_tenant, p_site, p_request, p_actor, p_reason, p_confirm, p_signature, p_selected);
  v_ref := v ->> 'erasure_ref';
  v_macs := ARRAY(SELECT jsonb_array_elements_text(v -> 'macs_cleared'));
  n_audit := public.audit_clean_macs(p_tenant, v_macs, v_ref, COALESCE((v ->> 'since')::timestamptz, '-infinity'));
  RETURN (v - 'macs_cleared' - 'since') || jsonb_build_object('activity_log_entries_cleaned', n_audit);
END $$;

-- restored from the previous definition
CREATE OR REPLACE FUNCTION public.guest_data_erase(p_tenant uuid, p_site uuid, p_kind text, p_id uuid, p_operator text, p_reason text)
 RETURNS jsonb
 LANGUAGE sql
 SECURITY DEFINER
 SET search_path TO 'pg_catalog', 'public', 'pg_temp'
AS $function$
  SELECT public.guest_data_erase_core(p_tenant, p_site, p_kind, p_id, p_operator, p_reason, false, NULL)
$function$;

DROP FUNCTION IF EXISTS public.ledger_reset();
DROP FUNCTION IF EXISTS public.ledger_destination_note(boolean,text);
DROP FUNCTION IF EXISTS public.ledger_destination_set(text,jsonb,bytea,bytea,text,integer,text);
DROP FUNCTION IF EXISTS public.ledger_destination_secret();
DROP FUNCTION IF EXISTS public.ledger_destination_get();
DROP FUNCTION IF EXISTS public.ledger_apply(jsonb,boolean);
DROP FUNCTION IF EXISTS iam_v2.ledger_replay_assisted_closed(uuid,uuid,jsonb);
DROP FUNCTION IF EXISTS iam_v2.ledger_replay_answer(uuid,uuid,jsonb);
DROP FUNCTION IF EXISTS iam_v2.ledger_replay_erase(uuid,uuid,jsonb);
DROP FUNCTION IF EXISTS iam_v2.ledger_append_erasure(uuid,uuid,text);
DROP FUNCTION IF EXISTS iam_v2.ledger_verify();
DROP FUNCTION IF EXISTS iam_v2.ledger_export(bigint);
DROP FUNCTION IF EXISTS iam_v2.ledger_head();
DROP FUNCTION IF EXISTS iam_v2.ledger_confirm(text[]);
DROP FUNCTION IF EXISTS iam_v2.ledger_pending();
DROP FUNCTION IF EXISTS iam_v2.ledger_row_json(iam_v2.erasure_ledger);
DROP FUNCTION IF EXISTS iam_v2.ledger_append(uuid,uuid,text,jsonb);
DO $g$ BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_scd') THEN
    GRANT EXECUTE ON FUNCTION public.erasure_journal_export(uuid,uuid) TO svc_scd;
    GRANT EXECUTE ON FUNCTION public.erasure_journal_replay(jsonb) TO svc_scd;
  END IF;
END $g$;
COMMIT;
