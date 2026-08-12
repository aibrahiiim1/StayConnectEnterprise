-- Reverse 0024. The 0021 paid-grant body is restored verbatim -- the version that re-implemented the
-- sequence rather than sharing a kernel -- because that is what reversing this migration means, even though
-- 0024 exists precisely because two implementations drift.
BEGIN;
REVOKE EXECUTE ON FUNCTION iam_v2.p4_grant_quoted_entitlement(uuid,uuid,uuid) FROM sc_commerce_runtime;
DROP FUNCTION IF EXISTS iam_v2.p4_grant_quoted_entitlement(uuid,uuid,uuid);

CREATE OR REPLACE FUNCTION iam_v2.p4_grant_paid_entitlement(
  p_tenant uuid, p_site uuid, p_settlement uuid)
RETURNS TABLE (entitlement_id uuid, already_granted boolean, superseded uuid)
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE
  se record; pu record; q record; ac record; snap jsonb;
  v_subject_key text; v_existing uuid; v_superseded uuid; v_new uuid;
  v_voucher uuid; v_account uuid; v_principal uuid;
  v_plan_rev uuid; v_window timestamptz; v_time_mode text; v_end_mode text;
BEGIN
  -- Ownership is checked as part of the lookup, not afterwards. A settlement that belongs to another
  -- tenant or another site of the same tenant simply does not resolve.
  SELECT * INTO se FROM iam_v2.settlements
   WHERE tenant_id = p_tenant AND site_id = p_site AND id = p_settlement FOR UPDATE;
  IF se.id IS NULL THEN
    RAISE EXCEPTION 'GRANT_SETTLEMENT_UNKNOWN: no such settlement in this tenant and site'
      USING ERRCODE = 'no_data_found';
  END IF;
  IF se.method <> 'ONLINE_PAYMENT' THEN
    RAISE EXCEPTION 'GRANT_WRONG_RAIL: settlement method is %; this operation grants only against an '
                    'online payment', se.method USING ERRCODE = 'check_violation';
  END IF;
  IF se.status <> 'SETTLED' THEN
    RAISE EXCEPTION 'GRANT_NOT_SETTLED: settlement is %; money is the authorization and nothing short of '
                    'SETTLED is money', se.status USING ERRCODE = 'check_violation';
  END IF;

  SELECT * INTO pu FROM iam_v2.purchases
   WHERE tenant_id = p_tenant AND site_id = p_site AND id = se.purchase_id FOR UPDATE;
  IF pu.id IS NULL THEN
    RAISE EXCEPTION 'GRANT_PURCHASE_UNKNOWN' USING ERRCODE = 'no_data_found';
  END IF;

  -- Already granted is a NO-OP, not an error, and it is checked under the locks above so two concurrent
  -- callers cannot both read "not granted".
  SELECT id INTO v_existing FROM iam_v2.entitlements WHERE purchase_id = pu.id LIMIT 1;
  IF v_existing IS NOT NULL THEN
    entitlement_id := v_existing; already_granted := true; superseded := NULL; RETURN NEXT; RETURN;
  END IF;
  IF pu.state <> 'AWAITING_SETTLEMENT' THEN
    RAISE EXCEPTION 'GRANT_PURCHASE_STATE: a paid grant requires the purchase to be AWAITING_SETTLEMENT, '
                    'not %', pu.state USING ERRCODE = 'check_violation';
  END IF;

  -- The pinned commercial evidence. The quote and the auth context are reached THROUGH the purchase, so a
  -- caller cannot point the grant at a different package or a different guest.
  SELECT * INTO q FROM iam_v2.offer_quotes
   WHERE tenant_id = p_tenant AND site_id = p_site AND id = pu.offer_quote_id;
  SELECT * INTO ac FROM iam_v2.auth_contexts
   WHERE tenant_id = p_tenant AND site_id = p_site AND id = pu.auth_context_id;
  IF q.id IS NULL OR ac.id IS NULL THEN
    RAISE EXCEPTION 'GRANT_EVIDENCE_MISSING: the purchase has no pinned quote or auth context'
      USING ERRCODE = 'no_data_found';
  END IF;
  snap := q.grant_snapshot;
  IF snap IS NULL OR snap->>'service_plan_revision_id' IS NULL THEN
    RAISE EXCEPTION 'GRANT_SNAPSHOT_UNREADABLE' USING ERRCODE = 'check_violation';
  END IF;
  v_plan_rev  := (snap->>'service_plan_revision_id')::uuid;
  v_time_mode := coalesce(snap->>'time_accounting_mode', 'VALIDITY_WINDOW');
  v_end_mode  := coalesce(nullif(snap->>'end_mode',''), 'MANUAL_END');
  v_window    := CASE WHEN snap->>'window_ends_at' IS NOT NULL
                      THEN (snap->>'window_ends_at')::timestamptz ELSE NULL END;

  v_voucher := ac.voucher_id; v_account := ac.guest_account_id; v_principal := ac.guest_principal_id;
  IF v_voucher IS NULL AND v_account IS NULL AND v_principal IS NULL THEN
    RAISE EXCEPTION 'GRANT_SUBJECT_UNRESOLVED: the auth context names no subject'
      USING ERRCODE = 'check_violation';
  END IF;

  -- The SUBJECT lock, taken with the same key shape the Go path uses, so the two entry points serialize
  -- against each other rather than each being individually safe and jointly racy.
  v_subject_key := 'phase2.subject|' || p_tenant::text || '|' || p_site::text || '|' ||
                   coalesce(v_voucher::text, v_account::text, v_principal::text);
  PERFORM pg_advisory_xact_lock(hashtext(v_subject_key));

  -- Re-read under the subject lock: another caller may have granted while we were waiting.
  SELECT id INTO v_existing FROM iam_v2.entitlements WHERE purchase_id = pu.id LIMIT 1;
  IF v_existing IS NOT NULL THEN
    entitlement_id := v_existing; already_granted := true; superseded := NULL; RETURN NEXT; RETURN;
  END IF;

  SELECT id INTO v_superseded FROM iam_v2.entitlements
   WHERE tenant_id = p_tenant AND site_id = p_site AND status IN ('PENDING','ACTIVE','SUSPENDED')
     AND ( (v_voucher   IS NOT NULL AND voucher_id         = v_voucher)
        OR (v_account   IS NOT NULL AND guest_account_id   = v_account)
        OR (v_principal IS NOT NULL AND guest_principal_id = v_principal) )
   ORDER BY activated_at DESC NULLS LAST, id LIMIT 1 FOR UPDATE;
  IF v_superseded IS NOT NULL THEN
    PERFORM iam_v2.apply_entitlement_transition(v_superseded, 'TERMINATED', now(), 'SUPERSEDED');
  END IF;

  INSERT INTO iam_v2.entitlements
    (tenant_id, site_id, voucher_id, guest_account_id, guest_principal_id, purchase_id,
     policy_snapshot, service_plan_revision_id, package_revision_id, time_accounting_mode,
     end_mode, window_ends_at, status, supersedes_entitlement_id, activated_at)
  VALUES (p_tenant, p_site, v_voucher, v_account, v_principal, pu.id,
          snap, v_plan_rev, pu.package_revision_id, v_time_mode,
          v_end_mode, v_window, 'ACTIVE', v_superseded, now())
  RETURNING id INTO v_new;
  PERFORM iam_v2.apply_entitlement_transition(v_new, 'ACTIVE', now(), 'GRANTED');
  UPDATE iam_v2.purchases SET state = 'GRANTED' WHERE id = pu.id;

  entitlement_id := v_new; already_granted := false; superseded := v_superseded; RETURN NEXT;
