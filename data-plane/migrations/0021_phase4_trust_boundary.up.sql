-- 0021 — PHASE 4: the restricted-role TRUST BOUNDARY. D18 / T0029. Receipt: T0039. Additive, reversible, DARK.
--
-- MEASURED, against the 0011..0020 chain: sc_payment_runtime holds no direct table DML, and T0038 reported
-- that honestly. It is not sufficient. The role also holds EXECUTE on the low-level SECURITY DEFINER
-- primitives, and a definer function is a privilege escalation by design -- so the grants that were supposed
-- to CONSTRAIN the runtime were handing it the owner's rights through a different door:
--
--   apply_payment_callback_v2(...)   asserts ANY status, including CAPTURED, for any correlated intent.
--                                    A compromised runtime could settle a settlement and grant access
--                                    without a provider ever being contacted.
--   p4_insert_entitlement(...)       creates an entitlement from 13 caller-supplied parameters: subject,
--                                    package revision, policy snapshot, window. Free guest access, on
--                                    demand, with fabricated evidence.
--   p4_terminate_live_entitlement... terminates any subject's live entitlement.
--   p4_mark_purchase_granted(...)    marks a purchase GRANTED with no reference to whether it was paid.
--
-- So the "no direct DML" property was true and almost meaningless: every outcome the DML would have
-- produced was reachable through a granted function, with fewer constraints.
--
-- THE FIX. The runtime gets HIGH-LEVEL operations that re-derive their own evidence from durable rows, and
-- loses EXECUTE on every low-level primitive. A high-level operation takes identifiers and nothing else:
-- there is no parameter through which a caller can substitute a subject, a package, a policy or an amount,
-- because the operation reads all of it itself, under lock, from rows the caller does not control.
--
-- WHAT THE DATABASE CANNOT DO, stated plainly. PostgreSQL privileges cannot prove that a provider actually
-- said CAPTURED. Cryptographic verification of a provider delivery happens in the application, against a
-- signing secret the database does not hold. So the trusted-computing boundary for provider authenticity is
-- the edged/payment process, NOT the database role -- and this migration does not pretend otherwise. What
-- it CAN do, and now does, is ensure that no unrelated runtime surface holds a generic "assert CAPTURED"
-- primitive: the only remaining path is one high-level operation that requires the intent to be genuinely
-- executing, so a compromised role's blast radius is bounded to payments it already started.
BEGIN;

-- ============================================================================
-- (1) The paid-grant operation, re-derived and locked
-- ============================================================================
-- p4_grant_paid_entitlement is the ONLY entitlement-creating operation the runtime may call. Compare its
-- signature with p4_insert_entitlement's thirteen parameters: this takes a tenant, a site and a settlement.
-- Everything else -- purchase, subject, package revision, service plan revision, policy snapshot, window --
-- is re-resolved here from the durable rows the settlement already points at, under FOR UPDATE, so there is
-- no parameter through which a caller could substitute anyone else's evidence.
--
-- It is NOT a second grant implementation. It performs the same three steps in the same order as the Go
-- Phase-2 writer -- supersede the subject's live entitlement, insert, mark the purchase granted -- because
-- that ordering IS the grant semantics. What changes is who may invoke it and with what: the Go writer
-- remains the caller for the free path, and the paid path now goes through here so that a restricted role
-- can complete it without holding the primitives.
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

-- ============================================================================
-- (2) The provider-outcome operation, narrowed
-- ============================================================================
-- p4_apply_provider_outcome replaces the runtime's access to apply_payment_callback_v2.
--
-- HONEST STATEMENT OF WHAT THIS DOES AND DOES NOT PROVE. It does not, and cannot, prove that a provider
-- said anything: the signature check that establishes that lives in the payment process, against a secret
-- the database never sees. The trusted-computing boundary for provider authenticity is therefore the
-- application, and this function is the narrowest DATABASE-side complement to it:
--
--   * it refuses unless the intent is genuinely PENDING -- i.e. some caller already crossed the durable
--     execution boundary for THIS payment -- so it cannot be used to conjure an outcome for an intent that
--     was never executed, nor for a settlement nobody has begun;
--   * it takes no tenant, site or merchant parameter, so it cannot be pointed at another property's money;
--   * it accepts only the three contractual outcomes, so it is not a general status-setting primitive.
--
-- A compromised runtime role can therefore still lie about the outcome of a payment IT STARTED. It cannot
-- settle an arbitrary settlement, grant an arbitrary entitlement, or act for another site. Bounding the
-- blast radius to payments already in flight is the honest achievable property; claiming the database
-- verifies provider authenticity would not be.
CREATE OR REPLACE FUNCTION iam_v2.p4_apply_provider_outcome(
  p_client_ref text, p_provider_event_id text, p_event_type text,
  p_outcome text, p_provider_txn_ref text, p_evidence jsonb)
