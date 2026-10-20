-- 0146 down -- assisted guest data requests go. Open requests are cancelled and scrubbed first: nobody can finish
-- one once the functions are gone, and a request file must not keep a device address. The request tables, their
-- records and events STAY, with the guard function their trigger needs: they are the proof that a request ran,
-- and hold no address or free text once closed. The erasure register keeps 'assisted' in its kind check (its rows
-- are append-only and stay as written). Re-applying 0146 finds them and carries on.

BEGIN;

DO $c$
BEGIN
  IF to_regclass('iam_v2.assisted_guest_requests') IS NOT NULL THEN
    INSERT INTO iam_v2.assisted_guest_request_events (request_id, actor, action, detail)
      SELECT q.id, 'system', 'CANCELLED', '{"reason": "migration 0146 rolled back"}'::jsonb
        FROM iam_v2.assisted_guest_requests q WHERE q.status = 'OPEN';
    UPDATE iam_v2.assisted_guest_requests
       SET status = 'CANCELLED', closed_by = 'system', closed_at = now(), close_reason = 'migration 0146 rolled back',
           result = '{"rolled_back": true}'::jsonb, macs = NULL, requester_ref = '[removed]',
           evidence_note = '[removed after the request closed]'
     WHERE status = 'OPEN';
    DELETE FROM iam_v2.assisted_guest_request_anchors;
    DELETE FROM iam_v2.assisted_guest_request_records r USING iam_v2.assisted_guest_requests q
     WHERE q.id = r.request_id AND q.purpose = 'ACCESS';
  END IF;
END $c$;

-- restored from 0144_guest_data_requests_stay_with_the_guest_who_asked.up.sql
CREATE OR REPLACE FUNCTION public.run_staff_retention(p_tenant uuid, p_site uuid)
  RETURNS jsonb
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = public, pg_temp AS $fn$
DECLARE v_days integer; v_audit_days integer; ids uuid[]; n_ops integer; n_audit integer; n_net integer; n_https integer;
BEGIN
  SELECT r.disabled_account_days INTO v_days FROM public.operator_account_retention_get() r;
  SELECT r.audit_log_days INTO v_audit_days FROM iam_v2.data_retention_get(p_tenant, p_site) r;

  SELECT COALESCE(array_agg(o.id), '{}') INTO ids FROM (
    SELECT o.id FROM public.operators o
     WHERE o.status = 'disabled' AND o.anonymised_at IS NULL
       AND o.disabled_at < now() - make_interval(days => v_days)
     ORDER BY o.disabled_at LIMIT 500 FOR UPDATE) o;
  UPDATE public.operators o
     SET email = 'removed-' || left(replace(o.id::text, '-', ''), 12) || '@staff.invalid',
         display_name = NULL, password_hash = NULL, oidc_sub = NULL, anonymised_at = now(), updated_at = now()
   WHERE o.id = ANY (ids);
  GET DIAGNOSTICS n_ops = ROW_COUNT;
  DELETE FROM public.operator_mfa_recovery_codes WHERE operator_id = ANY (ids);
  DELETE FROM public.operator_mfa WHERE operator_id = ANY (ids);
  UPDATE public.audit_log l SET payload = l.payload - 'email' - 'display_name' - 'username'
   WHERE l.target_type = 'operator' AND l.target_id = ANY (ids::text[])
     AND (l.payload ? 'email' OR l.payload ? 'display_name' OR l.payload ? 'username');
  GET DIAGNOSTICS n_audit = ROW_COUNT;

  UPDATE public.system_network_audit x SET source_ip = NULL
   WHERE x.id IN (SELECT y.id FROM public.system_network_audit y
                   WHERE y.source_ip IS NOT NULL AND y.created_at < now() - make_interval(days => v_audit_days)
                   LIMIT 5000);
  GET DIAGNOSTICS n_net = ROW_COUNT;
  PERFORM set_config('public.staff_retention', 'on', true);
  UPDATE public.portal_https_settings_changes x SET source_ip = NULL
   WHERE x.id IN (SELECT y.id FROM public.portal_https_settings_changes y
                   WHERE y.source_ip IS NOT NULL AND y.changed_at < now() - make_interval(days => v_audit_days)
                   LIMIT 5000);
  GET DIAGNOSTICS n_https = ROW_COUNT;
  PERFORM set_config('public.staff_retention', '', true);

  RETURN jsonb_build_object('staff_accounts_anonymised', n_ops, 'audit_entries', n_audit,
                            'staff_addresses_cleared', n_net + n_https);
