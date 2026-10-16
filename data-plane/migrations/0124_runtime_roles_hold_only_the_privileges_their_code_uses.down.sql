-- Reverting 0124: the entitlement guard and the adjustment writer return to their 0123 form, and the privileges
-- 0124 withdrew are granted back exactly as the earlier migrations and Gate-P files gave them.
--
-- WHAT THIS COSTS. A caller can again lift the counter / window guard by setting iam_v2.allow_adjust itself, the
-- quota and the pinned plan of an entitlement are again writable by any role holding UPDATE on the table, and
-- every runtime role is back to the broader grants the audit found (B-7). Note that a Gate-P reconcile run from
-- deploy/gatep at or after 0124 narrows the grants again -- deploy the matching gatep files with the rollback.
-- No data is touched either way.

BEGIN;

CREATE OR REPLACE FUNCTION iam_v2.trg_entitlement_guard()
 RETURNS trigger
 LANGUAGE plpgsql
AS $function$
DECLARE s_key text; os_key text;
BEGIN
  IF TG_OP='UPDATE' THEN
    IF OLD.status='TERMINATED' AND NEW.status<>'TERMINATED' THEN
      RAISE EXCEPTION 'no transition out of TERMINATED'; END IF;
    IF ( NEW.consumed_data_bytes < OLD.consumed_data_bytes
      OR NEW.consumed_online_seconds < OLD.consumed_online_seconds
      OR (OLD.window_ends_at IS NOT NULL AND NEW.window_ends_at IS DISTINCT FROM OLD.window_ends_at) )
      AND current_setting('iam_v2.allow_adjust', true) IS DISTINCT FROM 'on'
    THEN RAISE EXCEPTION 'counter decrease / window move only via entitlement_adjustments'; END IF;
  END IF;
  IF NEW.supersedes_entitlement_id IS NOT NULL THEN
    SELECT CASE WHEN stay_id IS NOT NULL THEN 'stay:'||stay_id WHEN guest_account_id IS NOT NULL THEN 'acct:'||guest_account_id
                WHEN voucher_id IS NOT NULL THEN 'vou:'||voucher_id ELSE 'prin:'||guest_principal_id END
      INTO os_key FROM iam_v2.entitlements WHERE id=NEW.supersedes_entitlement_id;
    s_key := CASE WHEN NEW.stay_id IS NOT NULL THEN 'stay:'||NEW.stay_id WHEN NEW.guest_account_id IS NOT NULL THEN 'acct:'||NEW.guest_account_id
                  WHEN NEW.voucher_id IS NOT NULL THEN 'vou:'||NEW.voucher_id ELSE 'prin:'||NEW.guest_principal_id END;
    IF s_key IS DISTINCT FROM os_key THEN RAISE EXCEPTION 'cross-subject supersession rejected (% vs %)', s_key, os_key; END IF;
  END IF;
  RETURN NEW;
END; $function$;
COMMENT ON FUNCTION iam_v2.trg_entitlement_guard() IS NULL;

CREATE OR REPLACE FUNCTION iam_v2.apply_adjustment(p_ent uuid, p_field text, p_new text, p_actor uuid, p_reason text)
 RETURNS void
 LANGUAGE plpgsql
AS $function$
DECLARE t uuid; s uuid; oldv text;
BEGIN
  SELECT tenant_id, site_id INTO t, s FROM iam_v2.entitlements WHERE id=p_ent;
  PERFORM set_config('iam_v2.allow_adjust','on', true);
  IF p_field='consumed_data_bytes' THEN
    SELECT consumed_data_bytes::text INTO oldv FROM iam_v2.entitlements WHERE id=p_ent;
    UPDATE iam_v2.entitlements SET consumed_data_bytes=p_new::bigint, usage_version=usage_version+1 WHERE id=p_ent;
  ELSIF p_field='window_ends_at' THEN
    SELECT window_ends_at::text INTO oldv FROM iam_v2.entitlements WHERE id=p_ent;
    UPDATE iam_v2.entitlements SET window_ends_at=p_new::timestamptz, usage_version=usage_version+1 WHERE id=p_ent;
  ELSE RAISE EXCEPTION 'unsupported adjustment field %', p_field; END IF;
  INSERT INTO iam_v2.entitlement_adjustments(tenant_id,site_id,entitlement_id,field,old_value,new_value,actor,reason)
    VALUES (t,s,p_ent,p_field,oldv,p_new,p_actor,p_reason);
  PERFORM set_config('iam_v2.allow_adjust','off', true);
