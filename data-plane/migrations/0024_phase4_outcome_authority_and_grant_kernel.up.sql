-- 0024 — PHASE 4: independent provider-outcome authority, and ONE entitlement grant kernel.
-- D18 / T0029. Receipt: T0040. Additive, reversible, DARK.
--
-- TWO MEASURED FINDINGS FROM 0021.
--
-- (a) OUTCOME AUTHORITY. 0021 narrowed the outcome operation so it can only move a payment that is already
--     executing, and T0039 recorded honestly that a compromised runtime could still lie about a payment it
--     started. That residual is larger than it needs to be: the SAME database credential that creates
--     intents and crosses the execution boundary can also assert CAPTURED. One stolen DSN is therefore
--     sufficient to fabricate a financial outcome end to end -- start a payment, then declare it captured.
--
--     Provider authenticity is and remains an application concern: no database privilege can verify a
--     provider's signature. But the DATABASE can insist that asserting an outcome is a DIFFERENT authority
--     from executing a payment. That is what this migration does, and it is the narrowest boundary that
--     preserves the future authenticated-notification path: the notification handler holds the outcome
--     credential, the execution path does not, and neither alone is sufficient.
--
-- (b) THE GRANT KERNEL. 0021 moved the paid grant into p4_grant_paid_entitlement, which re-implemented the
--     supersede / insert / opening-transition / mark-granted sequence that the free Phase-2 path performs in
--     Go. Two implementations of the same semantics is exactly the drift the one-writer rule exists to
--     prevent: a change to supersession or to the opening transition now has to be made twice, and the
--     first symptom of missing one would be two guests with different entitlement shapes.
--
--     So the sequence becomes ONE kernel. Both entry points keep their own high-level authorization -- what
--     makes a grant legitimate differs completely between "the quote was free" and "the money arrived" --
--     and both then call the same kernel, which is the only thing in the schema that writes an entitlement.
BEGIN;

-- ============================================================================
-- (1) THE KERNEL
-- ============================================================================
-- Everything an entitlement grant IS: the subject lock, the already-granted check under that lock, the
-- supersession of the subject's live entitlement, the insert, its opening transition, and the purchase's
-- move to GRANTED -- in that order, because the order is the semantics.
--
-- It performs NO authorization. Deciding whether this purchase deserves an entitlement belongs to the
-- caller, because the answer is different for a free quote and for settled money, and a kernel that tried
-- to know both would end up with a flag that selects between two behaviours -- which is two
-- implementations wearing one name.
--
-- It is not granted to any runtime role. The two high-level operations below are the only callers.
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

-- ============================================================================
-- (2) The PAID entry point — authorization only, then the kernel
-- ============================================================================
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
REVOKE EXECUTE ON FUNCTION iam_v2.p4_grant_paid_entitlement(uuid,uuid,uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION iam_v2.p4_grant_paid_entitlement(uuid,uuid,uuid) TO sc_payment_runtime;

-- ============================================================================
-- (3) The FREE entry point — the same kernel, a different authorization
-- ============================================================================
-- What makes a free grant legitimate is that the QUOTE was free and the settlement therefore required
-- nothing. That is checked here and nowhere else, exactly as the paid path checks SETTLED and nowhere else.
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
REVOKE EXECUTE ON FUNCTION iam_v2.p4_grant_quoted_entitlement(uuid,uuid,uuid) FROM PUBLIC;

-- The Phase-2 portal runtime needs the FREE entry point and nothing else. It is a separate role from the
-- payment runtime because they are separate services with separate credentials; giving the portal the paid
-- operation would mean a compromised portal could grant against money it never took.
DO $$ BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='sc_commerce_runtime') THEN
    CREATE ROLE sc_commerce_runtime NOLOGIN;
  END IF;
END $$;
GRANT USAGE ON SCHEMA iam_v2 TO sc_commerce_runtime;
GRANT EXECUTE ON FUNCTION iam_v2.p4_grant_quoted_entitlement(uuid,uuid,uuid) TO sc_commerce_runtime;

-- ============================================================================
-- (4) INDEPENDENT OUTCOME AUTHORITY
-- ============================================================================
-- sc_payment_outcome is the only role that may assert what a provider said. The execution role keeps intent
-- creation, the durable execution boundary and the grant; it loses the outcome operation entirely.
--
-- WHAT THIS BUYS, precisely. A stolen execution credential can start payments and stop there: it cannot
-- declare any of them captured, so it cannot settle a settlement and cannot reach the grant. A stolen
-- outcome credential can assert an outcome, but only for a payment some OTHER credential already put into
-- flight -- it cannot create an intent, so it has nothing of its own to assert about.
--
-- WHAT IT DOES NOT BUY. It is not provider authentication. Verifying that a delivery really came from the
-- provider needs a signing secret the database never holds, and that check stays in the payment process.
-- This is defence in depth underneath it: two independent credentials instead of one.
DO $$ BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='sc_payment_outcome') THEN
    CREATE ROLE sc_payment_outcome NOLOGIN;
  END IF;
END $$;
GRANT USAGE ON SCHEMA iam_v2 TO sc_payment_outcome;
GRANT SELECT ON iam_v2.payment_transactions, iam_v2.settlements TO sc_payment_outcome;
GRANT EXECUTE ON FUNCTION
  iam_v2.p4_apply_provider_outcome(text,text,text,text,text,jsonb) TO sc_payment_outcome;

-- The execution role loses it. This is the whole point of the migration.
REVOKE EXECUTE ON FUNCTION
  iam_v2.p4_apply_provider_outcome(text,text,text,text,text,jsonb) FROM sc_payment_runtime;

-- ...and the outcome role gets nothing else. It cannot create an intent, begin an execution, or grant.
REVOKE EXECUTE ON FUNCTION iam_v2.begin_payment_execution(uuid) FROM sc_payment_outcome;
REVOKE EXECUTE ON FUNCTION iam_v2.p4_grant_paid_entitlement(uuid,uuid,uuid) FROM sc_payment_outcome;
REVOKE INSERT, UPDATE, DELETE ON iam_v2.payment_transactions FROM sc_payment_outcome;

INSERT INTO public.schema_migrations (version) VALUES ('0024_phase4_outcome_authority_and_grant_kernel')
  ON CONFLICT DO NOTHING;
COMMIT;