END $fn$;

DO $g$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
    EXECUTE 'REVOKE SELECT (tenant_id, state, client_ip, client_mac, user_agent, provider, created_at, consumed_at) ON public.social_oauth_states FROM iam_v2_owner';
    EXECUTE 'REVOKE UPDATE (client_ip, client_mac, user_agent) ON public.social_oauth_states FROM iam_v2_owner';
  END IF;
END $g$;

DROP FUNCTION IF EXISTS public.assisted_request_erase(uuid,uuid,uuid,text,text,text,text,jsonb);
DROP FUNCTION IF EXISTS public.assisted_request_export(uuid,uuid,uuid,text,text,text,text,jsonb);
DROP FUNCTION IF EXISTS public.assisted_request_close(uuid,uuid,uuid,text,text,text);
DROP FUNCTION IF EXISTS public.assisted_request_open(uuid,uuid,text,text,text[],timestamptz,timestamptz,jsonb,text,text,text,boolean,boolean,boolean,boolean,text);
DROP FUNCTION IF EXISTS public.assisted_request_reason_check(uuid,uuid,uuid,text);
DROP FUNCTION IF EXISTS public.assisted_reason_check(uuid,uuid,jsonb,text);
DROP FUNCTION IF EXISTS iam_v2.assisted_request_anchors_json(uuid,uuid,uuid);
DROP FUNCTION IF EXISTS iam_v2.assisted_request_erase_run(uuid,uuid,uuid,text,text,text,text,jsonb);
DROP FUNCTION IF EXISTS iam_v2.assisted_request_export_doc(uuid,uuid,uuid,text,text,text,text,jsonb);
DROP FUNCTION IF EXISTS iam_v2.assisted_validate_selection(uuid,uuid,uuid,text,jsonb,boolean);
DROP FUNCTION IF EXISTS iam_v2.expire_assisted_requests(uuid,uuid);
DROP FUNCTION IF EXISTS iam_v2.assisted_request_close(uuid,uuid,uuid,text,text,text);
DROP FUNCTION IF EXISTS iam_v2.assisted_close(uuid,uuid,uuid,text,text,text,jsonb,text);
DROP FUNCTION IF EXISTS iam_v2.assisted_request_list(uuid,uuid);
DROP FUNCTION IF EXISTS iam_v2.assisted_request_get(uuid,uuid,uuid);
DROP FUNCTION IF EXISTS iam_v2.assisted_request_open(uuid,uuid,text,text,text[],timestamptz,timestamptz,jsonb,text,text,text,boolean,boolean,boolean,boolean,text);
DROP FUNCTION IF EXISTS iam_v2.assisted_request_review_json(uuid,uuid,uuid);
DROP FUNCTION IF EXISTS iam_v2.assisted_review_signature(uuid,uuid,uuid);
DROP FUNCTION IF EXISTS iam_v2.assisted_record_detail(uuid,uuid,text,uuid);
DROP FUNCTION IF EXISTS iam_v2.assisted_request_review(uuid,uuid,uuid);
DROP FUNCTION IF EXISTS iam_v2.assisted_context(uuid,uuid,uuid);
DROP FUNCTION IF EXISTS iam_v2.assisted_device_whole(uuid,macaddr,uuid,uuid,timestamptz,timestamptz);
DROP FUNCTION IF EXISTS iam_v2.assisted_device_tied(uuid[],macaddr,uuid,uuid,uuid[],uuid[],uuid[],uuid[],uuid[]);
DROP FUNCTION IF EXISTS iam_v2.assisted_device_state(uuid[],macaddr,uuid,uuid,uuid[],uuid[],uuid[],uuid[],uuid[],uuid[]);
DROP FUNCTION IF EXISTS iam_v2.assisted_class(text,boolean,boolean,boolean,text);
DROP FUNCTION IF EXISTS iam_v2.mac_is_private(macaddr);

COMMIT;