END $fn$;
REVOKE EXECUTE ON FUNCTION iam_v2.p4_grant_paid_entitlement(uuid,uuid,uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION iam_v2.p4_grant_paid_entitlement(uuid,uuid,uuid) TO sc_payment_runtime;
DROP FUNCTION IF EXISTS iam_v2.p4_entitlement_grant_kernel(uuid,uuid,uuid,uuid,uuid,uuid,jsonb,uuid,uuid);

-- Outcome authority goes back to the execution role, which is the 0021/0023 posture.
GRANT EXECUTE ON FUNCTION
  iam_v2.p4_apply_provider_outcome(text,text,text,text,text,jsonb) TO sc_payment_runtime;
DO $$
DECLARE r text;
BEGIN
  FOREACH r IN ARRAY ARRAY['sc_payment_outcome','sc_commerce_runtime'] LOOP
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = r) THEN
      EXECUTE format('REVOKE ALL ON ALL TABLES IN SCHEMA iam_v2 FROM %I', r);
      EXECUTE format('REVOKE ALL ON ALL FUNCTIONS IN SCHEMA iam_v2 FROM %I', r);
      EXECUTE format('REVOKE ALL ON SCHEMA iam_v2 FROM %I', r);
      EXECUTE format('DROP ROLE %I', r);
    END IF;
  END LOOP;
END $$;
DELETE FROM public.schema_migrations WHERE version = '0024_phase4_outcome_authority_and_grant_kernel';
COMMIT;