END; $function$;

GRANT EXECUTE ON FUNCTION iam_v2.apply_adjustment(uuid, text, text, uuid, text) TO PUBLIC;
GRANT EXECUTE ON FUNCTION iam_v2.ingest_sample(uuid, bigint, bigint, bigint, integer) TO PUBLIC;

-- svc_netd
GRANT SELECT ON iam_v2.devices TO svc_netd;
GRANT EXECUTE ON FUNCTION iam_v2.apply_entitlement_transition(uuid, text, timestamptz, text) TO svc_netd;
GRANT EXECUTE ON FUNCTION iam_v2.ingest_absolute_counters(uuid, uuid, uuid, uuid, text, integer, bigint, bigint, bigint, timestamptz) TO svc_netd;
GRANT EXECUTE ON FUNCTION iam_v2.record_auth_context_offer(uuid, uuid, uuid, uuid, integer, bigint, timestamptz) TO svc_netd;
GRANT EXECUTE ON FUNCTION iam_v2.p3_controlled_operation_open(text) TO svc_netd;
GRANT INSERT, UPDATE, SELECT ON public.appliance_service_health TO svc_netd;

-- svc_acctd
GRANT SELECT, INSERT, UPDATE ON iam_v2.accounting_records, iam_v2.accounting_checkpoints, iam_v2.delayed_accounting_records,
                                iam_v2.entitlement_boundary_watermarks, iam_v2.session_entitlement_bindings TO svc_acctd;
GRANT SELECT ON iam_v2.devices, iam_v2.service_plans, iam_v2.internet_packages, iam_v2.internet_package_revisions,
                iam_v2.purchases, iam_v2.stays, iam_v2.pms_interfaces, iam_v2.session_online_watermarks,
                iam_v2.entitlement_devices, iam_v2.entitlement_termination_evidence TO svc_acctd;
GRANT EXECUTE ON FUNCTION iam_v2.register_class_origin(uuid, uuid, uuid, uuid, text, integer, bigint, bigint, bigint, timestamptz) TO svc_acctd;
GRANT EXECUTE ON FUNCTION iam_v2.apply_entitlement_transition(uuid, text, timestamptz, text) TO svc_acctd;
GRANT EXECUTE ON FUNCTION iam_v2.authorize_entitlement_device(uuid, uuid, timestamptz) TO svc_acctd;
GRANT EXECUTE ON FUNCTION iam_v2.rebind_session_entitlement(uuid, uuid, timestamptz) TO svc_acctd;
GRANT EXECUTE ON FUNCTION iam_v2.terminate_entitlement_at_boundary(uuid, timestamptz, text) TO svc_acctd;
GRANT EXECUTE ON FUNCTION iam_v2.entitlement_usage_bytes(uuid, timestamptz) TO svc_acctd;
GRANT EXECUTE ON FUNCTION iam_v2.p6_due_terminal(uuid) TO svc_acctd;
GRANT EXECUTE ON FUNCTION iam_v2.p3_controlled_operation_open(text) TO svc_acctd;
GRANT INSERT ON public.accounting_records TO svc_acctd;
GRANT INSERT, UPDATE, SELECT ON public.appliance_service_health TO svc_acctd;

-- svc_scd
REVOKE UPDATE (status) ON iam_v2.entitlements FROM svc_scd;
GRANT UPDATE ON iam_v2.sessions, iam_v2.entitlements TO svc_scd;
GRANT SELECT, INSERT, UPDATE ON iam_v2.verification_access_grants TO svc_scd;
GRANT INSERT ON public.pms_attempts TO svc_scd;
GRANT INSERT, UPDATE, SELECT ON public.appliance_service_health TO svc_scd;

