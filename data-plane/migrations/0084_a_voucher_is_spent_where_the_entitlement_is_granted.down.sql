-- Returning the entitlement grant kernel to its 0024 definition, without the voucher burn.
--
-- WHAT THIS COSTS, so a rollback is an informed choice rather than a surprise. Voucher single-use stops
-- being enforced anywhere that works. The Go statement in commerce_repo_pg.go is removed by the same
-- delivery and a rollback of the schema does not put it back -- and if it were put back it would raise
-- permission denied, because svc_scd holds no UPDATE on iam_v2.vouchers and this migration did not grant
-- it. So after this down-migration a voucher that has been redeemed stays UNUSED, and the same printed code
-- can be redeemed again, each time producing a fresh auth context and a fresh entitlement.
--
-- That is the state the appliance was in before 0084. It is survivable only because the Phase-2 portal
-- commerce surface is dark: nothing reaches the grant, so nothing redeems, so nothing double-redeems. DO
-- NOT run this down-migration and then enable the commerce surface.
--
-- WHAT IT DOES NOT COST. No data is written, moved or lost. No voucher already burned is un-burned: a
-- REDEEMED voucher stays REDEEMED, which is correct -- it was genuinely spent. No grant is added or
-- removed, no other function is touched, and the boundary assertion simply stops being made.

BEGIN;

CREATE OR REPLACE FUNCTION iam_v2.p4_entitlement_grant_kernel(
  p_tenant uuid, p_site uuid, p_purchase uuid,
  p_voucher uuid, p_account uuid, p_principal uuid,
  p_snapshot jsonb, p_plan_rev uuid, p_pkg_rev uuid)
RETURNS TABLE (entitlement_id uuid, already_granted boolean, superseded uuid)
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE
  v_subject_key text; v_existing uuid; v_superseded uuid; v_new uuid;
  v_time_mode text; v_end_mode text; v_window timestamptz; v_state text;
BEGIN
  IF p_voucher IS NULL AND p_account IS NULL AND p_principal IS NULL THEN
    RAISE EXCEPTION 'GRANT_SUBJECT_UNRESOLVED: an entitlement always belongs to exactly one subject'
      USING ERRCODE = 'check_violation';
  END IF;
  IF p_snapshot IS NULL OR p_snapshot->>'service_plan_revision_id' IS NULL THEN
    RAISE EXCEPTION 'GRANT_SNAPSHOT_UNREADABLE' USING ERRCODE = 'check_violation';
  END IF;
  v_time_mode := coalesce(p_snapshot->>'time_accounting_mode', 'VALIDITY_WINDOW');
  v_end_mode  := coalesce(nullif(p_snapshot->>'end_mode',''), 'MANUAL_END');
  v_window    := CASE WHEN p_snapshot->>'window_ends_at' IS NOT NULL
                      THEN (p_snapshot->>'window_ends_at')::timestamptz ELSE NULL END;

  v_subject_key := 'phase2.subject|' || p_tenant::text || '|' || p_site::text || '|' ||
                   coalesce(p_voucher::text, p_account::text, p_principal::text);
  PERFORM pg_advisory_xact_lock(hashtext(v_subject_key));

  SELECT id INTO v_existing FROM iam_v2.entitlements WHERE purchase_id = p_purchase LIMIT 1;
  IF v_existing IS NOT NULL THEN
    entitlement_id := v_existing; already_granted := true; superseded := NULL; RETURN NEXT; RETURN;
  END IF;

  SELECT id INTO v_superseded FROM iam_v2.entitlements
   WHERE tenant_id = p_tenant AND site_id = p_site AND status IN ('PENDING','ACTIVE','SUSPENDED')
     AND ( (p_voucher   IS NOT NULL AND voucher_id         = p_voucher)
        OR (p_account   IS NOT NULL AND guest_account_id   = p_account)
        OR (p_principal IS NOT NULL AND guest_principal_id = p_principal) )
   ORDER BY activated_at DESC NULLS LAST, id LIMIT 1 FOR UPDATE;
  IF v_superseded IS NOT NULL THEN
    PERFORM iam_v2.apply_entitlement_transition(v_superseded, 'TERMINATED', now(), 'SUPERSEDED');
  END IF;

  INSERT INTO iam_v2.entitlements
    (tenant_id, site_id, voucher_id, guest_account_id, guest_principal_id, purchase_id,
     policy_snapshot, service_plan_revision_id, package_revision_id, time_accounting_mode,
     end_mode, window_ends_at, status, supersedes_entitlement_id, activated_at)
  VALUES (p_tenant, p_site, p_voucher, p_account, p_principal, p_purchase,
          p_snapshot, p_plan_rev, p_pkg_rev, v_time_mode, v_end_mode, v_window,
          'ACTIVE', v_superseded, now())
  RETURNING id INTO v_new;
  PERFORM iam_v2.apply_entitlement_transition(v_new, 'ACTIVE', now(), 'GRANTED');

  SELECT state INTO v_state FROM iam_v2.purchases WHERE id = p_purchase FOR UPDATE;
  IF v_state NOT IN ('PENDING','AWAITING_SETTLEMENT') THEN
    RAISE EXCEPTION 'PURCHASE_STATE_TRANSITION: % -> GRANTED is not an approved transition', v_state
      USING ERRCODE = 'check_violation';
  END IF;
  UPDATE iam_v2.purchases SET state = 'GRANTED' WHERE id = p_purchase;

  entitlement_id := v_new; already_granted := false; superseded := v_superseded; RETURN NEXT;
END $fn$;

REVOKE EXECUTE ON FUNCTION iam_v2.p4_entitlement_grant_kernel(
  uuid,uuid,uuid,uuid,uuid,uuid,jsonb,uuid,uuid) FROM PUBLIC;

COMMIT;
