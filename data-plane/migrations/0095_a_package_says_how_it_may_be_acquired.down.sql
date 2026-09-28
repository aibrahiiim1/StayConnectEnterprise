-- Roll back 0095: the acquisition contract, voucher issuance gate and batch revocation, the anonymous access
-- subject, the settlement birth rule and the v2 grant kernel. The previous kernel (0088), grant entry points
-- (0024) and voucher_revoke (0087) are restored verbatim. Refuses while any anonymous-subject entitlement or
-- auth context exists, because dropping the column would orphan access that was really granted.

BEGIN;

DO $guard$
BEGIN
  IF EXISTS (SELECT 1 FROM iam_v2.entitlements WHERE anonymous_subject_id IS NOT NULL)
     OR EXISTS (SELECT 1 FROM iam_v2.auth_contexts WHERE anonymous_subject_id IS NOT NULL) THEN
    RAISE EXCEPTION '0095 down refused: anonymous-subject access exists';
  END IF;
  IF EXISTS (SELECT 1 FROM iam_v2.settlements WHERE method = 'PREPAID') THEN
    RAISE EXCEPTION '0095 down refused: PREPAID settlements exist';
  END IF;
END $guard$;

CREATE OR REPLACE FUNCTION iam_v2.p4_entitlement_grant_kernel(
  p_tenant uuid, p_site uuid, p_purchase uuid,
  p_voucher uuid, p_account uuid, p_principal uuid,
  p_snapshot jsonb, p_plan_rev uuid, p_pkg_rev uuid)
RETURNS TABLE (entitlement_id uuid, already_granted boolean, superseded uuid)
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE
  v_subject_key text; v_existing uuid; v_superseded uuid; v_new uuid;
  v_time_mode text; v_end_mode text; v_window timestamptz; v_state text;
  v_burned int; v_voucher_pkg uuid;
BEGIN
  IF p_voucher IS NULL AND p_account IS NULL AND p_principal IS NULL THEN
    RAISE EXCEPTION 'GRANT_SUBJECT_UNRESOLVED: an entitlement always belongs to exactly one subject'
      USING ERRCODE = 'check_violation';
  END IF;
  IF p_snapshot IS NULL OR p_snapshot->>'service_plan_revision_id' IS NULL THEN
    RAISE EXCEPTION 'GRANT_SNAPSHOT_UNREADABLE' USING ERRCODE = 'check_violation';
  END IF;

  -- A VOUCHER GRANTS WHAT IT WAS PRINTED FOR, AND NOTHING ELSE.
  --
  -- iam_v2.vouchers.package_revision_id is NOT NULL and has been pinned at issuance since mg3, and the
  -- issuance path's own comment states why: "what a voucher grants is fixed at issuance by an immutable
  -- revision, so republishing a package later cannot retroactively change what an already-printed card is
  -- worth". NOTHING ENFORCED IT. The auth context carries voucher_id and no package pin, so the offer and
  -- quote path was free to select any package the subject was eligible for -- and at a property with two
  -- voucher-eligible free tiers, a card printed for the lower one could be redeemed against the higher.
  --
  -- Checked HERE because this is the one kernel both grant entry points funnel through, it already receives
  -- both the voucher and the package revision, and it runs as its owner -- so no caller can route around
  -- it. Checked BEFORE the subject lock and before any write, so a grant that will be refused does not
  -- first terminate the guest's previous entitlement.
  --
  -- The offer path narrows to the pinned revision as well, so an operator never sees a choice this refuses;
  -- that is the product behaviour, and this is the invariant. A caller that disagrees with the card gets an
  -- error rather than a better package.
  IF p_voucher IS NOT NULL THEN
    IF p_pkg_rev IS NULL THEN
      RAISE EXCEPTION 'VOUCHER_PACKAGE_UNPINNED: a voucher grant must name the package revision it grants'
        USING ERRCODE = 'check_violation';
    END IF;
    SELECT package_revision_id INTO v_voucher_pkg FROM iam_v2.vouchers
     WHERE tenant_id = p_tenant AND site_id = p_site AND id = p_voucher;
    IF v_voucher_pkg IS NULL THEN
      RAISE EXCEPTION 'VOUCHER_NOT_FOUND: voucher % does not belong to this owner', p_voucher
        USING ERRCODE = 'check_violation';
    END IF;
    IF v_voucher_pkg <> p_pkg_rev THEN
      RAISE EXCEPTION 'VOUCHER_PACKAGE_MISMATCH: voucher % was printed against package revision %, and this '
                      'grant is for %; a card grants what it was printed for', p_voucher, v_voucher_pkg, p_pkg_rev
        USING ERRCODE = 'check_violation';
    END IF;
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

CREATE OR REPLACE FUNCTION iam_v2.p4_grant_quoted_entitlement(
  p_tenant uuid, p_site uuid, p_purchase uuid)