RETURNS text
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE tx record;
BEGIN
  IF p_outcome NOT IN ('CAPTURED','FAILED','UNKNOWN') THEN
    RAISE EXCEPTION 'PAYMENT_OUTCOME_INVALID: % is not a provider outcome this operation may apply',
      p_outcome USING ERRCODE = 'check_violation';
  END IF;
  SELECT * INTO tx FROM iam_v2.payment_transactions WHERE provider_ref = p_client_ref;
  IF tx.id IS NULL THEN
    RAISE EXCEPTION 'CALLBACK_UNCORRELATED: no payment intent matches this client reference'
      USING ERRCODE = 'no_data_found';
  END IF;
  -- A provider retries its webhooks, so a delivery that merely REPEATS the outcome already recorded is an
  -- ordinary event and is reported as a duplicate rather than an error. Nothing is written: the intent is
  -- already where the delivery says it should be.
  IF tx.status = p_outcome THEN
    RETURN 'DUPLICATE';
  END IF;
  -- The narrowing that matters: an outcome may only be applied to a payment that is actually executing.
  -- A terminal intent is not moved by a late delivery claiming something different -- that is a conflict,
  -- not a retry, and it belongs in manual review rather than in an automatic status change.
  IF tx.status <> 'PENDING' THEN
    RAISE EXCEPTION 'PAYMENT_NOT_EXECUTING: the intent is %; an outcome may only be applied to a payment '
                    'that crossed the execution boundary', tx.status USING ERRCODE = 'check_violation';
  END IF;
  RETURN iam_v2.apply_payment_callback_v2(
    tx.tenant_id, tx.provider, tx.merchant_account_id, p_client_ref,
    p_provider_event_id, p_event_type, p_outcome, nullif(p_provider_txn_ref,''), p_evidence);
END $fn$;
REVOKE EXECUTE ON FUNCTION
  iam_v2.p4_apply_provider_outcome(text,text,text,text,text,jsonb) FROM PUBLIC;

-- ============================================================================
-- (3) Take the primitives away
-- ============================================================================
-- Everything below was reachable by sc_payment_runtime and is now not. The high-level operations above call
-- them as the OWNER, which is exactly the point of a definer boundary: the owner composes primitives, the
-- caller gets an operation.
REVOKE EXECUTE ON FUNCTION
  iam_v2.apply_payment_callback_v2(uuid,text,uuid,text,text,text,text,text,jsonb) FROM sc_payment_runtime;
REVOKE EXECUTE ON FUNCTION iam_v2.p4_insert_entitlement(
  uuid,uuid,uuid,uuid,uuid,uuid,jsonb,uuid,uuid,text,text,timestamptz,uuid) FROM sc_payment_runtime;
REVOKE EXECUTE ON FUNCTION
  iam_v2.p4_terminate_live_entitlement_for_subject(uuid,uuid,uuid,uuid,uuid) FROM sc_payment_runtime;
REVOKE EXECUTE ON FUNCTION iam_v2.p4_mark_purchase_granted(uuid) FROM sc_payment_runtime;
-- apply_entitlement_transition was never granted to the runtime; re-asserted so a future CREATE OR REPLACE
-- cannot quietly hand it over via the PUBLIC default.
REVOKE EXECUTE ON FUNCTION
  iam_v2.apply_entitlement_transition(uuid,text,timestamptz,text) FROM sc_payment_runtime;

GRANT EXECUTE ON FUNCTION iam_v2.p4_grant_paid_entitlement(uuid,uuid,uuid) TO sc_payment_runtime;
GRANT EXECUTE ON FUNCTION
  iam_v2.p4_apply_provider_outcome(text,text,text,text,text,jsonb) TO sc_payment_runtime;

-- ============================================================================
-- (4) Actor and scope authority
-- ============================================================================
-- MEASURED: p4_resolve_recovery_hold, p4_release_financial_recovery and p4_declare_financial_recovery all
-- take an actor uuid as a parameter. The API never lets a request supply one -- the operator surface takes
-- it from the session and the decoder rejects an unknown field -- but the DATABASE would accept any uuid
-- from any caller holding EXECUTE, so the audit trail's authorship rests entirely on the application.
--
-- The honest boundary: authorship is established by the API session, and the database's job is to make sure
-- the recorded author is a REAL operator of THIS tenant rather than an arbitrary uuid. That is checkable
-- here, and it turns a forged actor from "recorded as fact" into "refused".
CREATE OR REPLACE FUNCTION iam_v2.p4_assert_financial_actor(p_tenant uuid, p_actor uuid)
RETURNS void
  LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = iam_v2, public, pg_temp AS $fn$
DECLARE ok boolean;
BEGIN
  IF p_actor IS NULL THEN
    RAISE EXCEPTION 'FINANCIAL_ACTOR_REQUIRED: an audited financial decision has an author'
      USING ERRCODE = 'check_violation';
  END IF;
  SELECT EXISTS (SELECT 1 FROM public.operators o
                  WHERE o.id = p_actor AND o.tenant_id = p_tenant AND o.status = 'active')
    INTO ok;
  IF NOT ok THEN
    RAISE EXCEPTION 'FINANCIAL_ACTOR_UNKNOWN: the recorded author is not an active operator of this '
                    'tenant' USING ERRCODE = 'check_violation';
  END IF;
END $fn$;
REVOKE EXECUTE ON FUNCTION iam_v2.p4_assert_financial_actor(uuid,uuid) FROM PUBLIC;

INSERT INTO public.schema_migrations (version) VALUES ('0021_phase4_trust_boundary')
  ON CONFLICT DO NOTHING;
COMMIT;