-- svc_edged
REVOKE UPDATE (entitlement_id) ON iam_v2.sessions FROM svc_edged;
GRANT UPDATE ON iam_v2.sessions TO svc_edged;
GRANT SELECT ON iam_v2.posting_presend_aborts, iam_v2.pms_interface_financial_setting_changes,
                iam_v2.pms_connection_settings_changes, iam_v2.pms_reconciliation_settings_changes,
                iam_v2.pms_room_inventory, iam_v2.pms_room_inventory_changes, iam_v2.pms_resync_coverage,
                iam_v2.voucher_revocations, iam_v2.verification_access_grants,
                iam_v2.payment_account_conversion_changes, iam_v2.pms_review_expiry_settings,
                iam_v2.pms_review_expiry_settings_changes, iam_v2.device_limit_disconnections TO svc_edged;
GRANT EXECUTE ON FUNCTION iam_v2.pms_known_room_inventory(uuid, uuid, uuid, bigint, integer) TO svc_edged;
GRANT EXECUTE ON FUNCTION iam_v2.pms_rebaseline_room_inventory(uuid, uuid, uuid, bigint, text, text) TO svc_edged;
GRANT EXECUTE ON FUNCTION iam_v2.pms_accept_startup_data_gap(uuid, uuid, uuid, text, text) TO svc_edged;
GRANT EXECUTE ON FUNCTION iam_v2.site_module_get(uuid, uuid) TO svc_edged;

-- svc_pmsd
REVOKE UPDATE (status) ON iam_v2.entitlements FROM svc_pmsd;
REVOKE UPDATE (state, ended, end_reason) ON iam_v2.sessions FROM svc_pmsd;
GRANT UPDATE ON iam_v2.entitlements, iam_v2.sessions TO svc_pmsd;
GRANT SELECT ON iam_v2.pms_resync_coverage, iam_v2.pms_room_inventory TO svc_pmsd;
GRANT EXECUTE ON FUNCTION iam_v2.pms_known_room_inventory(uuid, uuid, uuid, bigint, integer) TO svc_pmsd;

-- The 0096 nine, to every service role again
GRANT EXECUTE ON FUNCTION iam_v2.p5_controlled_operation_open(text) TO svc_scd, svc_edged, svc_acctd, svc_netd, svc_pmsd;
GRANT EXECUTE ON FUNCTION iam_v2.pms_connection_settings_get(uuid,uuid,uuid) TO svc_scd, svc_acctd, svc_netd;
GRANT EXECUTE ON FUNCTION iam_v2.pms_connection_settings_set(uuid,uuid,uuid,text,text,integer,integer,integer,integer,integer) TO svc_scd, svc_acctd, svc_netd, svc_pmsd;
GRANT EXECUTE ON FUNCTION iam_v2.pms_dispose_snapshot_cases(uuid,uuid,uuid,text,text) TO svc_scd, svc_edged, svc_acctd, svc_netd, svc_pmsd;
GRANT EXECUTE ON FUNCTION iam_v2.pms_reconciliation_settings_get(uuid,uuid) TO svc_scd, svc_acctd, svc_netd, svc_pmsd;
GRANT EXECUTE ON FUNCTION iam_v2.pms_reconciliation_settings_set(uuid,uuid,integer,integer,text,text,integer,integer) TO svc_scd, svc_edged, svc_acctd, svc_netd, svc_pmsd;
GRANT EXECUTE ON FUNCTION iam_v2.pms_record_resync_coverage(uuid,uuid,uuid,bigint,text[],integer,integer,integer) TO svc_scd, svc_edged, svc_acctd, svc_netd;
GRANT EXECUTE ON FUNCTION iam_v2.pms_roster_of_generation(uuid,uuid,uuid,bigint) TO svc_scd, svc_edged, svc_acctd, svc_netd, svc_pmsd;
GRANT EXECUTE ON FUNCTION iam_v2.pms_site_blocked_after_refusals(uuid,uuid) TO svc_scd, svc_edged, svc_acctd, svc_netd, svc_pmsd;

COMMIT;