RETURNS TABLE (entitlement_id uuid, already_granted boolean, superseded uuid)
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE pu record; se record; q record; ac record;
BEGIN
  SELECT * INTO pu FROM iam_v2.purchases
   WHERE tenant_id = p_tenant AND site_id = p_site AND id = p_purchase FOR UPDATE;
  IF pu.id IS NULL THEN RAISE EXCEPTION 'GRANT_PURCHASE_UNKNOWN' USING ERRCODE = 'no_data_found'; END IF;

  SELECT * INTO se FROM iam_v2.settlements
   WHERE tenant_id = p_tenant AND site_id = p_site AND purchase_id = pu.id;
  IF se.id IS NULL OR se.method <> 'NOT_REQUIRED' OR se.status <> 'NOT_REQUIRED' THEN
    RAISE EXCEPTION 'GRANT_NOT_FREE: this purchase requires settlement (% / %); a free grant is not the '
                    'right authorization for it', coalesce(se.method,'none'), coalesce(se.status,'none')
      USING ERRCODE = 'check_violation';
  END IF;

  SELECT * INTO q  FROM iam_v2.offer_quotes
   WHERE tenant_id = p_tenant AND site_id = p_site AND id = pu.offer_quote_id;
  SELECT * INTO ac FROM iam_v2.auth_contexts
   WHERE tenant_id = p_tenant AND site_id = p_site AND id = pu.auth_context_id;
  IF q.id IS NULL OR ac.id IS NULL THEN
    RAISE EXCEPTION 'GRANT_EVIDENCE_MISSING: the purchase has no pinned quote or auth context'
      USING ERRCODE = 'no_data_found';
  END IF;
  IF coalesce(q.price_minor, 0) <> 0 THEN
    RAISE EXCEPTION 'GRANT_NOT_FREE: the pinned quote is priced at %; money has to arrive first',
      q.price_minor USING ERRCODE = 'check_violation';
  END IF;

  RETURN QUERY SELECT * FROM iam_v2.p4_entitlement_grant_kernel(
    p_tenant, p_site, pu.id, ac.voucher_id, ac.guest_account_id, ac.guest_principal_id,
    q.grant_snapshot, (q.grant_snapshot->>'service_plan_revision_id')::uuid, pu.package_revision_id);
END $fn$;

CREATE OR REPLACE FUNCTION iam_v2.p4_grant_paid_entitlement(
  p_tenant uuid, p_site uuid, p_settlement uuid)
RETURNS TABLE (entitlement_id uuid, already_granted boolean, superseded uuid)
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE se record; pu record; q record; ac record;
BEGIN
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
  IF pu.id IS NULL THEN RAISE EXCEPTION 'GRANT_PURCHASE_UNKNOWN' USING ERRCODE = 'no_data_found'; END IF;
  IF EXISTS (SELECT 1 FROM iam_v2.entitlements WHERE purchase_id = pu.id) THEN
    RETURN QUERY SELECT e.id, true, NULL::uuid FROM iam_v2.entitlements e WHERE e.purchase_id = pu.id LIMIT 1;
    RETURN;
  END IF;
  IF pu.state <> 'AWAITING_SETTLEMENT' THEN
    RAISE EXCEPTION 'GRANT_PURCHASE_STATE: a paid grant requires the purchase to be AWAITING_SETTLEMENT, '
                    'not %', pu.state USING ERRCODE = 'check_violation';
  END IF;

  SELECT * INTO q  FROM iam_v2.offer_quotes
   WHERE tenant_id = p_tenant AND site_id = p_site AND id = pu.offer_quote_id;
  SELECT * INTO ac FROM iam_v2.auth_contexts
   WHERE tenant_id = p_tenant AND site_id = p_site AND id = pu.auth_context_id;
  IF q.id IS NULL OR ac.id IS NULL THEN
    RAISE EXCEPTION 'GRANT_EVIDENCE_MISSING: the purchase has no pinned quote or auth context'
      USING ERRCODE = 'no_data_found';
  END IF;

  RETURN QUERY SELECT * FROM iam_v2.p4_entitlement_grant_kernel(
    p_tenant, p_site, pu.id, ac.voucher_id, ac.guest_account_id, ac.guest_principal_id,
    q.grant_snapshot, (q.grant_snapshot->>'service_plan_revision_id')::uuid, pu.package_revision_id);
END $fn$;

CREATE OR REPLACE FUNCTION iam_v2.voucher_revoke(
    p_tenant uuid, p_site uuid, p_voucher uuid, p_operator uuid, p_reason text)
  RETURNS void
LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, public, pg_temp AS $$
DECLARE v_n integer;
BEGIN
  IF p_operator IS NULL THEN
    RAISE EXCEPTION 'VOUCHER_REVOKE_NEEDS_AN_OPERATOR' USING ERRCODE = 'check_violation';
  END IF;
  IF length(btrim(coalesce(p_reason, ''))) < 4 THEN
    RAISE EXCEPTION 'VOUCHER_REVOKE_NEEDS_A_REASON' USING ERRCODE = 'check_violation';
  END IF;
  -- UNUSED only. A REDEEMED voucher has already granted an entitlement, and revoking the card would not
  -- take that entitlement back -- so answering "revoked" would be a false statement about access. The
  -- entitlement is ended through the session/entitlement surface, which is a different action.
  UPDATE iam_v2.vouchers
     SET state = 'REVOKED'
   WHERE tenant_id = p_tenant AND site_id = p_site AND id = p_voucher AND state = 'UNUSED';
  GET DIAGNOSTICS v_n = ROW_COUNT;
  -- Not a silent no-op: the same reasoning as 0084's burn. "Nothing happened" and "it was already spent"
  -- are different answers and the caller must be able to tell them apart.
  IF v_n <> 1 THEN
    RAISE EXCEPTION 'VOUCHER_NOT_REVOCABLE: no UNUSED voucher % for tenant %/site %', p_voucher, p_tenant, p_site
      USING ERRCODE = 'check_violation';
  END IF;
END $$;

DROP FUNCTION IF EXISTS iam_v2.p4_grant_voucher_entitlement(uuid,uuid,uuid);
DROP FUNCTION IF EXISTS iam_v2.p4_entitlement_grant_kernel_v2(uuid,uuid,uuid,uuid,uuid,uuid,uuid,uuid,uuid,jsonb,uuid,uuid);

DROP INDEX IF EXISTS iam_v2.ent_live_anonymous;
ALTER TABLE iam_v2.entitlements DROP CONSTRAINT IF EXISTS ent_one_subject;
ALTER TABLE iam_v2.entitlements ADD CONSTRAINT ent_one_subject
  CHECK (num_nonnulls(stay_id, guest_account_id, voucher_id, guest_principal_id) = 1);
ALTER TABLE iam_v2.auth_contexts DROP CONSTRAINT IF EXISTS ac_method_subject;
ALTER TABLE iam_v2.auth_contexts ADD CONSTRAINT ac_method_subject CHECK (
      (method = 'PMS'             AND stay_id IS NOT NULL)
   OR (method = 'VOUCHER'         AND voucher_id IS NOT NULL)
   OR (method = 'ACCOUNT'         AND guest_account_id IS NOT NULL)
   OR (method IN ('OTP','SOCIAL') AND guest_principal_id IS NOT NULL)
   OR (method = 'POST_STAY_PIN'   AND post_stay_profile_id IS NOT NULL));
ALTER TABLE iam_v2.auth_contexts DROP CONSTRAINT IF EXISTS ac_one_subject;
ALTER TABLE iam_v2.auth_contexts ADD CONSTRAINT ac_one_subject
  CHECK (num_nonnulls(stay_id, guest_account_id, voucher_id, guest_principal_id, post_stay_profile_id) = 1);
ALTER TABLE iam_v2.auth_contexts DROP CONSTRAINT IF EXISTS auth_contexts_method_check;
ALTER TABLE iam_v2.auth_contexts ADD CONSTRAINT auth_contexts_method_check
  CHECK (method IN ('PMS','VOUCHER','ACCOUNT','OTP','SOCIAL','POST_STAY_PIN'));
ALTER TABLE iam_v2.auth_contexts DROP CONSTRAINT IF EXISTS ac_anonymous_subject_fk;
ALTER TABLE iam_v2.entitlements DROP CONSTRAINT IF EXISTS ent_anonymous_subject_fk;
ALTER TABLE iam_v2.auth_contexts DROP COLUMN IF EXISTS anonymous_subject_id;
ALTER TABLE iam_v2.entitlements DROP COLUMN IF EXISTS anonymous_subject_id;
DROP TABLE IF EXISTS iam_v2.anonymous_subject_credentials;
DROP TABLE IF EXISTS iam_v2.anonymous_access_subjects;

DROP FUNCTION IF EXISTS iam_v2.voucher_batch_revoke(uuid,uuid,uuid,uuid,text);
DROP TRIGGER IF EXISTS voucher_revocations_append_only ON iam_v2.voucher_revocations;
DROP TABLE IF EXISTS iam_v2.voucher_revocations;
DROP FUNCTION IF EXISTS iam_v2.voucher_revocations_append_only();
DROP TRIGGER IF EXISTS voucher_issuance_gate ON iam_v2.vouchers;
DROP FUNCTION IF EXISTS iam_v2.voucher_issuance_gate();
DROP TRIGGER IF EXISTS p4_settlement_birth ON iam_v2.settlements;
DROP FUNCTION IF EXISTS iam_v2.p4_settlement_birth();
ALTER TABLE iam_v2.internet_package_revisions DROP CONSTRAINT IF EXISTS ipr_acquisition_methods;

DO $own$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
    EXECUTE 'ALTER FUNCTION iam_v2.p4_entitlement_grant_kernel(uuid,uuid,uuid,uuid,uuid,uuid,jsonb,uuid,uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.p4_grant_quoted_entitlement(uuid,uuid,uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.p4_grant_paid_entitlement(uuid,uuid,uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.voucher_revoke(uuid,uuid,uuid,uuid,text) OWNER TO iam_v2_owner';
  END IF;
END $own$;

COMMIT;
