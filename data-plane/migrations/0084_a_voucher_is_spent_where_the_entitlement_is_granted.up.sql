-- A VOUCHER IS SPENT WHERE THE ENTITLEMENT IS GRANTED.
--
-- Single-use redemption is the whole commercial meaning of a voucher, and on this appliance it cannot
-- happen. The burn is written in Go, inside the commerce transaction, as a direct statement:
--
--     UPDATE iam_v2.vouchers v SET state = 'REDEEMED'
--       FROM iam_v2.entitlements e
--      WHERE e.id = $1 AND v.id = e.voucher_id AND ... AND v.state = 'UNUSED'
--
-- and the role that runs it holds no UPDATE on that table. Read off PRE-LIVE 172.21.60.25:
--
--     has_table_privilege('svc_scd','iam_v2.vouchers','SELECT') = t
--     has_table_privilege('svc_scd','iam_v2.vouchers','INSERT') = t
--     has_table_privilege('svc_scd','iam_v2.vouchers','UPDATE') = f
--
-- So the statement raises "permission denied for table vouchers", and because it sits in the SAME
-- transaction as the grant, the whole grant rolls back. The consequence is not a voucher that redeems
-- twice. It is a voucher that cannot redeem ONCE: authentication succeeds, the quote is confirmed, and the
-- entitlement the guest just bought disappears on commit. No guest has met this because the Phase-2 portal
-- commerce surface is dark and nothing reaches the grant -- which is precisely why a defect of this size
-- survived: the code path that fails is the code path nothing runs.
--
-- WHY THE FIX IS NOT A GRANT
-- --------------------------
-- The obvious repair is GRANT UPDATE ON iam_v2.vouchers TO svc_scd, and it is the wrong one. The grant
-- files decline that privilege on purpose and say why -- svc-scd-iamv2-guest-auth-grants.sql:225-228:
--
--     * UPDATE on vouchers or guest_access_accounts from THIS file. Redemption and lockout accounting are
--       writes the accepted domain performs through its own guarded paths;
--
-- and svc-voucher-iamv2-grants.sql:26 declines DELETE for the same reason. A service role that can UPDATE
-- the voucher table can set any voucher to any state from anywhere in the process; the burn needs exactly
-- one transition, in exactly one place, and only as part of a grant that is otherwise valid.
--
-- iam_v2.p4_entitlement_grant_kernel is that place. It is SECURITY DEFINER, owned by iam_v2_owner, which
-- already holds UPDATE on iam_v2.vouchers; it is the single kernel BOTH grant entry points funnel through
-- (p4_grant_quoted_entitlement for a free quote, p4_grant_paid_entitlement for a settled one), so a burn
-- placed here covers the paid path too, which the Go statement never did; it already receives the voucher
-- id as p_voucher; and it already holds the subject advisory lock, so the burn is serialized against every
-- concurrent grant for the same voucher without adding a second locking scheme.
--
-- The result is that svc_scd needs NO new privilege. This migration LOWERS the privilege the product
-- requires rather than raising it, and it asserts at the end that svc_scd still cannot UPDATE the table, so
-- a later delivery cannot quietly reintroduce the grant this one made unnecessary.
--
-- WHY IT REFUSES RATHER THAN SKIPS
-- --------------------------------
-- WHERE state = 'UNUSED' on its own is a silent no-op when the voucher is already spent, which would let a
-- second grant succeed against a REDEEMED voucher -- the original defect wearing a different hat. So the
-- burn checks that it actually burned something and raises VOUCHER_NOT_REDEEMABLE if it did not.
--
-- That cannot fire on a legitimate retry. The kernel returns early, above this point, whenever the purchase
-- already has an entitlement (already_granted), so anything reaching the burn is a FRESH grant. A fresh
-- grant against a voucher that is not UNUSED is exactly the case single-use exists to refuse. Authentication
-- already refuses it one layer earlier -- voucherRedeemable admits only UNUSED inside its redemption
-- window -- and this closes the window between that check and the grant.
--
-- WHAT IS NOT CHANGED
-- -------------------
--   * No table, column, type, index, constraint or trigger. The only object redefined is one function, and
--     its signature, return type, volatility, security mode and search_path are identical.
--   * No grant is added to any role. No grant is removed.
--   * The account and post-stay-principal subjects are untouched: the burn is guarded by
--     p_voucher IS NOT NULL and those credentials are legitimately reusable.
--   * Entitlement supersession, purchase state, the opening transition and the advisory lock are all
--     byte-identical to 0024. This migration adds one guarded block and nothing else.
--
-- THE GO SIDE. data-plane/internal/iamv2/commerce_repo_pg.go drops its own UPDATE in the same delivery. It
-- has to: left in place it would run second, find the voucher already REDEEMED, match no row, and still
-- raise permission denied on a table it may not write.

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

-- THE BOUNDARY THIS MIGRATION EXISTS TO KEEP. If a later delivery grants svc_scd UPDATE on the voucher
-- table, the burn stops needing the kernel and the guarded path becomes optional again. Assert it here, so
-- the assertion is part of the schema's meaning rather than a note in a document.
DO $a$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_scd') THEN
    IF has_table_privilege('svc_scd', 'iam_v2.vouchers', 'UPDATE') THEN
      RAISE EXCEPTION 'svc_scd holds UPDATE on iam_v2.vouchers; the single-use burn is performed by the '
                      'SECURITY DEFINER grant kernel and the service role must not be able to write '
                      'voucher state directly';
    END IF;
  END IF;
END;
$a$;

COMMIT;
