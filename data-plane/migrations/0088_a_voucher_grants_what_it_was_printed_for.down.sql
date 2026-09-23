-- Removing the package pin from the voucher grant kernel.
--
-- WHAT THIS COSTS. A voucher stops being constrained to the package revision it was printed against, so a
-- card printed for one tier can again be redeemed against any package its subject is eligible for -- while
-- iam_v2.vouchers.package_revision_id still records the revision it was printed for and the operator screen
-- still says that is what it grants. The claim returns and the enforcement does not, which is the state
-- 0088 exists to end. Do not run this because the offer path narrowed: the narrowing is the behaviour, this
-- is the invariant underneath it.
--
-- WHAT IT DOES NOT COST. No entitlement, voucher or purchase is touched. 0084's single-use burn is restored
-- exactly as 0084 wrote it -- this direction replaces the kernel with that version, not with a version that
-- has neither guard.

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
  v_burned int;
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

  -- The SUBJECT lock. Taken before the already-granted check so two concurrent callers cannot both read
  -- "not granted" and both grant. The key shape is shared with the Go path deliberately: two entry points
  -- that lock differently are two entry points that do not serialize against each other.
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
  -- The row and its opening transition are inseparable: an ACTIVE entitlement whose status no transition
  -- backs cannot commit (Phase-3's deferred coherence constraint), and separating them is the T0037 defect.
  PERFORM iam_v2.apply_entitlement_transition(v_new, 'ACTIVE', now(), 'GRANTED');

  -- SINGLE-USE: the voucher is spent here, inside the grant, or the grant does not happen.
  --
  -- This runs as the function owner, so it needs no privilege from the caller. It is inside the subject
  -- advisory lock taken above, so two concurrent grants for one voucher cannot both burn it. And it is in
  -- the transaction that creates the entitlement, so a grant can never commit with the voucher still
  -- spendable -- which was the requirement the Go statement stated and could not keep.
  IF p_voucher IS NOT NULL THEN
    UPDATE iam_v2.vouchers
       SET state = 'REDEEMED'
     WHERE tenant_id = p_tenant AND site_id = p_site AND id = p_voucher
       AND state = 'UNUSED';
    GET DIAGNOSTICS v_burned = ROW_COUNT;
    IF v_burned <> 1 THEN
      -- Nothing was burned, and the early return above means this is not an idempotent retry. Either the
      -- voucher is already spent, revoked or expired, or it does not belong to this owner. A grant that
      -- cannot spend its own credential must not commit.
      RAISE EXCEPTION 'VOUCHER_NOT_REDEEMABLE: voucher % is not UNUSED for this owner; a grant cannot spend '
                      'a voucher that is already spent, revoked or expired', p_voucher
        USING ERRCODE = 'check_violation';
    END IF;
  END IF;

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

DO $own$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
    EXECUTE 'ALTER FUNCTION iam_v2.p4_entitlement_grant_kernel(uuid,uuid,uuid,uuid,uuid,uuid,jsonb,uuid,uuid) '
            'OWNER TO iam_v2_owner';
  END IF;
END $own$;

COMMIT;
