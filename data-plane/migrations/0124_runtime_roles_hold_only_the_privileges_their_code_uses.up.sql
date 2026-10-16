-- 0124 -- EVERY RUNTIME ROLE HOLDS ONLY THE PRIVILEGES ITS CODE USES, AND AN ENTITLEMENT'S TERMS CANNOT BE
-- REWRITTEN AROUND THE GUARD (audit B-7 / backend F-7).
--
-- WHAT WAS WRONG
--
--   1. GRANTS BROADER THAN PURPOSE. Derived statement by statement from the binaries at this head (cmd/<binary>
--      plus every internal package it links), the service roles held privileges no code path uses:
--        * svc_netd and svc_acctd could EXECUTE iam_v2.apply_entitlement_transition -- the one writer of an
--          entitlement's state -- although neither calls it (the definers they call reach it as their owner);
--        * svc_acctd held INSERT/UPDATE on five accounting tables, and EXECUTE on authorize_entitlement_device
--          and terminate_entitlement_at_boundary, which migration 0039 says it must not hold and asserts;
--        * svc_scd held table-wide UPDATE on iam_v2.sessions (0033: "NOT UPDATE") and iam_v2.entitlements,
--          although it issues no UPDATE against either -- it needs only a row LOCK on entitlements;
--        * svc_edged and svc_pmsd held table-wide UPDATE on sessions / entitlements for one column's write or a
--          lock;
--        * 0096 handed nine PMS definers -- including the connection and roster-reconciliation SETTERS -- to
--          every service role, so netd, acctd and scd could each rewrite PMS settings;
--        * and a scatter of reads and executes nothing calls (see deploy/gatep for each, with its reason).
--
--   2. THE ENTITLEMENT GUARD COULD BE SWITCHED OFF BY ITS CALLER. iam_v2.trg_entitlement_guard refuses a counter
--      decrease or a moved window unless `current_setting('iam_v2.allow_adjust') = 'on'` -- and a custom setting
--      is settable by ANY role: `SET LOCAL iam_v2.allow_adjust = 'on'` and then an UPDATE gave a guest their
--      data back or moved their window, with no adjustment recorded. The same setting, set before calling a
--      definer, lifted the guard for the definer's statements too. And data_quota_bytes,
--      service_plan_revision_id and the other terms of a grant were not guarded at all: a role with UPDATE could
--      raise a quota or swap the plan an entitlement was sold under.
--
-- WHAT THIS DOES
--
--   * Rewrites the guard (same trigger, same name):
--       - a caller that is neither the controlled writers' owner nor a superuser may change NO column of an
--         entitlement except status -- and status is already the controlled writer's alone (its own guard);
--       - a counter decrease or window move needs either a superuser with iam_v2.allow_adjust = 'on' (the
--         maintenance path, unchanged) or an ADJUSTMENT RECORDED IN THIS TRANSACTION: the setting must name an
--         iam_v2.entitlement_adjustments row for this entitlement and this field, written by this transaction,
--         carrying this new value. Only the owner writes that table, so a runtime role can no longer forge it,
--         and setting the value before calling a definer unlocks nothing.
--   * Rewrites iam_v2.apply_adjustment to record the adjustment FIRST and name it, and takes EXECUTE on it and on
--     the scratch-era iam_v2.ingest_sample away from PUBLIC (both are SECURITY INVOKER writers no runtime path
--     calls; with schema USAGE granted to the service roles, PUBLIC's EXECUTE had become theirs).
--   * Revokes, here, every privilege deploy/gatep stops granting, so the live database narrows with the
--     migration and not only at the next reconcile, and grants the column-level replacements.
--   * Asserts the result. deploy/gatep/gatep-grants.sql asserts the same shape after EVERY reconcile, so a later
--     migration or grant file that widens a role again fails the reconcile instead of landing silently.
--
-- NO DATA CHANGES. No row is written, moved or removed. Every path the binaries exercise keeps working: each
-- remaining grant is named, with the statement that uses it, in deploy/gatep.

BEGIN;

-- ---------------------------------------------------------------------------------------------------------
-- 1. The entitlement guard
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION iam_v2.trg_entitlement_guard()
 RETURNS trigger
 LANGUAGE plpgsql
AS $function$
DECLARE s_key text; os_key text; v_owner text; v_trusted boolean; v_adj text; v_field text; v_new text;
BEGIN
  IF TG_OP='UPDATE' THEN
    IF OLD.status='TERMINATED' AND NEW.status<>'TERMINATED' THEN
      RAISE EXCEPTION 'no transition out of TERMINATED'; END IF;

    -- WHO IS WRITING. The controlled writers are SECURITY DEFINER functions owned by the owner of
    -- apply_entitlement_transition, so inside them current_user is that owner. A superuser is the maintenance
    -- path. Everybody else -- every runtime role -- is untrusted here. Resolved from the catalog on every call,
    -- like p3_controlled_writer_only, so a re-owned schema is followed rather than hard-coded.
    SELECT pg_get_userbyid(p.proowner) INTO v_owner FROM pg_proc p
     WHERE p.oid = to_regprocedure('iam_v2.apply_entitlement_transition(uuid,text,timestamptz,text)');
    v_trusted := current_user = v_owner
              OR EXISTS (SELECT 1 FROM pg_roles WHERE rolname = current_user AND rolsuper);

    -- AN UNTRUSTED CALLER CHANGES NOTHING BUT STATUS, and status has its own controlled-writer guard
    -- (p3_entitlement_controlled_writer), which refuses it outside apply_entitlement_transition. The window, the
    -- counters, the quota (data_quota_bytes), the pinned plan and package (service_plan_revision_id,
    -- package_revision_id), the policy snapshot, the subject and the scope are the terms the entitlement was
    -- granted on; none is a runtime write.
    IF NOT v_trusted AND (to_jsonb(NEW) - 'status') IS DISTINCT FROM (to_jsonb(OLD) - 'status') THEN
      RAISE EXCEPTION 'entitlements: the terms of a grant are written only by the controlled iam_v2 writers (caller %)',
        current_user USING ERRCODE = 'insufficient_privilege';
    END IF;

    IF ( NEW.consumed_data_bytes < OLD.consumed_data_bytes
      OR NEW.consumed_online_seconds < OLD.consumed_online_seconds
      OR (OLD.window_ends_at IS NOT NULL AND NEW.window_ends_at IS DISTINCT FROM OLD.window_ends_at) ) THEN
      v_adj := current_setting('iam_v2.allow_adjust', true);
      IF v_adj = 'on' AND EXISTS (SELECT 1 FROM pg_roles WHERE rolname = current_user AND rolsuper) THEN
        NULL;  -- the superuser maintenance path, as before
      ELSE
        -- The ONE field this statement moves, and the value it moves it to. A statement that moves two of them
        -- matches no single recorded adjustment and is refused.
        IF NEW.consumed_data_bytes < OLD.consumed_data_bytes
           AND NEW.window_ends_at IS NOT DISTINCT FROM OLD.window_ends_at
           AND NEW.consumed_online_seconds >= OLD.consumed_online_seconds THEN
          v_field := 'consumed_data_bytes'; v_new := NEW.consumed_data_bytes::text;
        ELSIF OLD.window_ends_at IS NOT NULL AND NEW.window_ends_at IS DISTINCT FROM OLD.window_ends_at
           AND NEW.consumed_data_bytes >= OLD.consumed_data_bytes
           AND NEW.consumed_online_seconds >= OLD.consumed_online_seconds THEN
          v_field := 'window_ends_at'; v_new := NEW.window_ends_at::text;
        END IF;
        IF v_field IS NULL OR v_adj IS NULL OR v_adj !~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
           OR NOT EXISTS (SELECT 1 FROM iam_v2.entitlement_adjustments a
                           WHERE a.id = v_adj::uuid AND a.entitlement_id = NEW.id AND a.field = v_field
                             AND a.created_at = now()
                             AND a.new_value IS NOT DISTINCT FROM v_new) THEN
          RAISE EXCEPTION 'counter decrease / window move only via entitlement_adjustments';
        END IF;
      END IF;
    END IF;
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

COMMENT ON FUNCTION iam_v2.trg_entitlement_guard() IS
  'Entitlement row guard (0124): no transition out of TERMINATED; a caller that is neither the controlled '
  'writers'' owner nor a superuser changes no column but status; a counter decrease or window move needs a '
  'superuser with iam_v2.allow_adjust=on or an entitlement_adjustments row recorded in this transaction for this '
  'entitlement, field and value, named by iam_v2.allow_adjust; no cross-subject supersession.';

-- ---------------------------------------------------------------------------------------------------------
-- 2. The adjustment writer records first, and names what it recorded
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION iam_v2.apply_adjustment(p_ent uuid, p_field text, p_new text, p_actor uuid, p_reason text)
 RETURNS void
 LANGUAGE plpgsql
AS $function$
DECLARE t uuid; s uuid; oldv text; v_new text; v_id uuid;
BEGIN
  SELECT tenant_id, site_id INTO t, s FROM iam_v2.entitlements WHERE id=p_ent;
  IF p_field='consumed_data_bytes' THEN
    SELECT consumed_data_bytes::text INTO oldv FROM iam_v2.entitlements WHERE id=p_ent;
    v_new := p_new::bigint::text;
  ELSIF p_field='window_ends_at' THEN
    SELECT window_ends_at::text INTO oldv FROM iam_v2.entitlements WHERE id=p_ent;
    v_new := p_new::timestamptz::text;
  ELSE RAISE EXCEPTION 'unsupported adjustment field %', p_field; END IF;
  -- Recorded BEFORE the write, in canonical text, so the guard can match the write to this exact record.
  INSERT INTO iam_v2.entitlement_adjustments(tenant_id,site_id,entitlement_id,field,old_value,new_value,actor,reason)
    VALUES (t,s,p_ent,p_field,oldv,v_new,p_actor,p_reason)
    RETURNING id INTO v_id;
  PERFORM set_config('iam_v2.allow_adjust', v_id::text, true);
  IF p_field='consumed_data_bytes' THEN
    UPDATE iam_v2.entitlements SET consumed_data_bytes=v_new::bigint, usage_version=usage_version+1 WHERE id=p_ent;
  ELSE
    UPDATE iam_v2.entitlements SET window_ends_at=v_new::timestamptz, usage_version=usage_version+1 WHERE id=p_ent;
  END IF;
  PERFORM set_config('iam_v2.allow_adjust','off', true);
END; $function$;

-- Both are SECURITY INVOKER writers that no runtime path calls. PUBLIC's EXECUTE, made effective for every
-- service role by their iam_v2 schema USAGE, is withdrawn; the owner keeps it.
REVOKE EXECUTE ON FUNCTION iam_v2.apply_adjustment(uuid, text, text, uuid, text) FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION iam_v2.ingest_sample(uuid, bigint, bigint, bigint, integer) FROM PUBLIC;

-- ---------------------------------------------------------------------------------------------------------
-- 3. The narrowing, mirrored in deploy/gatep (the reconcile reproduces exactly this)
-- ---------------------------------------------------------------------------------------------------------
-- svc_netd
REVOKE SELECT ON iam_v2.devices FROM svc_netd;
REVOKE EXECUTE ON FUNCTION iam_v2.apply_entitlement_transition(uuid, text, timestamptz, text) FROM svc_netd;
REVOKE EXECUTE ON FUNCTION iam_v2.ingest_absolute_counters(uuid, uuid, uuid, uuid, text, integer, bigint, bigint, bigint, timestamptz) FROM svc_netd;
REVOKE EXECUTE ON FUNCTION iam_v2.record_auth_context_offer(uuid, uuid, uuid, uuid, integer, bigint, timestamptz) FROM svc_netd;
REVOKE EXECUTE ON FUNCTION iam_v2.p3_controlled_operation_open(text) FROM svc_netd;
REVOKE SELECT, INSERT, UPDATE ON public.appliance_service_health FROM svc_netd;

-- svc_acctd
REVOKE INSERT, UPDATE ON iam_v2.accounting_records FROM svc_acctd;
REVOKE INSERT, UPDATE ON iam_v2.accounting_checkpoints FROM svc_acctd;
REVOKE INSERT, UPDATE ON iam_v2.delayed_accounting_records FROM svc_acctd;
REVOKE INSERT, UPDATE ON iam_v2.entitlement_boundary_watermarks FROM svc_acctd;
REVOKE INSERT, UPDATE ON iam_v2.session_entitlement_bindings FROM svc_acctd;
REVOKE SELECT ON iam_v2.accounting_checkpoints FROM svc_acctd;
REVOKE SELECT ON iam_v2.delayed_accounting_records FROM svc_acctd;
REVOKE SELECT ON iam_v2.entitlement_boundary_watermarks FROM svc_acctd;
REVOKE SELECT ON iam_v2.devices FROM svc_acctd;
REVOKE SELECT ON iam_v2.service_plans FROM svc_acctd;
REVOKE SELECT ON iam_v2.internet_packages FROM svc_acctd;
REVOKE SELECT ON iam_v2.internet_package_revisions FROM svc_acctd;
REVOKE SELECT ON iam_v2.purchases FROM svc_acctd;
REVOKE SELECT ON iam_v2.stays FROM svc_acctd;
REVOKE SELECT ON iam_v2.pms_interfaces FROM svc_acctd;
REVOKE SELECT ON iam_v2.session_online_watermarks FROM svc_acctd;
REVOKE SELECT ON iam_v2.entitlement_devices FROM svc_acctd;
REVOKE SELECT ON iam_v2.entitlement_termination_evidence FROM svc_acctd;
REVOKE EXECUTE ON FUNCTION iam_v2.register_class_origin(uuid, uuid, uuid, uuid, text, integer, bigint, bigint, bigint, timestamptz) FROM svc_acctd;
REVOKE EXECUTE ON FUNCTION iam_v2.apply_entitlement_transition(uuid, text, timestamptz, text) FROM svc_acctd;
REVOKE EXECUTE ON FUNCTION iam_v2.authorize_entitlement_device(uuid, uuid, timestamptz) FROM svc_acctd;
REVOKE EXECUTE ON FUNCTION iam_v2.rebind_session_entitlement(uuid, uuid, timestamptz) FROM svc_acctd;
REVOKE EXECUTE ON FUNCTION iam_v2.terminate_entitlement_at_boundary(uuid, timestamptz, text) FROM svc_acctd;
REVOKE EXECUTE ON FUNCTION iam_v2.entitlement_usage_bytes(uuid, timestamptz) FROM svc_acctd;
REVOKE EXECUTE ON FUNCTION iam_v2.p6_due_terminal(uuid) FROM svc_acctd;
REVOKE EXECUTE ON FUNCTION iam_v2.p3_controlled_operation_open(text) FROM svc_acctd;
REVOKE INSERT ON public.accounting_records FROM svc_acctd;
REVOKE SELECT, INSERT, UPDATE ON public.appliance_service_health FROM svc_acctd;

-- svc_scd: no UPDATE on sessions; entitlements only the one column a row lock needs
REVOKE UPDATE ON iam_v2.sessions FROM svc_scd;
REVOKE UPDATE ON iam_v2.entitlements FROM svc_scd;
GRANT UPDATE (status) ON iam_v2.entitlements TO svc_scd;
REVOKE SELECT, INSERT, UPDATE ON iam_v2.verification_access_grants FROM svc_scd;
REVOKE INSERT ON public.pms_attempts FROM svc_scd;
REVOKE SELECT, INSERT, UPDATE ON public.appliance_service_health FROM svc_scd;

-- svc_edged: one column of sessions (the transfer's re-point); reads and executes nothing calls
REVOKE UPDATE ON iam_v2.sessions FROM svc_edged;
GRANT UPDATE (entitlement_id) ON iam_v2.sessions TO svc_edged;
REVOKE SELECT ON iam_v2.posting_presend_aborts FROM svc_edged;
REVOKE SELECT ON iam_v2.pms_interface_financial_setting_changes FROM svc_edged;
REVOKE SELECT ON iam_v2.pms_connection_settings_changes FROM svc_edged;
REVOKE SELECT ON iam_v2.pms_reconciliation_settings_changes FROM svc_edged;
REVOKE SELECT ON iam_v2.pms_room_inventory FROM svc_edged;
REVOKE SELECT ON iam_v2.pms_room_inventory_changes FROM svc_edged;
REVOKE SELECT ON iam_v2.pms_resync_coverage FROM svc_edged;
REVOKE SELECT ON iam_v2.voucher_revocations FROM svc_edged;
REVOKE SELECT ON iam_v2.verification_access_grants FROM svc_edged;
REVOKE SELECT ON iam_v2.payment_account_conversion_changes FROM svc_edged;
REVOKE SELECT ON iam_v2.pms_review_expiry_settings FROM svc_edged;
REVOKE SELECT ON iam_v2.pms_review_expiry_settings_changes FROM svc_edged;
REVOKE SELECT ON iam_v2.device_limit_disconnections FROM svc_edged;
REVOKE EXECUTE ON FUNCTION iam_v2.pms_known_room_inventory(uuid, uuid, uuid, bigint, integer) FROM svc_edged;
REVOKE EXECUTE ON FUNCTION iam_v2.pms_rebaseline_room_inventory(uuid, uuid, uuid, bigint, text, text) FROM svc_edged;
REVOKE EXECUTE ON FUNCTION iam_v2.pms_accept_startup_data_gap(uuid, uuid, uuid, text, text) FROM svc_edged;
REVOKE EXECUTE ON FUNCTION iam_v2.site_module_get(uuid, uuid) FROM svc_edged;

-- svc_pmsd: column-level replacements for a lock and for the three session columns the conversion sets
REVOKE UPDATE ON iam_v2.entitlements FROM svc_pmsd;
REVOKE UPDATE ON iam_v2.sessions FROM svc_pmsd;
GRANT UPDATE (status) ON iam_v2.entitlements TO svc_pmsd;
GRANT UPDATE (state, ended, end_reason) ON iam_v2.sessions TO svc_pmsd;
REVOKE SELECT ON iam_v2.pms_resync_coverage FROM svc_pmsd;
REVOKE SELECT ON iam_v2.pms_room_inventory FROM svc_pmsd;
REVOKE EXECUTE ON FUNCTION iam_v2.pms_known_room_inventory(uuid, uuid, uuid, bigint, integer) FROM svc_pmsd;

-- The 0096 nine, from every role whose binary does not call them (see svc-payment-least-privilege-grants.sql).
REVOKE EXECUTE ON FUNCTION iam_v2.p5_controlled_operation_open(text) FROM svc_scd, svc_edged, svc_acctd, svc_netd, svc_pmsd;
REVOKE EXECUTE ON FUNCTION iam_v2.pms_connection_settings_get(uuid,uuid,uuid) FROM svc_scd, svc_acctd, svc_netd;
REVOKE EXECUTE ON FUNCTION iam_v2.pms_connection_settings_set(uuid,uuid,uuid,text,text,integer,integer,integer,integer,integer) FROM svc_scd, svc_acctd, svc_netd, svc_pmsd;
REVOKE EXECUTE ON FUNCTION iam_v2.pms_dispose_snapshot_cases(uuid,uuid,uuid,text,text) FROM svc_scd, svc_edged, svc_acctd, svc_netd, svc_pmsd;
REVOKE EXECUTE ON FUNCTION iam_v2.pms_reconciliation_settings_get(uuid,uuid) FROM svc_scd, svc_acctd, svc_netd, svc_pmsd;
REVOKE EXECUTE ON FUNCTION iam_v2.pms_reconciliation_settings_set(uuid,uuid,integer,integer,text,text,integer,integer) FROM svc_scd, svc_edged, svc_acctd, svc_netd, svc_pmsd;
REVOKE EXECUTE ON FUNCTION iam_v2.pms_record_resync_coverage(uuid,uuid,uuid,bigint,text[],integer,integer,integer) FROM svc_scd, svc_edged, svc_acctd, svc_netd;
REVOKE EXECUTE ON FUNCTION iam_v2.pms_roster_of_generation(uuid,uuid,uuid,bigint) FROM svc_scd, svc_edged, svc_acctd, svc_netd, svc_pmsd;
REVOKE EXECUTE ON FUNCTION iam_v2.pms_site_blocked_after_refusals(uuid,uuid) FROM svc_scd, svc_edged, svc_acctd, svc_netd, svc_pmsd;

-- ---------------------------------------------------------------------------------------------------------
-- 4. The shape, asserted (the same assertion closes every Gate-P reconcile)
-- ---------------------------------------------------------------------------------------------------------
DO $assert$
DECLARE bad text;
BEGIN
  -- The entitlement writer belongs to the paths that move entitlement state: admission (scd) and the checkout
  -- converter (pmsd). Never the enforcement owner, never the accounting daemon, never the console.
  SELECT string_agg(r, ', ') INTO bad FROM unnest(ARRAY['svc_netd','svc_acctd','svc_edged']) r
   WHERE EXISTS (SELECT 1 FROM pg_roles WHERE rolname = r)
     AND has_function_privilege(r, 'iam_v2.apply_entitlement_transition(uuid,text,timestamptz,text)', 'EXECUTE');
  IF bad IS NOT NULL THEN RAISE EXCEPTION '0124: % can execute apply_entitlement_transition', bad; END IF;

  -- netd and acctd hold no table write anywhere in iam_v2 (0039, restored).
  SELECT string_agg(format('%s %s %s', r.rolname, p.priv, c.relname), ', ') INTO bad
    FROM pg_roles r CROSS JOIN pg_class c CROSS JOIN unnest(ARRAY['INSERT','UPDATE','DELETE']) p(priv)
   WHERE r.rolname IN ('svc_netd','svc_acctd') AND c.relnamespace = 'iam_v2'::regnamespace AND c.relkind IN ('r','p')
     AND (has_table_privilege(r.rolname, c.oid, p.priv)
          OR (p.priv <> 'DELETE' AND has_any_column_privilege(r.rolname, c.oid, p.priv)));
  IF bad IS NOT NULL THEN RAISE EXCEPTION '0124: write authority in iam_v2 for %', bad; END IF;

  -- acctd executes none of the device-authorization or boundary writers (0039).
  SELECT string_agg(p.oid::regprocedure::text, ', ') INTO bad FROM pg_proc p
   WHERE p.pronamespace = 'iam_v2'::regnamespace
     AND p.proname IN ('authorize_entitlement_device','deauthorize_entitlement_device','p6_record_time_termination',
                       'p6_guest_release_device','p6_set_guest_device_self_service','terminate_entitlement_at_boundary',
                       'rebind_session_entitlement')
     AND has_function_privilege('svc_acctd', p.oid, 'EXECUTE');
  IF bad IS NOT NULL THEN RAISE EXCEPTION '0124: svc_acctd can execute %', bad; END IF;

  -- No runtime role holds table-level UPDATE on entitlements or sessions; column-level only, and only these.
  SELECT string_agg(format('%s %s', r.rolname, t), ', ') INTO bad
    FROM pg_roles r CROSS JOIN unnest(ARRAY['iam_v2.entitlements','iam_v2.sessions']) t
   WHERE r.rolname LIKE 'svc\_%' AND has_table_privilege(r.rolname, t, 'UPDATE');
  IF bad IS NOT NULL THEN RAISE EXCEPTION '0124: table-level UPDATE for %', bad; END IF;
  SELECT string_agg(format('%s %s.%s', r.rolname, c.relname, a.attname), ', ') INTO bad
    FROM pg_roles r CROSS JOIN pg_class c JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped
   WHERE r.rolname LIKE 'svc\_%' AND c.oid IN ('iam_v2.entitlements'::regclass, 'iam_v2.sessions'::regclass)
     AND has_column_privilege(r.rolname, c.oid, a.attnum, 'UPDATE')
     AND (r.rolname, c.relname, a.attname::text) NOT IN (
           ('svc_scd','entitlements','status'), ('svc_pmsd','entitlements','status'),
           ('svc_pmsd','sessions','state'), ('svc_pmsd','sessions','ended'), ('svc_pmsd','sessions','end_reason'),
           ('svc_edged','sessions','entitlement_id'));
  IF bad IS NOT NULL THEN RAISE EXCEPTION '0124: column UPDATE beyond purpose: %', bad; END IF;

  -- The adjustment and scratch ingest writers are the owner's alone.
  SELECT string_agg(f, ', ') INTO bad
    FROM unnest(ARRAY['iam_v2.apply_adjustment(uuid,text,text,uuid,text)',
                      'iam_v2.ingest_sample(uuid,bigint,bigint,bigint,integer)']) f
   WHERE has_function_privilege('public', f, 'EXECUTE')
      OR EXISTS (SELECT 1 FROM pg_roles r WHERE r.rolname LIKE 'svc\_%' AND has_function_privilege(r.rolname, f, 'EXECUTE'));
  IF bad IS NOT NULL THEN RAISE EXCEPTION '0124: % is executable by PUBLIC or a runtime role', bad; END IF;

  -- The PMS settings setters belong to the console alone.
  SELECT string_agg(format('%s %s', r.rolname, f), ', ') INTO bad
    FROM pg_roles r CROSS JOIN unnest(ARRAY[
           'iam_v2.pms_connection_settings_set(uuid,uuid,uuid,text,text,integer,integer,integer,integer,integer)',
           'iam_v2.pms_reconciliation_settings_set(uuid,uuid,integer,integer,text,text,integer,integer)',
           'iam_v2.pms_dispose_snapshot_cases(uuid,uuid,uuid,text,text)']) f
   WHERE r.rolname LIKE 'svc\_%' AND r.rolname <> 'svc_edged' AND has_function_privilege(r.rolname, f, 'EXECUTE');
  IF bad IS NOT NULL THEN RAISE EXCEPTION '0124: PMS settings writer reachable by %', bad; END IF;
END $assert$;

COMMIT;
