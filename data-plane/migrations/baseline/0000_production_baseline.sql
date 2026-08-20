-- FACTORY-CLEAN PRODUCTION BASELINE -- GENERATED, DO NOT HAND-EDIT.
--
-- Regenerate with: bash scripts/generate-production-baseline.sh
-- Verify with:    bash scripts/factory-clean-baseline-verify.sh
--
-- This is the CURRENT schema and only the current schema. A new Production appliance is built from
-- this file and never constructs the superseded guest-IAM tables, not even transiently. Existing
-- installations continue to upgrade through data-plane/migrations/0001..0049, which still create
-- those tables and then remove them, because that is what actually happened to them.
--
-- OWNERSHIP is deliberately absent: it belongs to Gate-P (deploy/gatep/gatep-iam-ownership.sql), and
-- a second source for it would be a competing security model. PRIVILEGES are present, because the
-- migrations REVOKE as well as create and those revocations are part of the schema's meaning.

-- Extensions. A --schema-restricted pg_dump omits these entirely.
CREATE EXTENSION IF NOT EXISTS "uuid-ossp" WITH SCHEMA public;
CREATE EXTENSION IF NOT EXISTS pgcrypto WITH SCHEMA public;
CREATE EXTENSION IF NOT EXISTS timescaledb WITH SCHEMA public;

-- Roles created by the migrations rather than by Gate-P. Idempotent, so a rebuild is safe.
DO $do$ BEGIN CREATE ROLE sc_commerce_runtime NOLOGIN; EXCEPTION WHEN duplicate_object THEN NULL; END $do$;
DO $do$ BEGIN CREATE ROLE sc_financial_operator NOLOGIN; EXCEPTION WHEN duplicate_object THEN NULL; END $do$;
DO $do$ BEGIN CREATE ROLE sc_financial_readonly NOLOGIN; EXCEPTION WHEN duplicate_object THEN NULL; END $do$;
DO $do$ BEGIN CREATE ROLE sc_payment_outcome NOLOGIN; EXCEPTION WHEN duplicate_object THEN NULL; END $do$;
DO $do$ BEGIN CREATE ROLE sc_payment_runtime NOLOGIN; EXCEPTION WHEN duplicate_object THEN NULL; END $do$;

--
-- PostgreSQL database dump
--

-- Dumped from database version 16.3
-- Dumped by pg_dump version 16.3

SET statement_timeout = 0;
SET lock_timeout = 0;
SET idle_in_transaction_session_timeout = 0;
SET client_encoding = 'UTF8';
SET standard_conforming_strings = on;
SELECT pg_catalog.set_config('search_path', '', false);
SET check_function_bodies = false;
SET xmloption = content;
SET client_min_messages = warning;
SET row_security = off;

--
-- Name: public; Type: SCHEMA; Schema: -; Owner: -
--

-- CREATE SCHEMA public;  -- always present; see generate-production-baseline.sh


--
-- Name: SCHEMA public; Type: COMMENT; Schema: -; Owner: -
--

COMMENT ON SCHEMA public IS 'standard public schema';


--
-- Name: iam_v2; Type: SCHEMA; Schema: -; Owner: -
--

CREATE SCHEMA iam_v2;


--
-- Name: activate_session_enforcement(uuid, uuid, uuid, text, integer, bigint); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.activate_session_enforcement(p_tenant uuid, p_site uuid, p_session uuid, p_bridge text, p_class_minor integer, p_epoch bigint) RETURNS text
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE v_state text; v_ended timestamptz; v_iface text; v_ip inet; v_device uuid; v_other int; cp record;
BEGIN
  SELECT state, ended, ingress_interface, ip, device_id INTO v_state, v_ended, v_iface, v_ip, v_device
    FROM iam_v2.sessions
   WHERE id = p_session AND tenant_id = p_tenant AND site_id = p_site
     FOR UPDATE;
  IF v_state IS NULL THEN
    RAISE EXCEPTION 'ENFORCE_SESSION_OUT_OF_SCOPE: session % is not in this tenant/site', p_session;
  END IF;
  -- A session that has ENDED must never be promoted. Without this, a delayed or replayed plan could
  -- resurrect access that a checkout or a revocation already removed.
  IF v_ended IS NOT NULL OR v_state IN ('ended','closed') THEN
    RAISE EXCEPTION 'ENFORCE_SESSION_ENDED: session % has ended and cannot be activated', p_session;
  END IF;
  -- The enforcement being reported must describe THIS session's source. The same coherence the accounting
  -- operations require: an activation quoting another session's bridge or class is not evidence about this one.
  IF v_iface IS DISTINCT FROM p_bridge OR iam_v2.p3_expected_class_minor(v_ip) IS DISTINCT FROM p_class_minor THEN
    RAISE EXCEPTION 'ENFORCE_SOURCE_MISMATCH: the enforcement result does not describe session %', p_session;
  END IF;
  IF p_epoch IS NULL OR p_epoch < 1 THEN
    RAISE EXCEPTION 'ENFORCE_INVALID: an activation needs the class generation it was enforced under';
  END IF;

  -- ACCOUNTABILITY IS VERIFIED HERE, NOT TAKEN ON TRUST.
  --
  -- "ACTIVE means authorized AND accountable" was, until this check existed, only an ordering convention in
  -- the applier's Go code: register the origin, then activate the class, then call this operation. An
  -- ordering convention is exactly as strong as the process that follows it, and this operation is reachable
  -- by anything holding the controlled-writer capability — a future caller, a repaired daemon, a mistaken
  -- retry with a stale epoch. Any of them could move a Session to 'active' while its traffic was metered by
  -- nothing, and every downstream reader would believe the traffic was accounted.
  --
  -- So the database confirms the accounting origin ITSELF, from the checkpoint table, keyed by the source
  -- tuple it derives from the Session row rather than from anything the caller stated: tenant, site,
  -- session, the Session's own device, the bridge, the class minor implied by the Session's IP, and the
  -- exact generation the applier says it enforced under.
  SELECT * INTO cp FROM iam_v2.accounting_checkpoints
   WHERE tenant_id = p_tenant AND site_id = p_site AND session_id = p_session
     AND source_device_id = v_device AND bridge = p_bridge AND class_minor = p_class_minor;
  IF cp.id IS NULL THEN
    -- No origin for this source: the class is forwarding from an unknown baseline, so its first reading
    -- would be billed as if every byte since the class was created had happened in one tick — or, worse,
    -- silently dropped. A Session must not claim to be accountable when nothing can account for it.
    RAISE EXCEPTION 'ENFORCE_NOT_ACCOUNTABLE: session % has no registered accounting origin for %/% (device %)',
      p_session, p_bridge, p_class_minor, v_device;
  END IF;
  IF cp.source_epoch IS DISTINCT FROM p_epoch THEN
    -- The origin describes a DIFFERENT generation of this class. Either the applier is quoting a stale epoch
    -- (its class was replaced under it) or the origin was reset into a newer generation after this activation
    -- was prepared. Either way the counters this Session would be billed from are not the ones it is running
    -- on, and promoting it would pin an accounting series to the wrong baseline.
    RAISE EXCEPTION 'ENFORCE_ORIGIN_EPOCH_MISMATCH: session % was enforced under generation % but its origin is generation %',
      p_session, p_epoch, cp.source_epoch;
  END IF;
  -- And nobody else may hold that class at this generation or later. Two sessions on one minor means one of
  -- them is being credited with the other's traffic, and the newer origin is the one that owns the series.
  SELECT count(*) INTO v_other FROM iam_v2.accounting_checkpoints cp2
   WHERE cp2.tenant_id = p_tenant AND cp2.site_id = p_site AND cp2.bridge = p_bridge
     AND cp2.class_minor = p_class_minor AND cp2.session_id <> p_session
     AND cp2.source_epoch >= p_epoch;
  IF v_other > 0 THEN
    RAISE EXCEPTION 'ENFORCE_CLASS_CONTESTED: another session holds %/% at generation % or later', p_bridge, p_class_minor, p_epoch;
  END IF;

  IF v_state = 'active' THEN
    -- IDEMPOTENT. A retried plan, a lost acknowledgement or a restart mid-flight converge here rather than
    -- creating a second anything.
    RETURN 'ALREADY_ACTIVE';
  END IF;
  IF v_state <> 'PENDING_ENFORCEMENT' THEN
    RAISE EXCEPTION 'ENFORCE_STATE_INVALID: session % is in state % and cannot be activated', p_session, v_state;
  END IF;

  UPDATE iam_v2.sessions SET state = 'active' WHERE id = p_session;
  RETURN 'ACTIVATED';
END $$;


--
-- Name: allocate_class_generation(uuid, uuid, uuid); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.allocate_class_generation(p_tenant uuid, p_site uuid, p_appliance uuid) RETURNS bigint
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE v_next bigint; v_pinned bigint;
BEGIN
  IF p_tenant IS NULL OR p_site IS NULL OR p_appliance IS NULL THEN
    RAISE EXCEPTION 'ACCT_INVALID: a class generation is allocated per tenant/site/appliance';
  END IF;
  INSERT INTO iam_v2.appliance_class_generation (tenant_id, site_id, appliance_id, last_generation)
    VALUES (p_tenant, p_site, p_appliance, 0)
    ON CONFLICT (tenant_id, site_id, appliance_id) DO NOTHING;
  -- Row lock first: two netd instances (or a restart racing itself) must not both read the same value.
  SELECT last_generation INTO v_next FROM iam_v2.appliance_class_generation
    WHERE tenant_id = p_tenant AND site_id = p_site AND appliance_id = p_appliance FOR UPDATE;
  -- RECONCILE against what is actually pinned. If this counter were ever lost, restored from an older
  -- backup, or rolled back, the checkpoints are the surviving evidence of which generations have been used.
  SELECT COALESCE(max(cp.source_epoch), 0) INTO v_pinned FROM iam_v2.accounting_checkpoints cp
    WHERE cp.tenant_id = p_tenant AND cp.site_id = p_site;
  v_next := GREATEST(COALESCE(v_next, 0), v_pinned) + 1;
  UPDATE iam_v2.appliance_class_generation
     SET last_generation = v_next, updated_at = now()
   WHERE tenant_id = p_tenant AND site_id = p_site AND appliance_id = p_appliance;
  RETURN v_next;
END $$;


--
-- Name: allocate_p_number(uuid, uuid, uuid); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.allocate_p_number(p_tenant uuid, p_site uuid, p_interface uuid) RETURNS bigint
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE v_p bigint;
BEGIN
  IF NOT EXISTS (SELECT 1 FROM iam_v2.pms_interfaces
                  WHERE tenant_id = p_tenant AND site_id = p_site AND id = p_interface) THEN
    RAISE EXCEPTION 'PNUMBER_INTERFACE_UNKNOWN: interface % is not in tenant %/site %',
      p_interface, p_tenant, p_site USING ERRCODE = 'foreign_key_violation';
  END IF;
  INSERT INTO iam_v2.pms_interface_pnumber_seq (tenant_id, site_id, pms_interface_id, next_p_number)
  VALUES (p_tenant, p_site, p_interface, 1)
  ON CONFLICT (pms_interface_id) DO NOTHING;

  UPDATE iam_v2.pms_interface_pnumber_seq
     SET next_p_number = next_p_number + 1
   WHERE pms_interface_id = p_interface
  RETURNING next_p_number - 1 INTO v_p;

  IF v_p IS NULL THEN
    RAISE EXCEPTION 'PNUMBER_ALLOCATION_FAILED: no sequence row for interface %', p_interface
      USING ERRCODE = 'no_data_found';
  END IF;
  RETURN v_p;
END $$;


--
-- Name: apply_adjustment(uuid, text, text, uuid, text); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.apply_adjustment(p_ent uuid, p_field text, p_new text, p_actor uuid, p_reason text) RETURNS void
    LANGUAGE plpgsql
    AS $$
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
END; $$;


--
-- Name: apply_entitlement_transition(uuid, text, timestamp with time zone, text); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.apply_entitlement_transition(p_ent uuid, p_to text, p_at timestamp with time zone, p_reason text) RETURNS void
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $_$
DECLARE v_from text; v_seq bigint; v_prev_at timestamptz; v_at timestamptz; v_term text;
BEGIN
  IF p_reason IS NOT NULL AND p_reason !~ '^[A-Z][A-Z0-9_]{0,63}$' THEN
    RAISE EXCEPTION 'transition reason must be a bounded machine code';
  END IF;
  SELECT status INTO v_from FROM iam_v2.entitlements WHERE id = p_ent FOR UPDATE;
  IF v_from IS NULL THEN RAISE EXCEPTION 'entitlement % not found', p_ent; END IF;
  SELECT COALESCE(max(seq),0)+1 INTO v_seq
    FROM iam_v2.entitlement_state_transitions WHERE entitlement_id = p_ent;
  SELECT effective_at INTO v_prev_at FROM iam_v2.entitlement_state_transitions
    WHERE entitlement_id = p_ent AND superseded_by IS NULL ORDER BY seq DESC LIMIT 1;
  -- TRUE effective time: the requested business time is recorded EXACTLY as given and is NEVER clamped to the
  -- previous transition - clamping would silently rewrite when the change actually took effect. An ordinary
  -- append that precedes the live head is a CORRECTION, and corrections must be explicit (fail closed here and
  -- go through supersede_entitlement_transition, which records the correction instead of hiding it).
  v_at := p_at;
  IF v_prev_at IS NOT NULL AND v_at < v_prev_at THEN
    RAISE EXCEPTION 'requested effective_at % precedes the live head % - use supersede_entitlement_transition to record a correction', v_at, v_prev_at;
  END IF;
  -- terminal_reason is the entitlements enum; a non-enum transition reason (e.g. a SEED/GRACE code) maps to OTHER.
  v_term := CASE WHEN p_to='TERMINATED' THEN
    (CASE WHEN p_reason IN ('TIME','DATA','HARD_EXPIRY','CHECKOUT','ADMIN','REVOKED','SUPERSEDED','CONVERTED','TRANSFERRED','CANCELLED','OTHER')
          THEN p_reason ELSE 'OTHER' END) ELSE NULL END;
  -- no session flag: status/history coherence is proven by the DEFERRED p3_entitlement_status_coherent trigger.
  UPDATE iam_v2.entitlements SET
    status = p_to,
    activated_at    = CASE WHEN p_to='ACTIVE' AND activated_at IS NULL THEN v_at ELSE activated_at END,
    terminal_reason = v_term,
    terminated_at   = CASE WHEN p_to='TERMINATED' THEN v_at ELSE NULL END
  WHERE id = p_ent;
  INSERT INTO iam_v2.entitlement_state_transitions(tenant_id,site_id,entitlement_id,seq,from_state,to_state,effective_at,recorded_at,reason)
    SELECT tenant_id, site_id, id, v_seq, CASE WHEN v_seq=1 THEN NULL ELSE v_from END, p_to, v_at, now(), p_reason
    FROM iam_v2.entitlements WHERE id = p_ent;
END $_$;


--
-- Name: apply_payment_callback_v2(uuid, text, uuid, text, text, text, text, text, jsonb); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.apply_payment_callback_v2(p_tenant uuid, p_provider text, p_merchant uuid, p_client_ref text, p_provider_event_id text, p_event_type text, p_asserted_status text, p_provider_txn_ref text DEFAULT NULL::text, p_evidence jsonb DEFAULT NULL::jsonb) RETURNS text
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE tx record; se record; v_moves boolean; v_bad text;
        v_captured bigint; v_returned bigint; v_target text;
BEGIN
  IF p_provider_event_id IS NULL OR btrim(p_provider_event_id) = '' THEN
    RAISE EXCEPTION 'CALLBACK_EVENT_ID_REQUIRED: a callback without a provider event id cannot be deduplicated'
      USING ERRCODE = 'check_violation';
  END IF;
  v_bad := iam_v2.p4_callback_evidence_safe(p_evidence);
  IF v_bad IS NOT NULL THEN
    RAISE EXCEPTION 'CALLBACK_EVIDENCE_UNSAFE: %', v_bad USING ERRCODE = 'check_violation';
  END IF;

  SELECT * INTO tx FROM iam_v2.payment_transactions
   WHERE tenant_id = p_tenant AND provider = p_provider AND merchant_account_id = p_merchant
     AND provider_ref = p_client_ref
   FOR UPDATE;
  IF tx.id IS NULL THEN
    RAISE EXCEPTION 'CALLBACK_UNCORRELATED: no payment intent matches this provider/merchant/client '
                    'reference; the callback is not applied' USING ERRCODE = 'no_data_found';
  END IF;

  -- (4) A conflicting provider reference is an AMBIGUITY, not a detail to discard. Raise before anything
  -- is recorded, so the conflict cannot be buried in an applied event.
  IF p_provider_txn_ref IS NOT NULL AND tx.provider_txn_ref IS NOT NULL
     AND p_provider_txn_ref <> tx.provider_txn_ref THEN
    RAISE EXCEPTION 'PAYMENT_EXTERNAL_REF_CONFLICT: this intent is pinned to provider reference %; the '
                    'callback carries %. Two provider transactions cannot claim one intent',
      tx.provider_txn_ref, p_provider_txn_ref USING ERRCODE = 'check_violation';
  END IF;

  v_moves := p_asserted_status IS NOT NULL AND p_asserted_status <> tx.status;

  BEGIN
    INSERT INTO iam_v2.payment_transaction_events
      (tenant_id, site_id, payment_transaction_id, provider, merchant_account_id,
       provider_event_id, event_type, asserted_status, detail, applied)
    VALUES (tx.tenant_id, tx.site_id, tx.id, tx.provider, tx.merchant_account_id,
            btrim(p_provider_event_id), p_event_type, p_asserted_status,
            coalesce(p_evidence, '{}'::jsonb), v_moves);
  EXCEPTION WHEN unique_violation THEN
    RETURN 'DUPLICATE';
  END;

  IF p_provider_txn_ref IS NOT NULL AND tx.provider_txn_ref IS NULL THEN
    UPDATE iam_v2.payment_transactions SET provider_txn_ref = p_provider_txn_ref WHERE id = tx.id;
  END IF;

  IF NOT v_moves THEN
    RETURN 'NOOP';
  END IF;
  UPDATE iam_v2.payment_transactions SET status = p_asserted_status WHERE id = tx.id;

  SELECT * INTO se FROM iam_v2.settlements WHERE id = tx.settlement_id FOR UPDATE;

  IF tx.transaction_type = 'CHARGE' THEN
    IF p_asserted_status = 'CAPTURED' THEN
      UPDATE iam_v2.settlements SET status = 'SETTLED' WHERE id = se.id AND status = 'IN_PROGRESS';
    ELSIF p_asserted_status IN ('FAILED','EXPIRED','CANCELLED') THEN
      UPDATE iam_v2.settlements SET status = 'FAILED' WHERE id = se.id AND status = 'IN_PROGRESS';
    ELSIF p_asserted_status = 'UNKNOWN' THEN
      UPDATE iam_v2.settlements SET status = 'MANUAL_REVIEW' WHERE id = se.id AND status = 'IN_PROGRESS';
    END IF;
  ELSIF p_asserted_status = 'CAPTURED' THEN
    SELECT coalesce(sum(amount_minor),0) INTO v_captured FROM iam_v2.payment_transactions
     WHERE settlement_id = tx.settlement_id AND transaction_type = 'CHARGE' AND status = 'CAPTURED';
    SELECT coalesce(sum(amount_minor),0) INTO v_returned FROM iam_v2.payment_transactions
     WHERE settlement_id = tx.settlement_id AND transaction_type IN ('REFUND','CHARGEBACK')
       AND status = 'CAPTURED';
    v_target := CASE WHEN v_returned >= v_captured THEN 'REVERSED' ELSE 'PARTIALLY_REVERSED' END;
    IF se.status IN ('SETTLED','PARTIALLY_REVERSED') AND se.status <> v_target THEN
      UPDATE iam_v2.settlements SET status = v_target WHERE id = se.id;
    END IF;
  END IF;
  RETURN 'APPLIED';
END $$;


--
-- Name: authorize_entitlement_device(uuid, uuid, timestamp with time zone); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.authorize_entitlement_device(p_ent uuid, p_device uuid, p_at timestamp with time zone) RETURNS uuid
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE v_t uuid; v_s uuid; v_status text; v_limit int; v_policy text; v_open int; v_seq bigint; v_id uuid; v_existing uuid;
BEGIN
  -- This operation writes a capability-scoped family, so it declares its own scope. Doing it here
  -- rather than relying on ownership is what lets Gate-P give every function its own owner without
  -- any of them losing the right to perform its own writes.
  PERFORM iam_v2.begin_controlled_operation('device_auth');
  SELECT tenant_id, site_id, status INTO v_t, v_s, v_status FROM iam_v2.entitlements WHERE id = p_ent FOR UPDATE;
  IF v_t IS NULL THEN RAISE EXCEPTION 'entitlement % not found', p_ent; END IF;
  IF v_status <> 'ACTIVE' THEN
    RAISE EXCEPTION 'device authorization requires an ACTIVE entitlement (status %)', v_status;
  END IF;
  -- the device must exist in the SAME tenant/site scope (never another tenant's device)
  PERFORM 1 FROM iam_v2.devices WHERE id = p_device AND tenant_id = v_t AND site_id = v_s;
  IF NOT FOUND THEN RAISE EXCEPTION 'device % is not in the entitlement scope', p_device; END IF;
  -- IDEMPOTENT: an already-open interval for this device is returned unchanged
  SELECT id INTO v_existing FROM iam_v2.entitlement_device_authorizations
    WHERE entitlement_id = p_ent AND device_id = p_device AND deauthorized_at IS NULL;
  IF v_existing IS NOT NULL THEN
    UPDATE iam_v2.entitlement_devices SET last_authorized = p_at
      WHERE entitlement_id = p_ent AND device_id = p_device;
    RETURN v_existing;
  END IF;
  -- device limit is enforced HERE, under the entitlement row lock, so concurrent authorizations cannot both win
  SELECT spr.max_concurrent_devices, spr.device_limit_policy INTO v_limit, v_policy
    FROM iam_v2.entitlements e JOIN iam_v2.service_plan_revisions spr ON spr.id = e.service_plan_revision_id
    WHERE e.id = p_ent;
  SELECT count(*) INTO v_open FROM iam_v2.entitlement_device_authorizations
    WHERE entitlement_id = p_ent AND deauthorized_at IS NULL;
  IF v_limit IS NOT NULL AND v_open >= v_limit THEN
    IF COALESCE(v_policy,'REJECT_NEW_DEVICE') <> 'REJECT_NEW_DEVICE' THEN
      RAISE EXCEPTION 'device limit policy % is not implemented in this phase (fail closed)', v_policy;
    END IF;
    RAISE EXCEPTION 'MAX_DEVICES_REACHED: entitlement % already has % of % devices authorized', p_ent, v_open, v_limit;
  END IF;
  INSERT INTO iam_v2.entitlement_devices(tenant_id,site_id,entitlement_id,device_id,status,first_authorized,last_authorized)
    VALUES (v_t,v_s,p_ent,p_device,'AUTHORIZED',p_at,p_at)
    ON CONFLICT (entitlement_id,device_id) DO UPDATE SET status='AUTHORIZED', last_authorized=p_at,
      first_authorized = COALESCE(iam_v2.entitlement_devices.first_authorized, p_at), disconnected_reason=NULL;
  SELECT COALESCE(max(seq),0)+1 INTO v_seq FROM iam_v2.entitlement_device_authorizations
    WHERE entitlement_id = p_ent AND device_id = p_device;
  INSERT INTO iam_v2.entitlement_device_authorizations(tenant_id,site_id,entitlement_id,device_id,seq,authorized_at)
    VALUES (v_t,v_s,p_ent,p_device,v_seq,p_at) RETURNING id INTO v_id;
  RETURN v_id;
END $$;


--
-- Name: begin_controlled_operation(text); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.begin_controlled_operation(p_family text) RETURNS void
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE v_token uuid;
BEGIN
  -- The allowlist is the whole authorization decision, and it is also what keeps the GUC name below safe:
  -- the family is never caller-shaped text by the time it is concatenated.
  IF p_family NOT IN ('stay','auth_resolution','commerce_intent','checkout_conversion','source_conflict',
                      'auth_context','device_auth','session_binding','grace_publication','alert') THEN
    RAISE EXCEPTION 'no approved capability-scoped controlled-writer family %', p_family;
  END IF;
  -- Bounded janitor. A transaction that rolled back or a backend that died leaves its row behind; nothing
  -- reads a stale row (the txid will not recur inside the retention window) but the table should not grow
  -- without limit on an appliance that runs for years.
  DELETE FROM iam_v2.controlled_operation_scope WHERE opened_at < now() - interval '1 hour';
  v_token := gen_random_uuid();
  INSERT INTO iam_v2.controlled_operation_scope (txid, family, token)
  VALUES (txid_current(), p_family, v_token)
  ON CONFLICT (txid, family) DO UPDATE SET token = EXCLUDED.token, opened_at = now();
  -- is_local = true: the setting dies with the transaction, so a scope cannot outlive the operation that
  -- opened it and be reused by the next statement on a pooled connection.
  PERFORM set_config('iam_v2.op_' || p_family, v_token::text, true);
END $$;


--
-- Name: begin_payment_execution(uuid); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.begin_payment_execution(p_txn uuid) RETURNS text
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE tx record; se record;
BEGIN
  SELECT * INTO tx FROM iam_v2.payment_transactions WHERE id = p_txn FOR UPDATE;
  IF tx.id IS NULL THEN
    RAISE EXCEPTION 'PAYMENT_NOT_EXECUTABLE: no such payment transaction %', p_txn
      USING ERRCODE = 'no_data_found';
  END IF;
  IF iam_v2.p4_financial_recovery_active(tx.tenant_id, tx.site_id) THEN
    RAISE EXCEPTION 'FINANCIAL_RECOVERY_MODE: execution is held pending operator reconciliation. Nothing '
                    'is replayed automatically after a restore' USING ERRCODE = 'check_violation';
  END IF;
  IF tx.status = 'PENDING' THEN RETURN 'ALREADY_EXECUTING'; END IF;
  IF tx.status <> 'CREATED' THEN
    RAISE EXCEPTION 'PAYMENT_NOT_EXECUTABLE: transaction is %; only a CREATED intent may begin executing',
      tx.status USING ERRCODE = 'check_violation';
  END IF;
  SELECT * INTO se FROM iam_v2.settlements WHERE id = tx.settlement_id FOR UPDATE;
  IF tx.transaction_type = 'CHARGE' AND se.status = 'REQUIRED' THEN
    UPDATE iam_v2.settlements SET status = 'IN_PROGRESS' WHERE id = se.id;
  ELSIF tx.transaction_type = 'CHARGE' AND se.status <> 'IN_PROGRESS' THEN
    RAISE EXCEPTION 'SETTLEMENT_NOT_EXECUTABLE: settlement is %; a charge cannot begin against it',
      se.status USING ERRCODE = 'check_violation';
  END IF;
  UPDATE iam_v2.payment_transactions SET status = 'PENDING' WHERE id = p_txn;
  RETURN 'EXECUTING';
END $$;


--
-- Name: bootstrap_emergency_grace(uuid, uuid); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.bootstrap_emergency_grace(p_tenant uuid, p_site uuid) RETURNS void
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE v_plan uuid; v_spr uuid; v_pkg uuid; v_ipr uuid;
BEGIN
  -- (item 9) serialize per tenant/site so >=24 concurrent bootstraps are safe (exactly one provisions; the rest
  -- verify). The tx-level advisory lock is released at commit; the caller supplies tenant/site as the key.
  PERFORM pg_advisory_xact_lock(hashtextextended(p_tenant::text || ':' || p_site::text || ':emergency-grace', 0));
  -- service plan (verify-before-mutate: a pre-existing row must already be system-shaped/enabled or we fail
  -- closed rather than adopt/repair it).
  SELECT id INTO v_plan FROM iam_v2.service_plans
    WHERE tenant_id=p_tenant AND site_id=p_site AND code='__sys_emergency_grace_plan__';
  IF v_plan IS NOT NULL THEN
    IF NOT EXISTS (SELECT 1 FROM iam_v2.service_plans WHERE id=v_plan AND enabled=true) THEN
      RAISE EXCEPTION 'reserved emergency service plan exists but is not enabled/system-shaped (fail closed)';
    END IF;
  END IF;
  IF v_plan IS NULL THEN
    INSERT INTO iam_v2.service_plans(tenant_id,site_id,code,enabled)
      VALUES (p_tenant,p_site,'__sys_emergency_grace_plan__',true) RETURNING id INTO v_plan;
  END IF;
  SELECT id INTO v_spr FROM iam_v2.service_plan_revisions WHERE service_plan_id=v_plan AND revision_no=1;
  IF v_spr IS NULL THEN
    INSERT INTO iam_v2.service_plan_revisions
      (tenant_id,site_id,service_plan_id,revision_no,name,down_kbps,up_kbps,max_concurrent_devices,device_limit_policy,time_accounting_mode,data_quota_bytes)
      VALUES (p_tenant,p_site,v_plan,1,'emergency-grace',5000,2000,1,'REJECT_NEW_DEVICE','VALIDITY_WINDOW',524288000)
      RETURNING id INTO v_spr;
  ELSE
    -- (item 8) verify an EXISTING revision has the EXACT approved attributes BEFORE re-pointing anything; a
    -- poisoned revision RAISES and (via rollback) leaves every current-revision pointer unchanged.
    IF NOT EXISTS (SELECT 1 FROM iam_v2.service_plan_revisions WHERE id=v_spr
        AND down_kbps=5000 AND up_kbps=2000 AND max_concurrent_devices=1
        AND device_limit_policy='REJECT_NEW_DEVICE' AND time_accounting_mode='VALIDITY_WINDOW' AND data_quota_bytes=524288000) THEN
      RAISE EXCEPTION 'reserved emergency service-plan revision 1 has mismatching attributes (poisoned; fail closed)';
    END IF;
  END IF;
  UPDATE iam_v2.service_plans SET current_revision_id=v_spr WHERE id=v_plan AND current_revision_id IS DISTINCT FROM v_spr;
  -- package
  SELECT id INTO v_pkg FROM iam_v2.internet_packages WHERE tenant_id=p_tenant AND site_id=p_site AND code='__sys_emergency_grace_pkg__';
  IF v_pkg IS NOT NULL AND NOT EXISTS (SELECT 1 FROM iam_v2.internet_packages WHERE id=v_pkg AND is_system AND active) THEN
    RAISE EXCEPTION 'reserved emergency package exists but is not system/active (poisoned; fail closed)';
  END IF;
  IF v_pkg IS NULL THEN
    INSERT INTO iam_v2.internet_packages(tenant_id,site_id,code,is_system)
      VALUES (p_tenant,p_site,'__sys_emergency_grace_pkg__',true) RETURNING id INTO v_pkg;
  END IF;
  SELECT id INTO v_ipr FROM iam_v2.internet_package_revisions WHERE package_id=v_pkg AND revision_no=1;
  IF v_ipr IS NULL THEN
    INSERT INTO iam_v2.internet_package_revisions
      (tenant_id,site_id,package_id,revision_no,service_plan_revision_id,package_type,price_minor,settlement_methods,duration_policy)
      VALUES (p_tenant,p_site,v_pkg,1,v_spr,'CHECKOUT_GRACE',0,ARRAY['NOT_REQUIRED']::text[],
              '{"end_mode":"GRACE_AFTER_CHECKOUT","grace_duration_seconds":3600,"policy_version":"EMERGENCY_GRACE_V1"}'::jsonb)
      RETURNING id INTO v_ipr;
  ELSE
    -- (item 8) verify the EXISTING package revision exactly (type/price/settlement/duration/end/version + its
    -- Plan-Revision relationship). Any mismatch is poisoned → RAISE (pointers unchanged).
    IF NOT EXISTS (SELECT 1 FROM iam_v2.internet_package_revisions WHERE id=v_ipr
        AND package_type='CHECKOUT_GRACE' AND price_minor=0 AND settlement_methods=ARRAY['NOT_REQUIRED']::text[]
        AND service_plan_revision_id=v_spr
        AND (duration_policy->>'grace_duration_seconds')='3600' AND (duration_policy->>'end_mode')='GRACE_AFTER_CHECKOUT'
        AND (duration_policy->>'policy_version')='EMERGENCY_GRACE_V1') THEN
      RAISE EXCEPTION 'reserved emergency package revision 1 has mismatching attributes (poisoned; fail closed)';
    END IF;
  END IF;
  UPDATE iam_v2.internet_packages SET current_revision_id=v_ipr WHERE id=v_pkg AND current_revision_id IS DISTINCT FROM v_ipr;
  -- final coherence assertion (the whole graph must be exactly OK after bootstrap).
  IF iam_v2.emergency_grace_health(p_tenant,p_site) <> 'OK' THEN
    RAISE EXCEPTION 'emergency-grace catalog not OK after bootstrap (fail closed)';
  END IF;
END $$;


--
-- Name: close_session(uuid, text); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.close_session(p_session uuid, p_reason text DEFAULT 'logout'::text) RETURNS text
    LANGUAGE plpgsql
    AS $$
DECLARE cur text;
BEGIN
  SELECT state INTO cur FROM iam_v2.sessions WHERE id=p_session FOR UPDATE;
  IF cur = 'ended' THEN RETURN 'ALREADY_ENDED'; END IF;
  UPDATE iam_v2.sessions SET state='ended', ended=now(), end_reason=p_reason WHERE id=p_session;
  RETURN 'ENDED';
END; $$;


--
-- Name: deauthorize_entitlement_device(uuid, uuid, timestamp with time zone, text); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.deauthorize_entitlement_device(p_ent uuid, p_device uuid, p_at timestamp with time zone, p_reason text) RETURNS boolean
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $_$
DECLARE v_open uuid; v_start timestamptz;
BEGIN
  -- This operation writes a capability-scoped family, so it declares its own scope. Doing it here
  -- rather than relying on ownership is what lets Gate-P give every function its own owner without
  -- any of them losing the right to perform its own writes.
  PERFORM iam_v2.begin_controlled_operation('device_auth');
  IF p_reason IS NOT NULL AND p_reason !~ '^[A-Z][A-Z0-9_]{0,63}$' THEN
    RAISE EXCEPTION 'deauthorization reason must be a bounded machine code';
  END IF;
  PERFORM 1 FROM iam_v2.entitlements WHERE id = p_ent FOR UPDATE;    -- L3 lock first
  SELECT id, authorized_at INTO v_open, v_start FROM iam_v2.entitlement_device_authorizations
    WHERE entitlement_id = p_ent AND device_id = p_device AND deauthorized_at IS NULL;
  IF v_open IS NULL THEN RETURN false; END IF;                        -- idempotent
  -- an interval may not close before it opened (that would invent negative authorized time)
  IF p_at < v_start THEN
    RAISE EXCEPTION 'deauthorization % precedes the interval start %', p_at, v_start;
  END IF;
  UPDATE iam_v2.entitlement_device_authorizations SET deauthorized_at = p_at WHERE id = v_open;
  UPDATE iam_v2.entitlement_devices SET status='DISCONNECTED', disconnected_reason = p_reason
    WHERE entitlement_id = p_ent AND device_id = p_device;
  RETURN true;
END $_$;


--
-- Name: emergency_grace_health(uuid, uuid); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.emergency_grace_health(p_tenant uuid, p_site uuid) RETURNS text
    LANGUAGE sql STABLE
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
  SELECT COALESCE((
    SELECT CASE WHEN
        ip.is_system AND ip.current_revision_id = ipr.id
        AND ipr.package_type='CHECKOUT_GRACE' AND ipr.price_minor=0
        AND ipr.settlement_methods = ARRAY['NOT_REQUIRED']::text[]
        AND (ipr.duration_policy->>'grace_duration_seconds')='3600'
        AND (ipr.duration_policy->>'policy_version')='EMERGENCY_GRACE_V1'
        AND sp.enabled AND sp.current_revision_id = spr.id
        AND spr.down_kbps=5000 AND spr.up_kbps=2000 AND spr.data_quota_bytes=524288000
        AND spr.max_concurrent_devices=1 AND spr.device_limit_policy='REJECT_NEW_DEVICE'
        AND spr.time_accounting_mode='VALIDITY_WINDOW'
      THEN 'OK' ELSE 'EMERGENCY_GRACE_CATALOG_INVALID' END
    FROM iam_v2.internet_packages ip
    JOIN iam_v2.internet_package_revisions ipr ON ipr.tenant_id=ip.tenant_id AND ipr.site_id=ip.site_id AND ipr.package_id=ip.id AND ipr.revision_no=1
    JOIN iam_v2.service_plan_revisions spr ON spr.tenant_id=ipr.tenant_id AND spr.site_id=ipr.site_id AND spr.id=ipr.service_plan_revision_id
    JOIN iam_v2.service_plans sp ON sp.tenant_id=spr.tenant_id AND sp.site_id=spr.site_id AND sp.id=spr.service_plan_id
    WHERE ip.tenant_id=p_tenant AND ip.site_id=p_site AND ip.code='__sys_emergency_grace_pkg__'
  ), 'EMERGENCY_GRACE_CATALOG_ABSENT');
$$;


--
-- Name: end_session_enforcement(uuid, uuid, uuid, text); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.end_session_enforcement(p_tenant uuid, p_site uuid, p_session uuid, p_reason text) RETURNS text
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $_$
DECLARE v_state text; v_ended timestamptz;
BEGIN
  IF p_reason IS NOT NULL AND p_reason !~ '^[A-Z][A-Z0-9_]{0,63}$' THEN
    RAISE EXCEPTION 'ENFORCE_INVALID: end reason must be a bounded machine code';
  END IF;
  SELECT state, ended INTO v_state, v_ended FROM iam_v2.sessions
   WHERE id = p_session AND tenant_id = p_tenant AND site_id = p_site FOR UPDATE;
  IF v_state IS NULL THEN
    RAISE EXCEPTION 'ENFORCE_SESSION_OUT_OF_SCOPE: session % is not in this tenant/site', p_session;
  END IF;
  IF v_ended IS NOT NULL OR v_state IN ('ended','closed') THEN
    RETURN 'ALREADY_ENDED';
  END IF;
  UPDATE iam_v2.sessions
     SET state = 'ended', ended = GREATEST(now(), started), end_reason = COALESCE(p_reason,'ENFORCEMENT_TORN_DOWN')
   WHERE id = p_session;
  RETURN 'ENDED';
END $_$;


--
-- Name: entitlement_usage_bytes(uuid, timestamp with time zone); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.entitlement_usage_bytes(p_ent uuid, p_at timestamp with time zone) RETURNS TABLE(bytes_up bigint, bytes_down bigint, records bigint, latest_sampled_at timestamp with time zone)
    LANGUAGE sql STABLE
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
  SELECT COALESCE(sum(ar.bytes_up),0)::bigint, COALESCE(sum(ar.bytes_down),0)::bigint,
         count(*)::bigint, max(ar.sampled_at)
  FROM iam_v2.accounting_records ar
  JOIN iam_v2.sessions s ON s.id = ar.session_id
  WHERE ar.sampled_at <= p_at
    AND (
      EXISTS (SELECT 1 FROM iam_v2.session_entitlement_bindings b
              WHERE b.session_id = ar.session_id AND b.entitlement_id = p_ent
                AND b.bound_from <= ar.sampled_at AND (b.bound_until IS NULL OR b.bound_until > ar.sampled_at))
      OR (s.entitlement_id = p_ent
          AND NOT EXISTS (SELECT 1 FROM iam_v2.session_entitlement_bindings b2 WHERE b2.session_id = ar.session_id))
    );
$$;


--
-- Name: grace_package_matches_policy(uuid, uuid, uuid, integer, integer, integer, bigint, integer, text); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.grace_package_matches_policy(p_tenant uuid, p_site uuid, p_pkg_rev uuid, p_duration integer, p_down integer, p_up integer, p_quota bigint, p_dev_limit integer, p_dev_policy text) RETURNS boolean
    LANGUAGE sql STABLE
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $_$
  SELECT iam_v2.grace_package_mismatch_reason($1,$2,$3,$4,$5,$6,$7,$8,$9) IS NULL;
$_$;


--
-- Name: grace_package_mismatch_reason(uuid, uuid, uuid, integer, integer, integer, bigint, integer, text); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.grace_package_mismatch_reason(p_tenant uuid, p_site uuid, p_pkg_rev uuid, p_duration integer, p_down integer, p_up integer, p_quota bigint, p_dev_limit integer, p_dev_policy text) RETURNS text
    LANGUAGE plpgsql STABLE
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $_$
DECLARE r record;
BEGIN
  IF p_pkg_rev IS NULL THEN
    -- A policy with no Package cannot grant anything. The conversion treats it as invalid configuration and
    -- falls back to Emergency, so it must never be publishable as an ordinary policy.
    RETURN 'PACKAGE_REQUIRED';
  END IF;
  SELECT ipr.id, ipr.package_type, ipr.price_minor, ipr.settlement_methods, ipr.duration_policy,
         ipr.service_plan_revision_id, ip.is_system, ip.active AS pkg_active, ip.current_revision_id, ip.code AS pkg_code,
         spr.id AS spr_id, spr.down_kbps, spr.up_kbps, spr.data_quota_bytes,
         spr.max_concurrent_devices, spr.device_limit_policy, spr.time_accounting_mode, sp.enabled AS plan_enabled,
         sp.code AS plan_code
    INTO r
    FROM iam_v2.internet_package_revisions ipr
    JOIN iam_v2.internet_packages ip
      ON ip.tenant_id = ipr.tenant_id AND ip.site_id = ipr.site_id AND ip.id = ipr.package_id
    LEFT JOIN iam_v2.service_plan_revisions spr
      ON spr.tenant_id = ipr.tenant_id AND spr.site_id = ipr.site_id AND spr.id = ipr.service_plan_revision_id
    LEFT JOIN iam_v2.service_plans sp
      ON sp.tenant_id = spr.tenant_id AND sp.site_id = spr.site_id AND sp.id = spr.service_plan_id
   WHERE ipr.id = p_pkg_rev AND ipr.tenant_id = p_tenant AND ipr.site_id = p_site;

  IF r.id IS NULL THEN RETURN 'PACKAGE_NOT_IN_SITE'; END IF;
  IF r.current_revision_id IS DISTINCT FROM r.id THEN RETURN 'NOT_CURRENT_REVISION'; END IF;
  IF r.pkg_active IS NOT TRUE THEN RETURN 'PACKAGE_INACTIVE'; END IF;
  IF r.package_type <> 'CHECKOUT_GRACE' THEN RETURN 'PACKAGE_TYPE'; END IF;
  IF r.is_system IS NOT TRUE THEN RETURN 'PACKAGE_NOT_SYSTEM_OWNED'; END IF;
  -- The RESERVED Emergency catalog is the fallback of last resort, not a policy an operator may adopt as the
  -- ordinary one. Allowing it would make "configured" and "emergency" indistinguishable in the audit trail and
  -- would silence the very alert that tells an operator their real policy is broken.
  --
  -- Checking the PACKAGE code alone is not enough: an ordinary-looking package can pin the reserved Emergency
  -- SERVICE PLAN, which repurposes the same reserved infrastructure through a different door.
  IF r.pkg_code IN ('__sys_emergency_grace_pkg__','__sys_emergency_grace_plan__') THEN
    RETURN 'PACKAGE_IS_EMERGENCY_CATALOG';
  END IF;
  IF r.plan_code IN ('__sys_emergency_grace_plan__','__sys_emergency_grace_pkg__') THEN
    RETURN 'PLAN_IS_EMERGENCY_CATALOG';
  END IF;
  IF r.price_minor <> 0 THEN RETURN 'PACKAGE_NOT_FREE'; END IF;
  IF array_length(r.settlement_methods,1) <> 1 OR r.settlement_methods[1] <> 'NOT_REQUIRED' THEN
    RETURN 'PACKAGE_SETTLEMENT';
  END IF;
  IF r.spr_id IS NULL THEN RETURN 'PLAN_REVISION_MISSING'; END IF;
  IF r.plan_enabled IS NOT TRUE THEN RETURN 'PLAN_DISABLED'; END IF;
  -- duration policy: the package must END as grace, for exactly the published duration, under the approved
  -- policy version when it declares one.
  IF COALESCE(r.duration_policy->>'end_mode','') <> 'GRACE_AFTER_CHECKOUT' THEN RETURN 'DURATION_END_MODE'; END IF;
  IF jsonb_typeof(r.duration_policy->'grace_duration_seconds') IS DISTINCT FROM 'number'
     OR (r.duration_policy->>'grace_duration_seconds') !~ '^[0-9]{1,9}$'
     OR (r.duration_policy->>'grace_duration_seconds')::int <> p_duration THEN
    RETURN 'DURATION_SECONDS';
  END IF;
  -- The approved policy version is REQUIRED, not optional-if-present: a package that simply omits the key
  -- would otherwise pass, and "no declared version" is not the same as "the approved version". It must also be
  -- a scalar string — a number, array or object is a malformed declaration, not a version.
  IF jsonb_typeof(r.duration_policy->'policy_version') IS DISTINCT FROM 'string' THEN
    RETURN 'DURATION_POLICY_VERSION';
  END IF;
  IF r.duration_policy->>'policy_version' <> 'CHECKOUT_GRACE_V1' THEN
    RETURN 'DURATION_POLICY_VERSION';
  END IF;
  -- the pinned plan revision must carry EXACTLY the published scalars: a policy the plan cannot deliver is a
  -- promise to the guest that the enforcement path would quietly break.
  IF r.down_kbps <> p_down THEN RETURN 'PLAN_DOWN_KBPS'; END IF;
  IF r.up_kbps <> p_up THEN RETURN 'PLAN_UP_KBPS'; END IF;
  IF r.data_quota_bytes IS DISTINCT FROM p_quota THEN RETURN 'PLAN_DATA_QUOTA'; END IF;
  IF r.max_concurrent_devices <> p_dev_limit THEN RETURN 'PLAN_DEVICE_LIMIT'; END IF;
  IF r.device_limit_policy <> p_dev_policy THEN RETURN 'PLAN_DEVICE_POLICY'; END IF;
  IF r.time_accounting_mode <> 'VALIDITY_WINDOW' THEN RETURN 'PLAN_TIME_ACCOUNTING'; END IF;
  RETURN NULL;
END $_$;


--
-- Name: ingest_absolute_counters(uuid, uuid, uuid, uuid, text, integer, bigint, bigint, bigint, timestamp with time zone); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.ingest_absolute_counters(p_tenant uuid, p_site uuid, p_session uuid, p_source_device uuid, p_bridge text, p_class_minor integer, p_epoch bigint, p_abs_up bigint, p_abs_down bigint, p_sampled_at timestamp with time zone) RETURNS text
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE
  v_started timestamptz; v_device uuid; v_ended timestamptz; v_iface text; v_ip inet; v_other int;
  cp record; v_ent uuid; v_up bigint; v_down bigint; v_seq bigint; v_rec uuid; v_class text; v_delayed boolean;
BEGIN
  IF p_abs_up IS NULL OR p_abs_down IS NULL OR p_abs_up < 0 OR p_abs_down < 0 THEN
    RAISE EXCEPTION 'ACCT_INVALID: absolute counters must be non-negative (up=%, down=%)', p_abs_up, p_abs_down;
  END IF;
  IF p_sampled_at IS NULL OR p_bridge IS NULL OR p_bridge = '' OR p_class_minor IS NULL OR p_epoch IS NULL THEN
    RAISE EXCEPTION 'ACCT_INVALID: sampled_at, bridge, class minor and source epoch are all required';
  END IF;

  -- SCOPE AND SOURCE COHERENCE. Every field describing WHERE these counters came from is re-derived here from
  -- the Session's own row and compared. The daemon computing them correctly is not evidence: the operation
  -- has to be able to refuse a caller that computed them wrongly, was fed the wrong session, or is replaying
  -- one session's counters under another's identity.
  SELECT started, device_id, ended, ingress_interface, ip
    INTO v_started, v_device, v_ended, v_iface, v_ip
    FROM iam_v2.sessions
   WHERE id = p_session AND tenant_id = p_tenant AND site_id = p_site;
  IF v_started IS NULL THEN
    RAISE EXCEPTION 'ACCT_SESSION_OUT_OF_SCOPE: session % is not in this tenant/site', p_session;
  END IF;
  -- DEVICE. Without this a class minor reused by another guest would quietly bill its traffic to whoever
  -- held the minor before.
  IF v_device IS DISTINCT FROM p_source_device THEN
    RAISE EXCEPTION 'ACCT_SOURCE_MISMATCH: session % is not bound to device %', p_session, p_source_device;
  END IF;
  -- BRIDGE. The Session records the interface it is actually on. A Session with none cannot be measured at
  -- all: there is no server-pinned answer to compare against, and accepting the caller's bridge would let it
  -- open a second counter series for the same guest.
  IF v_iface IS NULL OR v_iface = '' THEN
    RAISE EXCEPTION 'ACCT_SOURCE_MISMATCH: session % records no ingress interface', p_session;
  END IF;
  IF v_iface IS DISTINCT FROM p_bridge THEN
    RAISE EXCEPTION 'ACCT_SOURCE_MISMATCH: session % is on %, not %', p_session, v_iface, p_bridge;
  END IF;
  -- ADDRESS + CLASS. The minor is a pure function of the Session's own address, so a caller cannot name a
  -- different guest's class, and a Session whose address changed cannot keep accruing against the old one.
  IF v_ip IS NULL THEN
    RAISE EXCEPTION 'ACCT_SOURCE_MISMATCH: session % has no address to measure', p_session;
  END IF;
  IF iam_v2.p3_expected_class_minor(v_ip) IS DISTINCT FROM p_class_minor THEN
    RAISE EXCEPTION 'ACCT_SOURCE_MISMATCH: session % belongs to class %, not %',
      p_session, iam_v2.p3_expected_class_minor(v_ip), p_class_minor;
  END IF;
  IF p_sampled_at < v_started THEN
    RAISE EXCEPTION 'ACCT_INVALID: sample time % precedes the session start %', p_sampled_at, v_started;
  END IF;
  -- ONE AUTHORITATIVE SERIES. Every component of the checkpoint key is now pinned to the Session's own row,
  -- so a second source tuple for the same Session cannot be invented. This assertion states that explicitly
  -- rather than leaving it as a property somebody has to re-derive: if a stale checkpoint from a previous
  -- address or bridge still exists, it is not the one this observation may advance.
  SELECT count(*) INTO v_other FROM iam_v2.accounting_checkpoints cp2
   WHERE cp2.session_id = p_session
     AND (cp2.bridge, cp2.class_minor, cp2.source_device_id) IS DISTINCT FROM (p_bridge, p_class_minor, p_source_device);
  -- An ENDED session owns no further traffic. Refusing here (rather than only on the delta path) means an
  -- ended session cannot even establish a baseline, so nothing can later be billed against it.
  IF v_ended IS NOT NULL THEN
    RAISE EXCEPTION 'ACCT_SESSION_OUT_OF_SCOPE: session % ended at %', p_session, v_ended;
  END IF;

  -- CHECKPOINT: locked (or created) before anything is decided, so two runtimes observing the same counter
  -- cannot both compute a delta from the same previous value.
  SELECT * INTO cp FROM iam_v2.accounting_checkpoints
    WHERE session_id = p_session AND source_device_id = p_source_device
      AND bridge = p_bridge AND class_minor = p_class_minor
    FOR UPDATE;

  IF cp.id IS NULL THEN
    -- FIRST OBSERVATION. There is nothing to subtract from, so nothing is billed: the absolute counter becomes
    -- the baseline. Storing it as usage would charge the guest for everything since the class was created.
    INSERT INTO iam_v2.accounting_checkpoints
      (tenant_id, site_id, session_id, source_device_id, bridge, class_minor, source_epoch,
       prev_bytes_up, prev_bytes_down, prev_sampled_at, last_classification)
      VALUES (p_tenant, p_site, p_session, p_source_device, p_bridge, p_class_minor, p_epoch,
              p_abs_up, p_abs_down, p_sampled_at, 'BASELINED');
    RETURN 'BASELINED';
  END IF;

  IF p_epoch < cp.source_epoch THEN
    -- An older generation than the one already accepted is a stale or misrouted reading, never new usage.
    RAISE EXCEPTION 'ACCT_STALE_EPOCH: epoch % is older than the accepted epoch %', p_epoch, cp.source_epoch;
  END IF;

  IF p_epoch > cp.source_epoch THEN
    -- TRUSTED RESET. The TC owner says this managed class was replaced, so its counters legitimately restart.
    -- The new absolutes become the baseline; the bytes since the replacement are counted from here on.
    UPDATE iam_v2.accounting_checkpoints
       SET source_epoch = p_epoch, prev_bytes_up = p_abs_up, prev_bytes_down = p_abs_down,
           prev_sampled_at = p_sampled_at, last_classification = 'RESET_BASELINED', updated_at = now()
     WHERE id = cp.id;
    RETURN 'RESET_BASELINED';
  END IF;

  -- SAME EPOCH from here on.
  --
  -- TEMPORAL ORDER. Within one counter series, time only moves forward. An observation dated before the last
  -- accepted one is a delayed or misrouted delivery, and treating it as new usage would date a CURRENT delta
  -- into a HISTORICAL window — potentially one already frozen by a boundary watermark, where it would be
  -- recorded as delayed usage that never happened then.
  IF p_sampled_at < cp.prev_sampled_at THEN
    RAISE EXCEPTION 'ACCT_STALE_SAMPLE: sample at % precedes the last accepted sample at %',
      p_sampled_at, cp.prev_sampled_at;
  END IF;
  -- EQUAL sample times are explicitly ALLOWED when the counters advanced. The absolute counters are the
  -- authoritative evidence of how much was used; the timestamp only says when it was read. Two readings
  -- sharing an instant means the caller's clock is coarser than its sampling rate — a real and ordinary
  -- condition — and refusing the pair would throw away measured bytes to defend a precision assumption.
  -- A DECREASE at the same instant is still a regression and is refused below, and an identical pair is the
  -- replay case; neither can slip through here.

  IF p_abs_up = cp.prev_bytes_up AND p_abs_down = cp.prev_bytes_down THEN
    -- EXACT REPLAY. The counters have not moved since the last accepted observation, so whatever the caller
    -- believes about its own delivery, there is nothing new to store. This is what makes an uncertain commit
    -- safe: the retry sees the persisted state and is told so.
    --
    -- The persisted classification is reported so a caller retrying after an uncertain commit learns what
    -- actually happened to its observation — in particular whether it was ACCEPTED or landed in a frozen
    -- window as DELAYED. It is prefixed REPLAY: because the caller must be able to tell "your observation was
    -- accepted, just now" from "your observation was accepted, earlier": counting the second as fresh usage
    -- would make every retry look like new traffic in the daemon's own tallies and health.
    RETURN CASE WHEN cp.last_classification IN ('ACCEPTED','DELAYED')
                THEN 'REPLAY:' || cp.last_classification ELSE 'DUPLICATE' END;
  END IF;

  IF p_abs_up < cp.prev_bytes_up OR p_abs_down < cp.prev_bytes_down THEN
    -- A DECREASE WITHOUT A NEW EPOCH is ambiguous: it could be a silently recreated class, a misread, or a
    -- reused minor. Guessing "count from zero" would invent usage; guessing "ignore" would lose it. Fail closed
    -- and keep the checkpoint, so the next trustworthy observation can be judged against a known-good value.
    RAISE EXCEPTION 'ACCT_COUNTER_REGRESSION: counters went backwards without a new source epoch (up %->%, down %->%)',
      cp.prev_bytes_up, p_abs_up, cp.prev_bytes_down, p_abs_down;
  END IF;

  v_up := p_abs_up - cp.prev_bytes_up;
  v_down := p_abs_down - cp.prev_bytes_down;

  -- ATTRIBUTION at SAMPLE time, through the ONE shared resolver (iam_v2.p3_entitlement_at). There is no
  -- fallback to the session's current entitlement anywhere in this chain.
  v_ent := iam_v2.p3_entitlement_at(p_session, p_sampled_at);
  IF v_ent IS NULL THEN
    RAISE EXCEPTION 'ACCT_NO_BINDING: no entitlement was bound to session % at %', p_session, p_sampled_at;
  END IF;

  -- the per-session record sequence is allocated under the checkpoint lock, so it cannot collide
  SELECT COALESCE(max(sample_seq),0)+1 INTO v_seq FROM iam_v2.accounting_records WHERE session_id = p_session;
  INSERT INTO iam_v2.accounting_records
    (tenant_id, site_id, session_id, sample_seq, bytes_up, bytes_down, sampled_at, ingested_at)
    VALUES (p_tenant, p_site, p_session, v_seq, v_up, v_down, p_sampled_at, now())
    RETURNING id INTO v_rec;

  UPDATE iam_v2.sessions SET bytes_up = bytes_up + v_up, bytes_down = bytes_down + v_down
   WHERE id = p_session;

  SELECT EXISTS (SELECT 1 FROM iam_v2.delayed_accounting_records WHERE accounting_record_id = v_rec)
    INTO v_delayed;
  v_class := CASE WHEN v_delayed THEN 'DELAYED' ELSE 'ACCEPTED' END;

  UPDATE iam_v2.accounting_checkpoints
     SET prev_bytes_up = p_abs_up, prev_bytes_down = p_abs_down, prev_sampled_at = p_sampled_at,
         last_record_id = v_rec, last_classification = v_class, updated_at = now()
   WHERE id = cp.id;
  RETURN v_class;
END $$;


--
-- Name: ingest_sample(uuid, bigint, bigint, bigint, integer); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.ingest_sample(p_session uuid, p_seq bigint, p_up bigint, p_down bigint, p_epoch integer DEFAULT 1) RETURNS text
    LANGUAGE plpgsql
    AS $$
DECLARE w record; d bigint; t uuid; s uuid;
BEGIN
  SELECT tenant_id, site_id INTO t, s FROM iam_v2.sessions WHERE id=p_session;
  BEGIN
    INSERT INTO iam_v2.accounting_records(tenant_id,site_id,session_id,sample_seq,bytes_up,bytes_down)
      VALUES (t,s,p_session,p_seq,p_up,p_down);
  EXCEPTION WHEN unique_violation THEN RETURN 'DUPLICATE'; END;
  SELECT * INTO w FROM iam_v2.session_counter_watermarks WHERE session_id=p_session FOR UPDATE;
  IF NOT FOUND THEN
    INSERT INTO iam_v2.session_counter_watermarks(tenant_id,site_id,session_id,source_epoch,last_up,last_down,sample_seq,updated_at)
      VALUES (t,s,p_session,p_epoch,0,0,0,now());
    SELECT * INTO w FROM iam_v2.session_counter_watermarks WHERE session_id=p_session FOR UPDATE;
  END IF;
  IF p_epoch <> w.source_epoch THEN                 -- counter-reset epoch: treat sample as fresh delta from 0
    d := p_up + p_down;
    UPDATE iam_v2.session_counter_watermarks SET source_epoch=p_epoch, last_up=p_up, last_down=p_down,
      sample_seq=greatest(sample_seq,p_seq), updated_at=now() WHERE session_id=p_session;
  ELSIF p_seq <= w.sample_seq THEN
    RETURN 'STALE';                                 -- out-of-order / already-applied: ledgered, no double count
  ELSE
    d := greatest(p_up - w.last_up,0) + greatest(p_down - w.last_down,0);
    UPDATE iam_v2.session_counter_watermarks SET last_up=p_up, last_down=p_down, sample_seq=p_seq, updated_at=now()
      WHERE session_id=p_session;
  END IF;
  UPDATE iam_v2.entitlements e SET consumed_data_bytes = consumed_data_bytes + d, usage_version = usage_version+1
    FROM iam_v2.sessions ss WHERE ss.id=p_session AND e.id=ss.entitlement_id;
  RETURN 'APPLIED';
END; $$;


--
-- Name: issue_or_return_pms_context(uuid, uuid, uuid, uuid, uuid, uuid, uuid, uuid, integer); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.issue_or_return_pms_context(p_tenant uuid, p_site uuid, p_interface uuid, p_revision uuid, p_stay uuid, p_device uuid, p_guest_network uuid, p_request uuid, p_ttl_seconds integer) RETURNS TABLE(context_id uuid, reused boolean)
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $_$
DECLARE v_existing uuid; v_lifecycle int; v_ev bigint;
BEGIN
  -- This operation writes a capability-scoped family, so it declares its own scope. Doing it here
  -- rather than relying on ownership is what lets Gate-P give every function its own owner without
  -- any of them losing the right to perform its own writes.
  PERFORM iam_v2.begin_controlled_operation('auth_context');
  IF p_request IS NULL THEN
    RAISE EXCEPTION 'CONTEXT_INVALID: a PMS context must name the resolution it came from';
  END IF;
  -- An existing LIVE context for this resolution is returned as-is. Note what is deliberately not matched:
  -- a CONSUMED one. Returning that would hand back a credential that has already bought access.
  SELECT id INTO v_existing FROM iam_v2.auth_contexts
    WHERE tenant_id = p_tenant AND site_id = p_site AND resolution_request_id = p_request
      AND consumed_at IS NULL AND expires_at > now()
    FOR UPDATE;
  IF v_existing IS NOT NULL THEN
    RETURN QUERY SELECT v_existing, true;
    RETURN;
  END IF;

  -- Same authoritative Stay snapshot the plain issue path takes: IN_HOUSE, ACTIVE interface, occupancy
  -- evidence present, versioned, not clock-suspect, produced by the SAME revision, and still fresh.
  SELECT st.lifecycle_version, st.occupancy_evidence_version INTO v_lifecycle, v_ev
    FROM iam_v2.stays st
    JOIN iam_v2.pms_interfaces pi
      ON pi.tenant_id=st.tenant_id AND pi.site_id=st.site_id AND pi.id=st.pms_interface_id
    JOIN iam_v2.pms_interface_revisions pr
      ON pr.tenant_id=st.tenant_id AND pr.site_id=st.site_id
     AND pr.pms_interface_id=st.pms_interface_id AND pr.id=p_revision
   WHERE st.tenant_id=p_tenant AND st.site_id=p_site AND st.pms_interface_id=p_interface AND st.id=p_stay
     AND st.status='IN_HOUSE' AND pi.lifecycle_state='ACTIVE'
     AND st.occupancy_evidence_at IS NOT NULL AND st.occupancy_clock_suspect IS NOT TRUE
     AND st.occupancy_evidence_version > 0 AND st.occupancy_revision_id = p_revision
     AND st.occupancy_evidence_at > now() - make_interval(secs =>
           CASE WHEN (pr.config->>'max_auth_cache_age_seconds') ~ '^[1-9][0-9]{0,5}$'
                THEN CASE WHEN (pr.config->>'max_auth_cache_age_seconds')::int <= 604800
                          THEN (pr.config->>'max_auth_cache_age_seconds')::int ELSE 300 END
                ELSE 300 END)
   FOR UPDATE OF st;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'CONTEXT_INVALID: stay % is not eligible for a PMS context', p_stay;
  END IF;

  RETURN QUERY
    INSERT INTO iam_v2.auth_contexts
      (tenant_id, site_id, method, stay_id, pms_interface_id, authentication_interface_revision_id,
       device_id, guest_network_id, pinned_lifecycle_version, pinned_occupancy_evidence_version,
       resolution_request_id, expires_at)
    VALUES (p_tenant, p_site, 'PMS', p_stay, p_interface, p_revision, p_device, p_guest_network,
            v_lifecycle, v_ev, p_request, now() + make_interval(secs => p_ttl_seconds))
    RETURNING id, false;
END $_$;


--
-- Name: ns_capacity(text); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.ns_capacity(p text) RETURNS bigint
    LANGUAGE sql IMMUTABLE
    AS $$ SELECT hashtextextended(p, 7)  $$;


--
-- Name: ns_device_slot(text); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.ns_device_slot(p text) RETURNS bigint
    LANGUAGE sql IMMUTABLE
    AS $$ SELECT hashtextextended(p, 11) $$;


--
-- Name: ns_financial_review(text); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.ns_financial_review(p text) RETURNS bigint
    LANGUAGE sql IMMUTABLE
    AS $$ SELECT hashtextextended(p, 41) $$;


--
-- Name: ns_payment_parent(text); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.ns_payment_parent(p text) RETURNS bigint
    LANGUAGE sql IMMUTABLE
    AS $$ SELECT hashtextextended(p, 47) $$;


--
-- Name: p3_accounting_needs_binding(); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p3_accounting_needs_binding() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
BEGIN
  IF iam_v2.p3_entitlement_at(NEW.session_id, NEW.sampled_at) IS NULL THEN
    RAISE EXCEPTION 'ACCT_NO_BINDING: no entitlement was bound to session % at %', NEW.session_id, NEW.sampled_at;
  END IF;
  RETURN NEW;
END $$;


--
-- Name: p3_alert_action_guard(); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p3_alert_action_guard() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE prev_seq bigint; prev_action text;
BEGIN
  IF TG_OP <> 'INSERT' THEN RAISE EXCEPTION 'checkout_grace_alert_actions is append-only (% rejected)', TG_OP; END IF;
  SELECT seq, action INTO prev_seq, prev_action FROM iam_v2.checkout_grace_alert_actions
    WHERE audit_id=NEW.audit_id ORDER BY seq DESC LIMIT 1;
  IF prev_seq IS NULL THEN
    IF NEW.seq <> 1 OR NEW.action <> 'OPEN' THEN RAISE EXCEPTION 'first alert action must be seq=1 OPEN'; END IF;
  ELSE
    IF NEW.seq <> prev_seq + 1 THEN RAISE EXCEPTION 'alert action seq must be contiguous'; END IF;
    -- (item 10) legal edges only: OPEN->ACKNOWLEDGED|RESOLVED, ACKNOWLEDGED->RESOLVED, RESOLVED terminal.
    -- Rejects OPEN->OPEN, ACKNOWLEDGED->OPEN, repeated ACKNOWLEDGED, and any action after RESOLVED.
    IF NOT ( (prev_action='OPEN'         AND NEW.action IN ('ACKNOWLEDGED','RESOLVED'))
          OR (prev_action='ACKNOWLEDGED' AND NEW.action='RESOLVED') ) THEN
      RAISE EXCEPTION 'illegal alert action edge % -> %', prev_action, NEW.action;
    END IF;
  END IF;
  IF NEW.action IN ('ACKNOWLEDGED','RESOLVED') AND NEW.actor IS NULL THEN
    RAISE EXCEPTION 'ACKNOWLEDGED/RESOLVED alert action requires an actor';
  END IF;
  RETURN NEW;
END $$;


--
-- Name: p3_alert_open_on_audit(); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p3_alert_open_on_audit() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
BEGIN
  -- This operation writes a capability-scoped family, so it declares its own scope. Doing it here
  -- rather than relying on ownership is what lets Gate-P give every function its own owner without
  -- any of them losing the right to perform its own writes.
  PERFORM iam_v2.begin_controlled_operation('alert');
  IF NEW.alert_code IS NOT NULL THEN
    INSERT INTO iam_v2.checkout_grace_alert_actions(tenant_id, site_id, audit_id, seq, action, reason_code)
      VALUES (NEW.tenant_id, NEW.site_id, NEW.id, 1, 'OPEN', NEW.reason_code);
  END IF;
  RETURN NULL;
END $$;


--
-- Name: p3_checkout_audit_provenance(); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p3_checkout_audit_provenance() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE ev RECORD; ent RECORD; pur_episode int;
BEGIN
  IF NEW.boundary_event_id IS NOT NULL THEN
    SELECT tenant_id, site_id, pms_interface_id, stay_id, event_type, processing_status, sequence_version, normalization_version
      INTO ev FROM iam_v2.stay_events WHERE id = NEW.boundary_event_id;
    IF ev.tenant_id IS NULL THEN RAISE EXCEPTION 'boundary_event_id does not reference a stay_event'; END IF;
    IF ev.tenant_id <> NEW.tenant_id OR ev.site_id <> NEW.site_id OR ev.pms_interface_id <> NEW.pms_interface_id
       OR ev.stay_id IS DISTINCT FROM NEW.stay_id THEN
      RAISE EXCEPTION 'boundary event scope must match the audit (tenant/site/interface/stay)';
    END IF;
    IF ev.event_type <> 'GO' THEN RAISE EXCEPTION 'boundary event must be the typed checkout (GO) event'; END IF;
    IF ev.processing_status <> 'APPLIED' THEN RAISE EXCEPTION 'boundary event must be APPLIED'; END IF;
    IF NEW.boundary_event_seq IS DISTINCT FROM ev.sequence_version
       OR NEW.boundary_normalization_version IS DISTINCT FROM ev.normalization_version THEN
      RAISE EXCEPTION 'audit boundary seq/normalization must match the source event';
    END IF;
  END IF;
  IF NEW.grace_entitlement_id IS NOT NULL THEN
    SELECT e.tenant_id, e.site_id, e.pms_interface_id, e.stay_id, e.purchase_id
      INTO ent FROM iam_v2.entitlements e WHERE e.id = NEW.grace_entitlement_id;
    IF ent.tenant_id IS NULL THEN RAISE EXCEPTION 'grace_entitlement_id does not reference an entitlement'; END IF;
    IF ent.tenant_id <> NEW.tenant_id OR ent.site_id <> NEW.site_id OR ent.pms_interface_id IS DISTINCT FROM NEW.pms_interface_id
       OR ent.stay_id IS DISTINCT FROM NEW.stay_id THEN
      RAISE EXCEPTION 'grace entitlement scope must match the audit (tenant/site/interface/stay)';
    END IF;
    SELECT checkout_episode INTO pur_episode FROM iam_v2.purchases WHERE id = ent.purchase_id;
    IF pur_episode IS DISTINCT FROM NEW.lifecycle_version THEN
      RAISE EXCEPTION 'grace purchase checkout_episode % must equal audit lifecycle_version %', pur_episode, NEW.lifecycle_version;
    END IF;
  END IF;
  RETURN NEW;
END $$;


--
-- Name: p3_checkout_grace_audit_appendonly(); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p3_checkout_grace_audit_appendonly() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
BEGIN
  RAISE EXCEPTION 'iam_v2.checkout_grace_audit is append-only (% rejected)', TG_OP;
END $$;


--
-- Name: p3_controlled_operation_open(text); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p3_controlled_operation_open(p_family text) RETURNS boolean
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE v_tok text;
BEGIN
  v_tok := current_setting('iam_v2.op_' || p_family, true);   -- missing_ok: NULL when never set
  IF v_tok IS NULL OR v_tok = '' THEN
    RETURN false;
  END IF;
  RETURN EXISTS (
    SELECT 1 FROM iam_v2.controlled_operation_scope s
     WHERE s.txid = txid_current() AND s.family = p_family AND s.token::text = v_tok);
END $$;


--
-- Name: p3_controlled_writer_only(); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p3_controlled_writer_only() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE owner_role text; changed boolean := true; v_sig text; v_oid oid; v_cap text;
BEGIN
  -- CAPABILITY-SCOPED FAMILIES (see 4t). The write is allowed when a scope for the family is open in this
  -- transaction, or when the caller is already the opener's owner — the latter so that the operations
  -- themselves, and a database whose roles have not yet been separated, behave identically.
  v_cap := CASE
    WHEN TG_TABLE_NAME IN ('stays','stay_events')                                   THEN 'stay'
    WHEN TG_TABLE_NAME = 'auth_resolutions'                                          THEN 'auth_resolution'
    WHEN TG_TABLE_NAME IN ('offer_quotes','purchases')                               THEN 'commerce_intent'
    WHEN TG_TABLE_NAME IN ('checkout_grace_audit','entitlement_boundary_watermarks') THEN 'checkout_conversion'
    WHEN TG_TABLE_NAME = 'pms_source_conflicts'                                      THEN 'source_conflict'
    WHEN TG_TABLE_NAME = 'auth_contexts'                                             THEN 'auth_context'
    WHEN TG_TABLE_NAME = 'entitlement_device_authorizations'                          THEN 'device_auth'
    WHEN TG_TABLE_NAME = 'session_entitlement_bindings'                               THEN 'session_binding'
    WHEN TG_TABLE_NAME = 'checkout_grace_policy_publications'                         THEN 'grace_publication'
    WHEN TG_TABLE_NAME = 'checkout_grace_alert_actions'                               THEN 'alert'
    ELSE NULL END;
  -- resolve the family's approved function owner INLINE (catalog-only). Deliberately NOT a call to the
  -- introspection helper: this trigger fires as whichever role is writing, and a cross-function EXECUTE
  -- dependency would break exactly the dedicated-owner separation Gate-P needs.
  v_sig := CASE
    WHEN TG_TABLE_NAME = 'site_checkout_grace_config'
      THEN 'iam_v2.publish_checkout_grace_config(uuid,uuid,uuid,int,int,int,bigint,int,text,int)'
    WHEN TG_TABLE_NAME = 'appliance_class_generation'
      THEN 'iam_v2.allocate_class_generation(uuid,uuid,uuid)'
    WHEN TG_TABLE_NAME = 'auth_context_offers'
      THEN 'iam_v2.record_auth_context_offer(uuid,uuid,uuid,uuid,int,bigint,timestamptz)'
    WHEN TG_TABLE_NAME IN ('accounting_records','accounting_checkpoints','delayed_accounting_records','sessions')
      THEN 'iam_v2.ingest_absolute_counters(uuid,uuid,uuid,uuid,text,int,bigint,bigint,bigint,timestamptz)'
    -- Capability-scoped families resolve their owner through the operation-scope opener (see 4t), as does the
    -- scope table itself: a token nobody but the opener can write is what makes the scope unforgeable.
    WHEN TG_TABLE_NAME IN ('stays','stay_events','auth_resolutions','offer_quotes','purchases',
                           'checkout_grace_audit','entitlement_boundary_watermarks','pms_source_conflicts',
                           'auth_contexts','entitlement_device_authorizations','session_entitlement_bindings',
                           'checkout_grace_policy_publications','checkout_grace_alert_actions',
                           'controlled_operation_scope')
      THEN 'iam_v2.begin_controlled_operation(text)'
    ELSE 'iam_v2.apply_entitlement_transition(uuid,text,timestamptz,text)' END;
  v_oid := to_regprocedure(v_sig);
  IF v_oid IS NULL THEN
    RAISE EXCEPTION 'controlled-writer function % is not resolvable (fail closed)', v_sig;
  END IF;
  SELECT pg_get_userbyid(proowner) INTO owner_role FROM pg_proc WHERE oid = v_oid;
  IF owner_role IS NULL OR owner_role = '' THEN
    RAISE EXCEPTION 'controlled-writer owner for % is not resolvable (fail closed)', v_sig;
  END IF;

  IF v_cap IS NOT NULL THEN
    -- EVERY write to a capability-scoped family is checked, including DELETE: an authoritative record that
    -- can be removed outside a declared operation is a record that can be made to have never happened.
    IF current_user <> owner_role AND NOT iam_v2.p3_controlled_operation_open(v_cap) THEN
      -- RAISE takes only '%' substitutions; '%L' is format()'s syntax and would print a literal L.
      RAISE EXCEPTION
        '%: writes to the % family require an open controlled operation (caller %) — call iam_v2.begin_controlled_operation(''%'') in the transaction that performs them',
        TG_TABLE_NAME, v_cap, current_user, v_cap;
    END IF;
    RETURN CASE WHEN TG_OP = 'DELETE' THEN OLD ELSE NEW END;
  END IF;

  -- (item 1) DELETE of the authoritative site grace config is ALWAYS a controlled-writer-only operation. There
  -- is no approved ordinary DELETE for this row; a future reset/disable must be its own audited, PO-approved API
  -- with explicit semantics (this guard deliberately does NOT silently convert DELETE into "disable").
  IF TG_OP = 'DELETE' THEN
    IF current_user <> owner_role THEN
      RAISE EXCEPTION '%: DELETE goes through an approved controlled iam_v2 writer (caller %)',
        TG_TABLE_NAME, current_user;
    END IF;
    RETURN OLD;
  END IF;
  IF TG_TABLE_NAME IN ('accounting_records','accounting_checkpoints','delayed_accounting_records',
                       'appliance_class_generation','auth_context_offers') THEN
    -- EVERY write is controlled. A physical measurement has exactly one legitimate author: the operation that
    -- computed it from a locked checkpoint. A raw INSERT here is invented usage; a raw UPDATE is rewritten
    -- history; and a raw checkpoint write is worse than either, because it silently changes what every FUTURE
    -- observation will be measured against.
    changed := true;
  ELSIF TG_TABLE_NAME = 'sessions' AND TG_OP = 'UPDATE' THEN
    -- A Session row is written by several legitimate paths (it is created, bound, rebound and ended), but two
    -- groups of columns are accounting state:
    --   * the USAGE TOTALS, advanced only by the ingestion operation in the same transaction as the record
    --     that justifies them — anything else moves a total with no row behind it;
    --   * the ACCOUNTING IDENTITY (address and ingress interface), which the operation re-derives the
    --     counter source from. Rewriting either retroactively changes which physical counters a Session is
    --     measured against, which is a silent way to make one guest's traffic land on another's checkpoint.
    -- ...and the ENFORCEMENT LIFECYCLE. A Session that says 'active' is a claim that the kernel is
    -- authorizing and metering this guest right now. Only the enforcement owner can know that, so promoting
    -- a Phase-3 session out of PENDING_ENFORCEMENT goes through activate_session_enforcement. Legacy
    -- transitions between the pre-existing values are untouched: this only guards the new state.
    changed := (NEW.bytes_up IS DISTINCT FROM OLD.bytes_up OR NEW.bytes_down IS DISTINCT FROM OLD.bytes_down
      OR NEW.ip IS DISTINCT FROM OLD.ip OR NEW.ingress_interface IS DISTINCT FROM OLD.ingress_interface
      OR (OLD.state = 'PENDING_ENFORCEMENT' AND NEW.state IS DISTINCT FROM OLD.state)
      OR NEW.state = 'PENDING_ENFORCEMENT');
  ELSIF TG_TABLE_NAME = 'entitlements' THEN
    changed := (NEW.status IS DISTINCT FROM OLD.status);   -- only status is controlled-writer-only
  ELSIF TG_TABLE_NAME = 'site_checkout_grace_config' AND TG_OP = 'UPDATE' THEN
    changed := (NEW.grace_package_revision_id IS DISTINCT FROM OLD.grace_package_revision_id
      OR NEW.grace_duration_seconds IS DISTINCT FROM OLD.grace_duration_seconds
      OR NEW.grace_down_kbps IS DISTINCT FROM OLD.grace_down_kbps
      OR NEW.grace_up_kbps IS DISTINCT FROM OLD.grace_up_kbps
      OR NEW.grace_data_quota_bytes IS DISTINCT FROM OLD.grace_data_quota_bytes
      OR NEW.grace_device_limit IS DISTINCT FROM OLD.grace_device_limit
      OR NEW.grace_device_limit_policy IS DISTINCT FROM OLD.grace_device_limit_policy
      OR NEW.eligibility_window_seconds IS DISTINCT FROM OLD.eligibility_window_seconds
      OR NEW.config_version IS DISTINCT FROM OLD.config_version);
  END IF;
  IF changed AND current_user <> owner_role THEN
    RAISE EXCEPTION '%: authoritative writes go through the controlled iam_v2 writer functions (caller %)',
      TG_TABLE_NAME, current_user;
  END IF;
  RETURN NEW;
END $$;


--
-- Name: p3_controlled_writer_owner(text); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p3_controlled_writer_owner(p_family text) RETURNS text
    LANGUAGE plpgsql STABLE
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE v_sig text; v_oid oid; v_owner text;
BEGIN
  v_sig := CASE p_family
    WHEN 'entitlement' THEN 'iam_v2.apply_entitlement_transition(uuid,text,timestamptz,text)'
    WHEN 'grace_config' THEN 'iam_v2.publish_checkout_grace_config(uuid,uuid,uuid,int,int,int,bigint,int,text,int)'
    WHEN 'accounting' THEN 'iam_v2.ingest_absolute_counters(uuid,uuid,uuid,uuid,text,int,bigint,bigint,bigint,timestamptz)'
    WHEN 'accounting_origin' THEN 'iam_v2.register_class_origin(uuid,uuid,uuid,uuid,text,int,bigint,bigint,bigint,timestamptz)'
    WHEN 'class_generation' THEN 'iam_v2.allocate_class_generation(uuid,uuid,uuid)'
    WHEN 'auth_offers' THEN 'iam_v2.record_auth_context_offer(uuid,uuid,uuid,uuid,int,bigint,timestamptz)'
    ELSE NULL END;
  IF v_sig IS NULL THEN
    RAISE EXCEPTION 'no approved controlled-writer family %', p_family;
  END IF;
  v_oid := to_regprocedure(v_sig);            -- NULL (not an error) when unresolvable
  IF v_oid IS NULL THEN
    RAISE EXCEPTION 'controlled-writer function % is not resolvable (fail closed)', v_sig;
  END IF;
  SELECT pg_get_userbyid(proowner) INTO v_owner FROM pg_proc WHERE oid = v_oid;
  IF v_owner IS NULL OR v_owner = '' THEN
    RAISE EXCEPTION 'controlled-writer owner for % is not resolvable (fail closed)', v_sig;
  END IF;
  RETURN v_owner;
END $$;


--
-- Name: p3_detect_delayed_accounting(); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p3_detect_delayed_accounting() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE v_ent uuid; v_t uuid; v_s uuid; v_wm uuid;
BEGIN
  -- The SAME binding answer the ingestion operation used — no second opinion, and no fallback to the
  -- session's current pointer. The BEFORE INSERT guard has already refused any row without one, so a NULL
  -- here would mean the guard was bypassed; fail closed rather than attribute it to whatever is current.
  v_ent := iam_v2.p3_entitlement_at(NEW.session_id, NEW.sampled_at);
  IF v_ent IS NULL THEN
    RAISE EXCEPTION 'ACCT_NO_BINDING: no entitlement was bound to session % at %', NEW.session_id, NEW.sampled_at;
  END IF;
  SELECT id INTO v_wm FROM iam_v2.entitlement_boundary_watermarks
    WHERE entitlement_id = v_ent AND boundary_at >= NEW.sampled_at ORDER BY boundary_at ASC LIMIT 1;
  IF v_wm IS NULL THEN RETURN NEW; END IF;   -- nothing frozen for this period
  SELECT tenant_id, site_id INTO v_t, v_s FROM iam_v2.sessions WHERE id = NEW.session_id;
  INSERT INTO iam_v2.delayed_accounting_records
    (tenant_id,site_id,accounting_record_id,session_id,entitlement_id,watermark_id,sampled_at,bytes_up,bytes_down)
    VALUES (v_t,v_s,NEW.id,NEW.session_id,v_ent,v_wm,NEW.sampled_at,NEW.bytes_up,NEW.bytes_down)
    ON CONFLICT (accounting_record_id) DO NOTHING;
  RETURN NEW;
END $$;


--
-- Name: p3_eda_insert_guard(); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p3_eda_insert_guard() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE prev_seq bigint; prev_deauth timestamptz; prev_open int;
BEGIN
  IF NOT EXISTS (SELECT 1 FROM iam_v2.entitlement_devices ed
                 WHERE ed.entitlement_id=NEW.entitlement_id AND ed.device_id=NEW.device_id) THEN
    RAISE EXCEPTION 'device authorization requires an entitlement_devices binding';
  END IF;
  SELECT seq, deauthorized_at INTO prev_seq, prev_deauth
    FROM iam_v2.entitlement_device_authorizations
    WHERE entitlement_id=NEW.entitlement_id AND device_id=NEW.device_id ORDER BY seq DESC LIMIT 1;
  SELECT count(*) INTO prev_open FROM iam_v2.entitlement_device_authorizations
    WHERE entitlement_id=NEW.entitlement_id AND device_id=NEW.device_id AND deauthorized_at IS NULL;
  IF prev_seq IS NULL THEN
    IF NEW.seq <> 1 THEN RAISE EXCEPTION 'first device authorization must have seq=1'; END IF;
  ELSE
    IF NEW.seq <> prev_seq + 1 THEN RAISE EXCEPTION 'device authorization seq must be contiguous'; END IF;
    IF prev_open > 0 THEN RAISE EXCEPTION 'a device may not have two open authorization intervals'; END IF;
    IF NEW.authorized_at < prev_deauth THEN RAISE EXCEPTION 'new authorization cannot begin before the prior interval closed'; END IF;
  END IF;
  RETURN NEW;
END $$;


--
-- Name: p3_entitlement_at(uuid, timestamp with time zone); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p3_entitlement_at(p_session uuid, p_at timestamp with time zone) RETURNS uuid
    LANGUAGE sql STABLE
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
  SELECT b.entitlement_id FROM iam_v2.session_entitlement_bindings b
   WHERE b.session_id = p_session AND b.bound_from <= p_at
     AND (b.bound_until IS NULL OR b.bound_until > p_at)
   ORDER BY b.seq DESC LIMIT 1;
$$;


--
-- Name: p3_entitlement_status_coherent(); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p3_entitlement_status_coherent() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE latest text; cur text;
BEGIN
  -- re-read the CURRENT (final, at-commit) row status rather than trusting the deferred trigger's captured NEW:
  -- a row updated several times in one tx queues several deferred events, each carrying a stale intermediate
  -- NEW — only the final committed status must agree with the latest transition.
  SELECT status INTO cur FROM iam_v2.entitlements WHERE id = NEW.id;
  IF cur IS NULL THEN RETURN NULL; END IF; -- row removed within the tx; nothing to check
  SELECT to_state INTO latest FROM iam_v2.entitlement_state_transitions
    WHERE entitlement_id = NEW.id AND superseded_by IS NULL ORDER BY seq DESC LIMIT 1;
  IF latest IS DISTINCT FROM cur THEN
    RAISE EXCEPTION 'entitlement % status % is not backed by its latest transition % (use apply_entitlement_transition)',
      NEW.id, cur, COALESCE(latest, 'NONE');
  END IF;
  RETURN NULL;
END $$;


--
-- Name: p3_est_insert_guard(); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p3_est_insert_guard() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE prev_seq bigint; prev_to text; prev_at timestamptz; cur_status text;
        max_seq bigint; max_rec timestamptz; tgt_ent uuid; tgt_by uuid;
BEGIN
  -- seq/recorded_at monotonicity is measured over the WHOLE table (knowledge only grows); the STATE CHAIN is
  -- measured over the LIVE (non-superseded) history, because an invalidated fact is no longer part of the chain.
  SELECT COALESCE(max(seq),0), max(recorded_at) INTO max_seq, max_rec
    FROM iam_v2.entitlement_state_transitions WHERE entitlement_id = NEW.entitlement_id;
  IF max_seq > 0 AND NEW.seq <> max_seq + 1 THEN
    RAISE EXCEPTION 'entitlement transition seq must be contiguous (% -> %)', max_seq, NEW.seq;
  END IF;
  IF max_seq = 0 AND NEW.seq <> 1 THEN
    RAISE EXCEPTION 'first entitlement transition must have seq=1 (got %)', NEW.seq;
  END IF;
  -- recorded_at (SYSTEM time) is the axis that can never move backwards. effective_at (BUSINESS time) is stored
  -- verbatim and may legitimately be earlier than an already-recorded fact, but only as an explicit correction
  -- that first INVALIDATES (supersedes) the facts it replaces.
  IF max_rec IS NOT NULL AND NEW.recorded_at < max_rec THEN
    RAISE EXCEPTION 'transition recorded_at cannot move backwards (% < %)', NEW.recorded_at, max_rec;
  END IF;
  IF NEW.supersedes IS NOT NULL THEN
    SELECT entitlement_id, superseded_by INTO tgt_ent, tgt_by
      FROM iam_v2.entitlement_state_transitions WHERE id = NEW.supersedes;
    IF tgt_ent IS NULL OR tgt_ent <> NEW.entitlement_id THEN
      RAISE EXCEPTION 'superseded transition % does not belong to entitlement %', NEW.supersedes, NEW.entitlement_id;
    END IF;
    -- the correction must ALREADY own the fact it claims to correct: the invalidated rows are marked with THIS
    -- row's id before it is inserted, so a caller cannot append a row that merely points at someone else's fact.
    IF tgt_by IS DISTINCT FROM NEW.id THEN
      RAISE EXCEPTION 'superseded transition % is not marked as corrected by this transition', NEW.supersedes;
    END IF;
  END IF;
  -- chain continuity is evaluated against what REMAINS live (post-invalidation)
  SELECT seq, to_state, effective_at INTO prev_seq, prev_to, prev_at
    FROM iam_v2.entitlement_state_transitions
    WHERE entitlement_id = NEW.entitlement_id AND superseded_by IS NULL ORDER BY seq DESC LIMIT 1;
  IF prev_seq IS NULL THEN
    IF NEW.from_state IS NOT NULL THEN RAISE EXCEPTION 'transition with no live predecessor must have from_state NULL'; END IF;
  ELSE
    IF NEW.from_state IS DISTINCT FROM prev_to THEN RAISE EXCEPTION 'from_state % must equal previous to_state %', NEW.from_state, prev_to; END IF;
    -- an append may not be back-dated behind the live chain: silently accepting an earlier effective_at would
    -- rewrite the state-at-boundary answer with no record. Corrections invalidate what they replace, first.
    IF NEW.effective_at < prev_at THEN
      RAISE EXCEPTION 'transition effective_at % precedes the live head % - record a correction (supersede_entitlement_transition / terminate_entitlement_at_boundary)', NEW.effective_at, prev_at;
    END IF;
  END IF;
  IF NEW.from_state IS NOT NULL AND NOT (
       (NEW.from_state='PENDING'   AND NEW.to_state IN ('ACTIVE','SUSPENDED','TERMINATED'))
    OR (NEW.from_state='ACTIVE'    AND NEW.to_state IN ('SUSPENDED','TERMINATED'))
    OR (NEW.from_state='SUSPENDED' AND NEW.to_state IN ('ACTIVE','TERMINATED'))) THEN
    RAISE EXCEPTION 'illegal entitlement transition % -> % (TERMINATED is terminal)', NEW.from_state, NEW.to_state;
  END IF;
  SELECT status INTO cur_status FROM iam_v2.entitlements WHERE id = NEW.entitlement_id;
  IF NEW.to_state IS DISTINCT FROM cur_status THEN
    RAISE EXCEPTION 'transition to_state % must equal entitlement current status % (use apply_entitlement_transition)', NEW.to_state, cur_status;
  END IF;
  RETURN NEW;
END $$;


--
-- Name: p3_expected_class_minor(inet); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p3_expected_class_minor(p_ip inet) RETURNS integer
    LANGUAGE sql IMMUTABLE
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
  SELECT CASE
    WHEN p_ip IS NULL OR family(p_ip) <> 4 THEN NULL
    ELSE 4096 + (((split_part(host(p_ip), '.', 3)::int & 15) << 8) | split_part(host(p_ip), '.', 4)::int)
  END;
$$;


--
-- Name: p3_grace_config_version_guard(); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p3_grace_config_version_guard() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE policy_changed boolean;
BEGIN
  policy_changed := (NEW.grace_package_revision_id IS DISTINCT FROM OLD.grace_package_revision_id
    OR NEW.grace_duration_seconds IS DISTINCT FROM OLD.grace_duration_seconds
    OR NEW.grace_down_kbps IS DISTINCT FROM OLD.grace_down_kbps OR NEW.grace_up_kbps IS DISTINCT FROM OLD.grace_up_kbps
    OR NEW.grace_data_quota_bytes IS DISTINCT FROM OLD.grace_data_quota_bytes
    OR NEW.grace_device_limit IS DISTINCT FROM OLD.grace_device_limit
    OR NEW.grace_device_limit_policy IS DISTINCT FROM OLD.grace_device_limit_policy
    OR NEW.eligibility_window_seconds IS DISTINCT FROM OLD.eligibility_window_seconds);
  IF NEW.config_version < OLD.config_version THEN
    RAISE EXCEPTION 'site grace config_version cannot decrease (% -> %)', OLD.config_version, NEW.config_version;
  END IF;
  IF policy_changed AND NEW.config_version <> OLD.config_version + 1 THEN
    RAISE EXCEPTION 'a grace policy change must increment config_version by exactly 1 (use publish_checkout_grace_config)';
  END IF;
  IF NOT policy_changed AND NEW.config_version <> OLD.config_version THEN
    RAISE EXCEPTION 'config_version may not change without a policy change';
  END IF;
  RETURN NEW;
END $$;


--
-- Name: p3_history_appendonly(); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p3_history_appendonly() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
BEGIN
  IF TG_OP = 'DELETE' THEN
    RAISE EXCEPTION '%: append-only history (DELETE rejected)', TG_TABLE_NAME;
  END IF;
  -- UPDATE: only entitlement_device_authorizations.deauthorized_at may go NULL->a value once; nothing else.
  IF TG_TABLE_NAME = 'entitlement_device_authorizations' THEN
    IF OLD.deauthorized_at IS NOT NULL THEN
      RAISE EXCEPTION 'entitlement_device_authorizations interval is immutable once closed';
    END IF;
    IF NEW.id IS DISTINCT FROM OLD.id OR NEW.entitlement_id IS DISTINCT FROM OLD.entitlement_id
       OR NEW.device_id IS DISTINCT FROM OLD.device_id OR NEW.seq IS DISTINCT FROM OLD.seq
       OR NEW.authorized_at IS DISTINCT FROM OLD.authorized_at THEN
      RAISE EXCEPTION 'entitlement_device_authorizations identity/interval-start immutable';
    END IF;
    IF NEW.deauthorized_at IS NULL THEN
      RAISE EXCEPTION 'entitlement_device_authorizations UPDATE must close the interval (set deauthorized_at)';
    END IF;
    RETURN NEW;
  END IF;
  -- entitlement_state_transitions: the ONLY permitted mutation is marking a row superseded (NULL -> the id of the
  -- correcting transition), exactly once. Every other column, including effective_at, stays immutable forever.
  IF TG_TABLE_NAME = 'entitlement_state_transitions' THEN
    IF OLD.superseded_by IS NOT NULL THEN
      RAISE EXCEPTION 'entitlement_state_transitions row % is already superseded', OLD.id;
    END IF;
    IF NEW.superseded_by IS NULL THEN
      RAISE EXCEPTION 'entitlement_state_transitions UPDATE must record a supersession';
    END IF;
    IF NEW.id IS DISTINCT FROM OLD.id OR NEW.entitlement_id IS DISTINCT FROM OLD.entitlement_id
       OR NEW.seq IS DISTINCT FROM OLD.seq OR NEW.from_state IS DISTINCT FROM OLD.from_state
       OR NEW.to_state IS DISTINCT FROM OLD.to_state OR NEW.effective_at IS DISTINCT FROM OLD.effective_at
       OR NEW.recorded_at IS DISTINCT FROM OLD.recorded_at OR NEW.supersedes IS DISTINCT FROM OLD.supersedes
       OR NEW.reason IS DISTINCT FROM OLD.reason THEN
      RAISE EXCEPTION 'entitlement_state_transitions is immutable except for the supersession mark';
    END IF;
    RETURN NEW;
  END IF;
  RAISE EXCEPTION '%: append-only history (UPDATE rejected)', TG_TABLE_NAME;
END $$;


--
-- Name: p3_rederive_entitlement_times(uuid); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p3_rederive_entitlement_times(p_ent uuid) RETURNS void
    LANGUAGE plpgsql
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
BEGIN
  UPDATE iam_v2.entitlements e SET
    activated_at = (SELECT min(t.effective_at) FROM iam_v2.entitlement_state_transitions t
                    WHERE t.entitlement_id = e.id AND t.superseded_by IS NULL AND t.to_state='ACTIVE'),
    terminated_at = (SELECT t.effective_at FROM iam_v2.entitlement_state_transitions t
                     WHERE t.entitlement_id = e.id AND t.superseded_by IS NULL AND t.to_state='TERMINATED'
                     ORDER BY t.seq DESC LIMIT 1)
  WHERE e.id = p_ent;
END $$;


--
-- Name: p3_reserved_grace_codes(); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p3_reserved_grace_codes() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE reserved text[] := ARRAY['__sys_emergency_grace_plan__','__sys_emergency_grace_pkg__'];
BEGIN
  IF TG_OP = 'DELETE' THEN
    IF OLD.code = ANY(reserved) THEN
      RAISE EXCEPTION 'reserved system grace object % cannot be deleted', OLD.code;
    END IF;
    RETURN OLD;
  END IF;
  -- protect BOTH the old and the new code: a reserved object cannot be renamed AWAY (OLD reserved -> NEW not),
  -- nor can a non-reserved row be renamed INTO the reserved namespace as a non-system object.
  IF TG_OP = 'UPDATE' AND OLD.code = ANY(reserved) AND NEW.code IS DISTINCT FROM OLD.code THEN
    RAISE EXCEPTION 'reserved system grace object code is immutable (cannot rename away from %)', OLD.code;
  END IF;
  IF NEW.code = ANY(reserved) THEN
    IF TG_TABLE_NAME = 'internet_packages' THEN
      IF NEW.is_system IS NOT TRUE THEN
        RAISE EXCEPTION 'reserved grace code % requires a system-owned package', NEW.code;
      END IF;
      IF NEW.active IS NOT TRUE THEN
        RAISE EXCEPTION 'reserved system grace package cannot be disabled';
      END IF;
      IF TG_OP = 'UPDATE' THEN
        IF NEW.is_system IS DISTINCT FROM OLD.is_system THEN
          RAISE EXCEPTION 'reserved system grace package is_system is immutable';
        END IF;
        -- current_revision_id may be SET once by bootstrap, never re-pointed afterwards.
        IF OLD.current_revision_id IS NOT NULL AND NEW.current_revision_id IS DISTINCT FROM OLD.current_revision_id THEN
          RAISE EXCEPTION 'reserved system grace package current revision cannot be re-pointed';
        END IF;
      END IF;
    ELSIF TG_TABLE_NAME = 'service_plans' THEN
      IF NEW.enabled IS NOT TRUE THEN
        RAISE EXCEPTION 'reserved system grace plan cannot be disabled';
      END IF;
      IF TG_OP = 'UPDATE' AND OLD.current_revision_id IS NOT NULL
         AND NEW.current_revision_id IS DISTINCT FROM OLD.current_revision_id THEN
        RAISE EXCEPTION 'reserved system grace plan current revision cannot be re-pointed';
      END IF;
    END IF;
  END IF;
  RETURN NEW;
END $$;


--
-- Name: p3_seb_appendonly(); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p3_seb_appendonly() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
BEGIN
  IF TG_OP = 'DELETE' THEN RAISE EXCEPTION 'session_entitlement_bindings: append-only (DELETE rejected)'; END IF;
  IF OLD.bound_until IS NOT NULL THEN RAISE EXCEPTION 'session binding interval is immutable once closed'; END IF;
  IF NEW.bound_until IS NULL THEN RAISE EXCEPTION 'session binding UPDATE must close the interval'; END IF;
  IF NEW.id IS DISTINCT FROM OLD.id OR NEW.session_id IS DISTINCT FROM OLD.session_id
     OR NEW.entitlement_id IS DISTINCT FROM OLD.entitlement_id OR NEW.seq IS DISTINCT FROM OLD.seq
     OR NEW.bound_from IS DISTINCT FROM OLD.bound_from THEN
    RAISE EXCEPTION 'session binding identity/interval-start immutable';
  END IF;
  RETURN NEW;
END $$;


--
-- Name: p3_seb_insert_guard(); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p3_seb_insert_guard() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE prev_seq bigint; prev_until timestamptz; prev_open boolean;
BEGIN
  SELECT seq, bound_until, bound_until IS NULL INTO prev_seq, prev_until, prev_open
    FROM iam_v2.session_entitlement_bindings WHERE session_id = NEW.session_id ORDER BY seq DESC LIMIT 1;
  IF prev_seq IS NULL THEN
    IF NEW.seq <> 1 THEN RAISE EXCEPTION 'first session binding must have seq=1 (got %)', NEW.seq; END IF;
  ELSE
    IF NEW.seq <> prev_seq + 1 THEN RAISE EXCEPTION 'session binding seq must be contiguous (% -> %)', prev_seq, NEW.seq; END IF;
    IF prev_open THEN RAISE EXCEPTION 'session % already has an OPEN binding interval', NEW.session_id; END IF;
    IF NEW.bound_from < prev_until THEN RAISE EXCEPTION 'session binding % cannot begin before the previous closed at %', NEW.bound_from, prev_until; END IF;
  END IF;
  RETURN NEW;
END $$;


--
-- Name: p3_session_close_binding(); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p3_session_close_binding() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
BEGIN
  -- This operation writes a capability-scoped family, so it declares its own scope. Doing it here
  -- rather than relying on ownership is what lets Gate-P give every function its own owner without
  -- any of them losing the right to perform its own writes.
  PERFORM iam_v2.begin_controlled_operation('session_binding');
  IF NEW.ended IS NOT NULL AND OLD.ended IS NULL THEN
    UPDATE iam_v2.session_entitlement_bindings b SET bound_until = GREATEST(NEW.ended, b.bound_from)
      WHERE b.session_id = NEW.id AND b.bound_until IS NULL;
  END IF;
  RETURN NULL;
END $$;


--
-- Name: p3_session_open_binding(); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p3_session_open_binding() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
BEGIN
  -- This operation writes a capability-scoped family, so it declares its own scope. Doing it here
  -- rather than relying on ownership is what lets Gate-P give every function its own owner without
  -- any of them losing the right to perform its own writes.
  PERFORM iam_v2.begin_controlled_operation('session_binding');
  INSERT INTO iam_v2.session_entitlement_bindings(tenant_id,site_id,session_id,entitlement_id,seq,bound_from)
    VALUES (NEW.tenant_id,NEW.site_id,NEW.id,NEW.entitlement_id,1,NEW.started);
  RETURN NULL;
END $$;


--
-- Name: p3_stay_event_appendonly(); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p3_stay_event_appendonly() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $_$
DECLARE ok_stay int;
BEGIN
  IF TG_OP = 'DELETE' THEN
    RAISE EXCEPTION 'stay_events is append-only (DELETE rejected)';
  END IF;

  IF TG_OP = 'INSERT' THEN
    IF NEW.processing_status <> 'PENDING' THEN
      RAISE EXCEPTION 'stay_events must be inserted as PENDING (no terminal event inserted directly)';
    END IF;
    IF NEW.stay_id IS NOT NULL THEN
      RAISE EXCEPTION 'stay_events cannot be inserted with a pre-resolved stay_id';
    END IF;
    IF NEW.processed_at IS NOT NULL THEN
      RAISE EXCEPTION 'stay_events.processed_at must be NULL on insert';
    END IF;
    IF NEW.review_code IS NOT NULL THEN
      RAISE EXCEPTION 'stay_events.review_code must be NULL on insert';
    END IF;
    RETURN NEW;
  END IF;

  -- UPDATE: immutable identity / normalization / admission columns
  IF   NEW.id                    IS DISTINCT FROM OLD.id
    OR NEW.tenant_id             IS DISTINCT FROM OLD.tenant_id
    OR NEW.site_id               IS DISTINCT FROM OLD.site_id
    OR NEW.pms_interface_id      IS DISTINCT FROM OLD.pms_interface_id
    OR NEW.external_event_identity IS DISTINCT FROM OLD.external_event_identity
    OR NEW.event_type            IS DISTINCT FROM OLD.event_type
    OR NEW.pms_timestamp_raw     IS DISTINCT FROM OLD.pms_timestamp_raw
    OR NEW.pms_timestamp_utc     IS DISTINCT FROM OLD.pms_timestamp_utc
    OR NEW.source_timezone       IS DISTINCT FROM OLD.source_timezone
    OR NEW.received_at           IS DISTINCT FROM OLD.received_at
    OR NEW.sequence_version      IS DISTINCT FROM OLD.sequence_version
    OR NEW.normalization_version IS DISTINCT FROM OLD.normalization_version
    OR NEW.clock_suspect         IS DISTINCT FROM OLD.clock_suspect
    OR NEW.payload               IS DISTINCT FROM OLD.payload
    OR NEW.admission_kind              IS DISTINCT FROM OLD.admission_kind
    OR NEW.admission_runtime_generation IS DISTINCT FROM OLD.admission_runtime_generation
    OR NEW.resync_generation           IS DISTINCT FROM OLD.resync_generation
    OR NEW.fingerprint_key_version     IS DISTINCT FROM OLD.fingerprint_key_version
  THEN
    RAISE EXCEPTION 'stay_events identity/normalization/admission columns are immutable (append-only)';
  END IF;

  -- Once terminal, the only permitted update is a no-op (no result/lineage field may change).
  IF OLD.processing_status <> 'PENDING' THEN
    IF NEW.processing_status IS DISTINCT FROM OLD.processing_status
       OR NEW.stay_id      IS DISTINCT FROM OLD.stay_id
       OR NEW.processed_at IS DISTINCT FROM OLD.processed_at
       OR NEW.review_code  IS DISTINCT FROM OLD.review_code THEN
      RAISE EXCEPTION 'terminal stay_events row is immutable (status/stay_id/processed_at/review_code frozen)';
    END IF;
    RETURN NEW;
  END IF;

  -- OLD is PENDING and staying PENDING: no result/lineage field may be set yet.
  IF NEW.processing_status = 'PENDING' THEN
    IF NEW.stay_id IS NOT NULL OR NEW.processed_at IS NOT NULL OR NEW.review_code IS NOT NULL THEN
      RAISE EXCEPTION 'stay_events result fields (stay_id/processed_at/review_code) may only be set on PENDING->terminal';
    END IF;
    RETURN NEW;
  END IF;

  -- PENDING -> terminal (one move). A RESYNC row's PUBLICATION is enforced by the consumer against the
  -- interface's published_resync_generation boundary; the row itself stays immutable append-first evidence.
  IF NEW.processing_status NOT IN ('APPLIED','SKIPPED_DUPLICATE','MANUAL_REVIEW','FAILED') THEN
    RAISE EXCEPTION 'invalid stay_events terminal processing_status %', NEW.processing_status;
  END IF;
  IF NEW.processed_at IS NULL THEN
    RAISE EXCEPTION 'stay_events.processed_at is required on PENDING->%', NEW.processing_status;
  END IF;
  -- stay_id lineage: NULL -> a same-interface Stay only
  IF NEW.stay_id IS NOT NULL THEN
    IF OLD.stay_id IS NOT NULL THEN
      RAISE EXCEPTION 'stay_events.stay_id may only go from NULL to a resolved Stay';
    END IF;
    SELECT count(*) INTO ok_stay FROM iam_v2.stays s
      WHERE s.id = NEW.stay_id AND s.tenant_id = NEW.tenant_id
        AND s.site_id = NEW.site_id AND s.pms_interface_id = NEW.pms_interface_id;
    IF ok_stay <> 1 THEN
      RAISE EXCEPTION 'stay_events.stay_id must reference a Stay in the same tenant/site/pms_interface';
    END IF;
  END IF;
  -- review_code vocabulary: bounded machine code only (no PII / payload / stack traces)
  IF NEW.review_code IS NOT NULL AND NEW.review_code !~ '^[A-Z][A-Z0-9_]{0,63}$' THEN
    RAISE EXCEPTION 'stay_events.review_code must match ^[A-Z][A-Z0-9_]{0,63}$ (bounded machine code)';
  END IF;
  -- result-specific invariants
  IF NEW.processing_status = 'APPLIED' THEN
    IF NEW.stay_id IS NULL THEN RAISE EXCEPTION 'APPLIED requires a resolved same-interface stay_id'; END IF;
    IF NEW.review_code IS NOT NULL THEN RAISE EXCEPTION 'APPLIED must not carry a review_code'; END IF;
  ELSIF NEW.processing_status = 'MANUAL_REVIEW' THEN
    IF NEW.review_code IS NULL THEN RAISE EXCEPTION 'MANUAL_REVIEW requires a bounded review_code'; END IF;
  ELSIF NEW.processing_status = 'FAILED' THEN
    IF NEW.review_code IS NULL THEN RAISE EXCEPTION 'FAILED requires a bounded review_code'; END IF;
  END IF;
  -- SKIPPED_DUPLICATE: processed_at required (checked); stay_id/review_code optional (validated above).
  RETURN NEW;
END $_$;


--
-- Name: p3_stay_lifecycle_guard(); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p3_stay_lifecycle_guard() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE allowed boolean; is_reinstate boolean; evidence_changed boolean;
BEGIN
  IF NEW.last_applied_event_version < OLD.last_applied_event_version THEN
    RAISE EXCEPTION 'stays.last_applied_event_version cannot decrease (% -> %)',
      OLD.last_applied_event_version, NEW.last_applied_event_version;
  END IF;

  IF NEW.occupancy_evidence_version < OLD.occupancy_evidence_version THEN
    RAISE EXCEPTION 'stays.occupancy_evidence_version cannot decrease (% -> %)',
      OLD.occupancy_evidence_version, NEW.occupancy_evidence_version;
  END IF;
  evidence_changed := (
       NEW.occupancy_evidence_at          IS DISTINCT FROM OLD.occupancy_evidence_at
    OR NEW.occupancy_revision_id          IS DISTINCT FROM OLD.occupancy_revision_id
    OR NEW.occupancy_normalization_version IS DISTINCT FROM OLD.occupancy_normalization_version
    OR NEW.occupancy_clock_suspect        IS DISTINCT FROM OLD.occupancy_clock_suspect);
  IF evidence_changed THEN
    IF NEW.occupancy_evidence_version <> OLD.occupancy_evidence_version + 1 THEN
      RAISE EXCEPTION 'a material occupancy-evidence change must increment occupancy_evidence_version by exactly 1 (% -> %)',
        OLD.occupancy_evidence_version, NEW.occupancy_evidence_version;
    END IF;
  ELSE
    IF NEW.occupancy_evidence_version <> OLD.occupancy_evidence_version THEN
      RAISE EXCEPTION 'stays.occupancy_evidence_version may not change without a material occupancy-evidence change (% -> %)',
        OLD.occupancy_evidence_version, NEW.occupancy_evidence_version;
    END IF;
  END IF;

  is_reinstate := (OLD.status = 'CHECKED_OUT' AND NEW.status = 'IN_HOUSE');

  IF NEW.effective_checkout_at IS DISTINCT FROM OLD.effective_checkout_at THEN
    IF is_reinstate THEN
      IF NEW.effective_checkout_at IS NOT NULL THEN
        RAISE EXCEPTION 'reinstatement must CLEAR effective_checkout_at (starts a new episode)';
      END IF;
    ELSIF OLD.status = 'IN_HOUSE' AND NEW.status = 'CHECKED_OUT' THEN
      IF NEW.effective_checkout_at IS NULL THEN
        RAISE EXCEPTION 'checkout must SET effective_checkout_at';
      END IF;
    ELSE
      RAISE EXCEPTION 'effective_checkout_at is immutable within an episode (% -> %, status % -> %)',
        OLD.effective_checkout_at, NEW.effective_checkout_at, OLD.status, NEW.status;
    END IF;
  END IF;

  IF NEW.lifecycle_version <> OLD.lifecycle_version THEN
    IF NOT (NEW.lifecycle_version = OLD.lifecycle_version + 1 AND is_reinstate) THEN
      RAISE EXCEPTION 'stays.lifecycle_version may increment by exactly 1 ONLY during a CHECKED_OUT->IN_HOUSE reinstatement (% -> %, % -> %)',
        OLD.lifecycle_version, NEW.lifecycle_version, OLD.status, NEW.status;
    END IF;
  END IF;

  IF NEW.status <> OLD.status THEN
    allowed := CASE
      WHEN OLD.status='RESERVED'    AND NEW.status IN ('IN_HOUSE','CANCELLED','NO_SHOW') THEN true
      WHEN OLD.status='IN_HOUSE'    AND NEW.status IN ('CHECKED_OUT')                    THEN true
      WHEN OLD.status='CHECKED_OUT' AND NEW.status IN ('IN_HOUSE')                       THEN true  -- reinstatement
      -- PHASE 5: the post-stay conversion, and the only arm this migration adds. It stays within the episode
      -- (lifecycle_version unchanged, enforced above) and keeps the immutable checkout boundary (unchanged,
      -- enforced above), so a post-stay Stay is still the same episode that checked out.
      WHEN OLD.status='CHECKED_OUT' AND NEW.status IN ('POST_STAY_ACTIVE')               THEN true
      ELSE false END;
    IF NOT allowed THEN
      RAISE EXCEPTION 'illegal stays.status transition % -> %', OLD.status, NEW.status;
    END IF;
    IF is_reinstate AND NEW.lifecycle_version <> OLD.lifecycle_version + 1 THEN
      RAISE EXCEPTION 'reinstatement (CHECKED_OUT->IN_HOUSE) must increment lifecycle_version exactly once';
    END IF;
  END IF;
  RETURN NEW;
END $$;


--
-- Name: p4_apply_provider_outcome(text, text, text, text, text, jsonb); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p4_apply_provider_outcome(p_client_ref text, p_provider_event_id text, p_event_type text, p_outcome text, p_provider_txn_ref text, p_evidence jsonb) RETURNS text
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
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
  -- THE NARROWING THAT MATTERS. An outcome may only be applied to a payment that is actually executing --
  -- so this cannot be used to conjure a result for an intent nobody started.
  --
  -- The one exception is a delivery that REPEATS the outcome already recorded, which is what every provider
  -- does when it retries a webhook. That is not a status change and it is not a manufacture; it is a fact
  -- about a delivery, and it still goes through to the callback ledger below so the retry is RECORDED
  -- rather than silently dropped. The ledger's unique index makes it a no-op, and the reference-conflict
  -- check still runs -- which is why this delegates instead of returning early.
  IF tx.status <> 'PENDING' AND tx.status IS DISTINCT FROM p_outcome THEN
    RAISE EXCEPTION 'PAYMENT_NOT_EXECUTING: the intent is %; an outcome may only be applied to a payment '
                    'that crossed the execution boundary', tx.status USING ERRCODE = 'check_violation';
  END IF;
  RETURN iam_v2.apply_payment_callback_v2(
    tx.tenant_id, tx.provider, tx.merchant_account_id, p_client_ref,
    p_provider_event_id, p_event_type, p_outcome, nullif(p_provider_txn_ref,''), p_evidence);
END $$;


--
-- Name: p4_assert_compliance_archived(uuid); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p4_assert_compliance_archived(p_tenant uuid) RETURNS void
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE v_any int; v_verified int;
BEGIN
  SELECT count(*) INTO v_any FROM iam_v2.compliance_archives
   WHERE tenant_id = p_tenant AND purpose = 'CROSS_CUSTOMER_PURGE';
  IF v_any = 0 THEN
    RAISE EXCEPTION 'COMPLIANCE_ARCHIVE_MISSING: tenant % has no compliance archive; its data may not be '
                    'purged until one has been produced and its custody acknowledged', p_tenant
      USING ERRCODE = 'check_violation';
  END IF;

  SELECT count(*) INTO v_verified FROM iam_v2.compliance_archives
   WHERE tenant_id = p_tenant AND purpose = 'CROSS_CUSTOMER_PURGE' AND receipt_verified;
  IF v_verified = 0 THEN
    RAISE EXCEPTION 'COMPLIANCE_RECEIPT_UNVERIFIED: tenant % has an archive but no EXTERNAL authority has '
                    'acknowledged custody of it. A local copy attested only by the appliance that is about '
                    'to delete the data is self-certification, not a compliance archive. No archival '
                    'receipt authority exists in this product, so this purge cannot proceed', p_tenant
      USING ERRCODE = 'check_violation';
  END IF;
END $$;


--
-- Name: p4_assert_financial_actor(uuid, uuid); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p4_assert_financial_actor(p_tenant uuid, p_actor uuid) RETURNS void
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'iam_v2', 'public', 'pg_temp'
    AS $$
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
END $$;


--
-- Name: p4_attempt_freshness_gate(); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p4_attempt_freshness_gate() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE v_block text; v_rev uuid;
BEGIN
  SELECT posting_interface_revision_id INTO v_rev FROM iam_v2.pms_postings WHERE id = NEW.internal_posting_id;
  v_block := iam_v2.p4_interface_freshness_block(NEW.tenant_id, NEW.site_id, NEW.pms_interface_id, v_rev, now());
  IF v_block IS NOT NULL THEN
    RAISE EXCEPTION 'INTERFACE_NOT_FRESH: %', v_block USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END $$;


--
-- Name: p4_attempt_lifecycle_gate(); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p4_attempt_lifecycle_gate() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE v_state text;
BEGIN
  SELECT lifecycle_state INTO v_state FROM iam_v2.pms_interfaces
   WHERE tenant_id = NEW.tenant_id AND site_id = NEW.site_id AND id = NEW.pms_interface_id;
  IF v_state = 'DECOMMISSIONED' THEN
    RAISE EXCEPTION 'INTERFACE_DECOMMISSIONED: interface % may not transmit', NEW.pms_interface_id
      USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END $$;


--
-- Name: p4_attempt_retry_gate(); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p4_attempt_retry_gate() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE v_last record; v_auth int; v_term text;
BEGIN
  SELECT attempt_no, outcome INTO v_last
    FROM iam_v2.posting_attempts
   WHERE internal_posting_id = NEW.internal_posting_id
   ORDER BY attempt_no DESC LIMIT 1;

  IF v_last IS NULL THEN
    IF NEW.attempt_no <> 1 THEN
      RAISE EXCEPTION 'ATTEMPT_SEQUENCE: first attempt for posting % must be attempt_no 1, got %',
        NEW.internal_posting_id, NEW.attempt_no USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
  END IF;

  IF NEW.attempt_no <> v_last.attempt_no + 1 THEN
    RAISE EXCEPTION 'ATTEMPT_SEQUENCE: next attempt for posting % must be %, got %',
      NEW.internal_posting_id, v_last.attempt_no + 1, NEW.attempt_no USING ERRCODE = 'check_violation';
  END IF;

  IF v_last.outcome = 'SENDING' THEN
    RAISE EXCEPTION 'ATTEMPT_IN_FLIGHT: attempt % for posting % is still SENDING; a second concurrent '
                    'attempt would risk a duplicate charge', v_last.attempt_no, NEW.internal_posting_id
      USING ERRCODE = 'check_violation';
  END IF;

  IF v_last.outcome IN ('UNKNOWN','ACKED') THEN
    SELECT terminal_action, retry_authorized_attempt_no INTO v_term, v_auth
      FROM iam_v2.posting_review_state WHERE posting_id = NEW.internal_posting_id;
    IF v_term IS DISTINCT FROM 'CONFIRM_NOT_POSTED_RETRY' OR v_auth IS DISTINCT FROM NEW.attempt_no THEN
      RAISE EXCEPTION 'RETRY_REQUIRES_REVIEW: posting % last attempt is %; attempt % is not authorized by '
                      'an audited CONFIRM_NOT_POSTED_RETRY', NEW.internal_posting_id, v_last.outcome, NEW.attempt_no
        USING ERRCODE = 'check_violation';
    END IF;
  END IF;

  RETURN NEW;
END $$;


--
-- Name: p4_authorize_zero_attempt_retry(uuid, uuid, text, jsonb); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p4_authorize_zero_attempt_retry(p_posting uuid, p_actor uuid, p_reason text, p_evidence jsonb) RETURNS uuid
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE po record; ob record; h record; v_action uuid; v_attempts int; v_bad text;
BEGIN
  SELECT * INTO po FROM iam_v2.pms_postings WHERE id = p_posting;
  IF po.id IS NULL THEN
    RAISE EXCEPTION 'REVIEW_POSTING_UNKNOWN: %', p_posting USING ERRCODE = 'no_data_found';
  END IF;
  PERFORM iam_v2.p4_assert_financial_actor(po.tenant_id, p_actor);
  IF p_reason IS NULL OR length(btrim(p_reason)) < 10 THEN
    RAISE EXCEPTION 'REVIEW_REASON_REQUIRED: a retry authorization records WHY, in at least 10 characters'
      USING ERRCODE = 'check_violation';
  END IF;
  -- Evidence screening. p4_callback_evidence_safe is deliberately NOT reused here: it enforces the
  -- PROVIDER-CALLBACK key allowlist (provider_status, provider_reason_code, ...), which is the wrong
  -- contract for a review decision and would reject the operator evidence the review surface actually
  -- collects. What matters in both places is the same, though -- the ledger is immutable, so nothing that
  -- looks like a secret may enter it -- so the screening is done here against the same shapes.
  IF p_evidence IS NOT NULL THEN
    IF jsonb_typeof(p_evidence) <> 'object' THEN
      RAISE EXCEPTION 'REVIEW_EVIDENCE_UNSAFE: evidence must be a bounded object'
        USING ERRCODE = 'check_violation';
    END IF;
    SELECT string_agg(k, ',') INTO v_bad FROM jsonb_object_keys(p_evidence) k
     WHERE k IN ('raw_body','raw','payload','body','token','secret','password','api_key','card',
                 'pan','cvv','authorization');
    IF v_bad IS NOT NULL THEN
      RAISE EXCEPTION 'REVIEW_EVIDENCE_UNSAFE: key(s) % may not enter an immutable audit record; record a '
                      'REFERENCE to the artefact, never its contents', v_bad
        USING ERRCODE = 'check_violation';
    END IF;
    SELECT string_agg(k, ',') INTO v_bad FROM jsonb_each_text(p_evidence) e(k, v)
     WHERE length(v) > 256 OR v ~ '[[:cntrl:]]'
        OR v ~ '(?i)(sk_live|pk_live|-----BEGIN|bearer [A-Za-z0-9._-]{20,}|[0-9]{13,19})';
    IF v_bad IS NOT NULL THEN
      RAISE EXCEPTION 'REVIEW_EVIDENCE_UNSAFE: value(s) under % look like a secret, a card number or an '
                      'unbounded blob', v_bad USING ERRCODE = 'check_violation';
    END IF;
  END IF;

  SELECT count(*) INTO v_attempts FROM iam_v2.posting_attempts WHERE internal_posting_id = p_posting;
  IF v_attempts > 0 THEN
    RAISE EXCEPTION 'RETRY_HAS_ATTEMPTS: this posting has % attempt(s); use the ordinary audited review '
                    'path, which is the one that knows how to read them', v_attempts
      USING ERRCODE = 'check_violation';
  END IF;

  SELECT * INTO ob FROM iam_v2.posting_outbox WHERE posting_id = p_posting FOR UPDATE;
  IF ob.id IS NULL OR ob.state <> 'HELD_RECOVERY' THEN
    RAISE EXCEPTION 'RETRY_NOT_HELD: this posting is not held by recovery (%); there is nothing to release',
      coalesce(ob.state,'no outbox row') USING ERRCODE = 'check_violation';
  END IF;

  SELECT * INTO h FROM iam_v2.financial_recovery_holds
   WHERE work_kind = 'POSTING_OUTBOX' AND work_id = ob.id
   ORDER BY held_at DESC LIMIT 1;
  IF h.id IS NULL OR h.resolution IS DISTINCT FROM 'CONFIRMED_NOT_COMPLETED' THEN
    RAISE EXCEPTION 'RETRY_NOT_ESTABLISHED: this posting has no reconciliation establishing that it was '
                    'NOT completed (%). A retry is only safe once someone has checked the folio',
      coalesce(h.resolution, 'unreconciled') USING ERRCODE = 'check_violation';
  END IF;

  -- The audited record. It goes into the SAME append-only review ledger every other financial decision
  -- goes into, so a reader auditing this posting sees one history rather than two.
  --
  -- 0011 guards that ledger with a writer token so it has exactly one writer. Setting the token here makes
  -- this function a SECOND sanctioned writer, which is a deliberate and narrow addition: it is the only way
  -- to record the zero-attempt case, it is a definer function no runtime role can call except the operator
  -- role, and every other path into the table remains closed. The alternative -- writing the decision
  -- somewhere else -- would give this posting two histories.
  PERFORM set_config('iam_v2.p4_review_writer', txid_current()::text, true);
  INSERT INTO iam_v2.posting_review_actions
    (tenant_id, site_id, posting_id, action, actor, reason, evidence)
  VALUES (po.tenant_id, po.site_id, p_posting, 'CONFIRM_NOT_POSTED_RETRY', p_actor, p_reason,
          coalesce(p_evidence, '{}'::jsonb))
  RETURNING id INTO v_action;

  INSERT INTO iam_v2.posting_review_state
    (posting_id, tenant_id, site_id, review_version, retry_authorized_attempt_no, updated_at)
  VALUES (p_posting, po.tenant_id, po.site_id, 1, 1, now())
  ON CONFLICT (posting_id) DO UPDATE
     SET review_version = iam_v2.posting_review_state.review_version + 1,
         retry_authorized_attempt_no = 1,
         updated_at = now();

  -- Only NOW does it become sendable, and only because an operator established it never was.
  UPDATE iam_v2.posting_outbox SET state = 'QUEUED' WHERE id = ob.id;
  RETURN v_action;
END $$;


--
-- Name: p4_callback_evidence_safe(jsonb); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p4_callback_evidence_safe(p jsonb) RETURNS text
    LANGUAGE plpgsql IMMUTABLE
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE k text; v text; allowed text[] := ARRAY['provider_status','provider_reason_code','provider_message',
                                                'provider_received_at','settled_currency','settled_amount_minor'];
BEGIN
  IF p IS NULL OR p = '{}'::jsonb THEN RETURN NULL; END IF;
  IF jsonb_typeof(p) <> 'object' THEN RETURN 'callback evidence must be a flat object'; END IF;
  FOR k, v IN SELECT key, value::text FROM jsonb_each_text(p) LOOP
    IF NOT (k = ANY(allowed)) THEN
      RETURN 'callback evidence key ' || k || ' is not in the allowed set: ' || array_to_string(allowed, ', ');
    END IF;
    IF length(v) > 200 THEN RETURN 'callback evidence value for ' || k || ' exceeds 200 characters'; END IF;
    IF v ~ '[\x00-\x1f\x7f]' THEN RETURN 'callback evidence value for ' || k || ' contains control characters'; END IF;
    IF v ~* '\m(pass(word|phrase)?|secret|api[-_ ]?key|token|bearer|authorization|credential|cvv|cvc|pan)\M'
       OR v ~ '(-----BEGIN|eyJ[A-Za-z0-9_-]{10,}|sk_live_|whsec_)'
       OR v ~ '\m(?:\d[ -]?){13,19}\M' THEN
      RETURN 'callback evidence value for ' || k || ' looks like a secret, card number or credential';
    END IF;
  END LOOP;
  -- nesting is refused wholesale: a nested value is a payload
  IF EXISTS (SELECT 1 FROM jsonb_each(p) WHERE jsonb_typeof(value) IN ('object','array')) THEN
    RETURN 'callback evidence must be flat; nested objects and arrays are payloads';
  END IF;
  RETURN NULL;
END $$;


--
-- Name: p4_consume_retry_authorization(); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p4_consume_retry_authorization() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
BEGIN
  UPDATE iam_v2.posting_review_state
     SET retry_authorized_attempt_no = NULL,
         retry_authorization_consumed_at = now(),
         review_version = review_version + 1,
         updated_at = now()
   WHERE posting_id = NEW.internal_posting_id
     AND terminal_action = 'CONFIRM_NOT_POSTED_RETRY'
     AND retry_authorized_attempt_no = NEW.attempt_no;
  RETURN NULL;
END $$;


--
-- Name: p4_current_restore_generation(uuid, uuid); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p4_current_restore_generation(p_tenant uuid, p_site uuid) RETURNS bigint
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
  SELECT coalesce(max(restore_generation), 0) FROM iam_v2.financial_epochs
   WHERE tenant_id = p_tenant AND site_id = p_site;
$$;


--
-- Name: p4_declare_financial_recovery(uuid, uuid, uuid, text); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p4_declare_financial_recovery(p_tenant uuid, p_site uuid, p_actor uuid, p_reason text) RETURNS bigint
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE cur record; v_epoch bigint;
BEGIN
  PERFORM iam_v2.p4_assert_financial_actor(p_tenant, p_actor);
  IF p_reason IS NULL OR length(btrim(p_reason)) < 10 THEN
    RAISE EXCEPTION 'RECOVERY_REASON_REQUIRED: declaring recovery needs a reason of at least 10 characters'
      USING ERRCODE = 'check_violation';
  END IF;
  PERFORM pg_advisory_xact_lock(hashtext('p4.epoch:' || p_tenant::text || ':' || p_site::text));
  SELECT * INTO cur FROM iam_v2.financial_epochs
   WHERE tenant_id = p_tenant AND site_id = p_site ORDER BY epoch DESC LIMIT 1;
  IF cur.epoch IS NOT NULL AND cur.released_at IS NULL THEN
    RETURN cur.epoch;
  END IF;
  v_epoch := coalesce(cur.epoch, 0) + 1;
  INSERT INTO iam_v2.financial_epochs (tenant_id, site_id, epoch, system_identity, reason)
  VALUES (p_tenant, p_site, v_epoch, coalesce(cur.system_identity, 'operator-declared'), 'OPERATOR_DECLARED');
  -- The SAME full-rail hold a detected restore performs. An operator declaring recovery is making the same
  -- statement a restore detector makes, so it must have the same consequence.
  PERFORM iam_v2.p4_hold_financial_rails(p_tenant, p_site, v_epoch);
  RETURN v_epoch;
END $$;


--
-- Name: p4_entitlement_grant_kernel(uuid, uuid, uuid, uuid, uuid, uuid, jsonb, uuid, uuid); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p4_entitlement_grant_kernel(p_tenant uuid, p_site uuid, p_purchase uuid, p_voucher uuid, p_account uuid, p_principal uuid, p_snapshot jsonb, p_plan_rev uuid, p_pkg_rev uuid) RETURNS TABLE(entitlement_id uuid, already_granted boolean, superseded uuid)
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
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
END $$;


--
-- Name: p4_fias_exponent_gate(); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p4_fias_exponent_gate() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE v_kind text;
BEGIN
  IF NEW.posting_type = 'REVERSAL' THEN
    RETURN NEW;
  END IF;
  SELECT connector_kind INTO v_kind FROM iam_v2.pms_interfaces
   WHERE tenant_id = NEW.tenant_id AND site_id = NEW.site_id AND id = NEW.pms_interface_id;
  IF v_kind = 'protel-fias' AND NEW.currency_exponent IS NOT NULL AND NEW.currency_exponent <> 2 THEN
    RAISE EXCEPTION 'FIAS_EXPONENT_UNSUPPORTED: the protel-fias posting path is exponent 2 by contract '
                    '(section 9a); posting exponent is %', NEW.currency_exponent
      USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END $$;


--
-- Name: p4_financial_recovery_active(uuid, uuid); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p4_financial_recovery_active(p_tenant uuid, p_site uuid) RETURNS boolean
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
  SELECT EXISTS (SELECT 1 FROM iam_v2.financial_epochs
                  WHERE tenant_id = p_tenant AND site_id = p_site AND released_at IS NULL
                    AND reason IN ('RESTORE_DETECTED','OPERATOR_DECLARED'));
$$;


--
-- Name: p4_grant_paid_entitlement(uuid, uuid, uuid); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p4_grant_paid_entitlement(p_tenant uuid, p_site uuid, p_settlement uuid) RETURNS TABLE(entitlement_id uuid, already_granted boolean, superseded uuid)
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
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
END $$;


--
-- Name: p4_grant_quoted_entitlement(uuid, uuid, uuid); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p4_grant_quoted_entitlement(p_tenant uuid, p_site uuid, p_purchase uuid) RETURNS TABLE(entitlement_id uuid, already_granted boolean, superseded uuid)
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
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
END $$;


--
-- Name: p4_hold_financial_rails(uuid, uuid, bigint); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p4_hold_financial_rails(p_tenant uuid, p_site uuid, p_epoch bigint) RETURNS integer
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE v_held int := 0; v_n int;
BEGIN
  -- THE POSTING RAIL. This is the correction: the underlying outbox rows are moved to HELD_RECOVERY, so the
  -- worker's claim predicate (state = 'QUEUED') no longer matches them. Copying them into a ledger, as 0019
  -- did, left them exactly as claimable as before.
  --
  -- FOR UPDATE serializes against a worker claiming concurrently. Whichever transaction takes the row lock
  -- first wins: if the worker wins, its IN_FLIGHT row is then held here; if recovery wins, the worker's
  -- UPDATE finds HELD_RECOVERY and the gate below refuses it. There is no interleaving in which a claim
  -- escapes.
  PERFORM 1 FROM iam_v2.posting_outbox
   WHERE tenant_id = p_tenant AND site_id = p_site AND state IN ('QUEUED','IN_FLIGHT')
   FOR UPDATE;

  INSERT INTO iam_v2.financial_recovery_holds
    (tenant_id, site_id, epoch, work_kind, work_id, held_status, amount_minor, currency)
  SELECT o.tenant_id, o.site_id, p_epoch, 'POSTING_OUTBOX', o.id, o.state, NULL, NULL
    FROM iam_v2.posting_outbox o
   WHERE o.tenant_id = p_tenant AND o.site_id = p_site AND o.state IN ('QUEUED','IN_FLIGHT')
  ON CONFLICT DO NOTHING;
  GET DIAGNOSTICS v_n = ROW_COUNT; v_held := v_held + v_n;

  UPDATE iam_v2.posting_outbox SET state = 'HELD_RECOVERY'
   WHERE tenant_id = p_tenant AND site_id = p_site AND state IN ('QUEUED','IN_FLIGHT');

  -- THE PAYMENT RAIL. A payment cannot be "moved to held" -- its status machine is the financial record --
  -- so the hold is enforced by begin_payment_execution and the recovery gate refusing to start anything.
  -- What is recorded here is the ledger entry an operator reconciles.
  INSERT INTO iam_v2.financial_recovery_holds
    (tenant_id, site_id, epoch, work_kind, work_id, held_status, amount_minor, currency)
  SELECT t.tenant_id, t.site_id, p_epoch, 'PAYMENT_TRANSACTION', t.id, t.status, t.amount_minor, t.currency
    FROM iam_v2.payment_transactions t
   WHERE t.tenant_id = p_tenant AND t.site_id = p_site AND t.status IN ('CREATED','PENDING','UNKNOWN')
  ON CONFLICT DO NOTHING;
  GET DIAGNOSTICS v_n = ROW_COUNT; v_held := v_held + v_n;

  INSERT INTO iam_v2.financial_recovery_holds
    (tenant_id, site_id, epoch, work_kind, work_id, held_status, amount_minor, currency)
  SELECT se.tenant_id, se.site_id, p_epoch, 'SETTLEMENT', se.id, se.status, NULL, NULL
    FROM iam_v2.settlements se
   WHERE se.tenant_id = p_tenant AND se.site_id = p_site
     AND se.status IN ('REQUIRED','IN_PROGRESS','MANUAL_REVIEW')
  ON CONFLICT DO NOTHING;
  GET DIAGNOSTICS v_n = ROW_COUNT; v_held := v_held + v_n;

  RETURN v_held;
END $$;


--
-- Name: p4_insert_entitlement(uuid, uuid, uuid, uuid, uuid, uuid, jsonb, uuid, uuid, text, text, timestamp with time zone, uuid); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p4_insert_entitlement(p_tenant uuid, p_site uuid, p_voucher uuid, p_account uuid, p_principal uuid, p_purchase uuid, p_policy jsonb, p_plan_rev uuid, p_pkg_rev uuid, p_time_mode text, p_end_mode text, p_window_ends timestamp with time zone, p_supersedes uuid) RETURNS uuid
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE v_id uuid;
BEGIN
  INSERT INTO iam_v2.entitlements
    (tenant_id, site_id, voucher_id, guest_account_id, guest_principal_id, purchase_id,
     policy_snapshot, service_plan_revision_id, package_revision_id, time_accounting_mode,
     end_mode, window_ends_at, status, supersedes_entitlement_id, activated_at)
  VALUES (p_tenant, p_site, p_voucher, p_account, p_principal, p_purchase,
          p_policy, p_plan_rev, p_pkg_rev, p_time_mode,
          coalesce(nullif(p_end_mode,''), 'MANUAL_END'), p_window_ends, 'ACTIVE', p_supersedes, now())
  RETURNING id INTO v_id;
  PERFORM iam_v2.apply_entitlement_transition(v_id, 'ACTIVE', now(), 'GRANTED');
  RETURN v_id;
END $$;


--
-- Name: p4_interface_decommission_gate(); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p4_interface_decommission_gate() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE v_open int;
BEGIN
  IF NEW.lifecycle_state = 'DECOMMISSIONED' AND OLD.lifecycle_state IS DISTINCT FROM 'DECOMMISSIONED' THEN
    SELECT count(*) INTO v_open
      FROM iam_v2.posting_outbox o
     WHERE o.pms_interface_id = NEW.id AND o.state IN ('QUEUED','IN_FLIGHT','HELD_RECOVERY');
    IF v_open > 0 THEN
      RAISE EXCEPTION 'DECOMMISSION_BLOCKED: interface % still has % non-terminal posting(s)', NEW.id, v_open
        USING ERRCODE = 'check_violation';
    END IF;
    SELECT count(*) INTO v_open
      FROM iam_v2.posting_attempts a
     WHERE a.pms_interface_id = NEW.id AND a.outcome IN ('SENDING','UNKNOWN');
    IF v_open > 0 THEN
      RAISE EXCEPTION 'DECOMMISSION_BLOCKED: interface % still has % SENDING/UNKNOWN attempt(s)', NEW.id, v_open
        USING ERRCODE = 'check_violation';
    END IF;
  END IF;
  RETURN NEW;
END $$;


--
-- Name: p4_interface_freshness_block(uuid, uuid, uuid, uuid, timestamp with time zone); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p4_interface_freshness_block(p_tenant uuid, p_site uuid, p_interface uuid, p_revision uuid, p_at timestamp with time zone) RETURNS text
    LANGUAGE plpgsql STABLE
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE r record; hb_ms bigint; fresh_ms bigint; sync_ms bigint;
BEGIN
  SELECT * INTO r FROM iam_v2.pms_interface_runtime
   WHERE tenant_id = p_tenant AND site_id = p_site AND pms_interface_id = p_interface;
  IF NOT FOUND THEN
    RETURN 'RUNTIME_UNKNOWN';           -- no runtime state at all: fail closed, never assume healthy
  END IF;

  SELECT (config->>'heartbeat_timeout_ms')::bigint, (config->>'feed_freshness_ms')::bigint,
         (config->>'complete_sync_ms')::bigint
    INTO hb_ms, fresh_ms, sync_ms
    FROM iam_v2.pms_interface_revisions
   WHERE tenant_id = p_tenant AND site_id = p_site AND pms_interface_id = p_interface AND id = p_revision;

  -- axis 1: transport
  IF r.transport_status <> 'CONNECTED' THEN RETURN 'TRANSPORT_' || r.transport_status; END IF;
  IF hb_ms IS NOT NULL AND (r.last_heartbeat_at IS NULL
       OR r.last_heartbeat_at < p_at - make_interval(secs => hb_ms / 1000.0)) THEN
    RETURN 'TRANSPORT_HEARTBEAT_STALE';
  END IF;

  -- axis 2: feed continuity
  IF r.continuity_status <> 'CONTINUOUS' THEN RETURN 'CONTINUITY_' || r.continuity_status; END IF;
  IF fresh_ms IS NOT NULL AND (r.last_valid_event_at IS NULL
       OR r.last_valid_event_at < p_at - make_interval(secs => fresh_ms / 1000.0)) THEN
    RETURN 'CONTINUITY_FEED_STALE';
  END IF;

  -- axis 3: complete sync
  IF r.sync_status <> 'IN_SYNC' THEN RETURN 'SYNC_' || r.sync_status; END IF;
  IF sync_ms IS NOT NULL AND (r.last_complete_sync_at IS NULL
       OR r.last_complete_sync_at < p_at - make_interval(secs => sync_ms / 1000.0)) THEN
    RETURN 'SYNC_STALE';
  END IF;

  -- axis 4: pin coherence
  IF r.pinned_revision_id IS DISTINCT FROM p_revision THEN RETURN 'PIN_REVISION_MISMATCH'; END IF;
  IF r.published_resync_generation <> r.resync_generation_seq THEN RETURN 'PIN_RESYNC_IN_FLIGHT'; END IF;

  RETURN NULL;
END $$;


--
-- Name: p4_mark_purchase_granted(uuid); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p4_mark_purchase_granted(p_purchase uuid) RETURNS void
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE v_state text;
BEGIN
  SELECT state INTO v_state FROM iam_v2.purchases WHERE id = p_purchase FOR UPDATE;
  IF v_state IS NULL THEN
    RAISE EXCEPTION 'PURCHASE_UNKNOWN: %', p_purchase USING ERRCODE = 'no_data_found';
  END IF;
  IF v_state NOT IN ('PENDING','AWAITING_SETTLEMENT') THEN
    RAISE EXCEPTION 'PURCHASE_STATE_TRANSITION: % -> GRANTED is not an approved transition', v_state
      USING ERRCODE = 'check_violation';
  END IF;
  UPDATE iam_v2.purchases SET state = 'GRANTED' WHERE id = p_purchase;
END $$;


--
-- Name: p4_outbox_recovery_gate(); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p4_outbox_recovery_gate() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
BEGIN
  IF NEW.state = 'IN_FLIGHT' AND OLD.state <> 'IN_FLIGHT'
     AND iam_v2.p4_financial_recovery_active(NEW.tenant_id, NEW.site_id) THEN
    RAISE EXCEPTION 'FINANCIAL_RECOVERY_MODE: this site is in financial recovery; a posting may not be '
                    'claimed or transmitted until an operator has reconciled what already happened'
      USING ERRCODE = 'check_violation';
  END IF;
  -- Leaving HELD_RECOVERY has exactly two sanctioned routes, and an ordinary UPDATE is neither.
  --
  --   1. a recovery reconciliation decision, which sets the session flag below;
  --   2. the EXISTING audited retry authorization -- record_posting_review_action grants exactly one
  --      attempt and records who granted it. That is the path the contract names for making
  --      CONFIRMED_NOT_COMPLETED work retryable, so blocking it here would have replaced one gap with
  --      another: held work that can never be finished at all.
  --
  -- Route 2 is recognised structurally, from the review state, rather than by a second session flag. A flag
  -- proves a code path was taken; the authorization proves a decision was made and by whom.
  IF OLD.state = 'HELD_RECOVERY' AND NEW.state <> 'HELD_RECOVERY'
     AND coalesce(current_setting('iam_v2.p4_recovery_reconciling', true), '') <> 'on'
     AND NOT EXISTS (SELECT 1 FROM iam_v2.posting_review_state rs
                      WHERE rs.posting_id = NEW.posting_id
                        AND rs.retry_authorized_attempt_no IS NOT NULL) THEN
    RAISE EXCEPTION 'RECOVERY_HOLD_RELEASE_UNCONTROLLED: held posting work leaves HELD_RECOVERY only '
                    'through a recovery reconciliation decision or an audited retry authorization'
      USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END $$;


--
-- Name: p4_payment_admission_gate(); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p4_payment_admission_gate() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE v_status text;
BEGIN
  SELECT status INTO v_status FROM iam_v2.settlements WHERE id = NEW.settlement_id FOR UPDATE;
  IF v_status IS NULL THEN
    RAISE EXCEPTION 'PAYMENT_SETTLEMENT_UNKNOWN: settlement % does not exist', NEW.settlement_id
      USING ERRCODE = 'no_data_found';
  END IF;
  IF v_status IN ('SETTLED','FAILED','PARTIALLY_REVERSED','REVERSED') AND NEW.transaction_type = 'CHARGE' THEN
    RAISE EXCEPTION 'PAYMENT_SETTLEMENT_CLOSED: settlement is %; it cannot admit another charge. A charge '
                    'admitted here could later be CAPTURED while the settlement stays terminal, leaving '
                    'captured money whose settlement says otherwise', v_status
      USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END $$;


--
-- Name: p4_payment_creation_gate(); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p4_payment_creation_gate() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $_$
DECLARE se record; pu record; par record; v_refunded bigint;
BEGIN
  IF NEW.currency !~ '^[A-Z]{3}$' THEN
    RAISE EXCEPTION 'PAYMENT_CURRENCY_INVALID: % is not an ISO-4217 code', NEW.currency
      USING ERRCODE = 'check_violation';
  END IF;
  IF NEW.currency_exponent < 0 OR NEW.currency_exponent > 4 THEN
    RAISE EXCEPTION 'PAYMENT_EXPONENT_INVALID: %', NEW.currency_exponent USING ERRCODE = 'check_violation';
  END IF;
  -- A new financial intent starts at CREATED. Inserting one already CAPTURED would skip the whole machine.
  IF NEW.status <> 'CREATED' THEN
    RAISE EXCEPTION 'PAYMENT_MUST_START_CREATED: a payment transaction is created as CREATED, not %',
      NEW.status USING ERRCODE = 'check_violation';
  END IF;
  IF NEW.provider_txn_ref IS NOT NULL THEN
    RAISE EXCEPTION 'PAYMENT_EXTERNAL_REF_TOO_EARLY: provider_txn_ref is assigned by the provider, not at '
                    'creation' USING ERRCODE = 'check_violation';
  END IF;

  SELECT * INTO se FROM iam_v2.settlements
   WHERE tenant_id = NEW.tenant_id AND site_id = NEW.site_id AND id = NEW.settlement_id;
  IF se.id IS NULL THEN
    RAISE EXCEPTION 'PAYMENT_SETTLEMENT_UNKNOWN: settlement % is not in this tenant/site', NEW.settlement_id
      USING ERRCODE = 'foreign_key_violation';
  END IF;
  SELECT * INTO pu FROM iam_v2.purchases
   WHERE tenant_id = NEW.tenant_id AND site_id = NEW.site_id AND id = se.purchase_id;

  IF NEW.transaction_type = 'CHARGE' THEN
    IF se.method <> 'ONLINE_PAYMENT' THEN
      RAISE EXCEPTION 'PAYMENT_WRONG_RAIL: settlement method is %; an online payment charge requires '
                      'ONLINE_PAYMENT', se.method USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.amount_minor IS DISTINCT FROM pu.amount_minor
       OR NEW.currency IS DISTINCT FROM pu.currency
       OR NEW.currency_exponent IS DISTINCT FROM pu.currency_exponent THEN
      RAISE EXCEPTION 'PAYMENT_AMOUNT_NOT_SERVER_PINNED: charge %/%/% <> pinned purchase %/%/%',
        NEW.amount_minor, NEW.currency, NEW.currency_exponent,
        pu.amount_minor, pu.currency, pu.currency_exponent USING ERRCODE = 'check_violation';
    END IF;
    -- the duplicate-charge bound is now ptx_one_live_charge_per_settlement, enforced at COMMIT
    RETURN NEW;
  END IF;

  -- REFUND / CHARGEBACK. Serialize on the parent FIRST, so the sum below cannot miss a concurrent sibling.
  PERFORM pg_advisory_xact_lock(iam_v2.ns_payment_parent(NEW.parent_transaction_id::text));

  SELECT * INTO par FROM iam_v2.payment_transactions WHERE id = NEW.parent_transaction_id;
  IF par.id IS NULL THEN
    RAISE EXCEPTION 'PAYMENT_PARENT_UNKNOWN: %', NEW.parent_transaction_id
      USING ERRCODE = 'foreign_key_violation';
  END IF;
  IF par.transaction_type <> 'CHARGE' THEN
    RAISE EXCEPTION 'PAYMENT_PARENT_NOT_A_CHARGE: parent is %', par.transaction_type
      USING ERRCODE = 'check_violation';
  END IF;
  IF par.status <> 'CAPTURED' THEN
    RAISE EXCEPTION 'PAYMENT_PARENT_NOT_CAPTURED: parent is %; there is nothing to return', par.status
      USING ERRCODE = 'check_violation';
  END IF;
  IF par.tenant_id <> NEW.tenant_id OR par.site_id <> NEW.site_id
     OR par.settlement_id <> NEW.settlement_id THEN
    RAISE EXCEPTION 'PAYMENT_PARENT_OUT_OF_SCOPE: the parent belongs to a different tenant/site/settlement'
      USING ERRCODE = 'check_violation';
  END IF;
  IF par.merchant_account_id <> NEW.merchant_account_id OR par.provider <> NEW.provider THEN
    RAISE EXCEPTION 'PAYMENT_PARENT_MERCHANT_MISMATCH: money returns to the account it came from'
      USING ERRCODE = 'check_violation';
  END IF;
  IF par.currency <> NEW.currency OR par.currency_exponent <> NEW.currency_exponent THEN
    RAISE EXCEPTION 'PAYMENT_PARENT_CURRENCY_MISMATCH: %/% <> %/% (no implicit FX)',
      NEW.currency, NEW.currency_exponent, par.currency, par.currency_exponent
      USING ERRCODE = 'check_violation';
  END IF;
  SELECT coalesce(sum(amount_minor), 0) INTO v_refunded
    FROM iam_v2.payment_transactions
   WHERE parent_transaction_id = NEW.parent_transaction_id
     AND transaction_type IN ('REFUND','CHARGEBACK')
     AND status IN ('CREATED','PENDING','CAPTURED','UNKNOWN');
  IF v_refunded + NEW.amount_minor > par.amount_minor THEN
    RAISE EXCEPTION 'PAYMENT_REFUND_EXCEEDS_CHARGE: % already returned + % requested > % captured',
      v_refunded, NEW.amount_minor, par.amount_minor USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END $_$;


--
-- Name: p4_payment_identity_gate(); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p4_payment_identity_gate() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE acct record;
BEGIN
  SELECT * INTO acct FROM iam_v2.payment_provider_accounts
   WHERE tenant_id = NEW.tenant_id AND site_id = NEW.site_id AND id = NEW.merchant_account_id;
  IF acct.id IS NULL THEN
    RAISE EXCEPTION 'PAYMENT_ACCOUNT_UNKNOWN: merchant account % is not configured for this site',
      NEW.merchant_account_id USING ERRCODE = 'check_violation';
  END IF;
  IF NEW.provider IS DISTINCT FROM acct.provider THEN
    RAISE EXCEPTION 'PAYMENT_PROVIDER_MISMATCH: the transaction says provider %, the configured account '
                    'says %. A payment must name the provider it is actually going to',
      NEW.provider, acct.provider USING ERRCODE = 'check_violation';
  END IF;
  IF acct.status <> 'ACTIVE' THEN
    RAISE EXCEPTION 'PAYMENT_ACCOUNT_NOT_ACTIVE: merchant account % is %; a disabled account cannot take '
                    'money', acct.id, acct.status USING ERRCODE = 'check_violation';
  END IF;
  IF acct.currency IS NOT NULL AND acct.currency <> NEW.currency THEN
    RAISE EXCEPTION 'PAYMENT_ACCOUNT_CURRENCY: account % settles in %, this payment is in %. Phase 4 '
                    'performs no conversion', acct.id, acct.currency, NEW.currency
      USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END $$;


--
-- Name: p4_payment_status_machine(); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p4_payment_status_machine() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
BEGIN
  IF TG_OP = 'DELETE' THEN
    RAISE EXCEPTION 'PAYMENT_IMMUTABLE: payment transactions are never deleted'
      USING ERRCODE = 'feature_not_supported';
  END IF;

  IF ROW(NEW.tenant_id, NEW.site_id, NEW.settlement_id, NEW.merchant_account_id, NEW.transaction_type,
         NEW.parent_transaction_id, NEW.provider, NEW.provider_ref, NEW.idempotency_key,
         NEW.amount_minor, NEW.currency, NEW.currency_exponent, NEW.intent_created_at)
     IS DISTINCT FROM
     ROW(OLD.tenant_id, OLD.site_id, OLD.settlement_id, OLD.merchant_account_id, OLD.transaction_type,
         OLD.parent_transaction_id, OLD.provider, OLD.provider_ref, OLD.idempotency_key,
         OLD.amount_minor, OLD.currency, OLD.currency_exponent, OLD.intent_created_at) THEN
    RAISE EXCEPTION 'PAYMENT_IDENTITY_IMMUTABLE: only status and the provider reference may change'
      USING ERRCODE = 'check_violation';
  END IF;

  -- (4) write-once, and a CONFLICT is an error rather than a silent discard.
  IF OLD.provider_txn_ref IS NOT NULL AND NEW.provider_txn_ref IS DISTINCT FROM OLD.provider_txn_ref THEN
    RAISE EXCEPTION 'PAYMENT_EXTERNAL_REF_CONFLICT: this intent is already pinned to provider reference '
                    '%; a different reference (%) means two provider transactions claim one intent',
      OLD.provider_txn_ref, coalesce(NEW.provider_txn_ref, '<null>') USING ERRCODE = 'check_violation';
  END IF;

  IF NEW.status = OLD.status THEN
    RETURN NEW;
  END IF;
  IF OLD.status IN ('CAPTURED','FAILED','EXPIRED','CANCELLED','UNKNOWN') THEN
    RAISE EXCEPTION 'PAYMENT_STATUS_TERMINAL: % is terminal (attempted % -> %)',
      OLD.status, OLD.status, NEW.status USING ERRCODE = 'check_violation';
  END IF;
  -- §16: the ONLY edge out of CREATED is PENDING. An intent that was never attempted cannot be terminal.
  IF OLD.status = 'CREATED' AND NEW.status <> 'PENDING' THEN
    RAISE EXCEPTION 'PAYMENT_STATUS_TRANSITION: CREATED -> % is not an approved transition; the only edge '
                    'out of CREATED is PENDING (section 16)', NEW.status USING ERRCODE = 'check_violation';
  END IF;
  IF OLD.status = 'PENDING' AND NEW.status NOT IN
     ('CAPTURED','FAILED','EXPIRED','CANCELLED','UNKNOWN') THEN
    RAISE EXCEPTION 'PAYMENT_STATUS_TRANSITION: PENDING -> % is not an approved transition', NEW.status
      USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END $$;


--
-- Name: p4_posting_currency_gate(); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p4_posting_currency_gate() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE
  v_if_cur char(3); v_if_exp smallint;
  v_pu_cur char(3); v_pu_exp smallint; v_pkg uuid;
  v_pk_cur char(3); v_pk_exp smallint;
BEGIN
  -- (1) the posting must state its own money explicitly. mg7 left both columns nullable.
  IF NEW.currency IS NULL OR NEW.currency_exponent IS NULL THEN
    RAISE EXCEPTION 'POSTING_CURRENCY_UNSET: posting % states no currency/exponent', NEW.id
      USING ERRCODE = 'check_violation';
  END IF;

  -- (2) the PINNED interface revision must be financially onboarded. Note the lookup is composite-pinned:
  -- a revision belonging to another tenant/site/interface simply does not resolve, and NULL fails closed.
  SELECT financial_base_currency, financial_base_currency_exponent
    INTO v_if_cur, v_if_exp
    FROM iam_v2.pms_interface_revisions
   WHERE tenant_id = NEW.tenant_id AND site_id = NEW.site_id
     AND pms_interface_id = NEW.pms_interface_id AND id = NEW.posting_interface_revision_id;
  IF v_if_cur IS NULL THEN
    RAISE EXCEPTION 'INTERFACE_CURRENCY_NOT_ONBOARDED: interface % revision % has no financial base currency',
      NEW.pms_interface_id, NEW.posting_interface_revision_id USING ERRCODE = 'check_violation';
  END IF;

  -- (3) posting currency must EQUAL the pinned interface currency, exponent included. Not convertible-to.
  IF NEW.currency <> v_if_cur OR NEW.currency_exponent IS DISTINCT FROM v_if_exp THEN
    RAISE EXCEPTION 'POSTING_CURRENCY_MISMATCH: posting %/% <> pinned interface %/% (no implicit FX)',
      NEW.currency, NEW.currency_exponent, v_if_cur, v_if_exp USING ERRCODE = 'check_violation';
  END IF;

  -- (4) the pinned Purchase must be in the same currency.
  SELECT currency, currency_exponent, package_revision_id
    INTO v_pu_cur, v_pu_exp, v_pkg
    FROM iam_v2.purchases
   WHERE tenant_id = NEW.tenant_id AND site_id = NEW.site_id AND id = NEW.purchase_id;
  IF v_pu_cur IS NULL OR v_pu_exp IS NULL THEN
    RAISE EXCEPTION 'PURCHASE_CURRENCY_UNSET: purchase % states no currency/exponent', NEW.purchase_id
      USING ERRCODE = 'check_violation';
  END IF;
  IF v_pu_cur <> v_if_cur OR v_pu_exp IS DISTINCT FROM v_if_exp THEN
    RAISE EXCEPTION 'PURCHASE_CURRENCY_MISMATCH: purchase %/% <> pinned interface %/% (no implicit FX)',
      v_pu_cur, v_pu_exp, v_if_cur, v_if_exp USING ERRCODE = 'check_violation';
  END IF;

  -- (5) and so must the pinned Package Revision — this is the contract's "Package currency must equal the
  -- pinned PMS Interface currency" requirement, now that there is an interface currency to compare against.
  SELECT currency, currency_exponent INTO v_pk_cur, v_pk_exp
    FROM iam_v2.internet_package_revisions
   WHERE tenant_id = NEW.tenant_id AND site_id = NEW.site_id AND id = v_pkg;
  IF v_pk_cur IS NULL OR v_pk_exp IS NULL THEN
    RAISE EXCEPTION 'PACKAGE_CURRENCY_UNSET: package revision % states no currency/exponent', v_pkg
      USING ERRCODE = 'check_violation';
  END IF;
  IF v_pk_cur <> v_if_cur OR v_pk_exp IS DISTINCT FROM v_if_exp THEN
    RAISE EXCEPTION 'PACKAGE_CURRENCY_MISMATCH: package %/% <> pinned interface %/% (no implicit FX)',
      v_pk_cur, v_pk_exp, v_if_cur, v_if_exp USING ERRCODE = 'check_violation';
  END IF;

  RETURN NEW;
END $$;


--
-- Name: p4_posting_freshness_gate(); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p4_posting_freshness_gate() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE v_block text;
BEGIN
  IF NEW.posting_type = 'REVERSAL' THEN
    RETURN NEW;                       -- passive ledger row; it will never reach a wire
  END IF;
  v_block := iam_v2.p4_interface_freshness_block(
    NEW.tenant_id, NEW.site_id, NEW.pms_interface_id, NEW.posting_interface_revision_id, now());
  IF v_block IS NOT NULL THEN
    RAISE EXCEPTION 'INTERFACE_NOT_FRESH: %', v_block USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END $$;


--
-- Name: p4_posting_lifecycle_gate(); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p4_posting_lifecycle_gate() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE v_state text;
BEGIN
  SELECT lifecycle_state INTO v_state FROM iam_v2.pms_interfaces
   WHERE tenant_id = NEW.tenant_id AND site_id = NEW.site_id AND id = NEW.pms_interface_id;
  IF v_state IS NULL THEN
    RAISE EXCEPTION 'INTERFACE_UNKNOWN: no interface % in tenant/site', NEW.pms_interface_id
      USING ERRCODE = 'foreign_key_violation';
  END IF;
  IF NEW.posting_type = 'REVERSAL' THEN
    RETURN NEW;                       -- an audit record, not new financial work
  END IF;
  IF v_state IN ('DRAINING','DECOMMISSIONED') THEN
    RAISE EXCEPTION 'INTERFACE_NOT_ACCEPTING_WORK: interface % is %; no new financial work may be created',
      NEW.pms_interface_id, v_state USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END $$;


--
-- Name: p4_reconcile_financial_epoch(uuid, uuid, text); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p4_reconcile_financial_epoch(p_tenant uuid, p_site uuid, p_system_identity text) RETURNS text
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE cur record; v_epoch bigint;
BEGIN
  IF p_system_identity IS NULL OR btrim(p_system_identity) = '' THEN
    RAISE EXCEPTION 'RECOVERY_IDENTITY_REQUIRED: restore detection needs the running system identity'
      USING ERRCODE = 'check_violation';
  END IF;
  PERFORM pg_advisory_xact_lock(hashtext('p4.epoch:' || p_tenant::text || ':' || p_site::text));
  SELECT * INTO cur FROM iam_v2.financial_epochs
   WHERE tenant_id = p_tenant AND site_id = p_site ORDER BY epoch DESC LIMIT 1;

  IF cur.epoch IS NULL THEN
    INSERT INTO iam_v2.financial_epochs (tenant_id, site_id, epoch, system_identity, reason, released_at)
    VALUES (p_tenant, p_site, 1, p_system_identity, 'INITIAL', now());
    RETURN 'INITIALIZED';
  END IF;
  IF cur.system_identity = p_system_identity THEN
    RETURN CASE WHEN cur.released_at IS NULL THEN 'RECOVERY_ACTIVE' ELSE 'UNCHANGED' END;
  END IF;
  IF cur.released_at IS NULL THEN
    UPDATE iam_v2.financial_epochs SET system_identity = p_system_identity
     WHERE tenant_id = p_tenant AND site_id = p_site AND epoch = cur.epoch;
    PERFORM iam_v2.p4_hold_financial_rails(p_tenant, p_site, cur.epoch);
    RETURN 'RECOVERY_ACTIVE';
  END IF;

  v_epoch := cur.epoch + 1;
  INSERT INTO iam_v2.financial_epochs (tenant_id, site_id, epoch, system_identity, reason)
  VALUES (p_tenant, p_site, v_epoch, p_system_identity, 'RESTORE_DETECTED');
  PERFORM iam_v2.p4_hold_financial_rails(p_tenant, p_site, v_epoch);
  RETURN 'RECOVERY_ENTERED';
END $$;


--
-- Name: p4_reconcile_financial_epoch_v2(uuid, uuid, text, bigint, boolean); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p4_reconcile_financial_epoch_v2(p_tenant uuid, p_site uuid, p_system_identity text, p_marker_generation bigint, p_marker_present boolean) RETURNS text
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE cur record; v_epoch bigint; v_reason text := NULL; v_detect text; v_gen bigint;
BEGIN
  IF p_system_identity IS NULL OR btrim(p_system_identity) = '' THEN
    RAISE EXCEPTION 'RECOVERY_IDENTITY_REQUIRED' USING ERRCODE = 'check_violation';
  END IF;
  PERFORM pg_advisory_xact_lock(hashtext('p4.epoch:' || p_tenant::text || ':' || p_site::text));
  SELECT * INTO cur FROM iam_v2.financial_epochs
   WHERE tenant_id = p_tenant AND site_id = p_site ORDER BY epoch DESC LIMIT 1;
  v_gen := CASE WHEN p_marker_present THEN p_marker_generation ELSE 0 END;

  IF cur.epoch IS NULL THEN
    INSERT INTO iam_v2.financial_epochs
      (tenant_id, site_id, epoch, system_identity, reason, released_at, restore_generation)
    VALUES (p_tenant, p_site, 1, p_system_identity, 'INITIAL', now(), v_gen);
    RETURN 'INITIALIZED';
  END IF;

  IF p_marker_present AND p_marker_generation > cur.restore_generation THEN
    -- The database is OLDER than the appliance knows it should be: a restore happened.
    v_reason := 'MARKER_AHEAD'; v_detect := 'MANAGEMENT_MARKER';
  ELSIF p_marker_present AND p_marker_generation < cur.restore_generation THEN
    -- The MARKER is older than the database. The management partition was rolled back -- which the
    -- documented runbook can do all by itself, because it backs up /etc/stayconnect as a tar and restoring
    -- an old one carries an old marker with it.
    --
    -- Held, not ignored. The two records disagree about how many times this site has been restored, and
    -- until someone establishes which is right, nobody can say whether the financial data is current. A
    -- disagreement is exactly as much reason to stop as a detected rollback.
    v_reason := 'MARKER_BEHIND'; v_detect := 'MANAGEMENT_MARKER';
  ELSIF cur.system_identity <> p_system_identity THEN
    v_reason := 'IDENTITY_CHANGED'; v_detect := 'SYSTEM_IDENTITY';
  ELSIF cur.restore_generation > 0 AND NOT p_marker_present THEN
    v_reason := 'MARKER_MISSING'; v_detect := 'MANAGEMENT_MARKER';
  END IF;

  IF v_reason IS NULL THEN
    RETURN CASE WHEN cur.released_at IS NULL THEN 'RECOVERY_ACTIVE' ELSE 'UNCHANGED' END;
  END IF;

  IF cur.released_at IS NULL THEN
    UPDATE iam_v2.financial_epochs
       SET system_identity = p_system_identity,
           restore_generation = greatest(cur.restore_generation, v_gen)
     WHERE tenant_id = p_tenant AND site_id = p_site AND epoch = cur.epoch;
    PERFORM iam_v2.p4_hold_financial_rails(p_tenant, p_site, cur.epoch);
    RETURN 'RECOVERY_ACTIVE';
  END IF;

  v_epoch := cur.epoch + 1;
  INSERT INTO iam_v2.financial_epochs
    (tenant_id, site_id, epoch, system_identity, reason, restore_generation)
  VALUES (p_tenant, p_site, v_epoch, p_system_identity, 'RESTORE_DETECTED',
          greatest(cur.restore_generation, v_gen));
  INSERT INTO iam_v2.financial_restore_events
    (tenant_id, site_id, restore_generation, manifest_sha256, restore_kind, detected_by, restored_by)
  VALUES (p_tenant, p_site, greatest(cur.restore_generation, v_gen), repeat('0', 64),
          'UNSUPPORTED_RAW_SNAPSHOT', v_detect, v_reason)
  ON CONFLICT DO NOTHING;
  PERFORM iam_v2.p4_hold_financial_rails(p_tenant, p_site, v_epoch);
  RETURN 'RECOVERY_ENTERED';
END $$;


--
-- Name: p4_record_compliance_archive(uuid, uuid, text, text, jsonb); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p4_record_compliance_archive(p_tenant uuid, p_site uuid, p_manifest_sha text, p_artifact_path text, p_row_counts jsonb) RETURNS uuid
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $_$
DECLARE v_id uuid;
BEGIN
  IF p_manifest_sha IS NULL OR p_manifest_sha !~ '^[0-9a-f]{64}$' THEN
    RAISE EXCEPTION 'ARCHIVE_DIGEST_REQUIRED: a compliance archive is identified by the digest of the '
                    'artefact actually written' USING ERRCODE = 'check_violation';
  END IF;
  INSERT INTO iam_v2.compliance_archives
    (tenant_id, site_id, manifest_sha256, receipt_verified, purpose, artifact_path, row_counts,
     receipt_blocked_reason)
  VALUES (p_tenant, p_site, p_manifest_sha, false, 'CROSS_CUSTOMER_PURGE', p_artifact_path,
          coalesce(p_row_counts, '{}'::jsonb),
          'No external archival receipt authority exists in this product; the artefact and its digest are '
          'recorded, the counter-signature is not available')
  RETURNING id INTO v_id;
  RETURN v_id;
END $_$;


--
-- Name: p4_record_compliance_receipt(uuid, text, text); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p4_record_compliance_receipt(p_archive uuid, p_authority text, p_reference text) RETURNS void
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE a record;
BEGIN
  IF p_authority IS NULL OR btrim(p_authority) = '' OR p_reference IS NULL OR btrim(p_reference) = '' THEN
    RAISE EXCEPTION 'COMPLIANCE_RECEIPT_EVIDENCE_REQUIRED: a verified receipt names the authority that '
                    'acknowledged custody and its reference' USING ERRCODE = 'check_violation';
  END IF;
  SELECT * INTO a FROM iam_v2.compliance_archives WHERE id = p_archive FOR UPDATE;
  IF a.id IS NULL THEN
    RAISE EXCEPTION 'COMPLIANCE_ARCHIVE_UNKNOWN: %', p_archive USING ERRCODE = 'no_data_found';
  END IF;
  IF a.receipt_verified THEN
    RAISE EXCEPTION 'COMPLIANCE_RECEIPT_ALREADY_RECORDED: custody was already acknowledged by % at %',
      a.receipt_authority, a.receipt_verified_at USING ERRCODE = 'check_violation';
  END IF;
  UPDATE iam_v2.compliance_archives
     SET receipt_verified = true, receipt_authority = p_authority, receipt_reference = p_reference,
         receipt_verified_at = now(), receipt_blocked_reason = NULL
   WHERE id = p_archive;
END $$;


--
-- Name: p4_record_supported_restore(uuid, uuid, bigint, text, timestamp with time zone, text); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p4_record_supported_restore(p_tenant uuid, p_site uuid, p_generation bigint, p_manifest_sha text, p_backup_taken_at timestamp with time zone, p_restored_by text) RETURNS bigint
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $_$
DECLARE cur record; v_epoch bigint;
BEGIN
  IF p_manifest_sha IS NULL OR p_manifest_sha !~ '^[0-9a-f]{64}$' THEN
    RAISE EXCEPTION 'RESTORE_MANIFEST_REQUIRED: a supported restore is identified by its verified manifest '
                    'digest' USING ERRCODE = 'check_violation';
  END IF;
  PERFORM pg_advisory_xact_lock(hashtext('p4.epoch:' || p_tenant::text || ':' || p_site::text));
  SELECT * INTO cur FROM iam_v2.financial_epochs
   WHERE tenant_id = p_tenant AND site_id = p_site ORDER BY epoch DESC LIMIT 1;
  IF cur.epoch IS NOT NULL AND p_generation <= cur.restore_generation THEN
    RAISE EXCEPTION 'RESTORE_GENERATION_NOT_ADVANCED: the database already records generation %; a restore '
                    'must advance it', cur.restore_generation USING ERRCODE = 'check_violation';
  END IF;

  v_epoch := coalesce(cur.epoch, 0) + 1;
  IF cur.epoch IS NOT NULL AND cur.released_at IS NULL THEN
    v_epoch := cur.epoch;  -- already held; a restore during recovery does not open a second epoch
    UPDATE iam_v2.financial_epochs SET restore_generation = p_generation
     WHERE tenant_id = p_tenant AND site_id = p_site AND epoch = v_epoch;
  ELSE
    INSERT INTO iam_v2.financial_epochs
      (tenant_id, site_id, epoch, system_identity, reason, restore_generation)
    VALUES (p_tenant, p_site, v_epoch, coalesce(cur.system_identity, 'restored'),
            'RESTORE_DETECTED', p_generation);
  END IF;

  INSERT INTO iam_v2.financial_restore_events
    (tenant_id, site_id, restore_generation, manifest_sha256, backup_taken_at, restored_by,
     restore_kind, detected_by)
  VALUES (p_tenant, p_site, p_generation, p_manifest_sha, p_backup_taken_at, p_restored_by,
          'SUPPORTED', 'RESTORE_TOOL')
  ON CONFLICT DO NOTHING;

  PERFORM iam_v2.p4_hold_financial_rails(p_tenant, p_site, v_epoch);
  RETURN v_epoch;
END $_$;


--
-- Name: p4_recovery_gate(); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p4_recovery_gate() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
BEGIN
  IF iam_v2.p4_financial_recovery_active(NEW.tenant_id, NEW.site_id) THEN
    RAISE EXCEPTION 'FINANCIAL_RECOVERY_MODE: this site is in financial recovery after a restore or an '
                    'operator declaration. New financial work is held until an operator has reconciled '
                    'what already happened. Guest access is unaffected'
      USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END $$;


--
-- Name: p4_recovery_hold_immutable(); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p4_recovery_hold_immutable() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
BEGIN
  IF TG_OP = 'DELETE' THEN
    RAISE EXCEPTION 'RECOVERY_HOLD_IMMUTABLE: a recovery hold is never deleted; it is resolved'
      USING ERRCODE = 'feature_not_supported';
  END IF;
  IF OLD.resolution IS NOT NULL THEN
    RAISE EXCEPTION 'RECOVERY_HOLD_ALREADY_RESOLVED: this hold was resolved as % at %; a conclusion about '
                    'money is not revised in place', OLD.resolution, OLD.resolved_at
      USING ERRCODE = 'check_violation';
  END IF;
  IF (NEW.work_kind, NEW.work_id, NEW.epoch, NEW.held_status) IS DISTINCT FROM
     (OLD.work_kind, OLD.work_id, OLD.epoch, OLD.held_status) THEN
    RAISE EXCEPTION 'RECOVERY_HOLD_IMMUTABLE: what was held cannot be rewritten'
      USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END $$;


--
-- Name: p4_release_financial_recovery(uuid, uuid, uuid, text); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p4_release_financial_recovery(p_tenant uuid, p_site uuid, p_actor uuid, p_note text) RETURNS bigint
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE cur record; v_open int; v_sendable int; v_live int; v_inprog int;
BEGIN
  PERFORM iam_v2.p4_assert_financial_actor(p_tenant, p_actor);
  IF p_note IS NULL OR length(btrim(p_note)) < 10 THEN
    RAISE EXCEPTION 'RECOVERY_NOTE_REQUIRED: releasing recovery records why it is safe to resume'
      USING ERRCODE = 'check_violation';
  END IF;
  PERFORM pg_advisory_xact_lock(hashtext('p4.epoch:' || p_tenant::text || ':' || p_site::text));
  SELECT * INTO cur FROM iam_v2.financial_epochs
   WHERE tenant_id = p_tenant AND site_id = p_site AND released_at IS NULL;
  IF cur.epoch IS NULL THEN
    RAISE EXCEPTION 'RECOVERY_NOT_ACTIVE: this site is not in financial recovery'
      USING ERRCODE = 'no_data_found';
  END IF;

  SELECT count(*) INTO v_open FROM iam_v2.financial_recovery_holds
   WHERE tenant_id = p_tenant AND site_id = p_site AND epoch = cur.epoch AND resolution IS NULL;
  IF v_open > 0 THEN
    RAISE EXCEPTION 'RECOVERY_HOLDS_UNRESOLVED: % held item(s) have not been reconciled', v_open
      USING ERRCODE = 'check_violation';
  END IF;

  -- The check that a resolution count cannot give. A conclusion is a claim ABOUT a record; this asks the
  -- records themselves whether anything is still sendable or still in flight.
  -- Sendable work is only unsafe when nobody has accounted for it. A posting made sendable by the audited
  -- zero-attempt authorization (0025) is the opposite of unaccounted for: an operator established that the
  -- folio was never charged and authorized exactly one attempt, which is the whole point of that path.
  -- Refusing release for it would mean the one safe route out of a zero-attempt restore could never be
  -- completed -- two correct rules cancelling each other out.
  SELECT count(*) INTO v_sendable FROM iam_v2.posting_outbox o
   WHERE o.tenant_id = p_tenant AND o.site_id = p_site AND o.state IN ('QUEUED','IN_FLIGHT')
     AND NOT EXISTS (SELECT 1 FROM iam_v2.posting_review_state rs
                      WHERE rs.posting_id = o.posting_id
                        AND rs.retry_authorized_attempt_no IS NOT NULL);
  IF v_sendable > 0 THEN
    RAISE EXCEPTION 'RECOVERY_STATE_UNSAFE: % posting(s) are still sendable. Every hold may be resolved '
                    'and the underlying command still be waiting to go out', v_sendable
      USING ERRCODE = 'check_violation';
  END IF;
  SELECT count(*) INTO v_live FROM iam_v2.payment_transactions
   WHERE tenant_id = p_tenant AND site_id = p_site AND status IN ('CREATED','PENDING');
  IF v_live > 0 THEN
    RAISE EXCEPTION 'RECOVERY_STATE_UNSAFE: % payment(s) are still live', v_live
      USING ERRCODE = 'check_violation';
  END IF;
  SELECT count(*) INTO v_inprog FROM iam_v2.settlements
   WHERE tenant_id = p_tenant AND site_id = p_site AND status = 'IN_PROGRESS';
  IF v_inprog > 0 THEN
    RAISE EXCEPTION 'RECOVERY_STATE_UNSAFE: % settlement(s) are still IN_PROGRESS', v_inprog
      USING ERRCODE = 'check_violation';
  END IF;

  UPDATE iam_v2.financial_epochs
     SET released_at = now(), released_by = p_actor, release_note = p_note
   WHERE tenant_id = p_tenant AND site_id = p_site AND epoch = cur.epoch;
  RETURN cur.epoch;
END $$;


--
-- Name: p4_resolve_payment_account(uuid, uuid); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p4_resolve_payment_account(p_tenant uuid, p_site uuid) RETURNS TABLE(account_id uuid, provider text, merchant_account_ref text)
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE acct record;
BEGIN
  SELECT * INTO acct FROM iam_v2.payment_provider_accounts
   WHERE tenant_id = p_tenant AND site_id = p_site AND status = 'ACTIVE' AND is_default
   LIMIT 1;
  IF acct.id IS NULL THEN
    RAISE EXCEPTION 'PAYMENT_NO_CONFIGURED_ACCOUNT: this site has no ACTIVE default payment account; '
                    'online payment cannot be attempted' USING ERRCODE = 'no_data_found';
  END IF;
  account_id := acct.id; provider := acct.provider; merchant_account_ref := acct.merchant_account_ref;
  RETURN NEXT;
END $$;


--
-- Name: p4_resolve_recovery_hold(uuid, text, uuid, text); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p4_resolve_recovery_hold(p_hold uuid, p_resolution text, p_actor uuid, p_note text) RETURNS void
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE h record; v_tx record;
BEGIN
  IF p_resolution NOT IN ('CONFIRMED_COMPLETED','CONFIRMED_NOT_COMPLETED','ABANDONED','ESCALATED') THEN
    RAISE EXCEPTION 'RECOVERY_RESOLUTION_INVALID: %', p_resolution USING ERRCODE = 'check_violation';
  END IF;
  IF p_note IS NULL OR length(btrim(p_note)) < 10 THEN
    RAISE EXCEPTION 'RECOVERY_NOTE_REQUIRED: a reconciliation decision records HOW it was established, in '
                    'at least 10 characters' USING ERRCODE = 'check_violation';
  END IF;
  SELECT * INTO h FROM iam_v2.financial_recovery_holds WHERE id = p_hold FOR UPDATE;
  IF h.id IS NULL THEN
    RAISE EXCEPTION 'RECOVERY_HOLD_UNKNOWN: %', p_hold USING ERRCODE = 'no_data_found';
  END IF;
  PERFORM iam_v2.p4_assert_financial_actor(h.tenant_id, p_actor);

  -- The reconciliation session flag. It is set ONLY here, so the outbox gate can tell an audited decision
  -- apart from an ordinary UPDATE, and it is scoped to this transaction.
  PERFORM set_config('iam_v2.p4_recovery_reconciling', 'on', true);

  IF h.work_kind = 'POSTING_OUTBOX' THEN
    -- CONFIRMED_COMPLETED: the folio already has it. The command must stop being sendable, permanently.
    -- ABANDONED: nothing further will be done about it, which is also terminal.
    IF p_resolution IN ('CONFIRMED_COMPLETED','ABANDONED') THEN
      UPDATE iam_v2.posting_outbox SET state = 'DONE'
       WHERE id = h.work_id AND state = 'HELD_RECOVERY';
    END IF;
    -- CONFIRMED_NOT_COMPLETED and ESCALATED deliberately leave the row HELD_RECOVERY. Re-queueing it here
    -- would be exactly the automatic replay recovery exists to prevent; a retry becomes possible only
    -- through record_posting_review_action, which authorizes ONE attempt and audits who authorized it.
  ELSIF h.work_kind = 'PAYMENT_TRANSACTION' THEN
    SELECT * INTO v_tx FROM iam_v2.payment_transactions WHERE id = h.work_id FOR UPDATE;
    IF v_tx.id IS NOT NULL AND v_tx.status IN ('CREATED','PENDING') THEN
      IF p_resolution = 'CONFIRMED_NOT_COMPLETED' THEN
        -- Nothing was charged. The intent is closed; a new attempt is a new purchase flow, never a replay
        -- of this one.
        IF v_tx.status = 'CREATED' THEN
          UPDATE iam_v2.payment_transactions SET status = 'PENDING' WHERE id = v_tx.id;
        END IF;
        UPDATE iam_v2.payment_transactions SET status = 'FAILED' WHERE id = v_tx.id;
      ELSE
        -- CONFIRMED_COMPLETED, ABANDONED and ESCALATED all leave the money AMBIGUOUS from the database's
        -- point of view. Recording a capture here would settle a settlement and grant access on an
        -- operator's say-so, bypassing the authenticated provider boundary entirely -- so the payment goes
        -- to UNKNOWN and the settlement to MANUAL_REVIEW, where the existing audited model decides.
        IF v_tx.status = 'CREATED' THEN
          UPDATE iam_v2.payment_transactions SET status = 'PENDING' WHERE id = v_tx.id;
        END IF;
        UPDATE iam_v2.payment_transactions SET status = 'UNKNOWN' WHERE id = v_tx.id;
        -- The ambiguity belongs to the SETTLEMENT as much as to the payment: an operator looking at the
        -- settlement must see that its outcome is unresolved, not that it is quietly still in progress.
        UPDATE iam_v2.settlements SET status = 'MANUAL_REVIEW'
         WHERE id = v_tx.settlement_id AND status = 'IN_PROGRESS';
      END IF;
    END IF;
  ELSIF h.work_kind = 'SETTLEMENT' THEN
    -- Only an IN_PROGRESS settlement is ambiguous: something was started against it and nobody knows how
    -- it ended. A REQUIRED settlement is simply still awaiting money, which a restore did not change, and
    -- section 16 has no REQUIRED -> MANUAL_REVIEW edge -- so it is left exactly where it is rather than
    -- widening the state machine to make the reconciliation look tidier.
    UPDATE iam_v2.settlements SET status = 'MANUAL_REVIEW'
     WHERE id = h.work_id AND status = 'IN_PROGRESS';
  END IF;

  UPDATE iam_v2.financial_recovery_holds
     SET resolution = p_resolution, resolved_at = now(), resolved_by = p_actor, resolution_note = p_note
   WHERE id = p_hold;
END $$;


--
-- Name: p4_reversal_ledger_guard(); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p4_reversal_ledger_guard() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE orig record; v_already bigint;
BEGIN
  IF NEW.posting_type <> 'REVERSAL' THEN
    RETURN NEW;
  END IF;

  -- (a) Only the audited Manual-Review operation may write one. The same transaction-scoped token that
  -- guards the review ledger guards this, so a reversal cannot appear from anywhere else -- not from a
  -- worker, not from a repair script, not from an ad-hoc session.
  IF current_setting('iam_v2.p4_review_writer', true) IS DISTINCT FROM txid_current()::text THEN
    RAISE EXCEPTION 'REVERSAL_WRITER_ONLY: a reversal ledger row is created only by the audited '
                    'CREATE_REVERSAL review action'
      USING ERRCODE = 'insufficient_privilege';
  END IF;

  -- (b) It must reference a real, in-scope CHARGE. A reversal of nothing is not evidence of anything.
  SELECT id, posting_type, amount_minor, currency, currency_exponent, tenant_id, site_id, pms_interface_id
    INTO orig FROM iam_v2.pms_postings WHERE id = NEW.reverses_posting_id;
  IF orig.id IS NULL THEN
    RAISE EXCEPTION 'REVERSAL_ORIGINAL_UNKNOWN: posting % does not exist', NEW.reverses_posting_id
      USING ERRCODE = 'foreign_key_violation';
  END IF;
  IF orig.posting_type <> 'CHARGE' THEN
    RAISE EXCEPTION 'REVERSAL_ORIGINAL_NOT_A_CHARGE: % is a %', orig.id, orig.posting_type
      USING ERRCODE = 'check_violation';
  END IF;
  IF orig.tenant_id <> NEW.tenant_id OR orig.site_id <> NEW.site_id
     OR orig.pms_interface_id <> NEW.pms_interface_id THEN
    RAISE EXCEPTION 'REVERSAL_OUT_OF_SCOPE: the original belongs to a different tenant/site/interface'
      USING ERRCODE = 'check_violation';
  END IF;

  -- (c) Same money, same units. A reversal in another currency would be an implicit conversion, and the
  -- exponent has to match for the sum below to mean anything at all.
  IF NEW.currency IS DISTINCT FROM orig.currency OR NEW.currency_exponent IS DISTINCT FROM orig.currency_exponent THEN
    RAISE EXCEPTION 'REVERSAL_CURRENCY_MISMATCH: reversal %/% <> original %/% (no implicit FX)',
      NEW.currency, NEW.currency_exponent, orig.currency, orig.currency_exponent
      USING ERRCODE = 'check_violation';
  END IF;

  -- (d) TA is a POSITIVE amount here, exactly as on a charge. The direction is carried by posting_type, not
  -- by a negative number -- §9a rule 5 says a negative TA is unverified, so this schema never stores one.
  IF NEW.amount_minor <= 0 THEN
    RAISE EXCEPTION 'REVERSAL_AMOUNT_INVALID: a reversal records a POSITIVE amount; direction is carried '
                    'by posting_type, never by a negative TA (section 9a rule 5)'
      USING ERRCODE = 'check_violation';
  END IF;

  -- (e) Cumulative bound: the sum of reversals may never exceed the charge they reverse.
  SELECT coalesce(sum(amount_minor), 0) INTO v_already
    FROM iam_v2.pms_postings
   WHERE posting_type = 'REVERSAL' AND reverses_posting_id = NEW.reverses_posting_id;
  IF v_already + NEW.amount_minor > orig.amount_minor THEN
    RAISE EXCEPTION 'REVERSAL_EXCEEDS_CHARGE: % already reversed + % requested > % charged',
      v_already, NEW.amount_minor, orig.amount_minor USING ERRCODE = 'check_violation';
  END IF;

  RETURN NEW;
END $$;


--
-- Name: p4_reversal_never_executes(); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p4_reversal_never_executes() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE v_type text; v_posting uuid;
BEGIN
  -- Branch with IF, not CASE: plpgsql resolves record fields at RUNTIME, and a CASE evaluates the field
  -- reference in both arms, so NEW.posting_id would be looked up on posting_attempts and fail there.
  IF TG_TABLE_NAME = 'posting_outbox' THEN
    v_posting := NEW.posting_id;
  ELSE
    v_posting := NEW.internal_posting_id;
  END IF;
  SELECT posting_type INTO v_type FROM iam_v2.pms_postings WHERE id = v_posting;
  IF v_type = 'REVERSAL' THEN
    RAISE EXCEPTION 'REVERSAL_NOT_EXECUTABLE: programmatic PMS reversal is capability=false in v1. The '
                    'reversal ledger row is a passive audit record; correction is a manual Front Office '
                    'operation (section 9a rule 5, Gate 3B)'
      USING ERRCODE = 'feature_not_supported';
  END IF;
  RETURN NEW;
END $$;


--
-- Name: p4_review_writer_only(); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p4_review_writer_only() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE v_tok text;
BEGIN
  v_tok := current_setting('iam_v2.p4_review_writer', true);
  IF v_tok IS NULL OR v_tok <> txid_current()::text THEN
    RAISE EXCEPTION 'REVIEW_WRITER_ONLY: posting_review_actions is written only by '
                    'iam_v2.record_posting_review_action() (concurrency-safe review boundary)'
      USING ERRCODE = 'insufficient_privilege';
  END IF;
  RETURN NEW;
END $$;


--
-- Name: p4_settlement_state_machine(); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p4_settlement_state_machine() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE v_captured bigint; v_returned bigint;
BEGIN
  IF NEW.status = OLD.status THEN
    RETURN NEW;
  END IF;
  IF OLD.purchase_id <> NEW.purchase_id OR OLD.method <> NEW.method THEN
    RAISE EXCEPTION 'SETTLEMENT_IDENTITY_IMMUTABLE: purchase and method are fixed at creation'
      USING ERRCODE = 'check_violation';
  END IF;

  IF NOT (
       (OLD.status = 'REQUIRED'      AND NEW.status = 'IN_PROGRESS')
    OR (OLD.status = 'IN_PROGRESS'   AND NEW.status IN ('SETTLED','FAILED','MANUAL_REVIEW'))
    OR (OLD.status = 'MANUAL_REVIEW' AND NEW.status IN ('SETTLED','FAILED'))
    OR (OLD.status = 'SETTLED'       AND NEW.status IN ('PARTIALLY_REVERSED','REVERSED'))
    OR (OLD.status = 'PARTIALLY_REVERSED' AND NEW.status = 'REVERSED')
  ) THEN
    RAISE EXCEPTION 'SETTLEMENT_TRANSITION: % -> % is not an approved transition (section 16)',
      OLD.status, NEW.status USING ERRCODE = 'check_violation';
  END IF;

  IF NEW.status = 'SETTLED' THEN
    IF NEW.method = 'ONLINE_PAYMENT' THEN
      IF NOT EXISTS (SELECT 1 FROM iam_v2.payment_transactions
                      WHERE settlement_id = NEW.id AND transaction_type = 'CHARGE' AND status = 'CAPTURED') THEN
        RAISE EXCEPTION 'SETTLEMENT_NOT_EVIDENCED: ONLINE_PAYMENT settles only on a CAPTURED charge'
          USING ERRCODE = 'check_violation';
      END IF;
    ELSIF NEW.method = 'PMS_POSTING' THEN
      IF NOT EXISTS (SELECT 1 FROM iam_v2.pms_postings p
                       JOIN iam_v2.posting_attempts a ON a.internal_posting_id = p.id
                      WHERE p.settlement_id = NEW.id AND p.posting_type = 'CHARGE'
                        AND a.outcome = 'ACKED' AND a.pa_as_status = 'OK') THEN
        RAISE EXCEPTION 'SETTLEMENT_NOT_EVIDENCED: PMS_POSTING settles only on a posting the PMS ACKed OK'
          USING ERRCODE = 'check_violation';
      END IF;
    END IF;
  END IF;

  IF NEW.status IN ('PARTIALLY_REVERSED','REVERSED') THEN
    IF NEW.method <> 'ONLINE_PAYMENT' THEN
      RAISE EXCEPTION 'SETTLEMENT_REVERSAL_WRONG_RAIL: only an ONLINE_PAYMENT settlement is reversed by '
                      'provider refunds; the PMS rail records a PASSIVE reversal and is corrected manually'
        USING ERRCODE = 'check_violation';
    END IF;
    SELECT coalesce(sum(amount_minor),0) INTO v_captured FROM iam_v2.payment_transactions
     WHERE settlement_id = NEW.id AND transaction_type = 'CHARGE' AND status = 'CAPTURED';
    SELECT coalesce(sum(amount_minor),0) INTO v_returned FROM iam_v2.payment_transactions
     WHERE settlement_id = NEW.id AND transaction_type IN ('REFUND','CHARGEBACK') AND status = 'CAPTURED';
    IF v_returned = 0 THEN
      RAISE EXCEPTION 'SETTLEMENT_NOT_EVIDENCED: nothing has been returned' USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.status = 'REVERSED' AND v_returned < v_captured THEN
      RAISE EXCEPTION 'SETTLEMENT_PARTIAL: % of % returned; this is PARTIALLY_REVERSED', v_returned, v_captured
        USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.status = 'PARTIALLY_REVERSED' AND v_returned >= v_captured THEN
      RAISE EXCEPTION 'SETTLEMENT_FULL: % of % returned; this is REVERSED', v_returned, v_captured
        USING ERRCODE = 'check_violation';
    END IF;
  END IF;
  RETURN NEW;
END $$;


--
-- Name: p4_terminate_live_entitlement_for_subject(uuid, uuid, uuid, uuid, uuid); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p4_terminate_live_entitlement_for_subject(p_tenant uuid, p_site uuid, p_voucher uuid, p_account uuid, p_principal uuid) RETURNS uuid
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE v_id uuid;
BEGIN
  IF p_voucher IS NULL AND p_account IS NULL AND p_principal IS NULL THEN
    RAISE EXCEPTION 'GRANT_SUBJECT_REQUIRED: an entitlement always belongs to exactly one subject'
      USING ERRCODE = 'check_violation';
  END IF;
  SELECT id INTO v_id FROM iam_v2.entitlements
   WHERE tenant_id = p_tenant AND site_id = p_site AND status IN ('PENDING','ACTIVE','SUSPENDED')
     AND ( (p_voucher   IS NOT NULL AND voucher_id         = p_voucher)
        OR (p_account   IS NOT NULL AND guest_account_id   = p_account)
        OR (p_principal IS NOT NULL AND guest_principal_id = p_principal) )
   ORDER BY activated_at DESC NULLS LAST, id
   LIMIT 1 FOR UPDATE;
  IF v_id IS NULL THEN RETURN NULL; END IF;
  PERFORM iam_v2.apply_entitlement_transition(v_id, 'TERMINATED', now(), 'SUPERSEDED');
  RETURN v_id;
END $$;


--
-- Name: p5_begin_controlled_operation(text); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p5_begin_controlled_operation(p_family text) RETURNS void
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE v_token uuid;
BEGIN
  -- The allowlist IS the authorization decision, and it is also what keeps the GUC name below safe: by the
  -- time the family is concatenated it is never caller-shaped text.
  IF p_family NOT IN ('post_stay_identity','entitlement_transfer') THEN
    RAISE EXCEPTION 'no approved Phase-5 capability-scoped controlled-writer family %', p_family;
  END IF;
  DELETE FROM iam_v2.controlled_operation_scope WHERE opened_at < now() - interval '1 hour';
  v_token := gen_random_uuid();
  INSERT INTO iam_v2.controlled_operation_scope (txid, family, token)
  VALUES (txid_current(), p_family, v_token)
  ON CONFLICT (txid, family) DO UPDATE SET token = EXCLUDED.token, opened_at = now();
  PERFORM set_config('iam_v2.op_' || p_family, v_token::text, true);
END $$;


--
-- Name: p5_controlled_operation_open(text); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p5_controlled_operation_open(p_family text) RETURNS boolean
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE v_tok text;
BEGIN
  v_tok := current_setting('iam_v2.op_' || p_family, true);   -- missing_ok: NULL when never set
  IF v_tok IS NULL OR v_tok = '' THEN
    RETURN false;
  END IF;
  RETURN EXISTS (
    SELECT 1 FROM iam_v2.controlled_operation_scope s
     WHERE s.txid = txid_current() AND s.family = p_family AND s.token::text = v_tok);
END $$;


--
-- Name: p5_controlled_writer_only(); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p5_controlled_writer_only() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE owner_role text; v_oid oid; v_cap text;
BEGIN
  v_cap := CASE
    WHEN TG_TABLE_NAME = 'post_stay_profiles'     THEN 'post_stay_identity'
    WHEN TG_TABLE_NAME IN ('entitlement_transfers','stay_links') THEN 'entitlement_transfer'
    ELSE NULL END;
  IF v_cap IS NULL THEN
    RAISE EXCEPTION 'p5_controlled_writer_only attached to an unmapped table % (fail closed)', TG_TABLE_NAME;
  END IF;
  -- Resolve the opener's owner INLINE from the catalog, exactly as the Phase-3 guard does: this trigger runs
  -- as whichever role is writing, so a cross-function EXECUTE dependency would break the dedicated-owner
  -- separation Gate P needs.
  v_oid := to_regprocedure('iam_v2.p5_begin_controlled_operation(text)');
  IF v_oid IS NULL THEN
    RAISE EXCEPTION 'Phase-5 controlled-writer opener is not resolvable (fail closed)';
  END IF;
  SELECT pg_get_userbyid(proowner) INTO owner_role FROM pg_proc WHERE oid = v_oid;
  IF owner_role IS NULL OR owner_role = '' THEN
    RAISE EXCEPTION 'Phase-5 controlled-writer owner is not resolvable (fail closed)';
  END IF;
  -- DELETE is checked too. An authoritative record that can be removed outside a declared operation is a
  -- record that can be made never to have happened.
  IF current_user <> owner_role AND NOT iam_v2.p5_controlled_operation_open(v_cap) THEN
    RAISE EXCEPTION
      '%: writes to the % family require an open controlled operation (caller %) — call iam_v2.p5_begin_controlled_operation(''%'') in the transaction that performs them',
      TG_TABLE_NAME, v_cap, current_user, v_cap;
  END IF;
  RETURN CASE WHEN TG_OP = 'DELETE' THEN OLD ELSE NEW END;
END $$;


--
-- Name: p5_entitlement_transfer_guard(); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p5_entitlement_transfer_guard() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE
  v_from_iface uuid; v_to_iface uuid;
  v_from_stay_of_ent uuid; v_to_stay_of_ent uuid;
  v_from_status text; v_from_reason text; v_to_supersedes uuid;
  v_cursor uuid; v_hops int := 0;
BEGIN
  IF TG_OP <> 'INSERT' THEN
    -- Lineage is a historical fact. A transfer that can be edited or removed is a lineage that cannot be
    -- trusted to answer "where did this access come from".
    RAISE EXCEPTION 'entitlement_transfers is append-only (attempted %)', TG_OP;
  END IF;

  IF NEW.reason <> 'CROSS_PMS_TRANSFER' THEN
    RAISE EXCEPTION 'entitlement_transfers records CROSS_PMS_TRANSFER only (got %)', NEW.reason;
  END IF;

  -- (a) TWO DIFFERENT PMS INTERFACES. This is the line between a transfer and a room move, and until now
  --     nothing enforced it.
  SELECT pms_interface_id INTO v_from_iface FROM iam_v2.stays
   WHERE tenant_id=NEW.tenant_id AND site_id=NEW.site_id AND id=NEW.from_stay_id;
  SELECT pms_interface_id INTO v_to_iface FROM iam_v2.stays
   WHERE tenant_id=NEW.tenant_id AND site_id=NEW.site_id AND id=NEW.to_stay_id;
  IF v_from_iface IS NULL OR v_to_iface IS NULL THEN
    RAISE EXCEPTION 'both transfer Stays must exist in scope (fail closed)';
  END IF;
  IF v_from_iface = v_to_iface THEN
    RAISE EXCEPTION 'a cross-PMS transfer requires two DIFFERENT PMS interfaces; same-interface movement is a room move, not a transfer';
  END IF;

  -- (b) The entitlements must actually belong to the Stays named. Without this the lineage could point at a
  --     real pair of entitlements and a fictional pair of Stays.
  SELECT stay_id, status, terminal_reason INTO v_from_stay_of_ent, v_from_status, v_from_reason
    FROM iam_v2.entitlements
   WHERE tenant_id=NEW.tenant_id AND site_id=NEW.site_id AND id=NEW.from_entitlement_id;
  SELECT stay_id, supersedes_entitlement_id INTO v_to_stay_of_ent, v_to_supersedes
    FROM iam_v2.entitlements
   WHERE tenant_id=NEW.tenant_id AND site_id=NEW.site_id AND id=NEW.to_entitlement_id;
  IF v_from_stay_of_ent IS DISTINCT FROM NEW.from_stay_id THEN
    RAISE EXCEPTION 'the source entitlement does not belong to the source Stay';
  END IF;
  IF v_to_stay_of_ent IS DISTINCT FROM NEW.to_stay_id THEN
    RAISE EXCEPTION 'the destination entitlement does not belong to the destination Stay';
  END IF;

  -- (c) STATE COUPLING. A transfer row whose source is still live would claim the access moved while it is
  --     still being served in the old place.
  IF v_from_status <> 'TERMINATED' OR v_from_reason IS DISTINCT FROM 'TRANSFERRED' THEN
    RAISE EXCEPTION 'the source entitlement must be TERMINATED with reason TRANSFERRED (is % / %)',
      v_from_status, v_from_reason;
  END IF;

  -- (d) A TRANSFER IS NOT A SUPERSESSION. Supersession is same-subject; a transfer crosses Stays, which is
  --     exactly why it needs its own typed relationship. Both must never be asserted for one entitlement.
  IF v_to_supersedes IS NOT NULL THEN
    RAISE EXCEPTION 'a transfer-created entitlement carries no supersedes_entitlement_id (supersession is same-subject)';
  END IF;

  -- (e) CYCLE-FREE. from/to are UNIQUE, so the lineage is a set of chains; a cycle exists exactly when the
  --     source is reachable by walking FORWARD from the destination. The hop bound is a fail-closed backstop:
  --     a chain longer than this is not a legitimate guest movement and must not spin a trigger.
  v_cursor := NEW.to_entitlement_id;
  LOOP
    v_hops := v_hops + 1;
    IF v_hops > 64 THEN
      RAISE EXCEPTION 'transfer lineage exceeds the supported depth (fail closed)';
    END IF;
    SELECT to_entitlement_id INTO v_cursor FROM iam_v2.entitlement_transfers
     WHERE from_entitlement_id = v_cursor;
    EXIT WHEN v_cursor IS NULL;
    IF v_cursor = NEW.from_entitlement_id THEN
      RAISE EXCEPTION 'transfer would create a cycle in the entitlement lineage';
    END IF;
  END LOOP;

  RETURN NEW;
END $$;


--
-- Name: p5_post_stay_authenticable(uuid, uuid, uuid); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p5_post_stay_authenticable(p_tenant uuid, p_site uuid, p_profile uuid) RETURNS boolean
    LANGUAGE sql STABLE
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
  SELECT EXISTS (
    SELECT 1
      FROM iam_v2.post_stay_profiles p
      JOIN iam_v2.stays s
        ON s.tenant_id = p.tenant_id AND s.site_id = p.site_id AND s.id = p.origin_stay_id
     WHERE p.tenant_id = p_tenant AND p.site_id = p_site AND p.id = p_profile
       AND p.status = 'ACTIVE'
       AND p.valid_until > now()
       AND s.lifecycle_version = p.origin_lifecycle_version
       AND s.status IN ('CHECKED_OUT','POST_STAY_ACTIVE'));
$$;


--
-- Name: p5_post_stay_profile_guard(); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p5_post_stay_profile_guard() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE v_status text; v_lifecycle int;
BEGIN
  IF TG_OP = 'DELETE' THEN
    RAISE EXCEPTION 'post_stay_profiles rows are never deleted; revoke the profile instead';
  END IF;

  IF TG_OP = 'INSERT' THEN
    SELECT s.status, s.lifecycle_version INTO v_status, v_lifecycle
      FROM iam_v2.stays s
     WHERE s.tenant_id = NEW.tenant_id AND s.site_id = NEW.site_id AND s.id = NEW.origin_stay_id
     FOR UPDATE;
    IF NOT FOUND THEN
      RAISE EXCEPTION 'post-stay profile origin Stay % is not in scope (fail closed)', NEW.origin_stay_id;
    END IF;
    IF v_lifecycle <> NEW.origin_lifecycle_version THEN
      RAISE EXCEPTION 'post-stay profile must name the CURRENT Stay episode (stay is at lifecycle_version %, profile names %)',
        v_lifecycle, NEW.origin_lifecycle_version;
    END IF;
    IF v_status NOT IN ('IN_HOUSE','CHECKED_OUT') THEN
      RAISE EXCEPTION 'post-stay profile may only be issued from an IN_HOUSE or CHECKED_OUT Stay (stay is %)', v_status;
    END IF;
    -- The minting call returned the plaintext, so the reveal happened. A profile inserted WITHOUT one would
    -- be a credential nobody has ever seen and nobody can ever be given: unusable, and indistinguishable
    -- afterwards from one whose reveal was lost.
    IF NEW.pin_revealed_at IS NULL THEN
      RAISE EXCEPTION 'a post-stay profile records its one-time reveal at mint; the plaintext exists only in that response';
    END IF;
    RETURN NEW;
  END IF;

  -- UPDATE. Identity and lineage are immutable.
  IF NEW.id                          IS DISTINCT FROM OLD.id
     OR NEW.tenant_id                IS DISTINCT FROM OLD.tenant_id
     OR NEW.site_id                  IS DISTINCT FROM OLD.site_id
     OR NEW.origin_stay_id           IS DISTINCT FROM OLD.origin_stay_id
     OR NEW.origin_lifecycle_version IS DISTINCT FROM OLD.origin_lifecycle_version
     OR NEW.created_at               IS DISTINCT FROM OLD.created_at
     OR NEW.issued_at                IS DISTINCT FROM OLD.issued_at THEN
    RAISE EXCEPTION 'post-stay profile identity and origin lineage are read-only';
  END IF;
  IF OLD.status = 'REVOKED' AND NEW.status <> 'REVOKED' THEN
    RAISE EXCEPTION 'a revoked post-stay profile is never reactivated (issue a new one)';
  END IF;

  IF NEW.pin_hash IS DISTINCT FROM OLD.pin_hash THEN
    -- A NEW GENERATION. It is a different secret, minted and returned by this same statement, so it carries
    -- its OWN reveal. Inheriting the previous generation's timestamp would silently consume the new secret's
    -- one-time reveal and leave no record that it was shown.
    IF NEW.pin_generation <> OLD.pin_generation + 1 THEN
      RAISE EXCEPTION 'a new post-stay PIN must increment pin_generation by exactly 1 (% -> %)',
        OLD.pin_generation, NEW.pin_generation;
    END IF;
    IF NEW.pin_revealed_at IS NULL OR NEW.pin_revealed_at IS NOT DISTINCT FROM OLD.pin_revealed_at THEN
      RAISE EXCEPTION 'a newly minted post-stay PIN records its own reveal';
    END IF;
  ELSE
    IF NEW.pin_generation IS DISTINCT FROM OLD.pin_generation THEN
      RAISE EXCEPTION 'pin_generation may not change without a new PIN';
    END IF;
    -- SAME generation: the reveal already happened and cannot happen again. This is the rule that makes
    -- "show me that PIN again" impossible rather than merely unimplemented.
    IF NEW.pin_revealed_at IS DISTINCT FROM OLD.pin_revealed_at THEN
      RAISE EXCEPTION 'a post-stay PIN is revealed exactly once per generation';
    END IF;
  END IF;
  RETURN NEW;
END $$;


--
-- Name: p5_stay_link_guard(); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p5_stay_link_guard() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE v_from_iface uuid; v_to_iface uuid;
BEGIN
  IF TG_OP <> 'INSERT' THEN
    RAISE EXCEPTION 'stay_links is append-only (attempted %)', TG_OP;
  END IF;
  IF NEW.reason = 'POST_STAY' THEN
    RAISE EXCEPTION 'stay_links(POST_STAY) is not written: post-stay has one real Stay and its lineage is post_stay_profiles.origin_stay_id (contract 7.3)';
  END IF;
  IF NEW.from_stay = NEW.to_stay THEN
    RAISE EXCEPTION 'a stay link joins two different Stays';
  END IF;
  SELECT pms_interface_id INTO v_from_iface FROM iam_v2.stays
   WHERE tenant_id=NEW.tenant_id AND site_id=NEW.site_id AND id=NEW.from_stay;
  SELECT pms_interface_id INTO v_to_iface FROM iam_v2.stays
   WHERE tenant_id=NEW.tenant_id AND site_id=NEW.site_id AND id=NEW.to_stay;
  IF v_from_iface IS NULL OR v_to_iface IS NULL THEN
    RAISE EXCEPTION 'both linked Stays must exist in scope (fail closed)';
  END IF;
  IF v_from_iface = v_to_iface THEN
    RAISE EXCEPTION 'a CROSS_PMS_TRANSFER link requires two DIFFERENT PMS interfaces';
  END IF;
  RETURN NEW;
END $$;


--
-- Name: p6_data_crossing(uuid); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p6_data_crossing(p_entitlement uuid) RETURNS timestamp with time zone
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
  SELECT min(x.sampled_at)
    FROM iam_v2.entitlements e
    JOIN iam_v2.service_plan_revisions spr ON spr.id = e.service_plan_revision_id
    CROSS JOIN LATERAL (
      SELECT ar.sampled_at,
             sum(ar.bytes_up + ar.bytes_down) OVER (ORDER BY ar.sampled_at, ar.id) AS running
        FROM iam_v2.accounting_records ar
        JOIN iam_v2.session_entitlement_bindings b ON b.session_id = ar.session_id
         AND b.entitlement_id = e.id AND b.bound_from <= ar.sampled_at
         AND (b.bound_until IS NULL OR b.bound_until > ar.sampled_at)
    ) x
   WHERE e.id = p_entitlement
     AND spr.data_quota_bytes IS NOT NULL
     AND x.running >= spr.data_quota_bytes;
$$;


--
-- Name: FUNCTION p6_data_crossing(p_entitlement uuid); Type: COMMENT; Schema: iam_v2; Owner: -
--

COMMENT ON FUNCTION iam_v2.p6_data_crossing(p_entitlement uuid) IS 'The instant attributed usage first reached the plan quota, or NULL. The single implementation: both the expiry sweep''s candidate query and the sanctioned expiry writer call it, so they cannot disagree about when -- or whether -- a guest ran out of data.';


--
-- Name: p6_due_terminal(uuid); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p6_due_terminal(p_entitlement uuid) RETURNS TABLE(reason text, at timestamp with time zone, time_cause text)
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE
  e      record;
  v_win  timestamptz;
  v_data timestamptz;
  v_agg  timestamptz;
BEGIN
  SELECT id, status, time_accounting_mode, window_ends_at
    INTO e FROM iam_v2.entitlements WHERE id = p_entitlement;
  IF NOT FOUND OR e.status NOT IN ('ACTIVE', 'PENDING', 'SUSPENDED') THEN
    RETURN;
  END IF;

  IF e.window_ends_at IS NOT NULL AND e.window_ends_at <= now() THEN
    v_win := e.window_ends_at;
  END IF;
  v_data := iam_v2.p6_data_crossing(p_entitlement);
  -- Exhaustion counts as a terminal condition only when its instant is provable.
  v_agg := iam_v2.p6_exhaustion_instant(p_entitlement);

  IF v_win IS NULL AND v_data IS NULL AND v_agg IS NULL THEN
    RETURN;
  END IF;

  at := LEAST(COALESCE(v_win, 'infinity'::timestamptz),
              COALESCE(v_data, 'infinity'::timestamptz),
              COALESCE(v_agg, 'infinity'::timestamptz));
  IF v_data IS NOT NULL AND v_data = at THEN
    reason := 'DATA'; time_cause := NULL;
  ELSIF v_agg IS NOT NULL AND v_agg = at THEN
    reason := 'TIME'; time_cause := 'AGGREGATE_ONLINE_TIME_EXHAUSTED';
  ELSE
    reason := 'TIME';
    time_cause := CASE WHEN e.time_accounting_mode = 'AGGREGATE_ONLINE_TIME'
                       THEN 'AGGREGATE_OUTER_WINDOW_EXPIRED' ELSE 'VALIDITY_WINDOW_ELAPSED' END;
  END IF;
  RETURN NEXT;
END $$;


--
-- Name: FUNCTION p6_due_terminal(p_entitlement uuid); Type: COMMENT; Schema: iam_v2; Owner: -
--

COMMENT ON FUNCTION iam_v2.p6_due_terminal(p_entitlement uuid) IS 'The terminal condition an entitlement has ALREADY reached -- earliest of outer window, data crossing and aggregate exhaustion -- or no row when it is healthy. This is the fact the expiry writer establishes for itself instead of accepting from its caller.';


--
-- Name: p6_exhaustion_instant(uuid); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p6_exhaustion_instant(p_entitlement uuid) RETURNS timestamp with time zone
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE
  e            record;
  a            record;
  v_budget     bigint;
  v_cons_floor bigint;
  v_run_bud    bigint;
  v_val        bigint;
  v_has_budget_history boolean;
BEGIN
  SELECT id, status, time_accounting_mode, consumed_online_seconds, online_time_exhausted_at,
         service_plan_revision_id
    INTO e FROM iam_v2.entitlements WHERE id = p_entitlement;
  IF NOT FOUND OR e.time_accounting_mode <> 'AGGREGATE_ONLINE_TIME' THEN
    RETURN NULL;
  END IF;
  IF e.online_time_exhausted_at IS NOT NULL THEN
    RETURN e.online_time_exhausted_at;
  END IF;

  SELECT spr.time_quota_seconds INTO v_budget
    FROM iam_v2.service_plan_revisions spr WHERE spr.id = e.service_plan_revision_id;
  IF v_budget IS NULL OR COALESCE(e.consumed_online_seconds, 0) < v_budget THEN
    RETURN NULL;
  END IF;

  SELECT EXISTS (SELECT 1 FROM iam_v2.entitlement_adjustments adj
                  WHERE adj.entitlement_id = p_entitlement
                    AND adj.field IN ('time_quota_seconds', 'service_plan_revision_id'))
    INTO v_has_budget_history;

  IF v_has_budget_history THEN
    BEGIN
      SELECT CASE WHEN first_adj.field = 'time_quota_seconds'
                  THEN NULLIF(first_adj.old_value, '')::bigint
                  ELSE (SELECT spr.time_quota_seconds FROM iam_v2.service_plan_revisions spr
                         WHERE spr.id = NULLIF(first_adj.old_value, '')::uuid) END
        INTO v_run_bud
        FROM (SELECT adj.field, adj.old_value FROM iam_v2.entitlement_adjustments adj
               WHERE adj.entitlement_id = p_entitlement
                 AND adj.field IN ('time_quota_seconds', 'service_plan_revision_id')
               ORDER BY adj.created_at, adj.id LIMIT 1) first_adj;
    EXCEPTION WHEN others THEN
      RETURN NULL;
    END;
    IF v_run_bud IS NULL THEN
      RETURN NULL;
    END IF;
  ELSE
    v_run_bud := v_budget;
  END IF;

  v_cons_floor := NULL;

  FOR a IN
    SELECT adj.field, adj.old_value, adj.new_value, adj.created_at
      FROM iam_v2.entitlement_adjustments adj
     WHERE adj.entitlement_id = p_entitlement
       AND adj.field IN ('consumed_online_seconds', 'time_quota_seconds', 'service_plan_revision_id')
     ORDER BY adj.created_at, adj.id
  LOOP
    BEGIN
      IF a.field = 'consumed_online_seconds' THEN
        v_val := NULLIF(a.new_value, '')::bigint;
        IF v_val IS NULL THEN RETURN NULL; END IF;
        v_cons_floor := v_val;
      ELSIF a.field = 'time_quota_seconds' THEN
        v_val := NULLIF(a.new_value, '')::bigint;
        IF v_val IS NULL THEN RETURN NULL; END IF;
        v_run_bud := v_val;
      ELSE
        SELECT spr.time_quota_seconds INTO v_val FROM iam_v2.service_plan_revisions spr
         WHERE spr.id = NULLIF(a.new_value, '')::uuid;
        IF v_val IS NULL THEN RETURN NULL; END IF;
        v_run_bud := v_val;
      END IF;
    EXCEPTION WHEN others THEN
      RETURN NULL;
    END;

    IF v_cons_floor IS NOT NULL AND v_run_bud IS NOT NULL AND v_cons_floor >= v_run_bud THEN
      RETURN a.created_at;
    END IF;
  END LOOP;

  -- Warn only while nothing has been done about it. A suspended entitlement has been handled: access is
  -- withheld and the transition says why, so repeating the line every sweep would bury the ones that still
  -- need somebody.
  IF e.status IN ('ACTIVE', 'PENDING') THEN
    RAISE WARNING 'entitlement % is at or over its online-time budget and its audited history cannot prove '
                  'when it crossed; access will be withheld and it will not be terminated for TIME until '
                  'something can date it', p_entitlement;
  END IF;
  RETURN NULL;
END $$;


--
-- Name: FUNCTION p6_exhaustion_instant(p_entitlement uuid); Type: COMMENT; Schema: iam_v2; Owner: -
--

COMMENT ON FUNCTION iam_v2.p6_exhaustion_instant(p_entitlement uuid) IS 'The instant an AGGREGATE_ONLINE_TIME budget ran out, from evidence only: the tick''s stamp, else the first instant at which the adjustment-recorded LOWER BOUND on consumption had already reached the budget as it then stood, else NULL. Ordinary accrual writes no adjustment, so the history bounds consumption from below and can never be used to argue that a budget cut found the entitlement under budget.';


--
-- Name: p6_expire_entitlement(uuid); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p6_expire_entitlement(p_entitlement uuid) RETURNS TABLE(reason text, at timestamp with time zone, devices integer, sessions integer)
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE
  due        record;
  v_status   text;
  v_devices  int;
  v_sessions int;
BEGIN
  -- The entitlement row is locked FIRST, in the global lock order, so the condition cannot be established
  -- against state another transaction is changing underneath.
  SELECT status INTO v_status FROM iam_v2.entitlements WHERE id = p_entitlement FOR UPDATE;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'no such entitlement %', p_entitlement USING ERRCODE = 'foreign_key_violation';
  END IF;
  IF v_status = 'TERMINATED' THEN
    -- Idempotent: a re-run, or a sweep that lost the race, changes nothing.
    RETURN;
  END IF;

  SELECT d.reason, d.at, d.time_cause INTO due FROM iam_v2.p6_due_terminal(p_entitlement) d;
  IF due.reason IS NULL THEN
    -- THE REFUSAL THAT MATTERS. A caller naming a healthy entitlement gets nothing: no termination, no
    -- revocation, no evidence. It cannot end access by asserting that access is over.
    RAISE EXCEPTION 'entitlement % has not reached any terminal condition', p_entitlement
      USING ERRCODE = 'insufficient_privilege';
  END IF;

  -- The ONE termination path, with the reason and instant this function derived.
  PERFORM iam_v2.terminate_entitlement_at_boundary(p_entitlement, due.at, due.reason);

  -- Phase-6 time evidence, likewise derived: which clock ran out is not a caller's claim either.
  IF due.time_cause IS NOT NULL THEN
    PERFORM iam_v2.p6_record_time_termination(p_entitlement, due.time_cause);
  END IF;

  PERFORM iam_v2.begin_controlled_operation('device_auth');

  WITH closed AS (
    UPDATE iam_v2.entitlement_device_authorizations a
       SET deauthorized_at = GREATEST(due.at, a.authorized_at)
     WHERE a.entitlement_id = p_entitlement AND a.deauthorized_at IS NULL
     RETURNING a.entitlement_id, a.device_id)
  UPDATE iam_v2.entitlement_devices ed
     SET status = 'DISCONNECTED', disconnected_reason = 'ENTITLEMENT_ENDED'
    FROM closed
   WHERE ed.entitlement_id = closed.entitlement_id AND ed.device_id = closed.device_id;
  GET DIAGNOSTICS v_devices = ROW_COUNT;

  UPDATE iam_v2.sessions
     SET state = 'ended', ended = GREATEST(due.at, started), end_reason = 'ENTITLEMENT_ENDED'
   WHERE entitlement_id = p_entitlement AND state IN ('active', 'PENDING_ENFORCEMENT');
  GET DIAGNOSTICS v_sessions = ROW_COUNT;

  reason := due.reason; at := due.at; devices := v_devices; sessions := v_sessions;
  RETURN NEXT;
END $$;


--
-- Name: FUNCTION p6_expire_entitlement(p_entitlement uuid); Type: COMMENT; Schema: iam_v2; Owner: -
--

COMMENT ON FUNCTION iam_v2.p6_expire_entitlement(p_entitlement uuid) IS 'Ends an entitlement that the DATABASE says has already reached a terminal condition, at the instant it reached it, with the reason and time-cause derived here rather than supplied. Refuses a healthy entitlement. The accounting daemon holds EXECUTE on this and on nothing it calls.';


--
-- Name: p6_guest_device_actions_append_only(); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p6_guest_device_actions_append_only() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
  RAISE EXCEPTION 'iam_v2.guest_device_actions is append-only: % refused', TG_OP
    USING ERRCODE = 'restrict_violation';
END $$;


--
-- Name: p6_guest_release_device(uuid, uuid, integer); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p6_guest_release_device(p_entitlement uuid, p_device uuid, p_max_releases_per_hour integer DEFAULT 20) RETURNS text
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE
  e record;
  b record;
  live_sessions int;
  recent int;
  released boolean;
BEGIN
  -- L3 FIRST, in the global lock order, exactly as authorize_entitlement_device and
  -- deauthorize_entitlement_device do. This is the boundary the admission path also takes, which is what
  -- makes the two mutually exclusive rather than merely usually-not-simultaneous.
  SELECT id, tenant_id, site_id, status INTO e
    FROM iam_v2.entitlements WHERE id = p_entitlement FOR UPDATE;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'no such entitlement' USING ERRCODE = 'foreign_key_violation';
  END IF;

  SELECT count(*) INTO recent FROM iam_v2.guest_device_actions
   WHERE entitlement_id = p_entitlement AND action = 'RELEASE'
     AND acted_at > now() - interval '1 hour';
  IF recent >= p_max_releases_per_hour THEN
    INSERT INTO iam_v2.guest_device_actions (tenant_id, site_id, entitlement_id, device_id, action, outcome, detail)
      VALUES (e.tenant_id, e.site_id, e.id, p_device, 'RELEASE', 'REFUSED_THROTTLED',
              format('%s release attempts in the last hour', recent));
    RETURN 'REFUSED_THROTTLED';
  END IF;

  SELECT entitlement_id, device_id, status INTO b
    FROM iam_v2.entitlement_devices
   WHERE entitlement_id = p_entitlement AND device_id = p_device;
  IF NOT FOUND THEN
    INSERT INTO iam_v2.guest_device_actions (tenant_id, site_id, entitlement_id, device_id, action, outcome, detail)
      VALUES (e.tenant_id, e.site_id, e.id, p_device, 'RELEASE', 'REFUSED_NOT_FOUND',
              'the device is not bound to this entitlement');
    RETURN 'REFUSED_NOT_FOUND';
  END IF;

  IF b.status <> 'AUTHORIZED' THEN
    INSERT INTO iam_v2.guest_device_actions (tenant_id, site_id, entitlement_id, device_id, action, outcome, detail)
      VALUES (e.tenant_id, e.site_id, e.id, p_device, 'RELEASE', 'REFUSED_ALREADY_RELEASED',
              format('binding is already %s', b.status));
    RETURN 'REFUSED_ALREADY_RELEASED';
  END IF;

  -- The offline read, still inside the L3 lock. It no longer needs FOR UPDATE: holding the entitlement lock
  -- excludes the admission path entirely, and the structural guard catches anything that could still slip
  -- past a lock. Counting here decides whether to refuse; the guard decides whether the world stays coherent.
  SELECT count(*) INTO live_sessions FROM iam_v2.sessions
   WHERE entitlement_id = p_entitlement AND device_id = p_device
     AND state IN ('active', 'PENDING_ENFORCEMENT');
  IF live_sessions > 0 THEN
    INSERT INTO iam_v2.guest_device_actions (tenant_id, site_id, entitlement_id, device_id, action, outcome, detail)
      VALUES (e.tenant_id, e.site_id, e.id, p_device, 'RELEASE', 'REFUSED_ONLINE',
              format('%s live session(s) on this device', live_sessions));
    RETURN 'REFUSED_ONLINE';
  END IF;

  -- THE APPROVED PRIMITIVE, not a second implementation of it. It closes the interval, flips the binding,
  -- declares the device_auth scope and re-takes L3 (already held, so it is a no-op that keeps the contract).
  released := iam_v2.deauthorize_entitlement_device(p_entitlement, p_device, now(), 'GUEST_SELF_SERVICE');
  IF NOT released THEN
    -- No open interval: the binding said AUTHORIZED but no interval was open, which means something else
    -- closed it between the two reads. Report it as already released rather than inventing a success.
    INSERT INTO iam_v2.guest_device_actions (tenant_id, site_id, entitlement_id, device_id, action, outcome, detail)
      VALUES (e.tenant_id, e.site_id, e.id, p_device, 'RELEASE', 'REFUSED_ALREADY_RELEASED',
              'no open authorization interval');
    RETURN 'REFUSED_ALREADY_RELEASED';
  END IF;

  INSERT INTO iam_v2.guest_device_actions (tenant_id, site_id, entitlement_id, device_id, action, outcome, detail)
    VALUES (e.tenant_id, e.site_id, e.id, p_device, 'RELEASE', 'OK', 'slot released by guest self-service');
  RETURN 'OK';
END $$;


--
-- Name: FUNCTION p6_guest_release_device(p_entitlement uuid, p_device uuid, p_max_releases_per_hour integer); Type: COMMENT; Schema: iam_v2; Owner: -
--

COMMENT ON FUNCTION iam_v2.p6_guest_release_device(p_entitlement uuid, p_device uuid, p_max_releases_per_hour integer) IS 'The guest device release, and the PRIVILEGE BOUNDARY for it. SECURITY DEFINER so the guest-facing runtime role can perform a controlled release without ever holding EXECUTE on deauthorize_entitlement_device -- which would release any device on any entitlement with no throttle, no ownership scope, no offline check and no audit. The only release the runtime can perform is one that passed all four.';


--
-- Name: p6_guest_release_device_policy(uuid, uuid); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p6_guest_release_device_policy(p_entitlement uuid, p_device uuid) RETURNS text
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE
  -- The policy limit lives HERE, on the server, next to the operation it governs. It is a constant rather
  -- than a setting because a per-appliance throttle would be a per-appliance way to disable the throttle,
  -- and nothing in the product needs that.
  c_max_releases_per_hour CONSTANT int := 20;
BEGIN
  RETURN iam_v2.p6_guest_release_device(p_entitlement, p_device, c_max_releases_per_hour);
END $$;


--
-- Name: FUNCTION p6_guest_release_device_policy(p_entitlement uuid, p_device uuid); Type: COMMENT; Schema: iam_v2; Owner: -
--

COMMENT ON FUNCTION iam_v2.p6_guest_release_device_policy(p_entitlement uuid, p_device uuid) IS 'THE ONLY release entry point a runtime role may hold. Its security policy is derived server-side: the hourly throttle is not a parameter, so no caller can choose it. The three-argument form is the internal test primitive and is never granted to a runtime role -- a role that can pass its own limit can pass 2147483647 and use the approved function to bypass the approved policy.';


--
-- Name: p6_online_watermark_monotonic(); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p6_online_watermark_monotonic() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
  IF NEW.accounted_through < OLD.accounted_through THEN
    RAISE EXCEPTION 'online watermark may not move backwards (% -> %): the interval before it is already charged',
      OLD.accounted_through, NEW.accounted_through USING ERRCODE = 'check_violation';
  END IF;
  IF NEW.accounted_seconds < OLD.accounted_seconds THEN
    RAISE EXCEPTION 'accounted_seconds may not decrease (% -> %): corrections go through entitlement_adjustments',
      OLD.accounted_seconds, NEW.accounted_seconds USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END $$;


--
-- Name: p6_over_budget_now(uuid); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p6_over_budget_now(p_entitlement uuid) RETURNS boolean
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
  SELECT COALESCE(e.consumed_online_seconds, 0) >= spr.time_quota_seconds
    FROM iam_v2.entitlements e
    JOIN iam_v2.service_plan_revisions spr ON spr.id = e.service_plan_revision_id
   WHERE e.id = p_entitlement
     AND e.time_accounting_mode = 'AGGREGATE_ONLINE_TIME'
     AND spr.time_quota_seconds IS NOT NULL;
$$;


--
-- Name: FUNCTION p6_over_budget_now(p_entitlement uuid); Type: COMMENT; Schema: iam_v2; Owner: -
--

COMMENT ON FUNCTION iam_v2.p6_over_budget_now(p_entitlement uuid) IS 'Whether an AGGREGATE_ONLINE_TIME entitlement has spent its budget as of NOW, from current authoritative state. It says nothing about WHEN that happened, which is a separate and much harder question.';


--
-- Name: p6_record_time_termination(uuid, text); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p6_record_time_termination(p_entitlement uuid, p_cause text) RETURNS void
    LANGUAGE plpgsql
    AS $$
DECLARE e record; v_budget bigint;
BEGIN
  IF p_cause NOT IN ('VALIDITY_WINDOW_ELAPSED','AGGREGATE_ONLINE_TIME_EXHAUSTED','AGGREGATE_OUTER_WINDOW_EXPIRED') THEN
    RAISE EXCEPTION 'unknown time-termination cause %', p_cause USING ERRCODE = 'invalid_parameter_value';
  END IF;
  SELECT id, tenant_id, site_id, status, terminal_reason, terminated_at, time_accounting_mode,
         window_ends_at, consumed_online_seconds, service_plan_revision_id
    INTO e FROM iam_v2.entitlements WHERE id = p_entitlement FOR SHARE;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'no such entitlement %', p_entitlement USING ERRCODE = 'foreign_key_violation';
  END IF;
  SELECT spr.time_quota_seconds INTO v_budget FROM iam_v2.service_plan_revisions spr
   WHERE spr.id = e.service_plan_revision_id;

  INSERT INTO iam_v2.entitlement_termination_evidence
    (entitlement_id, tenant_id, site_id, terminal_reason, cause_detail, time_mode,
     budget_seconds, consumed_online_seconds, window_ends_at, terminated_at)
  VALUES (e.id, e.tenant_id, e.site_id, e.terminal_reason, p_cause, e.time_accounting_mode,
          v_budget, e.consumed_online_seconds, e.window_ends_at, e.terminated_at);
END $$;


--
-- Name: FUNCTION p6_record_time_termination(p_entitlement uuid, p_cause text); Type: COMMENT; Schema: iam_v2; Owner: -
--

COMMENT ON FUNCTION iam_v2.p6_record_time_termination(p_entitlement uuid, p_cause text) IS 'The sanctioned way to record why a time-mode entitlement ended. It accepts the entitlement and the cause and derives every number from the entitlement and its pinned immutable plan revision, so no caller is in a position to state a budget, a consumption or a window at all.';


--
-- Name: p6_session_requires_authorized_binding(); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p6_session_requires_authorized_binding() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE v_status text;
BEGIN
  -- Only live states are constrained. A session may END on a released binding -- that is the ordinary result
  -- of a revocation -- and forbidding it would make every deauthorization path fail.
  IF NEW.state NOT IN ('active', 'PENDING_ENFORCEMENT') THEN
    RETURN NEW;
  END IF;

  SELECT status INTO v_status FROM iam_v2.entitlement_devices
   WHERE entitlement_id = NEW.entitlement_id AND device_id = NEW.device_id;

  IF v_status IS NULL THEN
    RAISE EXCEPTION 'session for a device with no authorization binding on entitlement % (device %)',
      NEW.entitlement_id, NEW.device_id USING ERRCODE = 'restrict_violation';
  END IF;
  IF v_status <> 'AUTHORIZED' THEN
    RAISE EXCEPTION 'a % session may not exist on a % binding: the device must be re-authorized through '
      'iam_v2.authorize_entitlement_device first', NEW.state, v_status USING ERRCODE = 'restrict_violation';
  END IF;
  RETURN NEW;
END $$;


--
-- Name: FUNCTION p6_session_requires_authorized_binding(); Type: COMMENT; Schema: iam_v2; Owner: -
--

COMMENT ON FUNCTION iam_v2.p6_session_requires_authorized_binding() IS 'A live session may not exist on a binding that is not AUTHORIZED. Row locks cannot lock the ABSENCE of a row, so no lock ordering alone can stop a release and a concurrent admission from producing a DISCONNECTED binding with an active session. This makes that state unrepresentable, and forces a released device back through authorize_entitlement_device -- re-checking the device limit and opening a new interval -- before it can be online again.';


--
-- Name: p6_set_guest_device_self_service(uuid, uuid, uuid, boolean, uuid, text, text); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p6_set_guest_device_self_service(p_tenant uuid, p_site uuid, p_appliance uuid, p_on boolean, p_operator uuid, p_operator_label text, p_reason text DEFAULT NULL::text) RETURNS boolean
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE
  v_old boolean;
  -- Advisory-lock namespace for Phase-6 per-appliance product settings, following the existing convention of
  -- a fixed namespace per contended resource (Phase 1A recorded LN_DEVICE_SLOT=11 and LN_CAPACITY=7).
  c_ln_appliance_setting CONSTANT int := 61;
BEGIN
  IF p_operator_label IS NULL OR btrim(p_operator_label) = '' THEN
    RAISE EXCEPTION 'an operator label is required: "somebody changed it" is not an audit record'
      USING ERRCODE = 'invalid_parameter_value';
  END IF;

  -- THE APPLIANCE IS THE ANCHOR. The settings row may not exist yet, so locking it would order nothing; the
  -- appliance identity always exists because the settings row is foreign-keyed to it. Transaction-scoped, so
  -- it is released at commit or rollback without any explicit unlock path to forget.
  PERFORM pg_advisory_xact_lock(c_ln_appliance_setting, hashtext(p_appliance::text));

  -- The row lock is still taken when the row DOES exist: the advisory lock orders the writers, and this keeps
  -- the ordinary row-level contract for anything else that touches the row.
  SELECT guest_device_self_service INTO v_old
    FROM iam_v2.appliance_product_settings
   WHERE tenant_id = p_tenant AND site_id = p_site AND appliance_id = p_appliance
   FOR UPDATE;

  INSERT INTO iam_v2.appliance_product_settings
      (tenant_id, site_id, appliance_id, guest_device_self_service, updated_at)
  VALUES (p_tenant, p_site, p_appliance, p_on, now())
  ON CONFLICT (tenant_id, site_id, appliance_id)
  DO UPDATE SET guest_device_self_service = EXCLUDED.guest_device_self_service, updated_at = now();

  INSERT INTO iam_v2.appliance_product_setting_changes
      (tenant_id, site_id, appliance_id, setting_key, old_value, new_value,
       changed_by_operator_id, changed_by, change_reason)
  VALUES (p_tenant, p_site, p_appliance, 'guest_device_self_service', v_old, p_on,
          p_operator, p_operator_label, nullif(btrim(coalesce(p_reason, '')), ''));

  RETURN v_old IS DISTINCT FROM p_on;
END $$;


--
-- Name: FUNCTION p6_set_guest_device_self_service(p_tenant uuid, p_site uuid, p_appliance uuid, p_on boolean, p_operator uuid, p_operator_label text, p_reason text); Type: COMMENT; Schema: iam_v2; Owner: -
--

COMMENT ON FUNCTION iam_v2.p6_set_guest_device_self_service(p_tenant uuid, p_site uuid, p_appliance uuid, p_on boolean, p_operator uuid, p_operator_label text, p_reason text) IS 'The ONLY way a runtime role may change the per-appliance setting. Change and audit are one operation, so the audit is mandatory by PRIVILEGE rather than convention. Mutations are serialized per APPLIANCE on a transaction advisory lock, because the settings row may not exist yet and a lock on an absent row orders nothing -- two concurrent first writes would each record a transition from "unset".';


--
-- Name: p6_setting_changes_append_only(); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p6_setting_changes_append_only() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
  RAISE EXCEPTION 'iam_v2.appliance_product_setting_changes is append-only: % refused', TG_OP
    USING ERRCODE = 'restrict_violation';
END $$;


--
-- Name: p6_skipped_intervals_append_only(); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p6_skipped_intervals_append_only() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
  RAISE EXCEPTION 'iam_v2.online_time_skipped_intervals is append-only (attempted %)', TG_OP
    USING ERRCODE = 'restrict_violation';
END $$;


--
-- Name: p6_suspend_over_budget(uuid, uuid); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p6_suspend_over_budget(p_tenant uuid, p_site uuid) RETURNS TABLE(entitlement uuid, devices integer, sessions integer)
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE
  e          record;
  v_devices  int;
  v_sessions int;
BEGIN
  FOR e IN
    SELECT en.id
      FROM iam_v2.entitlements en
     WHERE en.tenant_id = p_tenant AND en.site_id = p_site
       AND en.time_accounting_mode = 'AGGREGATE_ONLINE_TIME'
       AND en.status IN ('ACTIVE', 'PENDING')          -- already SUSPENDED: nothing to do, and nothing to say
       AND iam_v2.p6_over_budget_now(en.id)
       AND iam_v2.p6_exhaustion_instant(en.id) IS NULL -- datable ones terminate through the normal path
     ORDER BY en.id
     FOR UPDATE
  LOOP
    -- The status change goes through the SAME writer every other status change uses, so the transition
    -- history stays the single account of what happened to this entitlement. now() is honest here: this is
    -- when the system decided to withhold access, not a claim about when the budget ran out.
    PERFORM iam_v2.apply_entitlement_transition(e.id, 'SUSPENDED', now(), 'AGGREGATE_OVER_BUDGET');

    -- Fail closed on what it already holds. Closing an authorization interval is a device-authorization
    -- write, so the scope is declared, exactly as the expiry writer does.
    PERFORM iam_v2.begin_controlled_operation('device_auth');

    WITH closed AS (
      UPDATE iam_v2.entitlement_device_authorizations a
         SET deauthorized_at = GREATEST(now(), a.authorized_at)
       WHERE a.entitlement_id = e.id AND a.deauthorized_at IS NULL
       RETURNING a.entitlement_id, a.device_id)
    UPDATE iam_v2.entitlement_devices ed
       SET status = 'DISCONNECTED', disconnected_reason = 'ENTITLEMENT_SUSPENDED'
      FROM closed
     WHERE ed.entitlement_id = closed.entitlement_id AND ed.device_id = closed.device_id;
    GET DIAGNOSTICS v_devices = ROW_COUNT;

    UPDATE iam_v2.sessions se
       SET state = 'ended', ended = GREATEST(now(), se.started), end_reason = 'ENTITLEMENT_SUSPENDED'
     WHERE se.entitlement_id = e.id AND se.state IN ('active', 'PENDING_ENFORCEMENT');
    GET DIAGNOSTICS v_sessions = ROW_COUNT;

    entitlement := e.id; devices := v_devices; sessions := v_sessions;
    RETURN NEXT;
  END LOOP;
END $$;


--
-- Name: FUNCTION p6_suspend_over_budget(p_tenant uuid, p_site uuid); Type: COMMENT; Schema: iam_v2; Owner: -
--

COMMENT ON FUNCTION iam_v2.p6_suspend_over_budget(p_tenant uuid, p_site uuid) IS 'Withholds access from aggregate entitlements that are over budget now and whose crossing instant cannot be proven: SUSPENDED through the ordinary transition writer, devices and sessions closed with the non-terminal reason ENTITLEMENT_SUSPENDED. No terminated_at, no terminal_reason and no TIME evidence at either level, because nothing here is a claim that the entitlement ended.';


--
-- Name: p6_termination_evidence_append_only(); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p6_termination_evidence_append_only() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
  RAISE EXCEPTION 'iam_v2.entitlement_termination_evidence is append-only: % refused', TG_OP
    USING ERRCODE = 'restrict_violation';
END $$;


--
-- Name: p6_termination_evidence_matches_transition(); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p6_termination_evidence_matches_transition() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE e record; real_budget bigint;
BEGIN
  SELECT status, terminal_reason, terminated_at, time_accounting_mode, window_ends_at,
         consumed_online_seconds, service_plan_revision_id
    INTO e FROM iam_v2.entitlements
   WHERE id = NEW.entitlement_id AND tenant_id = NEW.tenant_id AND site_id = NEW.site_id
   FOR SHARE;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'termination evidence for an entitlement that does not exist in this scope (%)',
      NEW.entitlement_id USING ERRCODE = 'foreign_key_violation';
  END IF;
  IF e.status <> 'TERMINATED' THEN
    RAISE EXCEPTION 'termination evidence for entitlement % which is % , not TERMINATED: evidence may not describe a transition that did not occur',
      NEW.entitlement_id, e.status USING ERRCODE = 'restrict_violation';
  END IF;
  IF e.terminal_reason IS DISTINCT FROM NEW.terminal_reason THEN
    RAISE EXCEPTION 'termination evidence claims reason % but the entitlement terminated with %',
      NEW.terminal_reason, e.terminal_reason USING ERRCODE = 'restrict_violation';
  END IF;
  IF e.terminated_at IS DISTINCT FROM NEW.terminated_at THEN
    RAISE EXCEPTION 'termination evidence claims % but the entitlement terminated at %',
      NEW.terminated_at, e.terminated_at USING ERRCODE = 'restrict_violation';
  END IF;
  IF e.time_accounting_mode IS DISTINCT FROM NEW.time_mode THEN
    RAISE EXCEPTION 'termination evidence claims time mode % but the entitlement is %',
      NEW.time_mode, e.time_accounting_mode USING ERRCODE = 'restrict_violation';
  END IF;

  -- THE NUMBERS MUST BE THE REAL ONES, not merely numbers that agree with each other. Everything above this
  -- point makes a row internally consistent, and an internally consistent fabrication is still a fabrication:
  -- a caller could invent a budget and a consumption that satisfy every CHECK and describe a termination that
  -- never had those values. So each is compared against the state the termination actually used -- the
  -- entitlement's own counter, and the budget on the IMMUTABLE plan revision pinned into it at grant time.
  IF NEW.consumed_online_seconds IS NOT NULL
     AND NEW.consumed_online_seconds IS DISTINCT FROM e.consumed_online_seconds THEN
    RAISE EXCEPTION 'termination evidence claims % consumed seconds but the entitlement recorded %',
      NEW.consumed_online_seconds, e.consumed_online_seconds USING ERRCODE = 'restrict_violation';
  END IF;

  SELECT spr.time_quota_seconds INTO real_budget
    FROM iam_v2.service_plan_revisions spr
   WHERE spr.id = e.service_plan_revision_id;
  IF NEW.budget_seconds IS NOT NULL AND NEW.budget_seconds IS DISTINCT FROM real_budget THEN
    RAISE EXCEPTION 'termination evidence claims a % second budget but the pinned plan revision states %',
      NEW.budget_seconds, coalesce(real_budget::text, 'none') USING ERRCODE = 'restrict_violation';
  END IF;

  -- An outer-window expiry must name the entitlement's OWN immutable window. window_ends_at is stamped once
  -- and never moves, so evidence naming a different instant is describing a boundary that does not exist.
  IF NEW.window_ends_at IS NOT NULL AND NEW.window_ends_at IS DISTINCT FROM e.window_ends_at THEN
    RAISE EXCEPTION 'termination evidence names window % but the entitlement''s immutable window is %',
      NEW.window_ends_at, coalesce(e.window_ends_at::text, 'none') USING ERRCODE = 'restrict_violation';
  END IF;
  IF NEW.cause_detail = 'AGGREGATE_OUTER_WINDOW_EXPIRED' AND e.window_ends_at IS NULL THEN
    RAISE EXCEPTION 'outer-window expiry evidence for an entitlement that has no window at all'
      USING ERRCODE = 'restrict_violation';
  END IF;

  RETURN NEW;
END $$;


--
-- Name: p6_tick_online_time(uuid, uuid, timestamp with time zone, integer, uuid[], timestamp with time zone[]); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.p6_tick_online_time(p_tenant uuid, p_site uuid, p_now timestamp with time zone, p_max_charge_seconds integer, p_capped_entitlements uuid[] DEFAULT '{}'::uuid[], p_caps timestamp with time zone[] DEFAULT '{}'::timestamp with time zone[]) RETURNS TABLE(entitlement_id uuid, exhausted_at timestamp with time zone)
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE
  e           record;
  s           record;
  v_budget    bigint;
  v_before    bigint;
  v_charged   numeric;      -- exact seconds charged this tick, before flooring
  v_base      timestamptz;
  v_ceiling   timestamptz;  -- the last instant that is billable at all
  v_billable  timestamptz;  -- the last instant this tick may charge (observation bound)
  v_charge    numeric;
  v_remaining numeric;
  -- The billable intervals of this tick, one pair per contributing session. They are what the crossing is
  -- computed from: a rate that changes at every boundary cannot be derived from a count alone.
  v_starts    timestamptz[];
  v_ends      timestamptz[];
  v_points    timestamptz[];
  v_acc       numeric;
  v_rate      int;
  v_seg       numeric;
  v_p0        timestamptz;
  v_p1        timestamptz;
  v_cross     timestamptz;
  v_cap       timestamptz;   -- the caller's earliest known terminal instant for this entitlement
  k           int;
  i           int;
BEGIN
  IF p_max_charge_seconds IS NULL OR p_max_charge_seconds <= 0 THEN
    RAISE EXCEPTION 'a charge bound is required: unbounded accrual would charge unobserved time'
      USING ERRCODE = 'invalid_parameter_value';
  END IF;

  FOR e IN
    SELECT en.id, en.consumed_online_seconds, en.service_plan_revision_id, en.online_time_exhausted_at,
           en.window_ends_at
      FROM iam_v2.entitlements en
     WHERE en.tenant_id = p_tenant AND en.site_id = p_site
       AND en.time_accounting_mode = 'AGGREGATE_ONLINE_TIME'
       AND en.status IN ('ACTIVE', 'PENDING', 'SUSPENDED')
     ORDER BY en.id
     FOR UPDATE
  LOOP
    SELECT spr.time_quota_seconds INTO v_budget
      FROM iam_v2.service_plan_revisions spr WHERE spr.id = e.service_plan_revision_id;

    -- The caller's cap for this entitlement, if it named one. A DATA crossing that already happened is a
    -- terminal instant like any other: nothing after it was access, so nothing after it is billable.
    v_cap := NULL;
    IF array_length(p_capped_entitlements, 1) IS NOT NULL THEN
      FOR i IN 1 .. array_length(p_capped_entitlements, 1) LOOP
        IF p_capped_entitlements[i] = e.id THEN
          v_cap := p_caps[i];
          EXIT;
        END IF;
      END LOOP;
    END IF;

    v_before  := COALESCE(e.consumed_online_seconds, 0);

    -- Already exhausted and still live: charge nothing more, and report again so a caller that failed to
    -- terminate last time still converges -- but ONLY with a provable instant.
    --
    -- This branch used to stamp now() when the entitlement had reached its budget without a stamp, which
    -- wrote a historical fact nobody observed and then handed it to the termination, the evidence row and
    -- every session end time. p6_exhaustion_instant answers from evidence or answers NULL, and NULL means
    -- nothing is claimed: no stamp, no report, no termination for TIME. The entitlement stays subject to its
    -- window and its data quota, and the next tick that observes real online time crosses the budget
    -- properly and stamps it then.
    IF v_budget IS NOT NULL AND (e.online_time_exhausted_at IS NOT NULL OR v_before >= v_budget) THEN
      exhausted_at := iam_v2.p6_exhaustion_instant(e.id);
      IF exhausted_at IS NULL THEN
        CONTINUE;
      END IF;
      entitlement_id := e.id;
      RETURN NEXT;
      CONTINUE;
    END IF;

    v_charged := 0;
    v_starts  := ARRAY[]::timestamptz[];
    v_ends    := ARRAY[]::timestamptz[];

    FOR s IN
      SELECT se.id, se.state, se.started, se.ended, w.accounted_through
        FROM iam_v2.sessions se
        LEFT JOIN iam_v2.session_online_watermarks w ON w.session_id = se.id
       WHERE se.entitlement_id = e.id
         AND (se.state = 'active' OR (se.ended IS NOT NULL AND w.accounted_through IS NOT NULL
                                      AND w.accounted_through < se.ended))
       ORDER BY se.id
       FOR UPDATE OF se
    LOOP
      -- THE CEILING: the earliest applicable boundary. now, the session's own end, and -- the correction --
      -- the entitlement's immutable outer window. Access ended at the window, so no second after it was ever
      -- access, whatever the session row still says.
      v_ceiling := LEAST(p_now, COALESCE(s.ended, p_now),
                         COALESCE(e.window_ends_at, 'infinity'::timestamptz),
                         COALESCE(v_cap, 'infinity'::timestamptz));

      -- First observation: charge nothing, baseline here. A session with no watermark has never been seen by
      -- this path, so the interval since it started is not evidence of anything -- it may have been spent
      -- PENDING_ENFORCEMENT with no forwarding at all.
      IF s.accounted_through IS NULL THEN
        IF EXTRACT(EPOCH FROM (v_ceiling - s.started)) > p_max_charge_seconds THEN
          INSERT INTO iam_v2.online_time_skipped_intervals
            (tenant_id, site_id, session_id, entitlement_id, skipped_from, skipped_to, cause)
          VALUES (p_tenant, p_site, s.id, e.id, s.started, v_ceiling, 'UNOBSERVED_GAP');
        END IF;
        INSERT INTO iam_v2.session_online_watermarks
          (tenant_id, site_id, session_id, accounted_through, accounted_seconds)
        VALUES (p_tenant, p_site, s.id, GREATEST(v_ceiling, s.started), 0)
        ON CONFLICT (session_id) DO NOTHING;
        CONTINUE;
      END IF;

      v_base := s.accounted_through;
      IF v_ceiling <= v_base THEN
        CONTINUE;   -- nothing billable: already charged through, or entirely past the boundary
      END IF;

      -- The observation bound. Anything beyond it was not watched and is recorded rather than charged.
      v_billable := LEAST(v_ceiling, v_base + make_interval(secs => p_max_charge_seconds));
      IF v_billable < v_ceiling THEN
        INSERT INTO iam_v2.online_time_skipped_intervals
          (tenant_id, site_id, session_id, entitlement_id, skipped_from, skipped_to, cause)
        VALUES (p_tenant, p_site, s.id, e.id, v_billable, v_ceiling, 'UNOBSERVED_GAP');
      END IF;

      v_charge := EXTRACT(EPOCH FROM (v_billable - v_base));
      IF v_charge <= 0 THEN
        CONTINUE;
      END IF;

      -- The watermark advances to the CEILING: the skipped remainder has been recorded, and leaving the
      -- watermark short of it would charge it on the next tick after all.
      INSERT INTO iam_v2.session_online_watermarks
        (tenant_id, site_id, session_id, accounted_through, accounted_seconds)
      VALUES (p_tenant, p_site, s.id, v_ceiling, FLOOR(v_charge)::bigint)
      ON CONFLICT (session_id) DO UPDATE
        SET accounted_through = EXCLUDED.accounted_through,
            accounted_seconds = iam_v2.session_online_watermarks.accounted_seconds + EXCLUDED.accounted_seconds,
            updated_at = now();

      v_charged := v_charged + v_charge;
      v_starts  := v_starts || v_base;
      v_ends    := v_ends || v_billable;
    END LOOP;

    IF v_charged <= 0 THEN
      CONTINUE;
    END IF;

    IF v_budget IS NOT NULL AND v_before + v_charged >= v_budget THEN
      v_remaining := GREATEST(0, v_budget - v_before);

      -- THE PIECEWISE CROSSING. Every interval boundary is a point where the burn rate changes, so the
      -- budget is walked segment by segment: rate = how many intervals cover the segment, and the crossing
      -- falls inside the first segment whose contribution reaches what was left.
      SELECT array_agg(p ORDER BY p) INTO v_points
        FROM (SELECT DISTINCT unnest(v_starts || v_ends) AS p) x;

      v_acc   := 0;
      v_cross := NULL;
      FOR k IN 1 .. COALESCE(array_length(v_points, 1), 0) - 1 LOOP
        v_p0 := v_points[k];
        v_p1 := v_points[k + 1];
        v_rate := 0;
        FOR i IN 1 .. array_length(v_starts, 1) LOOP
          IF v_starts[i] <= v_p0 AND v_ends[i] >= v_p1 THEN
            v_rate := v_rate + 1;
          END IF;
        END LOOP;
        IF v_rate = 0 THEN
          CONTINUE;   -- a gap: nobody is online, and nothing burns
        END IF;
        v_seg := EXTRACT(EPOCH FROM (v_p1 - v_p0)) * v_rate;
        IF v_acc + v_seg >= v_remaining THEN
          v_cross := v_p0 + make_interval(secs => ((v_remaining - v_acc) / v_rate)::double precision);
          EXIT;
        END IF;
        v_acc := v_acc + v_seg;
      END LOOP;
      IF v_cross IS NULL THEN
        -- Only reachable through floating-point slack at the very last instant; the last boundary is the
        -- honest answer and it is never later than the window or now, both of which capped every interval.
        v_cross := v_points[array_length(v_points, 1)];
      END IF;

      exhausted_at := v_cross;
      UPDATE iam_v2.entitlements
         SET consumed_online_seconds = v_budget, online_time_exhausted_at = v_cross
       WHERE id = e.id;
      entitlement_id := e.id;
      RETURN NEXT;
    ELSE
      UPDATE iam_v2.entitlements
         SET consumed_online_seconds = v_before + FLOOR(v_charged)::bigint
       WHERE id = e.id;
    END IF;
  END LOOP;
END $$;


--
-- Name: FUNCTION p6_tick_online_time(p_tenant uuid, p_site uuid, p_now timestamp with time zone, p_max_charge_seconds integer, p_capped_entitlements uuid[], p_caps timestamp with time zone[]); Type: COMMENT; Schema: iam_v2; Owner: -
--

COMMENT ON FUNCTION iam_v2.p6_tick_online_time(p_tenant uuid, p_site uuid, p_now timestamp with time zone, p_max_charge_seconds integer, p_capped_entitlements uuid[], p_caps timestamp with time zone[]) IS 'One AGGREGATE_ONLINE_TIME accrual tick for a site. Billable only to the earliest of now, session end, the immutable outer window and any cap the caller passes; charges only sessions in state active, within the per-tick observation bound. Exhaustion is the exact piecewise crossing, and an already-exhausted entitlement is re-reported only with an instant that can be proven -- never with the clock of the tick that noticed. It terminates nothing.';


--
-- Name: publish_checkout_grace_config(uuid, uuid, uuid, integer, integer, integer, bigint, integer, text, integer); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.publish_checkout_grace_config(p_tenant uuid, p_site uuid, p_pkg_rev uuid, p_duration integer, p_down integer, p_up integer, p_quota bigint, p_dev_limit integer, p_dev_policy text, p_eligibility integer) RETURNS bigint
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE v_ver bigint;
BEGIN
  -- (item 2) eligibility_window_seconds is an AUTHORITATIVE grace-policy field: validated, versioned, compared
  -- for idempotency and included in material-change detection exactly like the shaping/quota/device fields.
  IF p_eligibility IS NULL OR p_eligibility <= 0 OR p_eligibility > 604800 THEN
    RAISE EXCEPTION 'eligibility_window_seconds must be within 1..604800 (got %)', p_eligibility;
  END IF;
  PERFORM pg_advisory_xact_lock(hashtextextended(p_tenant::text || ':' || p_site::text || ':grace-config', 0));
  SELECT config_version INTO v_ver FROM iam_v2.site_checkout_grace_config
    WHERE tenant_id=p_tenant AND site_id=p_site FOR UPDATE;
  IF v_ver IS NULL THEN
    INSERT INTO iam_v2.site_checkout_grace_config
      (tenant_id,site_id,grace_package_revision_id,grace_duration_seconds,grace_down_kbps,grace_up_kbps,
       grace_data_quota_bytes,grace_device_limit,grace_device_limit_policy,eligibility_window_seconds,config_version)
      VALUES (p_tenant,p_site,p_pkg_rev,p_duration,p_down,p_up,p_quota,p_dev_limit,p_dev_policy,p_eligibility,1);
    RETURN 1;
  END IF;
  -- idempotent re-publication of the IDENTICAL typed policy does NOT bump the version (a material change does).
  IF EXISTS (SELECT 1 FROM iam_v2.site_checkout_grace_config
             WHERE tenant_id=p_tenant AND site_id=p_site
               AND grace_package_revision_id IS NOT DISTINCT FROM p_pkg_rev
               AND grace_duration_seconds IS NOT DISTINCT FROM p_duration
               AND grace_down_kbps IS NOT DISTINCT FROM p_down AND grace_up_kbps IS NOT DISTINCT FROM p_up
               AND grace_data_quota_bytes IS NOT DISTINCT FROM p_quota
               AND grace_device_limit IS NOT DISTINCT FROM p_dev_limit
               AND grace_device_limit_policy IS NOT DISTINCT FROM p_dev_policy
               AND eligibility_window_seconds IS NOT DISTINCT FROM p_eligibility) THEN
    RETURN v_ver;
  END IF;
  UPDATE iam_v2.site_checkout_grace_config SET
    grace_package_revision_id=p_pkg_rev, grace_duration_seconds=p_duration, grace_down_kbps=p_down,
    grace_up_kbps=p_up, grace_data_quota_bytes=p_quota, grace_device_limit=p_dev_limit,
    grace_device_limit_policy=p_dev_policy, eligibility_window_seconds=p_eligibility, config_version=v_ver+1
    WHERE tenant_id=p_tenant AND site_id=p_site;
  RETURN v_ver+1;
END $$;


--
-- Name: publish_checkout_grace_policy(uuid, uuid, uuid, integer, integer, integer, bigint, integer, text, integer, integer, uuid, text); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.publish_checkout_grace_policy(p_tenant uuid, p_site uuid, p_pkg_rev uuid, p_duration integer, p_down integer, p_up integer, p_quota bigint, p_dev_limit integer, p_dev_policy text, p_eligibility integer, p_expected_version integer, p_actor uuid, p_reason text) RETURNS integer
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $_$
DECLARE v_current int; v_new int; v_mismatch text;
BEGIN
  -- This operation writes a capability-scoped family, so it declares its own scope. Doing it here
  -- rather than relying on ownership is what lets Gate-P give every function its own owner without
  -- any of them losing the right to perform its own writes.
  PERFORM iam_v2.begin_controlled_operation('grace_publication');
  -- ACTOR: an existing, active operator of this tenant. A policy nobody can be held to is not governed.
  IF p_actor IS NULL THEN RAISE EXCEPTION 'GRACE_ACTOR_INVALID: an actor is required'; END IF;
  PERFORM 1 FROM public.operators o
    WHERE o.id = p_actor AND o.status = 'active' AND (o.tenant_id IS NULL OR o.tenant_id = p_tenant);
  IF NOT FOUND THEN
    RAISE EXCEPTION 'GRACE_ACTOR_INVALID: actor % is not an active operator of this tenant', p_actor;
  END IF;
  -- A publication with no recorded reason is an unattributable change to what every departing guest receives.
  IF p_reason IS NULL OR p_reason !~ '^[A-Z][A-Z0-9_]{0,63}$' THEN
    RAISE EXCEPTION 'GRACE_ACTOR_INVALID: a bounded machine reason code is required';
  END IF;

  -- POLICY: only the capability that actually exists. DISCONNECT_OLDEST and ADMIN_APPROVAL are refused rather
  -- than accepted-and-approximated, because a policy the enforcement path cannot honour is worse than none.
  IF p_dev_policy <> 'REJECT_NEW_DEVICE' THEN
    RAISE EXCEPTION 'GRACE_POLICY_UNSUPPORTED: device limit policy % is not implemented', p_dev_policy;
  END IF;

  -- PACKAGE GRAPH: validated by THE SAME function the Checkout conversion uses, so a policy that would later
  -- be judged invalid (and silently fall back to Emergency Grace on every departure) is refused NOW, while an
  -- operator is looking at it. A NULL package is refused for exactly that reason: it is not a policy, it is a
  -- guaranteed Emergency fallback wearing a success message.
  v_mismatch := iam_v2.grace_package_mismatch_reason(p_tenant, p_site, p_pkg_rev,
                                                     p_duration, p_down, p_up, p_quota, p_dev_limit, p_dev_policy);
  IF v_mismatch IS NOT NULL THEN
    RAISE EXCEPTION 'GRACE_PACKAGE_INVALID: %', v_mismatch;
  END IF;

  -- OPTIMISTIC VERSION: the caller edited what it last read. Two operators publishing at once produce one
  -- winner and one explicit conflict, never a silent overwrite. 0 means "I believe nothing is published yet".
  -- It is MANDATORY here, not just at the HTTP layer: a NULL that meant "skip concurrency control" would make
  -- the database's own guarantee depend on a caller remembering to ask for it.
  IF p_expected_version IS NULL OR p_expected_version < 0 THEN
    RAISE EXCEPTION 'GRACE_VERSION_CONFLICT: an expected config_version (>= 0) is required';
  END IF;
  SELECT config_version INTO v_current FROM iam_v2.site_checkout_grace_config
    WHERE tenant_id = p_tenant AND site_id = p_site FOR UPDATE;
  IF COALESCE(v_current,0) <> p_expected_version THEN
    RAISE EXCEPTION 'GRACE_VERSION_CONFLICT: current version is % (caller expected %)', COALESCE(v_current,0), p_expected_version;
  END IF;

  v_new := iam_v2.publish_checkout_grace_config(p_tenant, p_site, p_pkg_rev, p_duration, p_down, p_up,
                                                p_quota, p_dev_limit, p_dev_policy, p_eligibility);

  -- IMMUTABLE publication audit. An identical re-publish is idempotent in the writer (the version does not
  -- move), so the audit is only appended when a NEW version was actually created — the record then means
  -- exactly what it says: this actor put this policy in force.
  INSERT INTO iam_v2.checkout_grace_policy_publications
    (tenant_id, site_id, config_version, actor, reason_code, grace_package_revision_id, policy_snapshot)
    SELECT p_tenant, p_site, v_new, p_actor, p_reason, p_pkg_rev,
           jsonb_build_object('grace_duration_seconds', p_duration, 'grace_down_kbps', p_down,
                              'grace_up_kbps', p_up, 'grace_data_quota_bytes', p_quota,
                              'grace_device_limit', p_dev_limit, 'grace_device_limit_policy', p_dev_policy,
                              'eligibility_window_seconds', p_eligibility)
    WHERE NOT EXISTS (SELECT 1 FROM iam_v2.checkout_grace_policy_publications
                      WHERE tenant_id = p_tenant AND site_id = p_site AND config_version = v_new);
  RETURN v_new;
END $_$;


--
-- Name: rebind_session_entitlement(uuid, uuid, timestamp with time zone); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.rebind_session_entitlement(p_session uuid, p_ent uuid, p_at timestamp with time zone) RETURNS uuid
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE v_t uuid; v_s uuid; v_open uuid; v_from timestamptz; v_cur uuid; v_seq bigint; v_id uuid;
BEGIN
  -- This operation writes a capability-scoped family, so it declares its own scope. Doing it here
  -- rather than relying on ownership is what lets Gate-P give every function its own owner without
  -- any of them losing the right to perform its own writes.
  PERFORM iam_v2.begin_controlled_operation('session_binding');
  SELECT tenant_id, site_id, entitlement_id INTO v_t, v_s, v_cur FROM iam_v2.sessions WHERE id = p_session FOR UPDATE;
  IF v_t IS NULL THEN RAISE EXCEPTION 'session % not found', p_session; END IF;
  PERFORM 1 FROM iam_v2.entitlements WHERE id = p_ent AND tenant_id = v_t AND site_id = v_s;
  IF NOT FOUND THEN RAISE EXCEPTION 'entitlement % is not in the session scope', p_ent; END IF;
  SELECT id, bound_from INTO v_open, v_from FROM iam_v2.session_entitlement_bindings
    WHERE session_id = p_session AND bound_until IS NULL;
  IF v_open IS NOT NULL THEN
    IF p_at < v_from THEN RAISE EXCEPTION 'rebinding at % precedes the open interval start %', p_at, v_from; END IF;
    UPDATE iam_v2.session_entitlement_bindings SET bound_until = p_at WHERE id = v_open;
  END IF;
  SELECT COALESCE(max(seq),0)+1 INTO v_seq FROM iam_v2.session_entitlement_bindings WHERE session_id = p_session;
  INSERT INTO iam_v2.session_entitlement_bindings(tenant_id,site_id,session_id,entitlement_id,seq,bound_from)
    VALUES (v_t,v_s,p_session,p_ent,v_seq,p_at) RETURNING id INTO v_id;
  UPDATE iam_v2.sessions SET entitlement_id = p_ent WHERE id = p_session;
  RETURN v_id;
END $$;


--
-- Name: record_alert_action(uuid, uuid, uuid, text, uuid, text, text); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.record_alert_action(p_tenant uuid, p_site uuid, p_audit uuid, p_action text, p_actor uuid, p_reason text, p_expected_state text) RETURNS bigint
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $_$
DECLARE v_seq bigint; v_head text; v_head_seq bigint; v_audit uuid;
BEGIN
  -- This operation writes a capability-scoped family, so it declares its own scope. Doing it here
  -- rather than relying on ownership is what lets Gate-P give every function its own owner without
  -- any of them losing the right to perform its own writes.
  PERFORM iam_v2.begin_controlled_operation('alert');
  IF p_action NOT IN ('ACKNOWLEDGED','RESOLVED') THEN
    -- OPEN belongs to the audit that raised the alert; an operator can only move it forward.
    RAISE EXCEPTION 'ALERT_ACTION_INVALID: % is not an operator action', p_action;
  END IF;
  -- Mandatory, and enforced HERE rather than only at the HTTP layer: an operator action with no reason is an
  -- unexplained state change in an audit trail whose whole purpose is explaining state changes.
  IF p_reason IS NULL OR p_reason !~ '^[A-Z][A-Z0-9_]{0,63}$' THEN
    RAISE EXCEPTION 'ALERT_ACTION_INVALID: a bounded machine reason code is required';
  END IF;
  -- NULL must never mean "act against whatever state you find": that is precisely the race the expected-state
  -- check exists to prevent.
  IF p_expected_state IS NULL OR p_expected_state NOT IN ('OPEN','ACKNOWLEDGED') THEN
    RAISE EXCEPTION 'ALERT_STATE_CONFLICT: an expected state of OPEN or ACKNOWLEDGED is required';
  END IF;
  -- scope: the alert must belong to THIS tenant+site, and the row lock serializes the whole lifecycle
  SELECT id INTO v_audit FROM iam_v2.checkout_grace_audit
    WHERE id = p_audit AND tenant_id = p_tenant AND site_id = p_site AND alert_code IS NOT NULL
    FOR UPDATE;
  IF v_audit IS NULL THEN
    RAISE EXCEPTION 'ALERT_NOT_FOUND: no alert % in this scope', p_audit;
  END IF;
  -- actor: an existing, active operator of the SAME tenant. An action nobody can be held to is not an audit.
  IF p_actor IS NULL THEN
    RAISE EXCEPTION 'ALERT_ACTOR_INVALID: an actor is required';
  END IF;
  PERFORM 1 FROM public.operators o
    WHERE o.id = p_actor AND o.status = 'active'
      AND (o.tenant_id IS NULL OR o.tenant_id = p_tenant);
  IF NOT FOUND THEN
    RAISE EXCEPTION 'ALERT_ACTOR_INVALID: actor % is not an active operator of this tenant', p_actor;
  END IF;
  SELECT action, seq INTO v_head, v_head_seq FROM iam_v2.checkout_grace_alert_actions
    WHERE audit_id = p_audit ORDER BY seq DESC LIMIT 1;
  IF v_head IS NULL THEN
    RAISE EXCEPTION 'ALERT_NOT_FOUND: alert % has no lifecycle', p_audit;
  END IF;
  -- optimistic state match: the caller acted on what it last saw, and nothing has moved since.
  IF p_expected_state <> v_head THEN
    RAISE EXCEPTION 'ALERT_STATE_CONFLICT: alert is % (caller expected %)', v_head, p_expected_state;
  END IF;
  IF v_head = 'RESOLVED' THEN
    RAISE EXCEPTION 'ALERT_STATE_CONFLICT: alert is already RESOLVED';
  END IF;
  IF v_head = 'ACKNOWLEDGED' AND p_action = 'ACKNOWLEDGED' THEN
    RAISE EXCEPTION 'ALERT_STATE_CONFLICT: alert is already ACKNOWLEDGED';
  END IF;
  v_seq := v_head_seq + 1;
  INSERT INTO iam_v2.checkout_grace_alert_actions(tenant_id, site_id, audit_id, seq, action, actor, reason_code)
    VALUES (p_tenant, p_site, p_audit, v_seq, p_action, p_actor, p_reason);
  RETURN v_seq;
END $_$;


--
-- Name: record_auth_context_offer(uuid, uuid, uuid, uuid, integer, bigint, timestamp with time zone); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.record_auth_context_offer(p_tenant uuid, p_site uuid, p_auth_context uuid, p_package_revision uuid, p_tier integer, p_evidence_version bigint, p_expires_at timestamp with time zone) RETURNS uuid
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE v_id uuid; v_consumed timestamptz;
BEGIN
  IF p_evidence_version IS NULL OR p_evidence_version <= 0 THEN
    RAISE EXCEPTION 'OFFER_INVALID: an offer must record the evidence version it was decided under';
  END IF;
  SELECT consumed_at INTO v_consumed FROM iam_v2.auth_contexts
    WHERE id = p_auth_context AND tenant_id = p_tenant AND site_id = p_site;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'OFFER_INVALID: auth context % is not in this tenant/site', p_auth_context;
  END IF;
  IF v_consumed IS NOT NULL THEN
    -- Offering something to a context that has already been redeemed would let a second grant find an offer
    -- that was never shown to the guest at the time they proved who they were.
    RAISE EXCEPTION 'OFFER_INVALID: auth context % is already consumed', p_auth_context;
  END IF;
  INSERT INTO iam_v2.auth_context_offers
    (tenant_id, site_id, auth_context_id, package_revision_id, matched_tier_order, evidence_version, expires_at)
    VALUES (p_tenant, p_site, p_auth_context, p_package_revision, p_tier, p_evidence_version, p_expires_at)
    ON CONFLICT (auth_context_id, package_revision_id) DO NOTHING
    RETURNING id INTO v_id;
  RETURN v_id;
END $$;


--
-- Name: record_posting_review_action(uuid, text, uuid, text, jsonb, integer, bigint); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.record_posting_review_action(p_posting uuid, p_action text, p_actor uuid, p_reason text, p_evidence jsonb DEFAULT '{}'::jsonb, p_expected_version integer DEFAULT NULL::integer, p_reversal_amount bigint DEFAULT NULL::bigint) RETURNS uuid
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE
  v_t uuid; v_s uuid; st record; v_action_id uuid; v_next_attempt int; v_attempts int; la record;
  o record; v_amount bigint; v_rev uuid;
BEGIN
  IF p_action NOT IN ('CONFIRM_POSTED','CONFIRM_NOT_POSTED_RETRY','CONFIRM_NOT_POSTED_ABANDON',
                      'CREATE_REVERSAL','ESCALATE') THEN
    RAISE EXCEPTION 'REVIEW_ACTION_UNKNOWN: % is not in the approved review catalog', p_action
      USING ERRCODE = 'check_violation';
  END IF;
  IF p_actor IS NULL OR p_reason IS NULL OR btrim(p_reason) = '' THEN
    RAISE EXCEPTION 'REVIEW_ACTOR_REASON_REQUIRED: every financial review decision is attributable'
      USING ERRCODE = 'check_violation';
  END IF;
  IF p_action <> 'ESCALATE'
     AND (p_evidence IS NULL OR jsonb_typeof(p_evidence) <> 'object' OR p_evidence = '{}'::jsonb) THEN
    RAISE EXCEPTION 'REVIEW_EVIDENCE_REQUIRED: a terminal financial decision must record its evidence'
      USING ERRCODE = 'check_violation';
  END IF;

  SELECT * INTO o FROM iam_v2.pms_postings WHERE id = p_posting;
  IF o.id IS NULL THEN
    RAISE EXCEPTION 'REVIEW_POSTING_UNKNOWN: posting % does not exist', p_posting
      USING ERRCODE = 'foreign_key_violation';
  END IF;
  v_t := o.tenant_id; v_s := o.site_id;

  PERFORM pg_advisory_xact_lock(iam_v2.ns_financial_review(p_posting::text));
  INSERT INTO iam_v2.posting_review_state (posting_id, tenant_id, site_id)
  VALUES (p_posting, v_t, v_s) ON CONFLICT (posting_id) DO NOTHING;
  SELECT * INTO st FROM iam_v2.posting_review_state WHERE posting_id = p_posting FOR UPDATE;

  IF p_expected_version IS NOT NULL AND p_expected_version <> st.review_version THEN
    RAISE EXCEPTION 'REVIEW_VERSION_STALE: expected version %, current is %',
      p_expected_version, st.review_version USING ERRCODE = 'serialization_failure';
  END IF;

  SELECT count(*) INTO v_attempts FROM iam_v2.posting_attempts WHERE internal_posting_id = p_posting;
  IF v_attempts = 0 AND p_action <> 'ESCALATE' THEN
    RAISE EXCEPTION 'REVIEW_NOT_APPLICABLE: posting % has no transmission attempt to decide about', p_posting
      USING ERRCODE = 'check_violation';
  END IF;

  IF p_action <> 'ESCALATE' AND st.terminal_action IS NOT NULL THEN
    IF st.terminal_action = p_action THEN
      RAISE EXCEPTION 'REVIEW_ALREADY_DECIDED: posting % is already decided as %', p_posting, st.terminal_action
        USING ERRCODE = 'unique_violation';
    END IF;
    RAISE EXCEPTION 'REVIEW_CONFLICT: posting % is already decided as %; % is incompatible',
      p_posting, st.terminal_action, p_action USING ERRCODE = 'unique_violation';
  END IF;

  SELECT attempt_no, outcome, pa_as_status INTO la
    FROM iam_v2.posting_attempts WHERE internal_posting_id = p_posting
    ORDER BY attempt_no DESC LIMIT 1;

  -- THE ACTION/STATE MATRIX.
  IF p_action = 'CONFIRM_NOT_POSTED_RETRY' THEN
    IF la.outcome = 'ACKED' AND la.pa_as_status = 'OK' THEN
      RAISE EXCEPTION 'REVIEW_RETRY_REFUSED: attempt % was ACKed OK by the PMS; retrying it would post the '
                      'charge twice. Use CREATE_REVERSAL or CONFIRM_POSTED.', la.attempt_no
        USING ERRCODE = 'check_violation';
    END IF;
    IF la.outcome = 'SENDING' THEN
      RAISE EXCEPTION 'REVIEW_RETRY_REFUSED: attempt % is still SENDING; its outcome is not yet known',
        la.attempt_no USING ERRCODE = 'check_violation';
    END IF;
  END IF;

  -- CREATE_REVERSAL corrects money the PMS is believed to hold. Reversing a charge nobody thinks was
  -- posted would put a correction in the ledger for a debit that never happened.
  IF p_action = 'CREATE_REVERSAL' THEN
    IF o.posting_type <> 'CHARGE' THEN
      RAISE EXCEPTION 'REVIEW_REVERSAL_REFUSED: only a CHARGE can be reversed' USING ERRCODE = 'check_violation';
    END IF;
    IF la.outcome = 'SENDING' THEN
      RAISE EXCEPTION 'REVIEW_REVERSAL_REFUSED: attempt % is still SENDING; its outcome is not yet known',
        la.attempt_no USING ERRCODE = 'check_violation';
    END IF;
    IF NOT (la.outcome = 'UNKNOWN' OR (la.outcome = 'ACKED' AND la.pa_as_status = 'OK')) THEN
      RAISE EXCEPTION 'REVIEW_REVERSAL_REFUSED: the latest attempt is %/%; nothing is believed posted, so '
                      'there is nothing to reverse. Use CONFIRM_NOT_POSTED_ABANDON.',
        la.outcome, coalesce(la.pa_as_status,'-') USING ERRCODE = 'check_violation';
    END IF;
    v_amount := coalesce(p_reversal_amount, o.amount_minor);
  ELSIF p_reversal_amount IS NOT NULL THEN
    RAISE EXCEPTION 'REVIEW_AMOUNT_NOT_APPLICABLE: only CREATE_REVERSAL carries an amount'
      USING ERRCODE = 'check_violation';
  END IF;

  PERFORM set_config('iam_v2.p4_review_writer', txid_current()::text, true);

  INSERT INTO iam_v2.posting_review_actions (tenant_id, site_id, posting_id, action, actor, reason, evidence)
  VALUES (v_t, v_s, p_posting, p_action, p_actor, p_reason, coalesce(p_evidence, '{}'::jsonb))
  RETURNING id INTO v_action_id;

  IF p_action = 'CREATE_REVERSAL' THEN
    -- §15: "a new ledger row referencing the original". It pins the SAME evidence the original pinned, so
    -- the correction is attached to the same authorization rather than to a fresh resolution.
    INSERT INTO iam_v2.pms_postings
      (tenant_id, site_id, pms_interface_id, settlement_id, purchase_id, stay_id, folio_id,
       posting_interface_revision_id, secret_generation_id, posting_type, reverses_posting_id,
       amount_minor, currency, currency_exponent, idempotency_key)
    VALUES (o.tenant_id, o.site_id, o.pms_interface_id, o.settlement_id, o.purchase_id, o.stay_id, o.folio_id,
            o.posting_interface_revision_id, o.secret_generation_id, 'REVERSAL', o.id,
            v_amount, o.currency, o.currency_exponent, o.idempotency_key || ':rev:' || v_action_id::text)
    RETURNING id INTO v_rev;
  END IF;

  PERFORM set_config('iam_v2.p4_review_writer', '', true);

  IF p_action = 'ESCALATE' THEN
    UPDATE iam_v2.posting_review_state
       SET escalation_count = escalation_count + 1, review_version = review_version + 1, updated_at = now()
     WHERE posting_id = p_posting;
  ELSE
    IF p_action = 'CONFIRM_NOT_POSTED_RETRY' THEN
      SELECT coalesce(max(attempt_no), 0) + 1 INTO v_next_attempt
        FROM iam_v2.posting_attempts WHERE internal_posting_id = p_posting;
    ELSE
      v_next_attempt := NULL;
    END IF;
    UPDATE iam_v2.posting_review_state
       SET terminal_action = p_action, terminal_action_id = v_action_id, decided_at = now(),
           retry_authorized_attempt_no = v_next_attempt, reversal_posting_id = v_rev,
           review_version = review_version + 1, updated_at = now()
     WHERE posting_id = p_posting;
  END IF;

  RETURN v_action_id;
END $$;


--
-- Name: register_class_origin(uuid, uuid, uuid, uuid, text, integer, bigint, bigint, bigint, timestamp with time zone); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.register_class_origin(p_tenant uuid, p_site uuid, p_session uuid, p_source_device uuid, p_bridge text, p_class_minor integer, p_epoch bigint, p_origin_up bigint, p_origin_down bigint, p_created_at timestamp with time zone) RETURNS text
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE v_started timestamptz; v_device uuid; v_ended timestamptz; v_iface text; v_ip inet; cp record;
BEGIN
  IF p_origin_up IS NULL OR p_origin_down IS NULL OR p_origin_up < 0 OR p_origin_down < 0 THEN
    RAISE EXCEPTION 'ACCT_INVALID: origin counters must be non-negative';
  END IF;
  IF p_epoch IS NULL OR p_epoch < 1 OR p_created_at IS NULL THEN
    RAISE EXCEPTION 'ACCT_INVALID: a class origin needs a source epoch (>= 1) and a creation time';
  END IF;

  -- the SAME source coherence the ingestion operation enforces: an origin is a checkpoint, and a checkpoint
  -- for a source tuple that does not describe this Session would be a way to pre-seed someone else's series
  SELECT started, device_id, ended, ingress_interface, ip
    INTO v_started, v_device, v_ended, v_iface, v_ip
    FROM iam_v2.sessions WHERE id = p_session AND tenant_id = p_tenant AND site_id = p_site;
  IF v_started IS NULL THEN
    RAISE EXCEPTION 'ACCT_SESSION_OUT_OF_SCOPE: session % is not in this tenant/site', p_session;
  END IF;
  IF v_ended IS NOT NULL THEN
    RAISE EXCEPTION 'ACCT_SESSION_OUT_OF_SCOPE: session % has ended', p_session;
  END IF;
  IF v_device IS DISTINCT FROM p_source_device
     OR v_iface IS DISTINCT FROM p_bridge
     OR iam_v2.p3_expected_class_minor(v_ip) IS DISTINCT FROM p_class_minor THEN
    RAISE EXCEPTION 'ACCT_SOURCE_MISMATCH: the class origin does not describe session %', p_session;
  END IF;

  SELECT * INTO cp FROM iam_v2.accounting_checkpoints
    WHERE session_id = p_session AND source_device_id = p_source_device
      AND bridge = p_bridge AND class_minor = p_class_minor
    FOR UPDATE;

  IF cp.id IS NULL THEN
    INSERT INTO iam_v2.accounting_checkpoints
      (tenant_id, site_id, session_id, source_device_id, bridge, class_minor, source_epoch,
       prev_bytes_up, prev_bytes_down, prev_sampled_at, last_classification)
      VALUES (p_tenant, p_site, p_session, p_source_device, p_bridge, p_class_minor, p_epoch,
              p_origin_up, p_origin_down, p_created_at, 'BASELINED');
    RETURN 'ORIGIN_REGISTERED';
  END IF;

  IF p_epoch < cp.source_epoch THEN
    RAISE EXCEPTION 'ACCT_STALE_EPOCH: origin epoch % is older than the accepted epoch %', p_epoch, cp.source_epoch;
  END IF;
  IF p_epoch = cp.source_epoch THEN
    -- The same class generation is already registered. Re-registering would move the origin forward and
    -- silently forgive whatever was used since — which is exactly the loss this operation exists to close.
    RETURN 'ORIGIN_UNCHANGED';
  END IF;

  -- A NEW generation: the class was replaced, so its counters legitimately restart from the stated origin.
  UPDATE iam_v2.accounting_checkpoints
     SET source_epoch = p_epoch, prev_bytes_up = p_origin_up, prev_bytes_down = p_origin_down,
         prev_sampled_at = p_created_at, last_classification = 'RESET_BASELINED', updated_at = now()
   WHERE id = cp.id;
  RETURN 'ORIGIN_RESET';
END $$;


--
-- Name: reserve_device_slot(uuid, uuid, text, text, integer); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.reserve_device_slot(p_ent uuid, p_dev uuid, p_cred text, p_appliance text, p_max integer) RETURNS text
    LANGUAGE plpgsql
    AS $$
DECLARE t uuid; s uuid; cnt int;
BEGIN
  SELECT tenant_id, site_id INTO t, s FROM iam_v2.entitlements WHERE id=p_ent;
  PERFORM pg_advisory_xact_lock(iam_v2.ns_device_slot(p_cred));   -- 1) device-slot namespace (11), before capacity
  PERFORM pg_advisory_xact_lock(iam_v2.ns_capacity(p_appliance)); -- 2) capacity namespace (7)
  -- reconnect: same device re-authorizes without burning a new slot
  IF EXISTS (SELECT 1 FROM iam_v2.entitlement_devices WHERE entitlement_id=p_ent AND device_id=p_dev) THEN
    UPDATE iam_v2.entitlement_devices SET status='AUTHORIZED', last_authorized=now()
      WHERE entitlement_id=p_ent AND device_id=p_dev;
    RETURN 'RECONNECT';
  END IF;
  SELECT count(*) INTO cnt FROM iam_v2.entitlement_devices WHERE entitlement_id=p_ent AND status='AUTHORIZED';
  IF cnt >= p_max THEN RETURN 'MAX_DEVICES_REACHED'; END IF;
  INSERT INTO iam_v2.entitlement_devices(tenant_id,site_id,entitlement_id,device_id,status,first_authorized,last_authorized)
    VALUES (t,s,p_ent,p_dev,'AUTHORIZED',now(),now());
  RETURN 'AUTHORIZED';
END; $$;


--
-- Name: selectable_grace_packages(uuid, uuid); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.selectable_grace_packages(p_tenant uuid, p_site uuid) RETURNS TABLE(package_revision_id uuid, package_code text, revision_no integer, service_plan_revision_id uuid, service_plan_code text, service_plan_revision_no integer, down_kbps integer, up_kbps integer, data_quota_bytes bigint, device_limit integer, device_limit_policy text, time_accounting_mode text, grace_duration_seconds integer, end_mode text, policy_version text, settlement_mode text, is_current boolean, is_active boolean, mismatch_reason text)
    LANGUAGE sql STABLE
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $_$
  WITH candidate AS (
    SELECT ipr.id AS package_revision_id, ip.code AS package_code, ipr.revision_no,
           spr.id AS service_plan_revision_id, sp.code AS service_plan_code, spr.revision_no AS service_plan_revision_no,
           spr.down_kbps, spr.up_kbps, spr.data_quota_bytes,
           spr.max_concurrent_devices AS device_limit, spr.device_limit_policy, spr.time_accounting_mode,
           -- a non-numeric or oversized duration yields NULL, which the validator then rejects for THIS row
           CASE WHEN jsonb_typeof(ipr.duration_policy->'grace_duration_seconds') = 'number'
                     AND (ipr.duration_policy->>'grace_duration_seconds') ~ '^[0-9]{1,9}$'
                THEN (ipr.duration_policy->>'grace_duration_seconds')::int END AS grace_duration_seconds,
           CASE WHEN jsonb_typeof(ipr.duration_policy->'end_mode') = 'string'
                THEN ipr.duration_policy->>'end_mode' END AS end_mode,
           CASE WHEN jsonb_typeof(ipr.duration_policy->'policy_version') = 'string'
                THEN ipr.duration_policy->>'policy_version' END AS policy_version,
           array_to_string(ipr.settlement_methods, ',') AS settlement_mode,
           (ip.current_revision_id = ipr.id) AS is_current, ip.active AS is_active
      FROM iam_v2.internet_package_revisions ipr
      JOIN iam_v2.internet_packages ip
        ON ip.tenant_id = ipr.tenant_id AND ip.site_id = ipr.site_id AND ip.id = ipr.package_id
      JOIN iam_v2.service_plan_revisions spr
        ON spr.tenant_id = ipr.tenant_id AND spr.site_id = ipr.site_id AND spr.id = ipr.service_plan_revision_id
      JOIN iam_v2.service_plans sp
        ON sp.tenant_id = spr.tenant_id AND sp.site_id = spr.site_id AND sp.id = spr.service_plan_id
     WHERE ipr.tenant_id = p_tenant AND ipr.site_id = p_site
       AND ipr.package_type = 'CHECKOUT_GRACE')
  SELECT c.package_revision_id, c.package_code, c.revision_no,
         c.service_plan_revision_id, c.service_plan_code, c.service_plan_revision_no,
         c.down_kbps, c.up_kbps, c.data_quota_bytes,
         c.device_limit, c.device_limit_policy, c.time_accounting_mode,
         c.grace_duration_seconds, c.end_mode, c.policy_version,
         c.settlement_mode, c.is_current, c.is_active,
         -- judged by the SAME function publication uses, against the candidate's OWN values
         iam_v2.grace_package_mismatch_reason(p_tenant, p_site, c.package_revision_id,
             COALESCE(c.grace_duration_seconds, -1), c.down_kbps, c.up_kbps, c.data_quota_bytes,
             c.device_limit, c.device_limit_policy) AS mismatch_reason
    FROM candidate c
   ORDER BY c.package_code, c.revision_no DESC;
$_$;


--
-- Name: supersede_entitlement_transition(uuid, text, timestamp with time zone, text); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.supersede_entitlement_transition(p_target uuid, p_to text, p_at timestamp with time zone, p_reason text) RETURNS uuid
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $_$
DECLARE v_ent uuid; v_seq bigint; v_new uuid; v_term text; v_head uuid; v_from text;
BEGIN
  IF p_reason IS NOT NULL AND p_reason !~ '^[A-Z][A-Z0-9_]{0,63}$' THEN
    RAISE EXCEPTION 'transition reason must be a bounded machine code';
  END IF;
  IF p_to NOT IN ('PENDING','ACTIVE','SUSPENDED','TERMINATED') THEN
    RAISE EXCEPTION 'invalid target state %', p_to;
  END IF;
  SELECT entitlement_id INTO v_ent
    FROM iam_v2.entitlement_state_transitions WHERE id = p_target AND superseded_by IS NULL;
  IF v_ent IS NULL THEN RAISE EXCEPTION 'transition % not found or already superseded', p_target; END IF;
  -- L3 Entitlement lock before any write (global lock order)
  PERFORM 1 FROM iam_v2.entitlements WHERE id = v_ent FOR UPDATE;
  SELECT id INTO v_head FROM iam_v2.entitlement_state_transitions
    WHERE entitlement_id = v_ent AND superseded_by IS NULL ORDER BY seq DESC LIMIT 1;
  IF v_head IS DISTINCT FROM p_target THEN
    RAISE EXCEPTION 'only the live head transition may be superseded (head is %)', v_head;
  END IF;
  SELECT COALESCE(max(seq),0)+1 INTO v_seq FROM iam_v2.entitlement_state_transitions WHERE entitlement_id = v_ent;
  v_new := gen_random_uuid();
  -- INVALIDATE first, then append: the chain guard must judge the correction against the history that remains.
  UPDATE iam_v2.entitlement_state_transitions SET superseded_by = v_new WHERE id = p_target;
  SELECT to_state INTO v_from FROM iam_v2.entitlement_state_transitions
    WHERE entitlement_id = v_ent AND superseded_by IS NULL ORDER BY seq DESC LIMIT 1;
  v_term := CASE WHEN p_to='TERMINATED' THEN
    (CASE WHEN p_reason IN ('TIME','DATA','HARD_EXPIRY','CHECKOUT','ADMIN','REVOKED','SUPERSEDED','CONVERTED','TRANSFERRED','CANCELLED','OTHER')
          THEN p_reason ELSE 'OTHER' END) ELSE NULL END;
  -- status first, so the INSERT guard's history<->row coherence check sees the corrected state
  UPDATE iam_v2.entitlements SET status = p_to, terminal_reason = v_term WHERE id = v_ent;
  INSERT INTO iam_v2.entitlement_state_transitions
    (id,tenant_id,site_id,entitlement_id,seq,from_state,to_state,effective_at,recorded_at,supersedes,reason)
    SELECT v_new, tenant_id, site_id, id, v_seq, v_from, p_to, p_at, now(), p_target, p_reason
    FROM iam_v2.entitlements WHERE id = v_ent;
  PERFORM iam_v2.p3_rederive_entitlement_times(v_ent);
  RETURN v_new;
END $_$;


--
-- Name: terminate_entitlement_at_boundary(uuid, timestamp with time zone, text); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.terminate_entitlement_at_boundary(p_ent uuid, p_at timestamp with time zone, p_reason text) RETURNS uuid
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $_$
DECLARE v_status text; v_seq bigint; v_new uuid; v_term text; v_from text; v_head uuid; v_marked int;
BEGIN
  IF p_reason IS NOT NULL AND p_reason !~ '^[A-Z][A-Z0-9_]{0,63}$' THEN
    RAISE EXCEPTION 'transition reason must be a bounded machine code';
  END IF;
  SELECT status INTO v_status FROM iam_v2.entitlements WHERE id = p_ent FOR UPDATE;
  IF v_status IS NULL THEN RAISE EXCEPTION 'entitlement % not found', p_ent; END IF;
  IF v_status = 'TERMINATED' THEN RETURN NULL; END IF;
  SELECT COALESCE(max(seq),0)+1 INTO v_seq FROM iam_v2.entitlement_state_transitions WHERE entitlement_id = p_ent;
  v_new := gen_random_uuid();
  SELECT id INTO v_head FROM iam_v2.entitlement_state_transitions
    WHERE entitlement_id = p_ent AND superseded_by IS NULL AND effective_at > p_at ORDER BY seq DESC LIMIT 1;
  UPDATE iam_v2.entitlement_state_transitions SET superseded_by = v_new
    WHERE entitlement_id = p_ent AND superseded_by IS NULL AND effective_at > p_at;
  GET DIAGNOSTICS v_marked = ROW_COUNT;
  SELECT to_state INTO v_from FROM iam_v2.entitlement_state_transitions
    WHERE entitlement_id = p_ent AND superseded_by IS NULL ORDER BY seq DESC LIMIT 1;
  v_term := CASE WHEN p_reason IN ('TIME','DATA','HARD_EXPIRY','CHECKOUT','ADMIN','REVOKED','SUPERSEDED','CONVERTED','TRANSFERRED','CANCELLED','OTHER')
                 THEN p_reason ELSE 'OTHER' END;
  UPDATE iam_v2.entitlements SET status = 'TERMINATED', terminal_reason = v_term WHERE id = p_ent;
  INSERT INTO iam_v2.entitlement_state_transitions
    (id,tenant_id,site_id,entitlement_id,seq,from_state,to_state,effective_at,recorded_at,supersedes,reason)
    SELECT v_new, tenant_id, site_id, id, v_seq, v_from, 'TERMINATED', p_at, now(),
           CASE WHEN v_marked > 0 THEN v_head ELSE NULL END, p_reason
    FROM iam_v2.entitlements WHERE id = p_ent;
  PERFORM iam_v2.p3_rederive_entitlement_times(p_ent);
  RETURN v_new;
END $_$;


--
-- Name: trg_entitlement_guard(); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.trg_entitlement_guard() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
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
END; $$;


--
-- Name: trg_offer_quote_immutable(); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.trg_offer_quote_immutable() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
  IF OLD.consumed_at IS NOT NULL THEN
    RAISE EXCEPTION 'offer_quote % is already consumed (immutable)', OLD.id USING ERRCODE = 'restrict_violation';
  END IF;
  IF NEW.consumed_at IS NULL THEN
    RAISE EXCEPTION 'offer_quote consumed_at may not be cleared' USING ERRCODE = 'restrict_violation';
  END IF;
  IF NEW.tenant_id            IS DISTINCT FROM OLD.tenant_id
   OR NEW.site_id             IS DISTINCT FROM OLD.site_id
   OR NEW.auth_context_id     IS DISTINCT FROM OLD.auth_context_id
   OR NEW.package_revision_id IS DISTINCT FROM OLD.package_revision_id
   OR NEW.pms_interface_id    IS DISTINCT FROM OLD.pms_interface_id
   OR NEW.settlement_mapping_id IS DISTINCT FROM OLD.settlement_mapping_id
   OR NEW.price_minor         IS DISTINCT FROM OLD.price_minor
   OR NEW.currency            IS DISTINCT FROM OLD.currency
   OR NEW.currency_exponent   IS DISTINCT FROM OLD.currency_exponent
   OR NEW.tax_code            IS DISTINCT FROM OLD.tax_code
   OR NEW.tax_rate_bp         IS DISTINCT FROM OLD.tax_rate_bp
   OR NEW.tax_amount_minor    IS DISTINCT FROM OLD.tax_amount_minor
   OR NEW.grant_snapshot      IS DISTINCT FROM OLD.grant_snapshot
   OR NEW.expires_at          IS DISTINCT FROM OLD.expires_at THEN
    RAISE EXCEPTION 'offer_quote % is immutable except one-time consumption', OLD.id USING ERRCODE = 'restrict_violation';
  END IF;
  RETURN NEW;
END $$;


--
-- Name: trg_posting_attempt_oneway(); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.trg_posting_attempt_oneway() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
  IF TG_OP='DELETE' THEN RAISE EXCEPTION 'posting_attempts is not deletable'; END IF;
  IF ROW(NEW.p_number,NEW.rn,NEW.g_number,NEW.sent_at,NEW.internal_posting_id,NEW.attempt_no,NEW.pms_interface_id)
     IS DISTINCT FROM ROW(OLD.p_number,OLD.rn,OLD.g_number,OLD.sent_at,OLD.internal_posting_id,OLD.attempt_no,OLD.pms_interface_id)
  THEN RAISE EXCEPTION 'posting_attempts identity is immutable'; END IF;
  IF OLD.outcome <> 'SENDING' AND NEW.outcome <> OLD.outcome THEN
     RAISE EXCEPTION 'posting_attempts.outcome is terminal (% -> %)', OLD.outcome, NEW.outcome; END IF;
  IF NEW.outcome = 'SENDING' AND OLD.outcome <> 'SENDING' THEN
     RAISE EXCEPTION 'posting_attempts.outcome cannot return to SENDING'; END IF;
  RETURN NEW;
END; $$;


--
-- Name: trg_posting_charge_gate(); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.trg_posting_charge_gate() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE strat text; st text; pa boolean;
BEGIN
  IF NEW.posting_type = 'CHARGE' THEN
    SELECT folio_identity_strategy INTO strat FROM iam_v2.pms_interface_revisions
      WHERE tenant_id=NEW.tenant_id AND site_id=NEW.site_id AND pms_interface_id=NEW.pms_interface_id AND id=NEW.posting_interface_revision_id;
    IF strat IS NULL OR strat='UNSET' THEN
      RAISE EXCEPTION 'FOLIO_STRATEGY_UNSET: financial CHARGE blocked fail-closed (interface %, revision %)', NEW.pms_interface_id, NEW.posting_interface_revision_id
        USING ERRCODE='check_violation';
    END IF;
    IF NEW.stay_id IS NOT NULL THEN
      SELECT status, posting_allowed INTO st, pa FROM iam_v2.stays
        WHERE tenant_id=NEW.tenant_id AND site_id=NEW.site_id AND pms_interface_id=NEW.pms_interface_id AND id=NEW.stay_id;
      IF st IS DISTINCT FROM 'IN_HOUSE' OR pa IS NOT TRUE THEN
        RAISE EXCEPTION 'POSTING_NOT_ALLOWED: stay % not IN_HOUSE/posting_allowed', NEW.stay_id USING ERRCODE='check_violation';
      END IF;
    END IF;
  END IF;
  RETURN NEW;
END; $$;


--
-- Name: trg_purchase_quote_pin_equal(); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.trg_purchase_quote_pin_equal() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE q iam_v2.offer_quotes%ROWTYPE;
BEGIN
  IF NEW.offer_quote_id IS NULL THEN
    RETURN NEW;  -- non-quote triggers (voucher/account auto-grant etc.) are pinned by their own paths
  END IF;
  SELECT * INTO q FROM iam_v2.offer_quotes WHERE id = NEW.offer_quote_id;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'purchase references unknown offer_quote %', NEW.offer_quote_id USING ERRCODE = 'foreign_key_violation';
  END IF;
  IF NEW.tenant_id            IS DISTINCT FROM q.tenant_id
   OR NEW.site_id             IS DISTINCT FROM q.site_id
   OR NEW.package_revision_id IS DISTINCT FROM q.package_revision_id
   OR NEW.auth_context_id     IS DISTINCT FROM q.auth_context_id
   OR NEW.pms_interface_id    IS DISTINCT FROM q.pms_interface_id
   OR NEW.settlement_mapping_id IS DISTINCT FROM q.settlement_mapping_id
   OR NEW.amount_minor        IS DISTINCT FROM q.price_minor
   OR NEW.currency            IS DISTINCT FROM q.currency
   OR NEW.currency_exponent   IS DISTINCT FROM q.currency_exponent
   OR NEW.tax_code            IS DISTINCT FROM q.tax_code
   OR NEW.tax_rate_bp         IS DISTINCT FROM q.tax_rate_bp
   OR NEW.tax_amount_minor    IS DISTINCT FROM q.tax_amount_minor THEN
    RAISE EXCEPTION 'purchase money/pin values must match its offer_quote % exactly', NEW.offer_quote_id
      USING ERRCODE = 'restrict_violation';
  END IF;
  RETURN NEW;
END $$;


--
-- Name: trg_reject_update_delete(); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.trg_reject_update_delete() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN RAISE EXCEPTION '% is immutable (no UPDATE/DELETE) on %', TG_TABLE_NAME, TG_OP; END; $$;


--
-- Name: trg_reject_update_delete_ptx_events(); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.trg_reject_update_delete_ptx_events() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN RAISE EXCEPTION 'payment_transaction_events is append-only (no % )', TG_OP; END $$;


--
-- Name: trg_secret_gen_guard(); Type: FUNCTION; Schema: iam_v2; Owner: -
--

CREATE FUNCTION iam_v2.trg_secret_gen_guard() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
  IF TG_OP='DELETE' THEN RAISE EXCEPTION 'secret generations are not deletable'; END IF;
  IF ROW(NEW.ciphertext,NEW.nonce,NEW.encryption_key_id,NEW.cipher_version,NEW.generation_no,NEW.pms_interface_id)
     IS DISTINCT FROM ROW(OLD.ciphertext,OLD.nonce,OLD.encryption_key_id,OLD.cipher_version,OLD.generation_no,OLD.pms_interface_id)
  THEN RAISE EXCEPTION 'secret generation identity is immutable (only superseded_at may change)'; END IF;
  RETURN NEW;
END; $$;


SET default_tablespace = '';

SET default_table_access_method = heap;

--
-- Name: accounting_checkpoints; Type: TABLE; Schema: iam_v2; Owner: -
--

CREATE TABLE iam_v2.accounting_checkpoints (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    session_id uuid NOT NULL,
    source_device_id uuid NOT NULL,
    bridge text NOT NULL,
    class_minor integer NOT NULL,
    source_epoch bigint NOT NULL,
    prev_bytes_up bigint NOT NULL,
    prev_bytes_down bigint NOT NULL,
    prev_sampled_at timestamp with time zone NOT NULL,
    last_record_id uuid,
    last_classification text NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT accounting_checkpoints_last_classification_check CHECK ((last_classification = ANY (ARRAY['BASELINED'::text, 'ACCEPTED'::text, 'DELAYED'::text, 'DUPLICATE'::text, 'RESET_BASELINED'::text]))),
    CONSTRAINT accounting_checkpoints_prev_bytes_down_check CHECK ((prev_bytes_down >= 0)),
    CONSTRAINT accounting_checkpoints_prev_bytes_up_check CHECK ((prev_bytes_up >= 0))
);


--
-- Name: accounting_records; Type: TABLE; Schema: iam_v2; Owner: -
--

CREATE TABLE iam_v2.accounting_records (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    session_id uuid NOT NULL,
    sample_seq bigint NOT NULL,
    bytes_up bigint DEFAULT 0 NOT NULL,
    bytes_down bigint DEFAULT 0 NOT NULL,
    sampled_at timestamp with time zone DEFAULT now() NOT NULL,
    ingested_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: checkout_grace_alert_actions; Type: TABLE; Schema: iam_v2; Owner: -
--

CREATE TABLE iam_v2.checkout_grace_alert_actions (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    audit_id uuid NOT NULL,
    seq bigint NOT NULL,
    action text NOT NULL,
    actor uuid,
    reason_code text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT checkout_grace_alert_actions_action_check CHECK ((action = ANY (ARRAY['OPEN'::text, 'ACKNOWLEDGED'::text, 'RESOLVED'::text]))),
    CONSTRAINT checkout_grace_alert_actions_reason_code_check CHECK (((reason_code IS NULL) OR (reason_code ~ '^[A-Z][A-Z0-9_]{0,63}$'::text))),
    CONSTRAINT checkout_grace_alert_actions_seq_check CHECK ((seq >= 1))
);


--
-- Name: checkout_grace_audit; Type: TABLE; Schema: iam_v2; Owner: -
--

CREATE TABLE iam_v2.checkout_grace_audit (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    pms_interface_id uuid NOT NULL,
    stay_id uuid NOT NULL,
    lifecycle_version integer NOT NULL,
    trigger text NOT NULL,
    is_emergency boolean DEFAULT false NOT NULL,
    policy_version text NOT NULL,
    alert_code text,
    reason_code text NOT NULL,
    grace_entitlement_id uuid,
    boundary_event_id uuid NOT NULL,
    boundary_event_seq bigint,
    boundary_normalization_version integer,
    boundary_reason_code text NOT NULL,
    config_version bigint DEFAULT 0 NOT NULL,
    boundary_at timestamp with time zone NOT NULL,
    boundary_clock_suspect boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT cga_coherent CHECK ((((trigger = 'CHECKOUT_GRACE'::text) AND (is_emergency = false) AND (policy_version = 'CHECKOUT_GRACE_V1'::text) AND (alert_code IS NULL) AND (grace_entitlement_id IS NOT NULL)) OR ((trigger = 'EMERGENCY_GRACE'::text) AND (is_emergency = true) AND (policy_version = 'EMERGENCY_GRACE_V1'::text) AND (NOT (alert_code IS DISTINCT FROM 'CHECKOUT_GRACE_CONFIG_INVALID'::text)) AND (grace_entitlement_id IS NOT NULL)) OR ((trigger = 'NO_GRACE'::text) AND (is_emergency = false) AND (policy_version = 'NONE'::text) AND (alert_code IS NULL) AND (grace_entitlement_id IS NULL)))),
    CONSTRAINT checkout_grace_audit_alert_code_check CHECK (((alert_code IS NULL) OR (alert_code = 'CHECKOUT_GRACE_CONFIG_INVALID'::text))),
    CONSTRAINT checkout_grace_audit_boundary_reason_code_check CHECK ((boundary_reason_code ~ '^[A-Z][A-Z0-9_]{0,63}$'::text)),
    CONSTRAINT checkout_grace_audit_lifecycle_version_check CHECK ((lifecycle_version > 0)),
    CONSTRAINT checkout_grace_audit_policy_version_check CHECK ((policy_version ~ '^[A-Z][A-Z0-9_]{0,63}$'::text)),
    CONSTRAINT checkout_grace_audit_reason_code_check CHECK ((reason_code ~ '^[A-Z][A-Z0-9_]{0,63}$'::text)),
    CONSTRAINT checkout_grace_audit_trigger_check CHECK ((trigger = ANY (ARRAY['CHECKOUT_GRACE'::text, 'EMERGENCY_GRACE'::text, 'NO_GRACE'::text])))
);


--
-- Name: active_operational_alerts; Type: VIEW; Schema: iam_v2; Owner: -
--

CREATE VIEW iam_v2.active_operational_alerts AS
 SELECT a.id AS audit_id,
    a.tenant_id,
    a.site_id,
    a.pms_interface_id,
    a.stay_id,
    a.lifecycle_version,
    a.alert_code,
    a.trigger,
    a.policy_version,
    a.reason_code,
    a.boundary_at,
    a.boundary_clock_suspect,
    a.created_at,
    COALESCE(h.action, 'OPEN'::text) AS state,
    COALESCE(h.action, 'OPEN'::text) AS alert_state,
    COALESCE(h.seq, (1)::bigint) AS alert_seq,
    h.created_at AS state_changed_at
   FROM (iam_v2.checkout_grace_audit a
     LEFT JOIN LATERAL ( SELECT act.action,
            act.seq,
            act.created_at
           FROM iam_v2.checkout_grace_alert_actions act
          WHERE (act.audit_id = a.id)
          ORDER BY act.seq DESC
         LIMIT 1) h ON (true))
  WHERE ((a.alert_code IS NOT NULL) AND (COALESCE(h.action, 'OPEN'::text) <> 'RESOLVED'::text));


--
-- Name: appliance_class_generation; Type: TABLE; Schema: iam_v2; Owner: -
--

CREATE TABLE iam_v2.appliance_class_generation (
    tenant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    appliance_id uuid NOT NULL,
    last_generation bigint DEFAULT 0 NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT appliance_class_generation_last_generation_check CHECK ((last_generation >= 0))
);


--
-- Name: appliance_product_setting_changes; Type: TABLE; Schema: iam_v2; Owner: -
--

CREATE TABLE iam_v2.appliance_product_setting_changes (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    appliance_id uuid NOT NULL,
    setting_key text NOT NULL,
    old_value boolean,
    new_value boolean NOT NULL,
    changed_at timestamp with time zone DEFAULT now() NOT NULL,
    changed_by_operator_id uuid NOT NULL,
    changed_by text NOT NULL,
    change_reason text,
    CONSTRAINT appliance_product_setting_changes_changed_by_check CHECK ((length(btrim(changed_by)) > 0)),
    CONSTRAINT appliance_product_setting_changes_setting_key_check CHECK ((setting_key = 'guest_device_self_service'::text))
);


--
-- Name: appliance_product_settings; Type: TABLE; Schema: iam_v2; Owner: -
--

CREATE TABLE iam_v2.appliance_product_settings (
    tenant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    appliance_id uuid NOT NULL,
    guest_device_self_service boolean DEFAULT false NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: TABLE appliance_product_settings; Type: COMMENT; Schema: iam_v2; Owner: -
--

COMMENT ON TABLE iam_v2.appliance_product_settings IS 'Per-appliance product settings, read from the site database on the appliance itself. Local-first by construction: no Central Control Plane call is on the read path, so an appliance with no uplink answers exactly as one with an uplink.';


--
-- Name: COLUMN appliance_product_settings.guest_device_self_service; Type: COMMENT; Schema: iam_v2; Owner: -
--

COMMENT ON COLUMN iam_v2.appliance_product_settings.guest_device_self_service IS 'OFF by default. When OFF the guest device-management capability is not exposed to the guest at all and ordinary authentication and device-limit behaviour is unchanged. This is the long-term PRODUCT control; the Phase-6 deployment gate is a separate and additionally required control.';


--
-- Name: auth_context_offers; Type: TABLE; Schema: iam_v2; Owner: -
--

CREATE TABLE iam_v2.auth_context_offers (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    auth_context_id uuid NOT NULL,
    package_revision_id uuid NOT NULL,
    matched_tier_order integer,
    evidence_version bigint NOT NULL,
    offered_at timestamp with time zone DEFAULT now() NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    CONSTRAINT aco_expiry_after_offer CHECK ((expires_at > offered_at)),
    CONSTRAINT auth_context_offers_evidence_version_check CHECK ((evidence_version > 0))
);


--
-- Name: auth_contexts; Type: TABLE; Schema: iam_v2; Owner: -
--

CREATE TABLE iam_v2.auth_contexts (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    method text NOT NULL,
    stay_id uuid,
    guest_account_id uuid,
    voucher_id uuid,
    guest_principal_id uuid,
    post_stay_profile_id uuid,
    pms_interface_id uuid,
    authentication_interface_revision_id uuid,
    device_id uuid NOT NULL,
    guest_network_id uuid NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    consumed_at timestamp with time zone,
    pinned_lifecycle_version integer,
    pinned_occupancy_evidence_version bigint,
    resolution_request_id uuid,
    CONSTRAINT ac_method_subject CHECK ((((method = 'PMS'::text) AND (stay_id IS NOT NULL)) OR ((method = 'VOUCHER'::text) AND (voucher_id IS NOT NULL)) OR ((method = 'ACCOUNT'::text) AND (guest_account_id IS NOT NULL)) OR ((method = ANY (ARRAY['OTP'::text, 'SOCIAL'::text])) AND (guest_principal_id IS NOT NULL)) OR ((method = 'POST_STAY_PIN'::text) AND (post_stay_profile_id IS NOT NULL)))),
    CONSTRAINT ac_one_subject CHECK ((num_nonnulls(stay_id, guest_account_id, voucher_id, guest_principal_id, post_stay_profile_id) = 1)),
    CONSTRAINT ac_pms_pins CHECK (((method <> 'PMS'::text) OR ((pms_interface_id IS NOT NULL) AND (authentication_interface_revision_id IS NOT NULL)))),
    CONSTRAINT ac_post_stay_pins CHECK (((method <> 'POST_STAY_PIN'::text) OR ((post_stay_profile_id IS NOT NULL) AND (pinned_lifecycle_version IS NOT NULL)))),
    CONSTRAINT auth_contexts_method_check CHECK ((method = ANY (ARRAY['PMS'::text, 'VOUCHER'::text, 'ACCOUNT'::text, 'OTP'::text, 'SOCIAL'::text, 'POST_STAY_PIN'::text]))),
    CONSTRAINT auth_contexts_pinned_lifecycle_version_check CHECK (((pinned_lifecycle_version IS NULL) OR (pinned_lifecycle_version > 0))),
    CONSTRAINT auth_contexts_pinned_occupancy_evidence_version_check CHECK (((pinned_occupancy_evidence_version IS NULL) OR (pinned_occupancy_evidence_version >= 0)))
);


--
-- Name: auth_resolutions; Type: TABLE; Schema: iam_v2; Owner: -
--

CREATE TABLE iam_v2.auth_resolutions (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    guest_network_id uuid NOT NULL,
    resolved_stay_id uuid,
    outcome_code text NOT NULL,
    resolved_at timestamp with time zone DEFAULT now() NOT NULL,
    resolution_request_id uuid
);


--
-- Name: checkout_grace_policy_publications; Type: TABLE; Schema: iam_v2; Owner: -
--

CREATE TABLE iam_v2.checkout_grace_policy_publications (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    config_version integer NOT NULL,
    actor uuid NOT NULL,
    reason_code text,
    grace_package_revision_id uuid,
    policy_snapshot jsonb NOT NULL,
    published_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT checkout_grace_policy_publications_config_version_check CHECK ((config_version >= 1)),
    CONSTRAINT checkout_grace_policy_publications_reason_code_check CHECK (((reason_code IS NULL) OR (reason_code ~ '^[A-Z][A-Z0-9_]{0,63}$'::text)))
);


--
-- Name: compliance_archives; Type: TABLE; Schema: iam_v2; Owner: -
--

CREATE TABLE iam_v2.compliance_archives (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    manifest_sha256 text NOT NULL,
    receipt_verified boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    purpose text DEFAULT 'CROSS_CUSTOMER_PURGE'::text NOT NULL,
    artifact_path text,
    row_counts jsonb DEFAULT '{}'::jsonb NOT NULL,
    receipt_blocked_reason text,
    receipt_authority text,
    receipt_reference text,
    receipt_verified_at timestamp with time zone,
    CONSTRAINT ca_receipt_evidence_matches_flag CHECK (((receipt_verified = false) OR ((receipt_verified = true) AND (receipt_authority IS NOT NULL) AND (btrim(receipt_authority) <> ''::text) AND (receipt_reference IS NOT NULL) AND (btrim(receipt_reference) <> ''::text) AND (receipt_verified_at IS NOT NULL)))),
    CONSTRAINT compliance_archives_purpose_check CHECK ((purpose = ANY (ARRAY['CROSS_CUSTOMER_PURGE'::text, 'RETENTION_EXPIRY'::text, 'OPERATOR_EXPORT'::text])))
);


--
-- Name: COLUMN compliance_archives.receipt_verified; Type: COMMENT; Schema: iam_v2; Owner: -
--

COMMENT ON COLUMN iam_v2.compliance_archives.receipt_verified IS 'TRUE only when an EXTERNAL archival authority has acknowledged custody of the artefact, with the authority and its reference recorded alongside. No such authority exists in this product, so this is false everywhere and cross-customer purge is consequently impossible. That is the intended failure mode: the alternative is letting the appliance that is about to delete the data certify its own copy.';


--
-- Name: controlled_operation_scope; Type: TABLE; Schema: iam_v2; Owner: -
--

CREATE UNLOGGED TABLE iam_v2.controlled_operation_scope (
    txid bigint NOT NULL,
    family text NOT NULL,
    token uuid NOT NULL,
    opened_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: TABLE controlled_operation_scope; Type: COMMENT; Schema: iam_v2; Owner: -
--

COMMENT ON TABLE iam_v2.controlled_operation_scope IS 'Transaction-scoped capability tokens for controlled-writer families whose operation is service logic. Written only by iam_v2.begin_controlled_operation().';


--
-- Name: delayed_accounting_records; Type: TABLE; Schema: iam_v2; Owner: -
--

CREATE TABLE iam_v2.delayed_accounting_records (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    accounting_record_id uuid NOT NULL,
    session_id uuid NOT NULL,
    entitlement_id uuid NOT NULL,
    watermark_id uuid NOT NULL,
    sampled_at timestamp with time zone NOT NULL,
    bytes_up bigint NOT NULL,
    bytes_down bigint NOT NULL,
    detected_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: device_network_appearances; Type: TABLE; Schema: iam_v2; Owner: -
--

CREATE TABLE iam_v2.device_network_appearances (
    tenant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    device_id uuid NOT NULL,
    guest_network_id uuid NOT NULL,
    first_seen timestamp with time zone,
    last_seen timestamp with time zone
);


--
-- Name: devices; Type: TABLE; Schema: iam_v2; Owner: -
--

CREATE TABLE iam_v2.devices (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    appliance_id uuid NOT NULL,
    mac macaddr NOT NULL,
    first_seen timestamp with time zone,
    last_seen timestamp with time zone,
    last_ip inet
);


--
-- Name: TABLE devices; Type: COMMENT; Schema: iam_v2; Owner: -
--

COMMENT ON TABLE iam_v2.devices IS 'Durable device identity. svc_scd may INSERT (a device appears the first time the appliance sees it) and may UPDATE only mac, last_seen and last_ip. It may not DELETE, and it may not move a device between tenant, site or appliance: those columns are outside its column-level grant.';


--
-- Name: entitlement_adjustments; Type: TABLE; Schema: iam_v2; Owner: -
--

CREATE TABLE iam_v2.entitlement_adjustments (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    entitlement_id uuid NOT NULL,
    field text NOT NULL,
    old_value text,
    new_value text,
    actor uuid,
    reason text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: entitlement_boundary_watermarks; Type: TABLE; Schema: iam_v2; Owner: -
--

CREATE TABLE iam_v2.entitlement_boundary_watermarks (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    entitlement_id uuid NOT NULL,
    boundary_at timestamp with time zone NOT NULL,
    bytes_up bigint NOT NULL,
    bytes_down bigint NOT NULL,
    records_counted bigint NOT NULL,
    latest_sampled_at timestamp with time zone,
    recorded_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT entitlement_boundary_watermarks_bytes_down_check CHECK ((bytes_down >= 0)),
    CONSTRAINT entitlement_boundary_watermarks_bytes_up_check CHECK ((bytes_up >= 0)),
    CONSTRAINT entitlement_boundary_watermarks_records_counted_check CHECK ((records_counted >= 0))
);


--
-- Name: entitlement_device_authorizations; Type: TABLE; Schema: iam_v2; Owner: -
--

CREATE TABLE iam_v2.entitlement_device_authorizations (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    entitlement_id uuid NOT NULL,
    device_id uuid NOT NULL,
    seq bigint NOT NULL,
    authorized_at timestamp with time zone NOT NULL,
    deauthorized_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT entitlement_device_authorizations_check CHECK (((deauthorized_at IS NULL) OR (deauthorized_at >= authorized_at))),
    CONSTRAINT entitlement_device_authorizations_seq_check CHECK ((seq >= 1))
);


--
-- Name: entitlement_devices; Type: TABLE; Schema: iam_v2; Owner: -
--

CREATE TABLE iam_v2.entitlement_devices (
    tenant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    entitlement_id uuid NOT NULL,
    device_id uuid NOT NULL,
    status text DEFAULT 'AUTHORIZED'::text NOT NULL,
    grandfathered boolean DEFAULT false NOT NULL,
    disconnected_reason text,
    first_authorized timestamp with time zone,
    last_authorized timestamp with time zone,
    CONSTRAINT entitlement_devices_status_check CHECK ((status = ANY (ARRAY['AUTHORIZED'::text, 'DISCONNECTED'::text])))
);


--
-- Name: COLUMN entitlement_devices.disconnected_reason; Type: COMMENT; Schema: iam_v2; Owner: -
--

COMMENT ON COLUMN iam_v2.entitlement_devices.disconnected_reason IS 'Why the device left this entitlement. Phase 6 adds GUEST_SELF_SERVICE, which is deliberately distinct from ENTITLEMENT_ENDED, CROSS_PMS_TRANSFER and operator-driven reasons: a slot the guest released is a different fact from one the system took back.';


--
-- Name: entitlement_state_transitions; Type: TABLE; Schema: iam_v2; Owner: -
--

CREATE TABLE iam_v2.entitlement_state_transitions (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    entitlement_id uuid NOT NULL,
    seq bigint NOT NULL,
    from_state text,
    to_state text NOT NULL,
    effective_at timestamp with time zone NOT NULL,
    recorded_at timestamp with time zone DEFAULT now() NOT NULL,
    supersedes uuid,
    superseded_by uuid,
    reason text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT entitlement_state_transitions_from_state_check CHECK (((from_state IS NULL) OR (from_state = ANY (ARRAY['PENDING'::text, 'ACTIVE'::text, 'SUSPENDED'::text, 'TERMINATED'::text])))),
    CONSTRAINT entitlement_state_transitions_reason_check CHECK (((reason IS NULL) OR (reason ~ '^[A-Z][A-Z0-9_]{0,63}$'::text))),
    CONSTRAINT entitlement_state_transitions_seq_check CHECK ((seq >= 1)),
    CONSTRAINT entitlement_state_transitions_to_state_check CHECK ((to_state = ANY (ARRAY['PENDING'::text, 'ACTIVE'::text, 'SUSPENDED'::text, 'TERMINATED'::text]))),
    CONSTRAINT est_no_self_supersede CHECK (((supersedes IS NULL) OR (supersedes <> id))),
    CONSTRAINT est_not_self_superseded CHECK (((superseded_by IS NULL) OR (superseded_by <> id)))
);


--
-- Name: entitlement_termination_evidence; Type: TABLE; Schema: iam_v2; Owner: -
--

CREATE TABLE iam_v2.entitlement_termination_evidence (
    entitlement_id uuid NOT NULL,
    tenant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    terminal_reason text NOT NULL,
    cause_detail text NOT NULL,
    time_mode text NOT NULL,
    budget_seconds bigint,
    consumed_online_seconds bigint,
    window_ends_at timestamp with time zone,
    terminated_at timestamp with time zone NOT NULL,
    recorded_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT entitlement_termination_evidence_budget_seconds_check CHECK (((budget_seconds IS NULL) OR (budget_seconds >= 0))),
    CONSTRAINT entitlement_termination_evidence_cause_detail_check CHECK ((cause_detail = ANY (ARRAY['VALIDITY_WINDOW_ELAPSED'::text, 'AGGREGATE_ONLINE_TIME_EXHAUSTED'::text, 'AGGREGATE_OUTER_WINDOW_EXPIRED'::text]))),
    CONSTRAINT entitlement_termination_evidence_consumed_online_seconds_check CHECK (((consumed_online_seconds IS NULL) OR (consumed_online_seconds >= 0))),
    CONSTRAINT entitlement_termination_evidence_terminal_reason_check CHECK ((terminal_reason = 'TIME'::text)),
    CONSTRAINT entitlement_termination_evidence_time_mode_check CHECK ((time_mode = ANY (ARRAY['VALIDITY_WINDOW'::text, 'AGGREGATE_ONLINE_TIME'::text]))),
    CONSTRAINT ete_aggregate_carries_its_budget CHECK (((cause_detail <> ALL (ARRAY['AGGREGATE_ONLINE_TIME_EXHAUSTED'::text, 'AGGREGATE_OUTER_WINDOW_EXPIRED'::text])) OR ((budget_seconds IS NOT NULL) AND (consumed_online_seconds IS NOT NULL)))),
    CONSTRAINT ete_detail_matches_mode CHECK (((cause_detail = ANY (ARRAY['AGGREGATE_ONLINE_TIME_EXHAUSTED'::text, 'AGGREGATE_OUTER_WINDOW_EXPIRED'::text])) = (time_mode = 'AGGREGATE_ONLINE_TIME'::text))),
    CONSTRAINT ete_exhaustion_reached_its_budget CHECK (((cause_detail <> 'AGGREGATE_ONLINE_TIME_EXHAUSTED'::text) OR (consumed_online_seconds >= budget_seconds))),
    CONSTRAINT ete_outer_window_is_distinguishable CHECK (((cause_detail <> 'AGGREGATE_OUTER_WINDOW_EXPIRED'::text) OR ((window_ends_at IS NOT NULL) AND (consumed_online_seconds < budget_seconds))))
);


--
-- Name: TABLE entitlement_termination_evidence; Type: COMMENT; Schema: iam_v2; Owner: -
--

COMMENT ON TABLE iam_v2.entitlement_termination_evidence IS 'Why a time-mode entitlement ended, with the numbers. The contract terminal_reason set is unchanged and is never widened here: an aggregate-budget exhaustion terminates with reason TIME, and THIS row says it was the online-minute budget rather than the wall-clock window, which budget it was, and what had been consumed when it crossed.';


--
-- Name: entitlement_transfers; Type: TABLE; Schema: iam_v2; Owner: -
--

CREATE TABLE iam_v2.entitlement_transfers (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    from_entitlement_id uuid NOT NULL,
    to_entitlement_id uuid NOT NULL,
    from_stay_id uuid NOT NULL,
    to_stay_id uuid NOT NULL,
    reason text DEFAULT 'CROSS_PMS_TRANSFER'::text NOT NULL,
    actor uuid NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT et_no_self CHECK ((from_entitlement_id <> to_entitlement_id)),
    CONSTRAINT et_two_stays CHECK ((from_stay_id <> to_stay_id))
);


--
-- Name: entitlements; Type: TABLE; Schema: iam_v2; Owner: -
--

CREATE TABLE iam_v2.entitlements (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    stay_id uuid,
    guest_account_id uuid,
    voucher_id uuid,
    guest_principal_id uuid,
    pms_interface_id uuid,
    purchase_id uuid NOT NULL,
    policy_snapshot jsonb NOT NULL,
    snapshot_version integer DEFAULT 1 NOT NULL,
    service_plan_revision_id uuid NOT NULL,
    package_revision_id uuid NOT NULL,
    time_accounting_mode text NOT NULL,
    end_mode text NOT NULL,
    window_ends_at timestamp with time zone,
    status text DEFAULT 'PENDING'::text NOT NULL,
    terminal_reason text,
    consumed_data_bytes bigint DEFAULT 0 NOT NULL,
    consumed_online_seconds bigint DEFAULT 0 NOT NULL,
    usage_version bigint DEFAULT 0 NOT NULL,
    renewal_number integer DEFAULT 1 NOT NULL,
    supersedes_entitlement_id uuid,
    is_emergency_grace boolean DEFAULT false NOT NULL,
    activated_at timestamp with time zone,
    terminated_at timestamp with time zone,
    online_time_exhausted_at timestamp with time zone,
    CONSTRAINT ent_one_subject CHECK ((num_nonnulls(stay_id, guest_account_id, voucher_id, guest_principal_id) = 1)),
    CONSTRAINT ent_terminal CHECK (((status = 'TERMINATED'::text) = (terminal_reason IS NOT NULL))),
    CONSTRAINT entitlements_consumed_data_bytes_check CHECK ((consumed_data_bytes >= 0)),
    CONSTRAINT entitlements_consumed_online_seconds_check CHECK ((consumed_online_seconds >= 0)),
    CONSTRAINT entitlements_end_mode_check CHECK ((end_mode = ANY (ARRAY['FIXED_AT'::text, 'VALIDITY_WINDOW'::text, 'AT_CHECKOUT'::text, 'EARLIEST_OF_FIXED_AND_CHECKOUT'::text, 'GRACE_AFTER_CHECKOUT'::text, 'MANUAL_END'::text]))),
    CONSTRAINT entitlements_status_check CHECK ((status = ANY (ARRAY['PENDING'::text, 'ACTIVE'::text, 'SUSPENDED'::text, 'TERMINATED'::text]))),
    CONSTRAINT entitlements_terminal_reason_check CHECK ((terminal_reason = ANY (ARRAY['TIME'::text, 'DATA'::text, 'HARD_EXPIRY'::text, 'CHECKOUT'::text, 'ADMIN'::text, 'REVOKED'::text, 'SUPERSEDED'::text, 'CONVERTED'::text, 'TRANSFERRED'::text, 'CANCELLED'::text, 'OTHER'::text])))
);


--
-- Name: COLUMN entitlements.online_time_exhausted_at; Type: COMMENT; Schema: iam_v2; Owner: -
--

COMMENT ON COLUMN iam_v2.entitlements.online_time_exhausted_at IS 'For AGGREGATE_ONLINE_TIME: the instant the online-time budget was exhausted, computed inside the tick that crossed it. Stable across retries -- a re-reported exhaustion carries the original instant, never the clock of the tick that re-reported it.';


--
-- Name: financial_epoch; Type: TABLE; Schema: iam_v2; Owner: -
--

CREATE TABLE iam_v2.financial_epoch (
    tenant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    epoch integer DEFAULT 1 NOT NULL,
    restore_generation integer DEFAULT 0 NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: financial_epochs; Type: TABLE; Schema: iam_v2; Owner: -
--

CREATE TABLE iam_v2.financial_epochs (
    tenant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    epoch bigint NOT NULL,
    system_identity text NOT NULL,
    entered_at timestamp with time zone DEFAULT now() NOT NULL,
    reason text NOT NULL,
    released_at timestamp with time zone,
    released_by uuid,
    release_note text,
    restore_generation bigint DEFAULT 0 NOT NULL,
    CONSTRAINT financial_epochs_reason_check CHECK ((reason = ANY (ARRAY['INITIAL'::text, 'RESTORE_DETECTED'::text, 'OPERATOR_DECLARED'::text]))),
    CONSTRAINT financial_epochs_release_note_check CHECK (((release_note IS NULL) OR (length(release_note) <= 2000)))
);


--
-- Name: TABLE financial_epochs; Type: COMMENT; Schema: iam_v2; Owner: -
--

COMMENT ON TABLE iam_v2.financial_epochs IS 'One row per run of a site''s financial history. An open row whose reason is RESTORE_DETECTED or OPERATOR_DECLARED means the site is in FINANCIAL_RECOVERY_MODE: money movement is held pending operator reconciliation. Guest access is unaffected.';


--
-- Name: COLUMN financial_epochs.restore_generation; Type: COMMENT; Schema: iam_v2; Owner: -
--

COMMENT ON COLUMN iam_v2.financial_epochs.restore_generation IS 'The restore generation this row was written under. It travels WITH the database through a pg_restore, which is what makes comparing it against the management-partition marker a rollback detector.';


--
-- Name: financial_recovery_holds; Type: TABLE; Schema: iam_v2; Owner: -
--

CREATE TABLE iam_v2.financial_recovery_holds (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    epoch bigint NOT NULL,
    work_kind text NOT NULL,
    work_id uuid NOT NULL,
    held_status text NOT NULL,
    amount_minor bigint,
    currency character(3),
    held_at timestamp with time zone DEFAULT now() NOT NULL,
    resolution text,
    resolved_at timestamp with time zone,
    resolved_by uuid,
    resolution_note text,
    CONSTRAINT financial_recovery_holds_held_status_check CHECK ((length(held_status) <= 64)),
    CONSTRAINT financial_recovery_holds_resolution_check CHECK ((resolution = ANY (ARRAY['CONFIRMED_COMPLETED'::text, 'CONFIRMED_NOT_COMPLETED'::text, 'ABANDONED'::text, 'ESCALATED'::text]))),
    CONSTRAINT financial_recovery_holds_resolution_note_check CHECK (((resolution_note IS NULL) OR (length(resolution_note) <= 2000))),
    CONSTRAINT financial_recovery_holds_work_kind_check CHECK ((work_kind = ANY (ARRAY['POSTING_OUTBOX'::text, 'PAYMENT_TRANSACTION'::text, 'SETTLEMENT'::text])))
);


--
-- Name: financial_restore_events; Type: TABLE; Schema: iam_v2; Owner: -
--

CREATE TABLE iam_v2.financial_restore_events (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    restore_generation bigint NOT NULL,
    manifest_sha256 text NOT NULL,
    backup_taken_at timestamp with time zone,
    restored_at timestamp with time zone DEFAULT now() NOT NULL,
    restored_by text,
    restore_kind text NOT NULL,
    detected_by text NOT NULL,
    CONSTRAINT financial_restore_events_detected_by_check CHECK ((detected_by = ANY (ARRAY['MANAGEMENT_MARKER'::text, 'SYSTEM_IDENTITY'::text, 'RESTORE_TOOL'::text]))),
    CONSTRAINT financial_restore_events_manifest_sha256_check CHECK ((manifest_sha256 ~ '^[0-9a-f]{64}$'::text)),
    CONSTRAINT financial_restore_events_restore_kind_check CHECK ((restore_kind = ANY (ARRAY['SUPPORTED'::text, 'UNSUPPORTED_RAW_SNAPSHOT'::text])))
);


--
-- Name: folios; Type: TABLE; Schema: iam_v2; Owner: -
--

CREATE TABLE iam_v2.folios (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    pms_interface_id uuid NOT NULL,
    external_folio_id text NOT NULL,
    identity_epoch integer DEFAULT 1 NOT NULL,
    folio_kind text DEFAULT 'GUEST'::text NOT NULL,
    status text DEFAULT 'OPEN'::text NOT NULL,
    CONSTRAINT folios_folio_kind_check CHECK ((folio_kind = ANY (ARRAY['GUEST'::text, 'COMPANY'::text, 'GROUP_MASTER'::text, 'OTHER'::text]))),
    CONSTRAINT folios_status_check CHECK ((status = ANY (ARRAY['OPEN'::text, 'CLOSED'::text])))
);


--
-- Name: guest_access_accounts; Type: TABLE; Schema: iam_v2; Owner: -
--

CREATE TABLE iam_v2.guest_access_accounts (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    username text NOT NULL,
    password_hash text NOT NULL,
    display_name text,
    notes text,
    enabled boolean DEFAULT true NOT NULL,
    valid_from timestamp with time zone,
    valid_until timestamp with time zone,
    assigned_package_id uuid,
    stay_id uuid,
    failed_attempts integer DEFAULT 0 NOT NULL,
    locked_until timestamp with time zone,
    last_login_at timestamp with time zone,
    login_count integer DEFAULT 0 NOT NULL
);


--
-- Name: guest_device_actions; Type: TABLE; Schema: iam_v2; Owner: -
--

CREATE TABLE iam_v2.guest_device_actions (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    entitlement_id uuid NOT NULL,
    device_id uuid,
    action text NOT NULL,
    outcome text NOT NULL,
    detail text,
    acted_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT guest_device_actions_action_check CHECK ((action = 'RELEASE'::text)),
    CONSTRAINT guest_device_actions_outcome_check CHECK ((outcome = ANY (ARRAY['OK'::text, 'REFUSED_ONLINE'::text, 'REFUSED_NOT_FOUND'::text, 'REFUSED_ALREADY_RELEASED'::text, 'REFUSED_DISABLED'::text, 'REFUSED_THROTTLED'::text])))
);


--
-- Name: TABLE guest_device_actions; Type: COMMENT; Schema: iam_v2; Owner: -
--

COMMENT ON TABLE iam_v2.guest_device_actions IS 'Every guest-initiated action that CHANGES durable device state, refusals included. RELEASE is the only such action: listing reads and changes nothing. An earlier version of this schema also named LIST, which was a standing claim that a guest''s list requests were investigable when no path recorded one -- and after the Phase-6 privilege audit no path can, because the guest surface holds no write on this table.';


--
-- Name: guest_network_pms_map; Type: TABLE; Schema: iam_v2; Owner: -
--

CREATE TABLE iam_v2.guest_network_pms_map (
    tenant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    guest_network_id uuid NOT NULL,
    pms_interface_id uuid NOT NULL,
    is_default boolean DEFAULT false NOT NULL,
    routing_mode text DEFAULT 'MAPPED'::text NOT NULL,
    CONSTRAINT guest_network_pms_map_routing_mode_check CHECK ((routing_mode = ANY (ARRAY['MAPPED'::text, 'ALL_ACTIVE_INTERFACES'::text])))
);


--
-- Name: guest_principal_identities; Type: TABLE; Schema: iam_v2; Owner: -
--

CREATE TABLE iam_v2.guest_principal_identities (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    guest_principal_id uuid NOT NULL,
    factor_type text NOT NULL,
    factor_issuer text DEFAULT ''::text NOT NULL,
    factor_value_norm text NOT NULL,
    verified_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT gpi_social_needs_issuer CHECK (((factor_type <> 'SOCIAL_SUBJECT'::text) OR (factor_issuer <> ''::text))),
    CONSTRAINT guest_principal_identities_factor_type_check CHECK ((factor_type = ANY (ARRAY['EMAIL'::text, 'PHONE'::text, 'SOCIAL_SUBJECT'::text])))
);


--
-- Name: guest_principals; Type: TABLE; Schema: iam_v2; Owner: -
--

CREATE TABLE iam_v2.guest_principals (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    display_name text
);


--
-- Name: internet_package_revisions; Type: TABLE; Schema: iam_v2; Owner: -
--

CREATE TABLE iam_v2.internet_package_revisions (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    package_id uuid NOT NULL,
    revision_no integer NOT NULL,
    service_plan_revision_id uuid NOT NULL,
    package_type text NOT NULL,
    price_minor bigint DEFAULT 0 NOT NULL,
    currency character(3),
    currency_exponent smallint,
    settlement_methods text[] DEFAULT '{NOT_REQUIRED}'::text[] NOT NULL,
    duration_policy jsonb DEFAULT '{}'::jsonb NOT NULL,
    plan_overrides jsonb,
    renewable boolean DEFAULT false NOT NULL,
    max_purchases_per_stay integer,
    display jsonb,
    visible_from timestamp with time zone,
    visible_until timestamp with time zone,
    CONSTRAINT internet_package_revisions_package_type_check CHECK ((package_type = ANY (ARRAY['FREE_STAY'::text, 'ONE_DAY'::text, 'REST_OF_STAY'::text, 'POST_STAY'::text, 'GENERAL'::text, 'CHECKOUT_GRACE'::text]))),
    CONSTRAINT internet_package_revisions_price_minor_check CHECK ((price_minor >= 0))
);


--
-- Name: internet_packages; Type: TABLE; Schema: iam_v2; Owner: -
--

CREATE TABLE iam_v2.internet_packages (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    code text NOT NULL,
    active boolean DEFAULT true NOT NULL,
    is_system boolean DEFAULT false NOT NULL,
    current_revision_id uuid,
    central_template_id uuid
);


--
-- Name: offer_quotes; Type: TABLE; Schema: iam_v2; Owner: -
--

CREATE TABLE iam_v2.offer_quotes (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    auth_context_id uuid NOT NULL,
    package_revision_id uuid NOT NULL,
    pms_interface_id uuid,
    settlement_mapping_id uuid,
    price_minor bigint NOT NULL,
    currency character(3),
    currency_exponent smallint,
    tax_code text,
    tax_rate_bp integer,
    tax_amount_minor bigint,
    grant_snapshot jsonb NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    consumed_at timestamp with time zone
);


--
-- Name: online_time_skipped_intervals; Type: TABLE; Schema: iam_v2; Owner: -
--

CREATE TABLE iam_v2.online_time_skipped_intervals (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    session_id uuid NOT NULL,
    entitlement_id uuid NOT NULL,
    skipped_from timestamp with time zone NOT NULL,
    skipped_to timestamp with time zone NOT NULL,
    cause text NOT NULL,
    recorded_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT online_time_skipped_intervals_cause_check CHECK ((cause = 'UNOBSERVED_GAP'::text)),
    CONSTRAINT online_time_skipped_intervals_check CHECK ((skipped_to > skipped_from))
);


--
-- Name: TABLE online_time_skipped_intervals; Type: COMMENT; Schema: iam_v2; Owner: -
--

COMMENT ON TABLE iam_v2.online_time_skipped_intervals IS 'Intervals of apparently-elapsed session time that were deliberately NOT charged to an AGGREGATE_ONLINE_TIME budget because the accounting service was not observing them. Under-charging is the intended failure direction; this table is what makes it visible rather than silent.';


--
-- Name: package_eligibility_rules; Type: TABLE; Schema: iam_v2; Owner: -
--

CREATE TABLE iam_v2.package_eligibility_rules (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    package_revision_id uuid NOT NULL,
    rule_type text NOT NULL,
    rule_value jsonb DEFAULT '{}'::jsonb NOT NULL
);


--
-- Name: package_grant_tiers; Type: TABLE; Schema: iam_v2; Owner: -
--

CREATE TABLE iam_v2.package_grant_tiers (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    package_revision_id uuid NOT NULL,
    tier_order integer NOT NULL,
    grant_value jsonb DEFAULT '{}'::jsonb NOT NULL
);


--
-- Name: package_settlement_mappings; Type: TABLE; Schema: iam_v2; Owner: -
--

CREATE TABLE iam_v2.package_settlement_mappings (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    package_revision_id uuid NOT NULL,
    pms_interface_id uuid NOT NULL,
    mapping_revision integer NOT NULL,
    posting_code text NOT NULL,
    tax_code text,
    tax_rate_bp integer,
    retired_at timestamp with time zone,
    replaces_mapping_id uuid
);


--
-- Name: payment_provider_accounts; Type: TABLE; Schema: iam_v2; Owner: -
--

CREATE TABLE iam_v2.payment_provider_accounts (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    provider text NOT NULL,
    merchant_account_ref text,
    display_name text,
    currency character(3),
    status text DEFAULT 'DISABLED'::text NOT NULL,
    is_default boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    provenance text DEFAULT 'CONFIGURED'::text NOT NULL,
    CONSTRAINT payment_provider_accounts_merchant_account_ref_check CHECK (((btrim(merchant_account_ref) <> ''::text) AND (length(merchant_account_ref) <= 128))),
    CONSTRAINT payment_provider_accounts_provenance_check CHECK ((provenance = ANY (ARRAY['CONFIGURED'::text, 'BACKFILLED_UNVERIFIED'::text]))),
    CONSTRAINT payment_provider_accounts_provider_check CHECK (((provider ~ '^[a-z][a-z0-9_-]{1,31}$'::text) AND (provider <> ALL (ARRAY['none'::text, 'unknown'::text, 'placeholder'::text, 'todo'::text, 'default'::text, 'null'::text, 'na'::text, 'n_a'::text])))),
    CONSTRAINT payment_provider_accounts_status_check CHECK ((status = ANY (ARRAY['ACTIVE'::text, 'DISABLED'::text]))),
    CONSTRAINT ppa_default_is_active CHECK (((NOT is_default) OR (status = 'ACTIVE'::text))),
    CONSTRAINT ppa_reference_matches_provenance CHECK ((((provenance = 'CONFIGURED'::text) AND (merchant_account_ref IS NOT NULL) AND (btrim(merchant_account_ref) <> ''::text) AND (length(merchant_account_ref) <= 128)) OR ((provenance = 'BACKFILLED_UNVERIFIED'::text) AND (merchant_account_ref IS NULL)))),
    CONSTRAINT ppa_unverified_is_never_live CHECK (((provenance = 'CONFIGURED'::text) OR ((status = 'DISABLED'::text) AND (NOT is_default))))
);


--
-- Name: TABLE payment_provider_accounts; Type: COMMENT; Schema: iam_v2; Owner: -
--

COMMENT ON TABLE iam_v2.payment_provider_accounts IS 'Authoritative per-site payment provider and merchant-account configuration. Identifiers only, never credentials. A payment intent RESOLVES its provider and merchant account from here; no request may supply either.';


--
-- Name: COLUMN payment_provider_accounts.provenance; Type: COMMENT; Schema: iam_v2; Owner: -
--

COMMENT ON COLUMN iam_v2.payment_provider_accounts.provenance IS 'CONFIGURED: an operator supplied this account and its external reference is the provider''s own. BACKFILLED_UNVERIFIED: reconstructed by migration 0018 from a payment row that predates this table. Its external reference is UNKNOWN, not invented, and it can never become ACTIVE or default.';


--
-- Name: payment_transaction_events; Type: TABLE; Schema: iam_v2; Owner: -
--

CREATE TABLE iam_v2.payment_transaction_events (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    payment_transaction_id uuid NOT NULL,
    provider_event_id text NOT NULL,
    event_type text NOT NULL,
    asserted_status text,
    applied boolean DEFAULT false NOT NULL,
    detail jsonb DEFAULT '{}'::jsonb NOT NULL,
    received_at timestamp with time zone DEFAULT now() NOT NULL,
    provider text NOT NULL,
    merchant_account_id uuid NOT NULL,
    CONSTRAINT payment_transaction_events_asserted_status_check CHECK (((asserted_status IS NULL) OR (asserted_status = ANY (ARRAY['CREATED'::text, 'PENDING'::text, 'CAPTURED'::text, 'FAILED'::text, 'EXPIRED'::text, 'CANCELLED'::text, 'UNKNOWN'::text])))),
    CONSTRAINT ptx_event_detail_safe CHECK ((iam_v2.p4_callback_evidence_safe(detail) IS NULL)),
    CONSTRAINT ptx_event_ids_bounded CHECK ((((length(provider_event_id) >= 1) AND (length(provider_event_id) <= 200)) AND (provider_event_id !~ '[\x00-\x1f\x7f]'::text) AND ((length(event_type) >= 1) AND (length(event_type) <= 100)) AND (event_type !~ '[\x00-\x1f\x7f]'::text)))
);


--
-- Name: TABLE payment_transaction_events; Type: COMMENT; Schema: iam_v2; Owner: -
--

COMMENT ON TABLE iam_v2.payment_transaction_events IS 'Append-only provider callback ledger. UNIQUE (payment_transaction_id, provider_event_id) is the duplicate-callback defence: a replayed webhook cannot be applied twice, whatever the caller does.';


--
-- Name: payment_transactions; Type: TABLE; Schema: iam_v2; Owner: -
--

CREATE TABLE iam_v2.payment_transactions (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    settlement_id uuid NOT NULL,
    merchant_account_id uuid NOT NULL,
    transaction_type text NOT NULL,
    parent_transaction_id uuid,
    provider text NOT NULL,
    provider_ref text NOT NULL,
    idempotency_key text NOT NULL,
    amount_minor bigint NOT NULL,
    currency character(3) NOT NULL,
    currency_exponent smallint NOT NULL,
    status text NOT NULL,
    provider_txn_ref text,
    intent_created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT payment_transactions_amount_minor_check CHECK ((amount_minor > 0)),
    CONSTRAINT payment_transactions_status_check CHECK ((status = ANY (ARRAY['CREATED'::text, 'PENDING'::text, 'CAPTURED'::text, 'FAILED'::text, 'EXPIRED'::text, 'CANCELLED'::text, 'UNKNOWN'::text]))),
    CONSTRAINT payment_transactions_transaction_type_check CHECK ((transaction_type = ANY (ARRAY['CHARGE'::text, 'REFUND'::text, 'CHARGEBACK'::text]))),
    CONSTRAINT ptx_local_ref_bounded CHECK ((((length(provider_ref) >= 1) AND (length(provider_ref) <= 200)) AND (provider_ref !~ '[\x00-\x1f\x7f]'::text))),
    CONSTRAINT ptx_parent CHECK (((transaction_type = 'CHARGE'::text) = (parent_transaction_id IS NULL))),
    CONSTRAINT ptx_provider_txn_ref_bounded CHECK (((provider_txn_ref IS NULL) OR (((length(provider_txn_ref) >= 1) AND (length(provider_txn_ref) <= 200)) AND (provider_txn_ref !~ '[\x00-\x1f\x7f]'::text))))
);


--
-- Name: COLUMN payment_transactions.provider_ref; Type: COMMENT; Schema: iam_v2; Owner: -
--

COMMENT ON COLUMN iam_v2.payment_transactions.provider_ref IS 'LOCAL durable intent reference, generated before any external call and sent to the provider as the client reference. It is the idempotency root: it exists even if the process dies before the provider answers.';


--
-- Name: COLUMN payment_transactions.provider_txn_ref; Type: COMMENT; Schema: iam_v2; Owner: -
--

COMMENT ON COLUMN iam_v2.payment_transactions.provider_txn_ref IS 'The reference the PROVIDER assigned. NULL until the provider answers; write-once thereafter.';


--
-- Name: pms_interface_pnumber_seq; Type: TABLE; Schema: iam_v2; Owner: -
--

CREATE TABLE iam_v2.pms_interface_pnumber_seq (
    tenant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    pms_interface_id uuid NOT NULL,
    next_p_number bigint DEFAULT 1 NOT NULL
);


--
-- Name: pms_interface_revisions; Type: TABLE; Schema: iam_v2; Owner: -
--

CREATE TABLE iam_v2.pms_interface_revisions (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    pms_interface_id uuid NOT NULL,
    revision_no integer NOT NULL,
    source_timezone text NOT NULL,
    folio_identity_strategy text DEFAULT 'UNSET'::text NOT NULL,
    config jsonb NOT NULL,
    normalization_version integer DEFAULT 1 NOT NULL,
    source_fingerprint text,
    financial_base_currency character(3),
    financial_base_currency_exponent smallint,
    CONSTRAINT pms_interface_revisions_folio_identity_strategy_check CHECK ((folio_identity_strategy = ANY (ARRAY['UNSET'::text, 'GLOBALLY_UNIQUE'::text, 'UNIQUE_PER_STAY'::text, 'REUSED_SEQUENTIAL'::text]))),
    CONSTRAINT pmsrev_financial_currency_exponent_range CHECK (((financial_base_currency_exponent IS NULL) OR ((financial_base_currency_exponent >= 0) AND (financial_base_currency_exponent <= 4)))),
    CONSTRAINT pmsrev_financial_currency_iso CHECK (((financial_base_currency IS NULL) OR (financial_base_currency ~ '^[A-Z]{3}$'::text))),
    CONSTRAINT pmsrev_financial_currency_pair CHECK (((financial_base_currency IS NULL) = (financial_base_currency_exponent IS NULL)))
);


--
-- Name: COLUMN pms_interface_revisions.financial_base_currency; Type: COMMENT; Schema: iam_v2; Owner: -
--

COMMENT ON COLUMN iam_v2.pms_interface_revisions.financial_base_currency IS 'G2/Tier-2: authoritative property financial currency for this revision. NULL = not financially onboarded (fail-closed: no posting may execute). Immutable with the revision; onboarding publishes a new revision.';


--
-- Name: COLUMN pms_interface_revisions.financial_base_currency_exponent; Type: COMMENT; Schema: iam_v2; Owner: -
--

COMMENT ON COLUMN iam_v2.pms_interface_revisions.financial_base_currency_exponent IS 'G2: ISO-4217 minor-unit exponent for financial_base_currency. Amounts are integer minor units everywhere.';


--
-- Name: pms_interface_runtime; Type: TABLE; Schema: iam_v2; Owner: -
--

CREATE TABLE iam_v2.pms_interface_runtime (
    tenant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    pms_interface_id uuid NOT NULL,
    pinned_revision_id uuid,
    pinned_secret_generation_id uuid,
    credential_mode text DEFAULT 'AUTH_KEY'::text NOT NULL,
    runtime_generation bigint DEFAULT 0 NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    transport_status text DEFAULT 'UNKNOWN'::text NOT NULL,
    last_connect_attempt_at timestamp with time zone,
    last_connected_at timestamp with time zone,
    last_heartbeat_at timestamp with time zone,
    disconnected_since timestamp with time zone,
    transport_error_code text,
    continuity_status text DEFAULT 'UNKNOWN'::text NOT NULL,
    last_valid_event_at timestamp with time zone,
    last_event_cursor text,
    discontinuity_detected_at timestamp with time zone,
    last_resync_marker_at timestamp with time zone,
    sync_status text DEFAULT 'UNKNOWN'::text NOT NULL,
    resync_requested_at timestamp with time zone,
    resync_started_at timestamp with time zone,
    last_complete_sync_at timestamp with time zone,
    sync_cursor text,
    last_sync_failure_code text,
    resync_generation_seq bigint DEFAULT 0 NOT NULL,
    published_resync_generation bigint DEFAULT 0 NOT NULL,
    CONSTRAINT pir_bounded_lengths CHECK ((((transport_error_code IS NULL) OR (length(transport_error_code) <= 200)) AND ((last_sync_failure_code IS NULL) OR (length(last_sync_failure_code) <= 200)) AND ((last_event_cursor IS NULL) OR (length(last_event_cursor) <= 4096)) AND ((sync_cursor IS NULL) OR (length(sync_cursor) <= 4096)))),
    CONSTRAINT pir_connected_pins CHECK (((transport_status <> 'CONNECTED'::text) OR ((pinned_revision_id IS NOT NULL) AND (last_connected_at IS NOT NULL) AND ((credential_mode = 'NONE'::text) OR (pinned_secret_generation_id IS NOT NULL))))),
    CONSTRAINT pir_generation_nonneg CHECK ((runtime_generation >= 0)),
    CONSTRAINT pir_heartbeat_not_future CHECK (((last_heartbeat_at IS NULL) OR (last_heartbeat_at <= updated_at))),
    CONSTRAINT pir_resync_coherent CHECK ((((resync_started_at IS NULL) OR (resync_requested_at IS NOT NULL)) AND ((resync_started_at IS NULL) OR (resync_requested_at IS NULL) OR (resync_started_at >= resync_requested_at)))),
    CONSTRAINT pir_resync_generation_coherent CHECK (((resync_generation_seq >= 0) AND (published_resync_generation >= 0) AND (published_resync_generation <= resync_generation_seq))),
    CONSTRAINT pms_interface_runtime_continuity_status_check CHECK ((continuity_status = ANY (ARRAY['UNKNOWN'::text, 'CONTINUOUS'::text, 'DISCONTINUOUS'::text, 'GAP_DETECTED'::text]))),
    CONSTRAINT pms_interface_runtime_credential_mode_check CHECK ((credential_mode = ANY (ARRAY['NONE'::text, 'AUTH_KEY'::text]))),
    CONSTRAINT pms_interface_runtime_sync_status_check CHECK ((sync_status = ANY (ARRAY['UNKNOWN'::text, 'IN_SYNC'::text, 'RESYNC_REQUIRED'::text, 'RESYNC_IN_PROGRESS'::text, 'SYNC_FAILED'::text]))),
    CONSTRAINT pms_interface_runtime_transport_status_check CHECK ((transport_status = ANY (ARRAY['UNKNOWN'::text, 'CONNECTING'::text, 'CONNECTED'::text, 'DISCONNECTED'::text, 'ERROR'::text])))
);


--
-- Name: pms_interface_secret_generations; Type: TABLE; Schema: iam_v2; Owner: -
--

CREATE TABLE iam_v2.pms_interface_secret_generations (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    pms_interface_id uuid NOT NULL,
    generation_no integer NOT NULL,
    ciphertext bytea NOT NULL,
    nonce bytea NOT NULL,
    encryption_key_id uuid NOT NULL,
    cipher_version integer NOT NULL,
    superseded_at timestamp with time zone
);


--
-- Name: pms_interfaces; Type: TABLE; Schema: iam_v2; Owner: -
--

CREATE TABLE iam_v2.pms_interfaces (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    connector_kind text NOT NULL,
    display_label text,
    lifecycle_state text DEFAULT 'ACTIVE'::text NOT NULL,
    current_revision_id uuid,
    CONSTRAINT pms_interfaces_lifecycle_state_check CHECK ((lifecycle_state = ANY (ARRAY['ACTIVE'::text, 'AUTH_DISABLED'::text, 'DRAINING'::text, 'DECOMMISSIONED'::text])))
);


--
-- Name: pms_postings; Type: TABLE; Schema: iam_v2; Owner: -
--

CREATE TABLE iam_v2.pms_postings (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    pms_interface_id uuid NOT NULL,
    settlement_id uuid NOT NULL,
    purchase_id uuid NOT NULL,
    stay_id uuid,
    folio_id uuid,
    posting_interface_revision_id uuid NOT NULL,
    secret_generation_id uuid,
    posting_type text NOT NULL,
    reverses_posting_id uuid,
    amount_minor bigint NOT NULL,
    currency character(3),
    currency_exponent smallint,
    idempotency_key text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT pms_postings_posting_type_check CHECK ((posting_type = ANY (ARRAY['CHARGE'::text, 'REVERSAL'::text]))),
    CONSTRAINT posting_reversal_link CHECK (((posting_type = 'REVERSAL'::text) = (reverses_posting_id IS NOT NULL)))
);


--
-- Name: pms_source_conflicts; Type: TABLE; Schema: iam_v2; Owner: -
--

CREATE TABLE iam_v2.pms_source_conflicts (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    interface_a uuid NOT NULL,
    interface_b uuid NOT NULL,
    severity text,
    resolution text,
    CONSTRAINT psc_order CHECK ((interface_a < interface_b))
);


--
-- Name: post_stay_profiles; Type: TABLE; Schema: iam_v2; Owner: -
--

CREATE TABLE iam_v2.post_stay_profiles (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    origin_stay_id uuid NOT NULL,
    origin_lifecycle_version integer NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    pin_hash text NOT NULL,
    pin_generation integer DEFAULT 1 NOT NULL,
    pin_set_at timestamp with time zone DEFAULT now() NOT NULL,
    pin_revealed_at timestamp with time zone,
    valid_until timestamp with time zone NOT NULL,
    status text DEFAULT 'ACTIVE'::text NOT NULL,
    revoked_at timestamp with time zone,
    revoked_by uuid,
    revoke_reason text,
    issued_via text NOT NULL,
    issued_by_operator uuid,
    issued_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT post_stay_profiles_issued_via_check CHECK ((issued_via = ANY (ARRAY['GUEST_AUTHENTICATED_SESSION'::text, 'OPERATOR_RESET'::text]))),
    CONSTRAINT post_stay_profiles_pin_generation_check CHECK ((pin_generation > 0)),
    CONSTRAINT post_stay_profiles_pin_hash_check CHECK ((pin_hash ~~ '$argon2id$%'::text)),
    CONSTRAINT post_stay_profiles_status_check CHECK ((status = ANY (ARRAY['ACTIVE'::text, 'REVOKED'::text]))),
    CONSTRAINT psp_issuer_coherent CHECK ((((issued_via = 'OPERATOR_RESET'::text) AND (issued_by_operator IS NOT NULL)) OR ((issued_via = 'GUEST_AUTHENTICATED_SESSION'::text) AND (issued_by_operator IS NULL)))),
    CONSTRAINT psp_revoked_coherent CHECK ((((status = 'ACTIVE'::text) AND (revoked_at IS NULL) AND (revoked_by IS NULL) AND (revoke_reason IS NULL)) OR ((status = 'REVOKED'::text) AND (revoked_at IS NOT NULL) AND (revoke_reason IS NOT NULL)))),
    CONSTRAINT psp_validity_window CHECK ((valid_until > created_at))
);


--
-- Name: posting_attempt_events; Type: TABLE; Schema: iam_v2; Owner: -
--

CREATE TABLE iam_v2.posting_attempt_events (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    posting_attempt_id uuid NOT NULL,
    event_type text NOT NULL,
    detail jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: posting_attempts; Type: TABLE; Schema: iam_v2; Owner: -
--

CREATE TABLE iam_v2.posting_attempts (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    internal_posting_id uuid NOT NULL,
    pms_interface_id uuid NOT NULL,
    attempt_no integer NOT NULL,
    p_number text NOT NULL,
    rn text,
    g_number text,
    sent_at timestamp with time zone NOT NULL,
    outcome text DEFAULT 'SENDING'::text NOT NULL,
    response_at timestamp with time zone,
    pa_as_status text,
    CONSTRAINT attempt_gnumber_verified CHECK (((g_number IS NOT NULL) AND (btrim(g_number) <> ''::text) AND (length(g_number) <= 32))),
    CONSTRAINT attempt_gnumber_wire_safe CHECK ((g_number !~ '[\x00-\x1f\x7f|]'::text)),
    CONSTRAINT attempt_pnumber_wire_safe CHECK ((p_number ~ '^[0-9]{1,18}$'::text)),
    CONSTRAINT attempt_rn_verified CHECK (((rn IS NOT NULL) AND (btrim(rn) <> ''::text) AND (length(rn) <= 32))),
    CONSTRAINT attempt_rn_wire_safe CHECK ((rn !~ '[\x00-\x1f\x7f|]'::text)),
    CONSTRAINT posting_attempts_outcome_check CHECK ((outcome = ANY (ARRAY['SENDING'::text, 'ACKED'::text, 'UNKNOWN'::text, 'FAILED'::text]))),
    CONSTRAINT posting_attempts_pa_as_status_check CHECK ((pa_as_status = ANY (ARRAY['OK'::text, 'NG'::text, 'NA'::text, 'NP'::text, 'NR'::text, 'RY'::text, 'UR'::text])))
);


--
-- Name: CONSTRAINT attempt_gnumber_verified ON posting_attempts; Type: COMMENT; Schema: iam_v2; Owner: -
--

COMMENT ON CONSTRAINT attempt_gnumber_verified ON iam_v2.posting_attempts IS 'G1: G# is mandatory financial targeting evidence on every attempt.';


--
-- Name: CONSTRAINT attempt_rn_verified ON posting_attempts; Type: COMMENT; Schema: iam_v2; Owner: -
--

COMMENT ON CONSTRAINT attempt_rn_verified ON iam_v2.posting_attempts IS 'G1: RN is mandatory financial TARGETING evidence on every attempt. It is evidence, never the posting identity.';


--
-- Name: posting_outbox; Type: TABLE; Schema: iam_v2; Owner: -
--

CREATE TABLE iam_v2.posting_outbox (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    pms_interface_id uuid NOT NULL,
    posting_id uuid NOT NULL,
    state text DEFAULT 'QUEUED'::text NOT NULL,
    enqueued_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT posting_outbox_state_check CHECK ((state = ANY (ARRAY['QUEUED'::text, 'IN_FLIGHT'::text, 'DONE'::text, 'HELD_RECOVERY'::text])))
);


--
-- Name: COLUMN posting_outbox.enqueued_at; Type: COMMENT; Schema: iam_v2; Owner: -
--

COMMENT ON COLUMN iam_v2.posting_outbox.enqueued_at IS 'When this work entered the outbox. Rows that predate migration 0020 carry the migration time, so their measured age understates the true wait; no history is invented to hide that.';


--
-- Name: posting_review_state; Type: TABLE; Schema: iam_v2; Owner: -
--

CREATE TABLE iam_v2.posting_review_state (
    posting_id uuid NOT NULL,
    tenant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    review_version integer DEFAULT 0 NOT NULL,
    terminal_action text,
    terminal_action_id uuid,
    retry_authorized_attempt_no integer,
    escalation_count integer DEFAULT 0 NOT NULL,
    decided_at timestamp with time zone,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    retry_authorization_consumed_at timestamp with time zone,
    reversal_posting_id uuid,
    CONSTRAINT prs_consumed_needs_authorization CHECK (((retry_authorization_consumed_at IS NULL) OR (terminal_action = 'CONFIRM_NOT_POSTED_RETRY'::text))),
    CONSTRAINT prs_retry_only_for_retry CHECK (((retry_authorized_attempt_no IS NULL) OR (terminal_action = 'CONFIRM_NOT_POSTED_RETRY'::text))),
    CONSTRAINT prs_reversal_needs_action CHECK (((reversal_posting_id IS NULL) OR (terminal_action = 'CREATE_REVERSAL'::text))),
    CONSTRAINT prs_terminal_catalog CHECK (((terminal_action IS NULL) OR (terminal_action = ANY (ARRAY['CONFIRM_POSTED'::text, 'CONFIRM_NOT_POSTED_RETRY'::text, 'CONFIRM_NOT_POSTED_ABANDON'::text, 'CREATE_REVERSAL'::text])))),
    CONSTRAINT prs_terminal_decided CHECK (((terminal_action IS NULL) = (decided_at IS NULL))),
    CONSTRAINT prs_terminal_pair CHECK (((terminal_action IS NULL) = (terminal_action_id IS NULL))),
    CONSTRAINT prs_version_nonneg CHECK ((review_version >= 0))
);


--
-- Name: TABLE posting_review_state; Type: COMMENT; Schema: iam_v2; Owner: -
--

COMMENT ON TABLE iam_v2.posting_review_state IS 'C21: one mutable decision pointer per posting. Serializes concurrent financial reviewers. The review LEDGER (posting_review_actions) remains fully append-only and is the authoritative history.';


--
-- Name: posting_execution_state; Type: VIEW; Schema: iam_v2; Owner: -
--

CREATE VIEW iam_v2.posting_execution_state AS
 SELECT p.id AS posting_id,
    p.tenant_id,
    p.site_id,
    p.pms_interface_id,
    p.posting_type,
    p.amount_minor,
    p.currency,
    p.currency_exponent,
    p.idempotency_key,
    p.created_at,
        CASE
            WHEN (la.attempt_no IS NULL) THEN 'NOT_ATTEMPTED'::text
            WHEN (la.outcome = 'SENDING'::text) THEN 'IN_FLIGHT'::text
            WHEN (la.outcome = 'UNKNOWN'::text) THEN 'UNKNOWN'::text
            WHEN (la.outcome = 'FAILED'::text) THEN 'NOT_SENT'::text
            WHEN ((la.outcome = 'ACKED'::text) AND (la.pa_as_status = 'OK'::text)) THEN 'POSTED'::text
            WHEN (la.outcome = 'ACKED'::text) THEN 'REJECTED'::text
            ELSE NULL::text
        END AS execution_state,
    la.attempt_no AS latest_attempt_no,
    la.p_number AS latest_p_number,
    la.outcome AS latest_attempt_outcome,
    la.pa_as_status AS latest_pa_as_status,
    ac.attempt_count,
    ac.unknown_attempt_count,
    (ac.unknown_attempt_count > 0) AS has_unknown_history,
    ob.state AS outbox_state,
    rs.terminal_action AS terminal_review_action,
    rs.review_version,
    rs.escalation_count,
    rs.retry_authorized_attempt_no,
    (rs.retry_authorization_consumed_at IS NOT NULL) AS retry_authorization_consumed,
    ((la.outcome = 'UNKNOWN'::text) AND (rs.terminal_action IS NULL)) AS awaiting_manual_review,
    iam_v2.p4_interface_freshness_block(p.tenant_id, p.site_id, p.pms_interface_id, p.posting_interface_revision_id, now()) AS freshness_block
   FROM ((((iam_v2.pms_postings p
     LEFT JOIN LATERAL ( SELECT a.attempt_no,
            a.outcome,
            a.p_number,
            a.pa_as_status
           FROM iam_v2.posting_attempts a
          WHERE (a.internal_posting_id = p.id)
          ORDER BY a.attempt_no DESC
         LIMIT 1) la ON (true))
     LEFT JOIN LATERAL ( SELECT count(*) AS attempt_count,
            count(*) FILTER (WHERE (a.outcome = 'UNKNOWN'::text)) AS unknown_attempt_count
           FROM iam_v2.posting_attempts a
          WHERE (a.internal_posting_id = p.id)) ac ON (true))
     LEFT JOIN LATERAL ( SELECT o.state
           FROM iam_v2.posting_outbox o
          WHERE ((o.posting_id = p.id) AND (o.state = ANY (ARRAY['QUEUED'::text, 'IN_FLIGHT'::text, 'HELD_RECOVERY'::text])))
         LIMIT 1) ob ON (true))
     LEFT JOIN iam_v2.posting_review_state rs ON ((rs.posting_id = p.id)));


--
-- Name: posting_review_actions; Type: TABLE; Schema: iam_v2; Owner: -
--

CREATE TABLE iam_v2.posting_review_actions (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    posting_id uuid NOT NULL,
    action text NOT NULL,
    actor uuid NOT NULL,
    reason text NOT NULL,
    evidence jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT posting_review_actions_action_check CHECK ((action = ANY (ARRAY['CONFIRM_POSTED'::text, 'CONFIRM_NOT_POSTED_RETRY'::text, 'CONFIRM_NOT_POSTED_ABANDON'::text, 'CREATE_REVERSAL'::text, 'ESCALATE'::text])))
);


--
-- Name: purchases; Type: TABLE; Schema: iam_v2; Owner: -
--

CREATE TABLE iam_v2.purchases (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    package_revision_id uuid NOT NULL,
    offer_quote_id uuid,
    auth_context_id uuid,
    pms_interface_id uuid,
    stay_id uuid,
    settlement_mapping_id uuid,
    authentication_interface_revision_id uuid,
    trigger text NOT NULL,
    amount_minor bigint DEFAULT 0 NOT NULL,
    currency character(3),
    currency_exponent smallint,
    tax_code text,
    tax_rate_bp integer,
    tax_amount_minor bigint,
    state text DEFAULT 'PENDING'::text NOT NULL,
    purchase_seq integer DEFAULT 1 NOT NULL,
    checkout_episode integer,
    CONSTRAINT purchase_guest_needs_quote CHECK (((trigger <> 'GUEST_SELECTION'::text) OR (offer_quote_id IS NOT NULL))),
    CONSTRAINT purchases_amount_minor_check CHECK ((amount_minor >= 0)),
    CONSTRAINT purchases_state_check CHECK ((state = ANY (ARRAY['PENDING'::text, 'AWAITING_SETTLEMENT'::text, 'MANUAL_REVIEW'::text, 'GRANTED'::text, 'FAILED'::text, 'CANCELLED'::text]))),
    CONSTRAINT purchases_trigger_check CHECK ((trigger = ANY (ARRAY['GUEST_SELECTION'::text, 'VOUCHER_REDEMPTION'::text, 'ACCOUNT_AUTO_GRANT'::text, 'OTP_SOCIAL_DEFAULT'::text, 'CHECKOUT_GRACE'::text, 'EMERGENCY_GRACE'::text, 'POST_STAY_CONVERSION'::text, 'CROSS_PMS_TRANSFER'::text, 'ADMIN_GRANT'::text, 'RENEWAL'::text])))
);


--
-- Name: service_plan_revisions; Type: TABLE; Schema: iam_v2; Owner: -
--

CREATE TABLE iam_v2.service_plan_revisions (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    service_plan_id uuid NOT NULL,
    revision_no integer NOT NULL,
    name text,
    down_kbps integer,
    up_kbps integer,
    max_concurrent_devices integer DEFAULT 1 NOT NULL,
    device_limit_policy text DEFAULT 'REJECT_NEW_DEVICE'::text NOT NULL,
    idle_timeout_seconds integer,
    max_continuous_session_seconds integer,
    time_accounting_mode text DEFAULT 'VALIDITY_WINDOW'::text NOT NULL,
    time_quota_seconds bigint,
    data_quota_bytes bigint,
    CONSTRAINT service_plan_revisions_device_limit_policy_check CHECK ((device_limit_policy = ANY (ARRAY['REJECT_NEW_DEVICE'::text, 'DISCONNECT_OLDEST'::text, 'ADMIN_APPROVAL'::text]))),
    CONSTRAINT service_plan_revisions_max_concurrent_devices_check CHECK ((max_concurrent_devices >= 1)),
    CONSTRAINT service_plan_revisions_time_accounting_mode_check CHECK ((time_accounting_mode = ANY (ARRAY['VALIDITY_WINDOW'::text, 'AGGREGATE_ONLINE_TIME'::text])))
);


--
-- Name: service_plans; Type: TABLE; Schema: iam_v2; Owner: -
--

CREATE TABLE iam_v2.service_plans (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    code text NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    current_revision_id uuid
);


--
-- Name: session_counter_watermarks; Type: TABLE; Schema: iam_v2; Owner: -
--

CREATE TABLE iam_v2.session_counter_watermarks (
    tenant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    session_id uuid NOT NULL,
    source_epoch integer DEFAULT 1 NOT NULL,
    last_up bigint DEFAULT 0 NOT NULL,
    last_down bigint DEFAULT 0 NOT NULL,
    sample_seq bigint DEFAULT 0 NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: session_entitlement_bindings; Type: TABLE; Schema: iam_v2; Owner: -
--

CREATE TABLE iam_v2.session_entitlement_bindings (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    session_id uuid NOT NULL,
    entitlement_id uuid NOT NULL,
    seq bigint NOT NULL,
    bound_from timestamp with time zone NOT NULL,
    bound_until timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT seb_interval_ordered CHECK (((bound_until IS NULL) OR (bound_until >= bound_from))),
    CONSTRAINT session_entitlement_bindings_seq_check CHECK ((seq >= 1))
);


--
-- Name: session_online_watermarks; Type: TABLE; Schema: iam_v2; Owner: -
--

CREATE TABLE iam_v2.session_online_watermarks (
    tenant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    session_id uuid NOT NULL,
    accounted_through timestamp with time zone NOT NULL,
    accounted_seconds bigint DEFAULT 0 NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT session_online_watermarks_accounted_seconds_check CHECK ((accounted_seconds >= 0))
);


--
-- Name: TABLE session_online_watermarks; Type: COMMENT; Schema: iam_v2; Owner: -
--

COMMENT ON TABLE iam_v2.session_online_watermarks IS 'Per-session durable charged-through instant for AGGREGATE_ONLINE_TIME. Bytes are idempotent because they are cumulative counters compared against a watermark (contract 6.4); wall-clock time has no counter, so it needs this watermark or a replayed tick, a delayed tick or a reboot would each double-charge.';


--
-- Name: sessions; Type: TABLE; Schema: iam_v2; Owner: -
--

CREATE TABLE iam_v2.sessions (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    entitlement_id uuid NOT NULL,
    device_id uuid NOT NULL,
    credential_method text,
    ip inet,
    mac macaddr,
    state text DEFAULT 'active'::text NOT NULL,
    started timestamp with time zone DEFAULT now() NOT NULL,
    ended timestamp with time zone,
    end_reason text,
    expires_at timestamp with time zone,
    bytes_up bigint DEFAULT 0 NOT NULL,
    bytes_down bigint DEFAULT 0 NOT NULL,
    ingress_interface text
);


--
-- Name: settlements; Type: TABLE; Schema: iam_v2; Owner: -
--

CREATE TABLE iam_v2.settlements (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    purchase_id uuid NOT NULL,
    method text NOT NULL,
    status text NOT NULL,
    CONSTRAINT settlements_method_check CHECK ((method = ANY (ARRAY['NOT_REQUIRED'::text, 'PREPAID'::text, 'PMS_POSTING'::text, 'ONLINE_PAYMENT'::text, 'MANUAL_APPROVAL'::text]))),
    CONSTRAINT settlements_status_check CHECK ((status = ANY (ARRAY['NOT_REQUIRED'::text, 'REQUIRED'::text, 'IN_PROGRESS'::text, 'SETTLED'::text, 'FAILED'::text, 'MANUAL_REVIEW'::text, 'PARTIALLY_REVERSED'::text, 'REVERSED'::text])))
);


--
-- Name: site_checkout_grace_config; Type: TABLE; Schema: iam_v2; Owner: -
--

CREATE TABLE iam_v2.site_checkout_grace_config (
    tenant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    grace_package_revision_id uuid,
    config jsonb DEFAULT '{}'::jsonb NOT NULL,
    eligibility_window_seconds integer DEFAULT 86400 NOT NULL,
    grace_duration_seconds integer,
    grace_down_kbps integer,
    grace_up_kbps integer,
    grace_data_quota_bytes bigint,
    grace_device_limit integer,
    grace_device_limit_policy text,
    config_version bigint DEFAULT 1 NOT NULL,
    CONSTRAINT grace_all_or_none CHECK ((((grace_duration_seconds IS NULL) AND (grace_down_kbps IS NULL) AND (grace_up_kbps IS NULL) AND (grace_data_quota_bytes IS NULL) AND (grace_device_limit IS NULL) AND (grace_device_limit_policy IS NULL)) OR ((grace_duration_seconds IS NOT NULL) AND (grace_down_kbps IS NOT NULL) AND (grace_up_kbps IS NOT NULL) AND (grace_data_quota_bytes IS NOT NULL) AND (grace_device_limit IS NOT NULL) AND (grace_device_limit_policy = 'REJECT_NEW_DEVICE'::text)))),
    CONSTRAINT grace_bounds CHECK (((eligibility_window_seconds > 0) AND (eligibility_window_seconds <= 604800) AND ((grace_duration_seconds IS NULL) OR ((grace_duration_seconds > 0) AND (grace_duration_seconds <= 604800))) AND ((grace_down_kbps IS NULL) OR ((grace_down_kbps > 0) AND (grace_down_kbps <= 10000000))) AND ((grace_up_kbps IS NULL) OR ((grace_up_kbps > 0) AND (grace_up_kbps <= 10000000))) AND ((grace_data_quota_bytes IS NULL) OR ((grace_data_quota_bytes > 0) AND (grace_data_quota_bytes <= '1099511627776'::bigint))) AND ((grace_device_limit IS NULL) OR ((grace_device_limit > 0) AND (grace_device_limit <= 1000))))),
    CONSTRAINT grace_config_no_dup_policy_keys CHECK ((NOT (config ?| ARRAY['eligibility_window_seconds'::text, 'grace_duration_seconds'::text, 'grace_down_kbps'::text, 'grace_up_kbps'::text, 'grace_data_quota_bytes'::text, 'grace_device_limit'::text, 'grace_device_limit_policy'::text, 'device_limit_policy'::text, 'data_quota_bytes'::text, 'duration_seconds'::text, 'down_kbps'::text, 'up_kbps'::text]))),
    CONSTRAINT site_checkout_grace_config_config_version_check CHECK ((config_version >= 1)),
    CONSTRAINT site_checkout_grace_config_grace_device_limit_policy_check CHECK (((grace_device_limit_policy IS NULL) OR (grace_device_limit_policy = 'REJECT_NEW_DEVICE'::text)))
);


--
-- Name: stay_events; Type: TABLE; Schema: iam_v2; Owner: -
--

CREATE TABLE iam_v2.stay_events (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    pms_interface_id uuid NOT NULL,
    stay_id uuid,
    external_event_identity text NOT NULL,
    event_type text NOT NULL,
    pms_timestamp_raw text,
    pms_timestamp_utc timestamp with time zone,
    source_timezone text,
    received_at timestamp with time zone DEFAULT now() NOT NULL,
    sequence_version bigint DEFAULT 0 NOT NULL,
    normalization_version integer DEFAULT 1 NOT NULL,
    clock_suspect boolean DEFAULT false NOT NULL,
    payload jsonb DEFAULT '{}'::jsonb NOT NULL,
    processing_status text DEFAULT 'PENDING'::text NOT NULL,
    processed_at timestamp with time zone,
    review_code text,
    admission_kind text DEFAULT 'LIVE'::text NOT NULL,
    admission_runtime_generation bigint DEFAULT 0 NOT NULL,
    resync_generation bigint DEFAULT 0 NOT NULL,
    fingerprint_key_version integer DEFAULT 0 NOT NULL,
    CONSTRAINT se_admission_coherent CHECK ((((admission_kind = 'LIVE'::text) AND (resync_generation = 0)) OR ((admission_kind = 'RESYNC'::text) AND (resync_generation > 0)))),
    CONSTRAINT stay_events_admission_kind_check CHECK ((admission_kind = ANY (ARRAY['LIVE'::text, 'RESYNC'::text]))),
    CONSTRAINT stay_events_admission_runtime_generation_check CHECK ((admission_runtime_generation >= 0)),
    CONSTRAINT stay_events_fingerprint_key_version_check CHECK ((fingerprint_key_version >= 0)),
    CONSTRAINT stay_events_processing_status_check CHECK ((processing_status = ANY (ARRAY['PENDING'::text, 'APPLIED'::text, 'SKIPPED_DUPLICATE'::text, 'MANUAL_REVIEW'::text, 'FAILED'::text]))),
    CONSTRAINT stay_events_resync_generation_check CHECK ((resync_generation >= 0)),
    CONSTRAINT stay_events_review_code_check CHECK (((review_code IS NULL) OR (length(review_code) <= 200)))
);


--
-- Name: stay_folios; Type: TABLE; Schema: iam_v2; Owner: -
--

CREATE TABLE iam_v2.stay_folios (
    tenant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    pms_interface_id uuid NOT NULL,
    stay_id uuid NOT NULL,
    folio_id uuid NOT NULL,
    is_default_posting_target boolean DEFAULT false NOT NULL
);


--
-- Name: stay_guests; Type: TABLE; Schema: iam_v2; Owner: -
--

CREATE TABLE iam_v2.stay_guests (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    pms_interface_id uuid NOT NULL,
    stay_id uuid NOT NULL,
    external_guest_id text,
    first_name_norm text,
    last_name_norm text,
    display_name text,
    is_primary boolean DEFAULT false NOT NULL,
    date_of_birth date,
    pin_hash text
);


--
-- Name: stay_links; Type: TABLE; Schema: iam_v2; Owner: -
--

CREATE TABLE iam_v2.stay_links (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    from_stay uuid NOT NULL,
    to_stay uuid NOT NULL,
    reason text NOT NULL,
    CONSTRAINT stay_links_reason_check CHECK ((reason = ANY (ARRAY['CROSS_PMS_TRANSFER'::text, 'POST_STAY'::text])))
);


--
-- Name: stays; Type: TABLE; Schema: iam_v2; Owner: -
--

CREATE TABLE iam_v2.stays (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    pms_interface_id uuid NOT NULL,
    external_reservation_id text NOT NULL,
    external_stay_identity text NOT NULL,
    normalized_room_number text,
    status text NOT NULL,
    lifecycle_version integer DEFAULT 1 NOT NULL,
    posting_allowed boolean DEFAULT false NOT NULL,
    posting_block_reason text,
    posting_permission_source text,
    posting_checked_at timestamp with time zone,
    last_applied_event_version bigint DEFAULT 0 NOT NULL,
    vip boolean,
    travel_agent text,
    room_type text,
    arrival date,
    departure date,
    effective_checkout_at timestamp with time zone,
    occupancy_evidence_at timestamp with time zone,
    occupancy_ingested_at timestamp with time zone,
    occupancy_revision_id uuid,
    occupancy_normalization_version integer,
    occupancy_clock_suspect boolean,
    occupancy_evidence_version bigint DEFAULT 0 NOT NULL,
    rate_plan text,
    last_applied_event_id uuid,
    CONSTRAINT posting_only_in_house CHECK (((posting_allowed = false) OR (status = 'IN_HOUSE'::text))),
    CONSTRAINT stays_effco_only_after_checkout CHECK (((effective_checkout_at IS NULL) OR (status = ANY (ARRAY['CHECKED_OUT'::text, 'POST_STAY_ACTIVE'::text])))),
    CONSTRAINT stays_evidence_version_coherent CHECK ((((occupancy_evidence_at IS NULL) AND (occupancy_evidence_version = 0)) OR ((occupancy_evidence_at IS NOT NULL) AND (occupancy_evidence_version > 0)))),
    CONSTRAINT stays_occupancy_all_or_none CHECK ((((occupancy_evidence_at IS NULL) AND (occupancy_ingested_at IS NULL) AND (occupancy_revision_id IS NULL) AND (occupancy_normalization_version IS NULL) AND (occupancy_clock_suspect IS NULL)) OR ((occupancy_evidence_at IS NOT NULL) AND (occupancy_ingested_at IS NOT NULL) AND (occupancy_revision_id IS NOT NULL) AND (occupancy_normalization_version IS NOT NULL) AND (occupancy_clock_suspect IS NOT NULL)))),
    CONSTRAINT stays_occupancy_evidence_version_check CHECK ((occupancy_evidence_version >= 0)),
    CONSTRAINT stays_occupancy_norm_pos CHECK (((occupancy_normalization_version IS NULL) OR (occupancy_normalization_version > 0))),
    CONSTRAINT stays_status_check CHECK ((status = ANY (ARRAY['RESERVED'::text, 'IN_HOUSE'::text, 'CHECKED_OUT'::text, 'POST_STAY_ACTIVE'::text, 'CANCELLED'::text, 'NO_SHOW'::text])))
);


--
-- Name: v_financial_payments; Type: VIEW; Schema: iam_v2; Owner: -
--

CREATE VIEW iam_v2.v_financial_payments AS
 SELECT tenant_id,
    site_id,
    id AS payment_id,
    settlement_id,
    transaction_type,
    status,
    provider,
    merchant_account_id,
    amount_minor,
    currency,
    currency_exponent,
    parent_transaction_id
   FROM iam_v2.payment_transactions t;


--
-- Name: VIEW v_financial_payments; Type: COMMENT; Schema: iam_v2; Owner: -
--

COMMENT ON VIEW iam_v2.v_financial_payments IS 'Redacted reporting projection. Deliberately omits provider_ref and idempotency_key: both are correlation handles, and a reporting role has nothing to correlate.';


--
-- Name: v_financial_recovery; Type: VIEW; Schema: iam_v2; Owner: -
--

CREATE VIEW iam_v2.v_financial_recovery AS
 SELECT tenant_id,
    site_id,
    epoch,
    reason,
    entered_at,
    released_at,
    ((released_at IS NULL) AND (reason <> 'INITIAL'::text)) AS recovery_active,
    ( SELECT count(*) AS count
           FROM iam_v2.financial_recovery_holds h
          WHERE ((h.tenant_id = e.tenant_id) AND (h.site_id = e.site_id) AND (h.epoch = e.epoch))) AS held_total,
    ( SELECT count(*) AS count
           FROM iam_v2.financial_recovery_holds h
          WHERE ((h.tenant_id = e.tenant_id) AND (h.site_id = e.site_id) AND (h.epoch = e.epoch) AND (h.resolution IS NULL))) AS held_open
   FROM iam_v2.financial_epochs e;


--
-- Name: v_financial_review_queue; Type: VIEW; Schema: iam_v2; Owner: -
--

CREATE VIEW iam_v2.v_financial_review_queue AS
 SELECT tenant_id,
    site_id,
    posting_id,
    review_version,
    terminal_action,
    escalation_count,
    decided_at
   FROM iam_v2.posting_review_state rs;


--
-- Name: v_financial_settlements; Type: VIEW; Schema: iam_v2; Owner: -
--

CREATE VIEW iam_v2.v_financial_settlements AS
 SELECT se.tenant_id,
    se.site_id,
    se.id AS settlement_id,
    se.purchase_id,
    se.method,
    se.status,
    p.amount_minor,
    p.currency,
    p.currency_exponent,
    p.state AS purchase_state
   FROM (iam_v2.settlements se
     JOIN iam_v2.purchases p ON (((p.tenant_id = se.tenant_id) AND (p.site_id = se.site_id) AND (p.id = se.purchase_id))));


--
-- Name: v_zero_attempt_recovery_queue; Type: VIEW; Schema: iam_v2; Owner: -
--

CREATE VIEW iam_v2.v_zero_attempt_recovery_queue AS
 SELECT o.tenant_id,
    o.site_id,
    o.posting_id,
    o.id AS outbox_id,
    p.pms_interface_id,
    p.amount_minor,
    p.currency,
    p.currency_exponent,
    h.id AS hold_id,
    h.resolution AS hold_resolution,
    h.resolved_at AS hold_resolved_at,
    rs.retry_authorized_attempt_no,
    ((h.resolution = 'CONFIRMED_NOT_COMPLETED'::text) AND (rs.retry_authorized_attempt_no IS NULL)) AS eligible_for_retry_authorization
   FROM (((iam_v2.posting_outbox o
     JOIN iam_v2.pms_postings p ON ((p.id = o.posting_id)))
     LEFT JOIN LATERAL ( SELECT fh.id,
            fh.tenant_id,
            fh.site_id,
            fh.epoch,
            fh.work_kind,
            fh.work_id,
            fh.held_status,
            fh.amount_minor,
            fh.currency,
            fh.held_at,
            fh.resolution,
            fh.resolved_at,
            fh.resolved_by,
            fh.resolution_note
           FROM iam_v2.financial_recovery_holds fh
          WHERE ((fh.work_kind = 'POSTING_OUTBOX'::text) AND (fh.work_id = o.id))
          ORDER BY fh.held_at DESC
         LIMIT 1) h ON (true))
     LEFT JOIN iam_v2.posting_review_state rs ON ((rs.posting_id = o.posting_id)))
  WHERE ((o.state = 'HELD_RECOVERY'::text) AND (NOT (EXISTS ( SELECT 1
           FROM iam_v2.posting_attempts a
          WHERE (a.internal_posting_id = o.posting_id)))));


--
-- Name: VIEW v_zero_attempt_recovery_queue; Type: COMMENT; Schema: iam_v2; Owner: -
--

COMMENT ON VIEW iam_v2.v_zero_attempt_recovery_queue IS 'Postings held by recovery with NO surviving attempts. They cannot appear in the ordinary review queue, which keys on attempts, and before this view the only safe way out of that state was unreachable from any operator surface.';


--
-- Name: voucher_batches; Type: TABLE; Schema: iam_v2; Owner: -
--

CREATE TABLE iam_v2.voucher_batches (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    package_revision_id uuid NOT NULL,
    label text
);


--
-- Name: voucher_code_key_generations; Type: TABLE; Schema: iam_v2; Owner: -
--

CREATE TABLE iam_v2.voucher_code_key_generations (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    generation_no integer NOT NULL,
    hmac_key_ciphertext bytea NOT NULL,
    aead_params jsonb NOT NULL,
    encryption_key_id uuid NOT NULL,
    superseded_at timestamp with time zone
);


--
-- Name: vouchers; Type: TABLE; Schema: iam_v2; Owner: -
--

CREATE TABLE iam_v2.vouchers (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    batch_id uuid,
    package_revision_id uuid NOT NULL,
    code_hmac bytea NOT NULL,
    code_ciphertext bytea NOT NULL,
    code_nonce bytea NOT NULL,
    code_key_generation_id uuid NOT NULL,
    code_last4 text NOT NULL,
    state text DEFAULT 'UNUSED'::text NOT NULL,
    redemption_valid_from timestamp with time zone,
    redemption_valid_until timestamp with time zone,
    notes text,
    CONSTRAINT vouchers_state_check CHECK ((state = ANY (ARRAY['UNUSED'::text, 'REDEEMED'::text, 'REVOKED'::text, 'REDEMPTION_EXPIRED'::text])))
);


--
-- Name: accounting_records; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.accounting_records (
    ts timestamp with time zone NOT NULL,
    session_id uuid NOT NULL,
    tenant_id uuid NOT NULL,
    appliance_id uuid NOT NULL,
    bytes_up bigint NOT NULL,
    bytes_down bigint NOT NULL
);


--
-- Name: appliance_boot_convergence; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.appliance_boot_convergence (
    id boolean DEFAULT true NOT NULL,
    boot_id text,
    boot_at timestamp with time zone,
    deadline_at timestamp with time zone,
    converged boolean DEFAULT false NOT NULL,
    converged_at timestamp with time zone,
    required_services text[] DEFAULT '{}'::text[] NOT NULL,
    pending_services text[] DEFAULT '{}'::text[] NOT NULL,
    alert_open boolean DEFAULT false NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT appliance_boot_convergence_id_check CHECK (id)
);


--
-- Name: appliance_recovery_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.appliance_recovery_events (
    id bigint NOT NULL,
    service text NOT NULL,
    event text NOT NULL,
    cause text,
    action text,
    backoff_level integer,
    result text,
    duration_ms bigint,
    actor text DEFAULT 'system'::text NOT NULL,
    detail jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: appliance_recovery_events_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

ALTER TABLE public.appliance_recovery_events ALTER COLUMN id ADD GENERATED ALWAYS AS IDENTITY (
    SEQUENCE NAME public.appliance_recovery_events_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
);


--
-- Name: appliance_service_health; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.appliance_service_health (
    service text NOT NULL,
    state text DEFAULT 'unknown'::text NOT NULL,
    process_state text,
    health_ok boolean,
    health_detail text,
    consecutive_failures integer DEFAULT 0 NOT NULL,
    restart_count bigint DEFAULT 0 NOT NULL,
    restarts_in_window integer DEFAULT 0 NOT NULL,
    restart_window_secs integer DEFAULT 0 NOT NULL,
    backoff_level integer DEFAULT 0 NOT NULL,
    backoff_ms bigint DEFAULT 0 NOT NULL,
    next_retry_at timestamp with time zone,
    first_failure_at timestamp with time zone,
    last_failure_at timestamp with time zone,
    last_failure_reason text,
    last_exit_code integer,
    last_exit_signal text,
    last_healthy_at timestamp with time zone,
    last_recovery_at timestamp with time zone,
    time_since_healthy_s bigint,
    degraded_dependency text,
    critical boolean DEFAULT true NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: appliances; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.appliances (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    serial text NOT NULL,
    name text NOT NULL,
    model text,
    version text,
    enrolled_at timestamp with time zone,
    last_seen_at timestamp with time zone,
    status text DEFAULT 'pending'::text NOT NULL,
    public_key text,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    identity_verified_at timestamp with time zone,
    CONSTRAINT appliances_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'enrolled'::text, 'online'::text, 'offline'::text, 'retired'::text])))
);


--
-- Name: audit_log; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.audit_log (
    ts timestamp with time zone DEFAULT now() NOT NULL,
    tenant_id uuid,
    actor_type text NOT NULL,
    actor_id text,
    action text NOT NULL,
    target_type text,
    target_id text,
    ip inet,
    user_agent text,
    payload jsonb DEFAULT '{}'::jsonb NOT NULL,
    CONSTRAINT audit_log_actor_type_check CHECK ((actor_type = ANY (ARRAY['operator'::text, 'system'::text, 'appliance'::text, 'guest'::text, 'api'::text])))
);


--
-- Name: auth_otps; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.auth_otps (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    appliance_id uuid,
    channel text NOT NULL,
    destination text NOT NULL,
    code_hash text NOT NULL,
    issued_at timestamp with time zone DEFAULT now() NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    attempts integer DEFAULT 0 NOT NULL,
    max_attempts integer DEFAULT 5 NOT NULL,
    consumed_at timestamp with time zone,
    ip inet,
    user_agent text,
    otp_key_generation integer,
    CONSTRAINT auth_otps_channel_check CHECK ((channel = ANY (ARRAY['email'::text, 'sms'::text])))
);


--
-- Name: auth_throttle_buckets; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.auth_throttle_buckets (
    scope_kind text NOT NULL,
    scope_key text NOT NULL,
    method text DEFAULT '*'::text NOT NULL,
    window_start timestamp with time zone NOT NULL,
    window_len_s integer NOT NULL,
    attempt_count integer DEFAULT 0 NOT NULL,
    blocked_until timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT auth_throttle_buckets_count_chk CHECK ((attempt_count >= 0)),
    CONSTRAINT auth_throttle_buckets_method_chk CHECK ((method = ANY (ARRAY['account'::text, 'otp'::text, 'voucher'::text, 'social'::text, 'pms'::text, 'post_stay_pin'::text, '*'::text]))),
    CONSTRAINT auth_throttle_buckets_scope_key_hex_chk CHECK ((scope_key ~ '^[0-9a-f]{64}$'::text)),
    CONSTRAINT auth_throttle_buckets_scope_kind_chk CHECK ((scope_kind = ANY (ARRAY['endpoint'::text, 'identity'::text, 'ip'::text, 'device'::text, 'method'::text]))),
    CONSTRAINT auth_throttle_buckets_window_len_chk CHECK ((window_len_s > 0))
);


--
-- Name: TABLE auth_throttle_buckets; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.auth_throttle_buckets IS 'Durable local-first auth throttle (D4). scope_key is an irreversible HMAC; method isolates auth methods; blocked_until applies across windows. No raw identity/IP/MAC/OTP.';


--
-- Name: backup_records; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.backup_records (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    started_at timestamp with time zone DEFAULT now() NOT NULL,
    finished_at timestamp with time zone,
    status text DEFAULT 'running'::text NOT NULL,
    kind text DEFAULT 'scheduled'::text NOT NULL,
    path text,
    size_bytes bigint,
    error text,
    CONSTRAINT backup_records_kind_check CHECK ((kind = ANY (ARRAY['scheduled'::text, 'manual'::text, 'pre_migration'::text]))),
    CONSTRAINT backup_records_status_check CHECK ((status = ANY (ARRAY['running'::text, 'ok'::text, 'failed'::text])))
);


--
-- Name: dhcp_pools; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.dhcp_pools (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    guest_network_id uuid NOT NULL,
    start_ip inet NOT NULL,
    end_ip inet NOT NULL,
    sort_order integer DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT dhcp_pool_order CHECK ((start_ip <= end_ip))
);


--
-- Name: dhcp_reservations; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.dhcp_reservations (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    guest_network_id uuid NOT NULL,
    mac macaddr NOT NULL,
    reserved_ip inet NOT NULL,
    hostname text,
    description text,
    enabled boolean DEFAULT true NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: edge_executed_commands; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.edge_executed_commands (
    command_id uuid NOT NULL,
    command_type text,
    status text,
    result jsonb,
    completed_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: edge_installed_updates; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.edge_installed_updates (
    update_id uuid NOT NULL,
    component text,
    version text,
    status text,
    installed_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: edge_offline_packages; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.edge_offline_packages (
    package_id uuid NOT NULL,
    nonce text,
    consumed_at timestamp with time zone DEFAULT now() NOT NULL,
    reconciled_at timestamp with time zone
);


--
-- Name: guest_networks; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.guest_networks (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    appliance_id uuid,
    name text NOT NULL,
    description text,
    ssid_label text,
    enabled boolean DEFAULT true NOT NULL,
    network_type text DEFAULT 'untagged'::text NOT NULL,
    parent_interface text NOT NULL,
    vlan_id integer,
    bridge_name text NOT NULL,
    gateway_cidr inet NOT NULL,
    gateway_ip inet NOT NULL,
    subnet_cidr cidr NOT NULL,
    dhcp_mode text DEFAULT 'local'::text NOT NULL,
    dns_mode text DEFAULT 'appliance'::text NOT NULL,
    dns_servers jsonb DEFAULT '[]'::jsonb NOT NULL,
    domain_name text DEFAULT 'guest.local'::text NOT NULL,
    lease_default_seconds integer DEFAULT 3600 NOT NULL,
    lease_min_seconds integer DEFAULT 900 NOT NULL,
    lease_max_seconds integer DEFAULT 7200 NOT NULL,
    relay_targets jsonb DEFAULT '[]'::jsonb NOT NULL,
    captive_portal_enabled boolean DEFAULT true NOT NULL,
    portal_url text,
    internet_access_enabled boolean DEFAULT true NOT NULL,
    nat_enabled boolean DEFAULT true NOT NULL,
    client_isolation_enabled boolean DEFAULT false NOT NULL,
    walled_garden_profile text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT gn_vlan_consistency CHECK ((((network_type = 'vlan'::text) AND (vlan_id IS NOT NULL)) OR ((network_type = 'untagged'::text) AND (vlan_id IS NULL)))),
    CONSTRAINT guest_networks_dhcp_mode_check CHECK ((dhcp_mode = ANY (ARRAY['local'::text, 'external'::text, 'relay'::text, 'disabled'::text]))),
    CONSTRAINT guest_networks_dns_mode_check CHECK ((dns_mode = ANY (ARRAY['appliance'::text, 'custom'::text]))),
    CONSTRAINT guest_networks_network_type_check CHECK ((network_type = ANY (ARRAY['untagged'::text, 'vlan'::text]))),
    CONSTRAINT guest_networks_vlan_id_check CHECK (((vlan_id IS NULL) OR ((vlan_id >= 1) AND (vlan_id <= 4094))))
);


--
-- Name: network_apply_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.network_apply_events (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    revision_id uuid,
    phase text NOT NULL,
    ok boolean NOT NULL,
    detail jsonb DEFAULT '{}'::jsonb NOT NULL,
    at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: network_config_revisions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.network_config_revisions (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    seq bigint NOT NULL,
    state text DEFAULT 'draft'::text NOT NULL,
    summary text,
    bundle_path text,
    intent jsonb DEFAULT '{}'::jsonb NOT NULL,
    validation jsonb DEFAULT '{}'::jsonb NOT NULL,
    previous_seq bigint,
    created_by uuid,
    validated_by uuid,
    applied_by uuid,
    confirmed_by uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    validated_at timestamp with time zone,
    applied_at timestamp with time zone,
    confirmed_at timestamp with time zone,
    confirm_deadline timestamp with time zone,
    failure_reason text,
    CONSTRAINT network_config_revisions_state_check CHECK ((state = ANY (ARRAY['draft'::text, 'validated'::text, 'applying'::text, 'pending_confirmation'::text, 'active'::text, 'failed'::text, 'rolled_back'::text, 'superseded'::text])))
);


--
-- Name: network_config_revisions_seq_seq; Type: SEQUENCE; Schema: public; Owner: -
--

ALTER TABLE public.network_config_revisions ALTER COLUMN seq ADD GENERATED ALWAYS AS IDENTITY (
    SEQUENCE NAME public.network_config_revisions_seq_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
);


--
-- Name: network_health_checks; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.network_health_checks (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    revision_id uuid,
    check_name text NOT NULL,
    ok boolean NOT NULL,
    detail text,
    at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: network_interfaces; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.network_interfaces (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    name text NOT NULL,
    mac macaddr,
    role text DEFAULT 'unused'::text NOT NULL,
    mode text DEFAULT 'auto'::text NOT NULL,
    parent text,
    vlan_capable boolean DEFAULT true NOT NULL,
    link_state text,
    speed_mbps integer,
    mtu integer,
    driver text,
    ip_addresses jsonb DEFAULT '[]'::jsonb NOT NULL,
    is_protected boolean DEFAULT false NOT NULL,
    last_seen_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT network_interfaces_mode_check CHECK ((mode = ANY (ARRAY['auto'::text, 'manual'::text, 'trunk'::text, 'bridge_slave'::text]))),
    CONSTRAINT network_interfaces_role_check CHECK ((role = ANY (ARRAY['management'::text, 'wan'::text, 'guest_access'::text, 'guest_trunk'::text, 'ha_sync'::text, 'unused'::text])))
);


--
-- Name: notification_providers; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.notification_providers (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    channel text NOT NULL,
    kind text NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    display_name text,
    api_key text,
    api_user text,
    from_address text,
    from_name text,
    region text,
    extra jsonb DEFAULT '{}'::jsonb NOT NULL,
    last_success_at timestamp with time zone,
    last_error text,
    last_error_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT notification_providers_channel_check CHECK ((channel = ANY (ARRAY['email'::text, 'sms'::text]))),
    CONSTRAINT notification_providers_kind_check CHECK ((kind = ANY (ARRAY['stub'::text, 'sendgrid'::text, 'ses'::text, 'twilio'::text])))
);


--
-- Name: operator_roles; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.operator_roles (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    operator_id uuid NOT NULL,
    tenant_id uuid,
    role text NOT NULL,
    CONSTRAINT operator_roles_role_check CHECK ((role = ANY (ARRAY['site_admin'::text, 'hotel_it_manager'::text, 'front_office_operator'::text, 'guest_relations_operator'::text, 'voucher_operator'::text, 'payments_operator'::text, 'site_viewer'::text, 'tenant_admin'::text, 'tenant_operator'::text, 'viewer'::text, 'billing'::text])))
);


--
-- Name: operators; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.operators (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid,
    email text NOT NULL,
    display_name text,
    password_hash text,
    status text DEFAULT 'active'::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    auth_method text DEFAULT 'local'::text NOT NULL,
    oidc_sub text,
    last_sso_login_at timestamp with time zone,
    CONSTRAINT operators_auth_method_check CHECK ((auth_method = ANY (ARRAY['local'::text, 'sso'::text]))),
    CONSTRAINT operators_status_check CHECK ((status = ANY (ARRAY['active'::text, 'disabled'::text, 'invited'::text])))
);


--
-- Name: otp_hmac_key_generations; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.otp_hmac_key_generations (
    generation integer NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    retired_at timestamp with time zone,
    active boolean DEFAULT true NOT NULL,
    note text,
    CONSTRAINT otp_hmac_key_generations_gen_chk CHECK ((generation >= 1))
);


--
-- Name: TABLE otp_hmac_key_generations; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.otp_hmac_key_generations IS 'OTP HMAC key-generation lifecycle (D7). No secret material — key bytes are appliance-local, 0600.';


--
-- Name: pms_attempts; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.pms_attempts (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    appliance_id uuid,
    room_number text NOT NULL,
    secondary_kind text NOT NULL,
    ip inet,
    success boolean NOT NULL,
    error_code text,
    attempted_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT pms_attempts_secondary_kind_check CHECK ((secondary_kind = ANY (ARRAY['first_name'::text, 'last_name'::text, 'reservation'::text, 'either'::text])))
);


--
-- Name: pms_providers; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.pms_providers (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    name text NOT NULL,
    kind text NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    display_name text,
    host text,
    port integer,
    use_tls boolean DEFAULT false NOT NULL,
    auth_key text,
    base_url text,
    api_key text,
    property_id text,
    extra jsonb DEFAULT '{}'::jsonb NOT NULL,
    field_map jsonb DEFAULT '{}'::jsonb NOT NULL,
    normalization jsonb DEFAULT '{}'::jsonb NOT NULL,
    stay_window jsonb DEFAULT '{}'::jsonb NOT NULL,
    status text DEFAULT 'idle'::text NOT NULL,
    last_record_at timestamp with time zone,
    last_error text,
    last_error_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    site_id uuid,
    CONSTRAINT pms_providers_kind_check CHECK ((kind = ANY (ARRAY['stub'::text, 'protel-fias'::text, 'opera-fias'::text, 'fidelio-fias'::text, 'mews'::text, 'apaleo'::text]))),
    CONSTRAINT pms_providers_status_check CHECK ((status = ANY (ARRAY['idle'::text, 'connecting'::text, 'connected'::text, 'degraded'::text, 'down'::text])))
);


--
-- Name: schema_migrations; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.schema_migrations (
    version text NOT NULL,
    applied_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: sites; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.sites (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    code text NOT NULL,
    name text NOT NULL,
    timezone text DEFAULT 'UTC'::text NOT NULL,
    country text,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: social_oauth_providers; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.social_oauth_providers (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    provider text NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    display_name text,
    client_id text NOT NULL,
    client_secret text NOT NULL,
    redirect_uri text NOT NULL,
    scopes text,
    extra jsonb DEFAULT '{}'::jsonb NOT NULL,
    last_success_at timestamp with time zone,
    last_error text,
    last_error_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT social_oauth_providers_provider_check CHECK ((provider = ANY (ARRAY['google'::text, 'apple'::text, 'facebook'::text, 'microsoft'::text])))
);


--
-- Name: social_oauth_states; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.social_oauth_states (
    state text NOT NULL,
    tenant_id uuid NOT NULL,
    appliance_id uuid,
    provider text NOT NULL,
    client_ip inet,
    client_mac macaddr,
    redirect_uri text NOT NULL,
    user_agent text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    consumed_at timestamp with time zone
);


--
-- Name: stripe_accounts; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.stripe_accounts (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    display_name text,
    publishable_key text NOT NULL,
    secret_key text NOT NULL,
    webhook_secret text NOT NULL,
    success_url text NOT NULL,
    cancel_url text NOT NULL,
    last_success_at timestamp with time zone,
    last_error text,
    last_error_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: stripe_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.stripe_events (
    event_id text NOT NULL,
    tenant_id uuid NOT NULL,
    event_type text NOT NULL,
    received_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: sync_checkpoints; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.sync_checkpoints (
    name text NOT NULL,
    value jsonb DEFAULT '{}'::jsonb NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: sync_outbox; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.sync_outbox (
    seq bigint NOT NULL,
    kind text NOT NULL,
    payload jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    sent_at timestamp with time zone,
    attempts integer DEFAULT 0 NOT NULL,
    next_attempt_at timestamp with time zone DEFAULT now() NOT NULL,
    dead boolean DEFAULT false NOT NULL,
    last_error text
);


--
-- Name: sync_outbox_seq_seq; Type: SEQUENCE; Schema: public; Owner: -
--

ALTER TABLE public.sync_outbox ALTER COLUMN seq ADD GENERATED ALWAYS AS IDENTITY (
    SEQUENCE NAME public.sync_outbox_seq_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
);


--
-- Name: system_network_audit; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.system_network_audit (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    actor text NOT NULL,
    actor_id uuid,
    source_ip text,
    action text NOT NULL,
    target text NOT NULL,
    previous_config jsonb,
    requested_config jsonb,
    validation_result jsonb,
    apply_result text,
    confirm_result text,
    rollback_result text,
    failure_reason text,
    backup_path text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    revision_id uuid,
    reason text,
    deadline timestamp with time zone,
    CONSTRAINT system_network_audit_target_check CHECK ((target = ANY (ARRAY['wan'::text, 'lan'::text, 'both'::text])))
);


--
-- Name: tenant_effective_limits; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.tenant_effective_limits (
    tenant_id uuid NOT NULL,
    key text NOT NULL,
    value_type text NOT NULL,
    int_value bigint,
    bool_value boolean,
    str_value text,
    source text DEFAULT 'license'::text NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT tenant_effective_limits_value_type_check CHECK ((value_type = ANY (ARRAY['int'::text, 'bool'::text, 'string'::text])))
);


--
-- Name: tenants; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.tenants (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    slug text NOT NULL,
    name text NOT NULL,
    status text DEFAULT 'active'::text NOT NULL,
    contact_email text,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    auth_methods jsonb DEFAULT '{"voucher": {"enabled": true, "template_id": null}}'::jsonb NOT NULL,
    branding jsonb DEFAULT '{}'::jsonb NOT NULL,
    CONSTRAINT tenants_status_check CHECK ((status = ANY (ARRAY['active'::text, 'suspended'::text, 'archived'::text])))
);


--
-- Name: walled_garden_rules; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.walled_garden_rules (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    site_id uuid,
    kind text NOT NULL,
    value text NOT NULL,
    ports integer[],
    description text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT walled_garden_rules_kind_check CHECK ((kind = ANY (ARRAY['domain'::text, 'cidr'::text, 'ip'::text])))
);


--
-- Name: accounting_checkpoints accounting_checkpoints_pkey; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.accounting_checkpoints
    ADD CONSTRAINT accounting_checkpoints_pkey PRIMARY KEY (id);


--
-- Name: accounting_checkpoints accounting_checkpoints_session_id_source_device_id_bridge_c_key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.accounting_checkpoints
    ADD CONSTRAINT accounting_checkpoints_session_id_source_device_id_bridge_c_key UNIQUE (session_id, source_device_id, bridge, class_minor);


--
-- Name: accounting_records accounting_records_pkey; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.accounting_records
    ADD CONSTRAINT accounting_records_pkey PRIMARY KEY (id);


--
-- Name: accounting_records accounting_records_session_id_sample_seq_key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.accounting_records
    ADD CONSTRAINT accounting_records_session_id_sample_seq_key UNIQUE (session_id, sample_seq);


--
-- Name: appliance_class_generation appliance_class_generation_pkey; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.appliance_class_generation
    ADD CONSTRAINT appliance_class_generation_pkey PRIMARY KEY (tenant_id, site_id, appliance_id);


--
-- Name: appliance_product_setting_changes appliance_product_setting_changes_pkey; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.appliance_product_setting_changes
    ADD CONSTRAINT appliance_product_setting_changes_pkey PRIMARY KEY (id);


--
-- Name: appliance_product_settings appliance_product_settings_pkey; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.appliance_product_settings
    ADD CONSTRAINT appliance_product_settings_pkey PRIMARY KEY (tenant_id, site_id, appliance_id);


--
-- Name: auth_context_offers auth_context_offers_auth_context_id_package_revision_id_key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.auth_context_offers
    ADD CONSTRAINT auth_context_offers_auth_context_id_package_revision_id_key UNIQUE (auth_context_id, package_revision_id);


--
-- Name: auth_context_offers auth_context_offers_pkey; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.auth_context_offers
    ADD CONSTRAINT auth_context_offers_pkey PRIMARY KEY (id);


--
-- Name: auth_contexts auth_contexts_id_pms_interface_id_key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.auth_contexts
    ADD CONSTRAINT auth_contexts_id_pms_interface_id_key UNIQUE (id, pms_interface_id);


--
-- Name: auth_contexts auth_contexts_pkey; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.auth_contexts
    ADD CONSTRAINT auth_contexts_pkey PRIMARY KEY (id);


--
-- Name: auth_contexts auth_contexts_tenant_id_site_id_id_key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.auth_contexts
    ADD CONSTRAINT auth_contexts_tenant_id_site_id_id_key UNIQUE (tenant_id, site_id, id);


--
-- Name: auth_resolutions auth_resolutions_pkey; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.auth_resolutions
    ADD CONSTRAINT auth_resolutions_pkey PRIMARY KEY (id);


--
-- Name: checkout_grace_alert_actions checkout_grace_alert_actions_audit_id_seq_key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.checkout_grace_alert_actions
    ADD CONSTRAINT checkout_grace_alert_actions_audit_id_seq_key UNIQUE (audit_id, seq);


--
-- Name: checkout_grace_alert_actions checkout_grace_alert_actions_pkey; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.checkout_grace_alert_actions
    ADD CONSTRAINT checkout_grace_alert_actions_pkey PRIMARY KEY (id);


--
-- Name: checkout_grace_audit checkout_grace_audit_pkey; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.checkout_grace_audit
    ADD CONSTRAINT checkout_grace_audit_pkey PRIMARY KEY (id);


--
-- Name: checkout_grace_audit checkout_grace_audit_tenant_id_site_id_id_key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.checkout_grace_audit
    ADD CONSTRAINT checkout_grace_audit_tenant_id_site_id_id_key UNIQUE (tenant_id, site_id, id);


--
-- Name: checkout_grace_audit checkout_grace_audit_tenant_id_site_id_stay_id_lifecycle_ve_key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.checkout_grace_audit
    ADD CONSTRAINT checkout_grace_audit_tenant_id_site_id_stay_id_lifecycle_ve_key UNIQUE (tenant_id, site_id, stay_id, lifecycle_version);


--
-- Name: checkout_grace_policy_publications checkout_grace_policy_publica_tenant_id_site_id_config_vers_key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.checkout_grace_policy_publications
    ADD CONSTRAINT checkout_grace_policy_publica_tenant_id_site_id_config_vers_key UNIQUE (tenant_id, site_id, config_version);


--
-- Name: checkout_grace_policy_publications checkout_grace_policy_publications_pkey; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.checkout_grace_policy_publications
    ADD CONSTRAINT checkout_grace_policy_publications_pkey PRIMARY KEY (id);


--
-- Name: compliance_archives compliance_archives_pkey; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.compliance_archives
    ADD CONSTRAINT compliance_archives_pkey PRIMARY KEY (id);


--
-- Name: controlled_operation_scope controlled_operation_scope_pkey; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.controlled_operation_scope
    ADD CONSTRAINT controlled_operation_scope_pkey PRIMARY KEY (txid, family);


--
-- Name: delayed_accounting_records delayed_accounting_records_accounting_record_id_key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.delayed_accounting_records
    ADD CONSTRAINT delayed_accounting_records_accounting_record_id_key UNIQUE (accounting_record_id);


--
-- Name: delayed_accounting_records delayed_accounting_records_pkey; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.delayed_accounting_records
    ADD CONSTRAINT delayed_accounting_records_pkey PRIMARY KEY (id);


--
-- Name: device_network_appearances device_network_appearances_pkey; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.device_network_appearances
    ADD CONSTRAINT device_network_appearances_pkey PRIMARY KEY (device_id, guest_network_id);


--
-- Name: devices devices_pkey; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.devices
    ADD CONSTRAINT devices_pkey PRIMARY KEY (id);


--
-- Name: devices devices_tenant_id_site_id_appliance_id_mac_key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.devices
    ADD CONSTRAINT devices_tenant_id_site_id_appliance_id_mac_key UNIQUE (tenant_id, site_id, appliance_id, mac);


--
-- Name: devices devices_tenant_id_site_id_id_key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.devices
    ADD CONSTRAINT devices_tenant_id_site_id_id_key UNIQUE (tenant_id, site_id, id);


--
-- Name: entitlement_adjustments entitlement_adjustments_pkey; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.entitlement_adjustments
    ADD CONSTRAINT entitlement_adjustments_pkey PRIMARY KEY (id);


--
-- Name: entitlement_boundary_watermarks entitlement_boundary_watermarks_entitlement_id_boundary_at_key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.entitlement_boundary_watermarks
    ADD CONSTRAINT entitlement_boundary_watermarks_entitlement_id_boundary_at_key UNIQUE (entitlement_id, boundary_at);


--
-- Name: entitlement_boundary_watermarks entitlement_boundary_watermarks_pkey; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.entitlement_boundary_watermarks
    ADD CONSTRAINT entitlement_boundary_watermarks_pkey PRIMARY KEY (id);


--
-- Name: entitlement_device_authorizations entitlement_device_authorizati_entitlement_id_device_id_seq_key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.entitlement_device_authorizations
    ADD CONSTRAINT entitlement_device_authorizati_entitlement_id_device_id_seq_key UNIQUE (entitlement_id, device_id, seq);


--
-- Name: entitlement_device_authorizations entitlement_device_authorizations_pkey; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.entitlement_device_authorizations
    ADD CONSTRAINT entitlement_device_authorizations_pkey PRIMARY KEY (id);


--
-- Name: entitlement_devices entitlement_devices_pkey; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.entitlement_devices
    ADD CONSTRAINT entitlement_devices_pkey PRIMARY KEY (entitlement_id, device_id);


--
-- Name: entitlement_state_transitions entitlement_state_transitions_entitlement_id_seq_key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.entitlement_state_transitions
    ADD CONSTRAINT entitlement_state_transitions_entitlement_id_seq_key UNIQUE (entitlement_id, seq);


--
-- Name: entitlement_state_transitions entitlement_state_transitions_pkey; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.entitlement_state_transitions
    ADD CONSTRAINT entitlement_state_transitions_pkey PRIMARY KEY (id);


--
-- Name: entitlement_state_transitions entitlement_state_transitions_supersedes_key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.entitlement_state_transitions
    ADD CONSTRAINT entitlement_state_transitions_supersedes_key UNIQUE (supersedes);


--
-- Name: entitlement_termination_evidence entitlement_termination_evidence_pkey; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.entitlement_termination_evidence
    ADD CONSTRAINT entitlement_termination_evidence_pkey PRIMARY KEY (entitlement_id);


--
-- Name: entitlement_transfers entitlement_transfers_from_entitlement_id_key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.entitlement_transfers
    ADD CONSTRAINT entitlement_transfers_from_entitlement_id_key UNIQUE (from_entitlement_id);


--
-- Name: entitlement_transfers entitlement_transfers_pkey; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.entitlement_transfers
    ADD CONSTRAINT entitlement_transfers_pkey PRIMARY KEY (id);


--
-- Name: entitlement_transfers entitlement_transfers_to_entitlement_id_key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.entitlement_transfers
    ADD CONSTRAINT entitlement_transfers_to_entitlement_id_key UNIQUE (to_entitlement_id);


--
-- Name: entitlements entitlements_pkey; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.entitlements
    ADD CONSTRAINT entitlements_pkey PRIMARY KEY (id);


--
-- Name: entitlements entitlements_purchase_id_key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.entitlements
    ADD CONSTRAINT entitlements_purchase_id_key UNIQUE (purchase_id);


--
-- Name: entitlements entitlements_supersedes_entitlement_id_key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.entitlements
    ADD CONSTRAINT entitlements_supersedes_entitlement_id_key UNIQUE (supersedes_entitlement_id);


--
-- Name: entitlements entitlements_tenant_id_id_key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.entitlements
    ADD CONSTRAINT entitlements_tenant_id_id_key UNIQUE (tenant_id, id);


--
-- Name: entitlements entitlements_tenant_id_site_id_id_key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.entitlements
    ADD CONSTRAINT entitlements_tenant_id_site_id_id_key UNIQUE (tenant_id, site_id, id);


--
-- Name: financial_epoch financial_epoch_pkey; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.financial_epoch
    ADD CONSTRAINT financial_epoch_pkey PRIMARY KEY (tenant_id, site_id);


--
-- Name: financial_epochs financial_epochs_pkey; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.financial_epochs
    ADD CONSTRAINT financial_epochs_pkey PRIMARY KEY (tenant_id, site_id, epoch);


--
-- Name: financial_recovery_holds financial_recovery_holds_pkey; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.financial_recovery_holds
    ADD CONSTRAINT financial_recovery_holds_pkey PRIMARY KEY (id);


--
-- Name: financial_recovery_holds financial_recovery_holds_tenant_id_site_id_epoch_work_kind__key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.financial_recovery_holds
    ADD CONSTRAINT financial_recovery_holds_tenant_id_site_id_epoch_work_kind__key UNIQUE (tenant_id, site_id, epoch, work_kind, work_id);


--
-- Name: financial_restore_events financial_restore_events_pkey; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.financial_restore_events
    ADD CONSTRAINT financial_restore_events_pkey PRIMARY KEY (id);


--
-- Name: financial_restore_events financial_restore_events_tenant_id_site_id_restore_generati_key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.financial_restore_events
    ADD CONSTRAINT financial_restore_events_tenant_id_site_id_restore_generati_key UNIQUE (tenant_id, site_id, restore_generation, restore_kind);


--
-- Name: folios folios_pkey; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.folios
    ADD CONSTRAINT folios_pkey PRIMARY KEY (id);


--
-- Name: folios folios_tenant_id_site_id_pms_interface_id_external_folio_id_key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.folios
    ADD CONSTRAINT folios_tenant_id_site_id_pms_interface_id_external_folio_id_key UNIQUE (tenant_id, site_id, pms_interface_id, external_folio_id, identity_epoch);


--
-- Name: folios folios_tenant_id_site_id_pms_interface_id_id_key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.folios
    ADD CONSTRAINT folios_tenant_id_site_id_pms_interface_id_id_key UNIQUE (tenant_id, site_id, pms_interface_id, id);


--
-- Name: guest_access_accounts guest_access_accounts_pkey; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.guest_access_accounts
    ADD CONSTRAINT guest_access_accounts_pkey PRIMARY KEY (id);


--
-- Name: guest_access_accounts guest_access_accounts_tenant_id_site_id_id_key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.guest_access_accounts
    ADD CONSTRAINT guest_access_accounts_tenant_id_site_id_id_key UNIQUE (tenant_id, site_id, id);


--
-- Name: guest_device_actions guest_device_actions_pkey; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.guest_device_actions
    ADD CONSTRAINT guest_device_actions_pkey PRIMARY KEY (id);


--
-- Name: guest_network_pms_map guest_network_pms_map_pkey; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.guest_network_pms_map
    ADD CONSTRAINT guest_network_pms_map_pkey PRIMARY KEY (guest_network_id, pms_interface_id);


--
-- Name: guest_principal_identities guest_principal_identities_pkey; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.guest_principal_identities
    ADD CONSTRAINT guest_principal_identities_pkey PRIMARY KEY (id);


--
-- Name: guest_principal_identities guest_principal_identities_tenant_id_factor_type_factor_iss_key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.guest_principal_identities
    ADD CONSTRAINT guest_principal_identities_tenant_id_factor_type_factor_iss_key UNIQUE (tenant_id, factor_type, factor_issuer, factor_value_norm);


--
-- Name: guest_principals guest_principals_pkey; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.guest_principals
    ADD CONSTRAINT guest_principals_pkey PRIMARY KEY (id);


--
-- Name: guest_principals guest_principals_tenant_id_id_key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.guest_principals
    ADD CONSTRAINT guest_principals_tenant_id_id_key UNIQUE (tenant_id, id);


--
-- Name: internet_package_revisions internet_package_revisions_package_id_revision_no_key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.internet_package_revisions
    ADD CONSTRAINT internet_package_revisions_package_id_revision_no_key UNIQUE (package_id, revision_no);


--
-- Name: internet_package_revisions internet_package_revisions_pkey; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.internet_package_revisions
    ADD CONSTRAINT internet_package_revisions_pkey PRIMARY KEY (id);


--
-- Name: internet_package_revisions internet_package_revisions_tenant_id_site_id_id_key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.internet_package_revisions
    ADD CONSTRAINT internet_package_revisions_tenant_id_site_id_id_key UNIQUE (tenant_id, site_id, id);


--
-- Name: internet_package_revisions internet_package_revisions_tenant_id_site_id_package_id_id_key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.internet_package_revisions
    ADD CONSTRAINT internet_package_revisions_tenant_id_site_id_package_id_id_key UNIQUE (tenant_id, site_id, package_id, id);


--
-- Name: internet_packages internet_packages_pkey; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.internet_packages
    ADD CONSTRAINT internet_packages_pkey PRIMARY KEY (id);


--
-- Name: internet_packages internet_packages_tenant_id_site_id_code_key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.internet_packages
    ADD CONSTRAINT internet_packages_tenant_id_site_id_code_key UNIQUE (tenant_id, site_id, code);


--
-- Name: internet_packages internet_packages_tenant_id_site_id_id_key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.internet_packages
    ADD CONSTRAINT internet_packages_tenant_id_site_id_id_key UNIQUE (tenant_id, site_id, id);


--
-- Name: offer_quotes offer_quotes_id_auth_context_id_package_revision_id_pms_int_key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.offer_quotes
    ADD CONSTRAINT offer_quotes_id_auth_context_id_package_revision_id_pms_int_key UNIQUE (id, auth_context_id, package_revision_id, pms_interface_id, settlement_mapping_id);


--
-- Name: offer_quotes offer_quotes_pkey; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.offer_quotes
    ADD CONSTRAINT offer_quotes_pkey PRIMARY KEY (id);


--
-- Name: offer_quotes offer_quotes_tenant_id_site_id_id_key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.offer_quotes
    ADD CONSTRAINT offer_quotes_tenant_id_site_id_id_key UNIQUE (tenant_id, site_id, id);


--
-- Name: online_time_skipped_intervals online_time_skipped_intervals_pkey; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.online_time_skipped_intervals
    ADD CONSTRAINT online_time_skipped_intervals_pkey PRIMARY KEY (id);


--
-- Name: package_eligibility_rules package_eligibility_rules_pkey; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.package_eligibility_rules
    ADD CONSTRAINT package_eligibility_rules_pkey PRIMARY KEY (id);


--
-- Name: package_grant_tiers package_grant_tiers_package_revision_id_tier_order_key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.package_grant_tiers
    ADD CONSTRAINT package_grant_tiers_package_revision_id_tier_order_key UNIQUE (package_revision_id, tier_order);


--
-- Name: package_grant_tiers package_grant_tiers_pkey; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.package_grant_tiers
    ADD CONSTRAINT package_grant_tiers_pkey PRIMARY KEY (id);


--
-- Name: package_settlement_mappings package_settlement_mappings_package_revision_id_pms_interfa_key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.package_settlement_mappings
    ADD CONSTRAINT package_settlement_mappings_package_revision_id_pms_interfa_key UNIQUE (package_revision_id, pms_interface_id, mapping_revision);


--
-- Name: package_settlement_mappings package_settlement_mappings_pkey; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.package_settlement_mappings
    ADD CONSTRAINT package_settlement_mappings_pkey PRIMARY KEY (id);


--
-- Name: package_settlement_mappings package_settlement_mappings_tenant_id_site_id_package_revis_key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.package_settlement_mappings
    ADD CONSTRAINT package_settlement_mappings_tenant_id_site_id_package_revis_key UNIQUE (tenant_id, site_id, package_revision_id, pms_interface_id, id);


--
-- Name: payment_provider_accounts payment_provider_accounts_pkey; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.payment_provider_accounts
    ADD CONSTRAINT payment_provider_accounts_pkey PRIMARY KEY (id);


--
-- Name: payment_provider_accounts payment_provider_accounts_tenant_id_site_id_id_key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.payment_provider_accounts
    ADD CONSTRAINT payment_provider_accounts_tenant_id_site_id_id_key UNIQUE (tenant_id, site_id, id);


--
-- Name: payment_provider_accounts payment_provider_accounts_tenant_id_site_id_provider_mercha_key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.payment_provider_accounts
    ADD CONSTRAINT payment_provider_accounts_tenant_id_site_id_provider_mercha_key UNIQUE (tenant_id, site_id, provider, merchant_account_ref);


--
-- Name: payment_transaction_events payment_transaction_events_payment_transaction_id_provider__key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.payment_transaction_events
    ADD CONSTRAINT payment_transaction_events_payment_transaction_id_provider__key UNIQUE (payment_transaction_id, provider_event_id);


--
-- Name: payment_transaction_events payment_transaction_events_pkey; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.payment_transaction_events
    ADD CONSTRAINT payment_transaction_events_pkey PRIMARY KEY (id);


--
-- Name: payment_transactions payment_transactions_idempotency_key_key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.payment_transactions
    ADD CONSTRAINT payment_transactions_idempotency_key_key UNIQUE (idempotency_key);


--
-- Name: payment_transactions payment_transactions_pkey; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.payment_transactions
    ADD CONSTRAINT payment_transactions_pkey PRIMARY KEY (id);


--
-- Name: payment_transactions payment_transactions_tenant_id_provider_merchant_account_id_key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.payment_transactions
    ADD CONSTRAINT payment_transactions_tenant_id_provider_merchant_account_id_key UNIQUE (tenant_id, provider, merchant_account_id, provider_ref);


--
-- Name: payment_transactions payment_transactions_tenant_id_site_id_settlement_id_id_key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.payment_transactions
    ADD CONSTRAINT payment_transactions_tenant_id_site_id_settlement_id_id_key UNIQUE (tenant_id, site_id, settlement_id, id);


--
-- Name: payment_transactions payment_transactions_tsi_key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.payment_transactions
    ADD CONSTRAINT payment_transactions_tsi_key UNIQUE (tenant_id, site_id, id);


--
-- Name: pms_interface_pnumber_seq pms_interface_pnumber_seq_pkey; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.pms_interface_pnumber_seq
    ADD CONSTRAINT pms_interface_pnumber_seq_pkey PRIMARY KEY (pms_interface_id);


--
-- Name: pms_interface_revisions pms_interface_revisions_pkey; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.pms_interface_revisions
    ADD CONSTRAINT pms_interface_revisions_pkey PRIMARY KEY (id);


--
-- Name: pms_interface_revisions pms_interface_revisions_pms_interface_id_revision_no_key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.pms_interface_revisions
    ADD CONSTRAINT pms_interface_revisions_pms_interface_id_revision_no_key UNIQUE (pms_interface_id, revision_no);


--
-- Name: pms_interface_revisions pms_interface_revisions_tenant_id_site_id_pms_interface_id__key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.pms_interface_revisions
    ADD CONSTRAINT pms_interface_revisions_tenant_id_site_id_pms_interface_id__key UNIQUE (tenant_id, site_id, pms_interface_id, id);


--
-- Name: pms_interface_runtime pms_interface_runtime_pkey; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.pms_interface_runtime
    ADD CONSTRAINT pms_interface_runtime_pkey PRIMARY KEY (tenant_id, site_id, pms_interface_id);


--
-- Name: pms_interface_secret_generations pms_interface_secret_generati_pms_interface_id_generation_n_key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.pms_interface_secret_generations
    ADD CONSTRAINT pms_interface_secret_generati_pms_interface_id_generation_n_key UNIQUE (pms_interface_id, generation_no);


--
-- Name: pms_interface_secret_generations pms_interface_secret_generati_tenant_id_site_id_pms_interfa_key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.pms_interface_secret_generations
    ADD CONSTRAINT pms_interface_secret_generati_tenant_id_site_id_pms_interfa_key UNIQUE (tenant_id, site_id, pms_interface_id, id);


--
-- Name: pms_interface_secret_generations pms_interface_secret_generations_pkey; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.pms_interface_secret_generations
    ADD CONSTRAINT pms_interface_secret_generations_pkey PRIMARY KEY (id);


--
-- Name: pms_interfaces pms_interfaces_pkey; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.pms_interfaces
    ADD CONSTRAINT pms_interfaces_pkey PRIMARY KEY (id);


--
-- Name: pms_interfaces pms_interfaces_tenant_id_site_id_id_key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.pms_interfaces
    ADD CONSTRAINT pms_interfaces_tenant_id_site_id_id_key UNIQUE (tenant_id, site_id, id);


--
-- Name: pms_postings pms_postings_id_pms_interface_id_key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.pms_postings
    ADD CONSTRAINT pms_postings_id_pms_interface_id_key UNIQUE (id, pms_interface_id);


--
-- Name: pms_postings pms_postings_idempotency_key_key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.pms_postings
    ADD CONSTRAINT pms_postings_idempotency_key_key UNIQUE (idempotency_key);


--
-- Name: pms_postings pms_postings_pkey; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.pms_postings
    ADD CONSTRAINT pms_postings_pkey PRIMARY KEY (id);


--
-- Name: pms_postings pms_postings_tenant_id_site_id_id_key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.pms_postings
    ADD CONSTRAINT pms_postings_tenant_id_site_id_id_key UNIQUE (tenant_id, site_id, id);


--
-- Name: pms_postings pms_postings_tenant_id_site_id_pms_interface_id_id_key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.pms_postings
    ADD CONSTRAINT pms_postings_tenant_id_site_id_pms_interface_id_id_key UNIQUE (tenant_id, site_id, pms_interface_id, id);


--
-- Name: pms_source_conflicts pms_source_conflicts_pkey; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.pms_source_conflicts
    ADD CONSTRAINT pms_source_conflicts_pkey PRIMARY KEY (id);


--
-- Name: pms_source_conflicts pms_source_conflicts_tenant_id_site_id_interface_a_interfac_key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.pms_source_conflicts
    ADD CONSTRAINT pms_source_conflicts_tenant_id_site_id_interface_a_interfac_key UNIQUE (tenant_id, site_id, interface_a, interface_b);


--
-- Name: post_stay_profiles post_stay_profiles_origin_stay_id_origin_lifecycle_version_key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.post_stay_profiles
    ADD CONSTRAINT post_stay_profiles_origin_stay_id_origin_lifecycle_version_key UNIQUE (origin_stay_id, origin_lifecycle_version);


--
-- Name: post_stay_profiles post_stay_profiles_pkey; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.post_stay_profiles
    ADD CONSTRAINT post_stay_profiles_pkey PRIMARY KEY (id);


--
-- Name: post_stay_profiles post_stay_profiles_tenant_id_site_id_id_key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.post_stay_profiles
    ADD CONSTRAINT post_stay_profiles_tenant_id_site_id_id_key UNIQUE (tenant_id, site_id, id);


--
-- Name: posting_attempt_events posting_attempt_events_pkey; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.posting_attempt_events
    ADD CONSTRAINT posting_attempt_events_pkey PRIMARY KEY (id);


--
-- Name: posting_attempts posting_attempts_internal_posting_id_attempt_no_key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.posting_attempts
    ADD CONSTRAINT posting_attempts_internal_posting_id_attempt_no_key UNIQUE (internal_posting_id, attempt_no);


--
-- Name: posting_attempts posting_attempts_pkey; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.posting_attempts
    ADD CONSTRAINT posting_attempts_pkey PRIMARY KEY (id);


--
-- Name: posting_attempts posting_attempts_tenant_id_site_id_id_key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.posting_attempts
    ADD CONSTRAINT posting_attempts_tenant_id_site_id_id_key UNIQUE (tenant_id, site_id, id);


--
-- Name: posting_attempts posting_attempts_tenant_id_site_id_pms_interface_id_p_numbe_key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.posting_attempts
    ADD CONSTRAINT posting_attempts_tenant_id_site_id_pms_interface_id_p_numbe_key UNIQUE (tenant_id, site_id, pms_interface_id, p_number);


--
-- Name: posting_outbox posting_outbox_pkey; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.posting_outbox
    ADD CONSTRAINT posting_outbox_pkey PRIMARY KEY (id);


--
-- Name: posting_review_actions posting_review_actions_pkey; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.posting_review_actions
    ADD CONSTRAINT posting_review_actions_pkey PRIMARY KEY (id);


--
-- Name: posting_review_state posting_review_state_pkey; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.posting_review_state
    ADD CONSTRAINT posting_review_state_pkey PRIMARY KEY (posting_id);


--
-- Name: purchases purchases_id_pms_interface_id_key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.purchases
    ADD CONSTRAINT purchases_id_pms_interface_id_key UNIQUE (id, pms_interface_id);


--
-- Name: purchases purchases_offer_quote_id_key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.purchases
    ADD CONSTRAINT purchases_offer_quote_id_key UNIQUE (offer_quote_id);


--
-- Name: purchases purchases_pkey; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.purchases
    ADD CONSTRAINT purchases_pkey PRIMARY KEY (id);


--
-- Name: purchases purchases_tenant_id_site_id_id_key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.purchases
    ADD CONSTRAINT purchases_tenant_id_site_id_id_key UNIQUE (tenant_id, site_id, id);


--
-- Name: service_plan_revisions service_plan_revisions_pkey; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.service_plan_revisions
    ADD CONSTRAINT service_plan_revisions_pkey PRIMARY KEY (id);


--
-- Name: service_plan_revisions service_plan_revisions_service_plan_id_revision_no_key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.service_plan_revisions
    ADD CONSTRAINT service_plan_revisions_service_plan_id_revision_no_key UNIQUE (service_plan_id, revision_no);


--
-- Name: service_plan_revisions service_plan_revisions_tenant_id_site_id_id_key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.service_plan_revisions
    ADD CONSTRAINT service_plan_revisions_tenant_id_site_id_id_key UNIQUE (tenant_id, site_id, id);


--
-- Name: service_plan_revisions service_plan_revisions_tenant_id_site_id_service_plan_id_id_key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.service_plan_revisions
    ADD CONSTRAINT service_plan_revisions_tenant_id_site_id_service_plan_id_id_key UNIQUE (tenant_id, site_id, service_plan_id, id);


--
-- Name: service_plans service_plans_pkey; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.service_plans
    ADD CONSTRAINT service_plans_pkey PRIMARY KEY (id);


--
-- Name: service_plans service_plans_tenant_id_site_id_code_key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.service_plans
    ADD CONSTRAINT service_plans_tenant_id_site_id_code_key UNIQUE (tenant_id, site_id, code);


--
-- Name: service_plans service_plans_tenant_id_site_id_id_key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.service_plans
    ADD CONSTRAINT service_plans_tenant_id_site_id_id_key UNIQUE (tenant_id, site_id, id);


--
-- Name: session_counter_watermarks session_counter_watermarks_pkey; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.session_counter_watermarks
    ADD CONSTRAINT session_counter_watermarks_pkey PRIMARY KEY (session_id);


--
-- Name: session_entitlement_bindings session_entitlement_bindings_pkey; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.session_entitlement_bindings
    ADD CONSTRAINT session_entitlement_bindings_pkey PRIMARY KEY (id);


--
-- Name: session_entitlement_bindings session_entitlement_bindings_session_id_seq_key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.session_entitlement_bindings
    ADD CONSTRAINT session_entitlement_bindings_session_id_seq_key UNIQUE (session_id, seq);


--
-- Name: session_online_watermarks session_online_watermarks_pkey; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.session_online_watermarks
    ADD CONSTRAINT session_online_watermarks_pkey PRIMARY KEY (session_id);


--
-- Name: sessions sessions_pkey; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.sessions
    ADD CONSTRAINT sessions_pkey PRIMARY KEY (id);


--
-- Name: sessions sessions_tenant_id_id_key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.sessions
    ADD CONSTRAINT sessions_tenant_id_id_key UNIQUE (tenant_id, id);


--
-- Name: sessions sessions_tenant_id_site_id_id_key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.sessions
    ADD CONSTRAINT sessions_tenant_id_site_id_id_key UNIQUE (tenant_id, site_id, id);


--
-- Name: settlements settlements_id_purchase_id_key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.settlements
    ADD CONSTRAINT settlements_id_purchase_id_key UNIQUE (id, purchase_id);


--
-- Name: settlements settlements_pkey; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.settlements
    ADD CONSTRAINT settlements_pkey PRIMARY KEY (id);


--
-- Name: settlements settlements_purchase_id_key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.settlements
    ADD CONSTRAINT settlements_purchase_id_key UNIQUE (purchase_id);


--
-- Name: settlements settlements_tenant_id_site_id_id_key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.settlements
    ADD CONSTRAINT settlements_tenant_id_site_id_id_key UNIQUE (tenant_id, site_id, id);


--
-- Name: site_checkout_grace_config site_checkout_grace_config_pkey; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.site_checkout_grace_config
    ADD CONSTRAINT site_checkout_grace_config_pkey PRIMARY KEY (tenant_id, site_id);


--
-- Name: stay_events stay_events_pkey; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.stay_events
    ADD CONSTRAINT stay_events_pkey PRIMARY KEY (id);


--
-- Name: stay_events stay_events_scoped_identity; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.stay_events
    ADD CONSTRAINT stay_events_scoped_identity UNIQUE (tenant_id, site_id, pms_interface_id, id);


--
-- Name: stay_folios stay_folios_pkey; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.stay_folios
    ADD CONSTRAINT stay_folios_pkey PRIMARY KEY (stay_id, folio_id);


--
-- Name: stay_folios stay_folios_tenant_id_site_id_pms_interface_id_stay_id_foli_key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.stay_folios
    ADD CONSTRAINT stay_folios_tenant_id_site_id_pms_interface_id_stay_id_foli_key UNIQUE (tenant_id, site_id, pms_interface_id, stay_id, folio_id);


--
-- Name: stay_guests stay_guests_pkey; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.stay_guests
    ADD CONSTRAINT stay_guests_pkey PRIMARY KEY (id);


--
-- Name: stay_links stay_links_from_stay_to_stay_reason_key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.stay_links
    ADD CONSTRAINT stay_links_from_stay_to_stay_reason_key UNIQUE (from_stay, to_stay, reason);


--
-- Name: stay_links stay_links_pkey; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.stay_links
    ADD CONSTRAINT stay_links_pkey PRIMARY KEY (id);


--
-- Name: stays stays_checkedout_needs_boundary; Type: CHECK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE iam_v2.stays
    ADD CONSTRAINT stays_checkedout_needs_boundary CHECK (((status <> 'CHECKED_OUT'::text) OR (effective_checkout_at IS NOT NULL))) NOT VALID;


--
-- Name: stays stays_pkey; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.stays
    ADD CONSTRAINT stays_pkey PRIMARY KEY (id);


--
-- Name: stays stays_tenant_id_site_id_id_key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.stays
    ADD CONSTRAINT stays_tenant_id_site_id_id_key UNIQUE (tenant_id, site_id, id);


--
-- Name: stays stays_tenant_id_site_id_pms_interface_id_external_reservati_key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.stays
    ADD CONSTRAINT stays_tenant_id_site_id_pms_interface_id_external_reservati_key UNIQUE (tenant_id, site_id, pms_interface_id, external_reservation_id, external_stay_identity);


--
-- Name: stays stays_tenant_id_site_id_pms_interface_id_id_key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.stays
    ADD CONSTRAINT stays_tenant_id_site_id_pms_interface_id_id_key UNIQUE (tenant_id, site_id, pms_interface_id, id);


--
-- Name: voucher_batches voucher_batches_pkey; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.voucher_batches
    ADD CONSTRAINT voucher_batches_pkey PRIMARY KEY (id);


--
-- Name: voucher_batches voucher_batches_tenant_id_site_id_id_key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.voucher_batches
    ADD CONSTRAINT voucher_batches_tenant_id_site_id_id_key UNIQUE (tenant_id, site_id, id);


--
-- Name: voucher_code_key_generations voucher_code_key_generations_pkey; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.voucher_code_key_generations
    ADD CONSTRAINT voucher_code_key_generations_pkey PRIMARY KEY (id);


--
-- Name: voucher_code_key_generations voucher_code_key_generations_tenant_id_generation_no_key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.voucher_code_key_generations
    ADD CONSTRAINT voucher_code_key_generations_tenant_id_generation_no_key UNIQUE (tenant_id, generation_no);


--
-- Name: voucher_code_key_generations voucher_code_key_generations_tenant_id_site_id_id_key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.voucher_code_key_generations
    ADD CONSTRAINT voucher_code_key_generations_tenant_id_site_id_id_key UNIQUE (tenant_id, site_id, id);


--
-- Name: vouchers vouchers_code_hmac_key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.vouchers
    ADD CONSTRAINT vouchers_code_hmac_key UNIQUE (code_hmac);


--
-- Name: vouchers vouchers_pkey; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.vouchers
    ADD CONSTRAINT vouchers_pkey PRIMARY KEY (id);


--
-- Name: vouchers vouchers_tenant_id_site_id_id_key; Type: CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.vouchers
    ADD CONSTRAINT vouchers_tenant_id_site_id_id_key UNIQUE (tenant_id, site_id, id);


--
-- Name: appliance_boot_convergence appliance_boot_convergence_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.appliance_boot_convergence
    ADD CONSTRAINT appliance_boot_convergence_pkey PRIMARY KEY (id);


--
-- Name: appliance_recovery_events appliance_recovery_events_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.appliance_recovery_events
    ADD CONSTRAINT appliance_recovery_events_pkey PRIMARY KEY (id);


--
-- Name: appliance_service_health appliance_service_health_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.appliance_service_health
    ADD CONSTRAINT appliance_service_health_pkey PRIMARY KEY (service);


--
-- Name: appliances appliances_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.appliances
    ADD CONSTRAINT appliances_pkey PRIMARY KEY (id);


--
-- Name: appliances appliances_serial_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.appliances
    ADD CONSTRAINT appliances_serial_key UNIQUE (serial);


--
-- Name: auth_otps auth_otps_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.auth_otps
    ADD CONSTRAINT auth_otps_pkey PRIMARY KEY (id);


--
-- Name: auth_throttle_buckets auth_throttle_buckets_pk; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.auth_throttle_buckets
    ADD CONSTRAINT auth_throttle_buckets_pk PRIMARY KEY (scope_kind, scope_key, method, window_start);


--
-- Name: backup_records backup_records_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.backup_records
    ADD CONSTRAINT backup_records_pkey PRIMARY KEY (id);


--
-- Name: dhcp_pools dhcp_pools_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.dhcp_pools
    ADD CONSTRAINT dhcp_pools_pkey PRIMARY KEY (id);


--
-- Name: dhcp_reservations dhcp_reservations_guest_network_id_mac_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.dhcp_reservations
    ADD CONSTRAINT dhcp_reservations_guest_network_id_mac_key UNIQUE (guest_network_id, mac);


--
-- Name: dhcp_reservations dhcp_reservations_guest_network_id_reserved_ip_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.dhcp_reservations
    ADD CONSTRAINT dhcp_reservations_guest_network_id_reserved_ip_key UNIQUE (guest_network_id, reserved_ip);


--
-- Name: dhcp_reservations dhcp_reservations_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.dhcp_reservations
    ADD CONSTRAINT dhcp_reservations_pkey PRIMARY KEY (id);


--
-- Name: edge_executed_commands edge_executed_commands_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.edge_executed_commands
    ADD CONSTRAINT edge_executed_commands_pkey PRIMARY KEY (command_id);


--
-- Name: edge_installed_updates edge_installed_updates_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.edge_installed_updates
    ADD CONSTRAINT edge_installed_updates_pkey PRIMARY KEY (update_id);


--
-- Name: edge_offline_packages edge_offline_packages_nonce_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.edge_offline_packages
    ADD CONSTRAINT edge_offline_packages_nonce_key UNIQUE (nonce);


--
-- Name: edge_offline_packages edge_offline_packages_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.edge_offline_packages
    ADD CONSTRAINT edge_offline_packages_pkey PRIMARY KEY (package_id);


--
-- Name: guest_networks guest_networks_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.guest_networks
    ADD CONSTRAINT guest_networks_pkey PRIMARY KEY (id);


--
-- Name: network_apply_events network_apply_events_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.network_apply_events
    ADD CONSTRAINT network_apply_events_pkey PRIMARY KEY (id);


--
-- Name: network_config_revisions network_config_revisions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.network_config_revisions
    ADD CONSTRAINT network_config_revisions_pkey PRIMARY KEY (id);


--
-- Name: network_config_revisions network_config_revisions_seq_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.network_config_revisions
    ADD CONSTRAINT network_config_revisions_seq_key UNIQUE (seq);


--
-- Name: network_health_checks network_health_checks_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.network_health_checks
    ADD CONSTRAINT network_health_checks_pkey PRIMARY KEY (id);


--
-- Name: network_interfaces network_interfaces_name_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.network_interfaces
    ADD CONSTRAINT network_interfaces_name_key UNIQUE (name);


--
-- Name: network_interfaces network_interfaces_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.network_interfaces
    ADD CONSTRAINT network_interfaces_pkey PRIMARY KEY (id);


--
-- Name: notification_providers notification_providers_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.notification_providers
    ADD CONSTRAINT notification_providers_pkey PRIMARY KEY (id);


--
-- Name: operator_roles operator_roles_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.operator_roles
    ADD CONSTRAINT operator_roles_pkey PRIMARY KEY (id);


--
-- Name: operators operators_email_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.operators
    ADD CONSTRAINT operators_email_key UNIQUE (email);


--
-- Name: operators operators_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.operators
    ADD CONSTRAINT operators_pkey PRIMARY KEY (id);


--
-- Name: otp_hmac_key_generations otp_hmac_key_generations_pk; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.otp_hmac_key_generations
    ADD CONSTRAINT otp_hmac_key_generations_pk PRIMARY KEY (generation);


--
-- Name: pms_attempts pms_attempts_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pms_attempts
    ADD CONSTRAINT pms_attempts_pkey PRIMARY KEY (id);


--
-- Name: pms_providers pms_providers_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pms_providers
    ADD CONSTRAINT pms_providers_pkey PRIMARY KEY (id);


--
-- Name: schema_migrations schema_migrations_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.schema_migrations
    ADD CONSTRAINT schema_migrations_pkey PRIMARY KEY (version);


--
-- Name: sites sites_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sites
    ADD CONSTRAINT sites_pkey PRIMARY KEY (id);


--
-- Name: sites sites_tenant_id_code_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sites
    ADD CONSTRAINT sites_tenant_id_code_key UNIQUE (tenant_id, code);


--
-- Name: social_oauth_providers social_oauth_providers_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.social_oauth_providers
    ADD CONSTRAINT social_oauth_providers_pkey PRIMARY KEY (id);


--
-- Name: social_oauth_states social_oauth_states_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.social_oauth_states
    ADD CONSTRAINT social_oauth_states_pkey PRIMARY KEY (state);


--
-- Name: stripe_accounts stripe_accounts_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.stripe_accounts
    ADD CONSTRAINT stripe_accounts_pkey PRIMARY KEY (id);


--
-- Name: stripe_events stripe_events_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.stripe_events
    ADD CONSTRAINT stripe_events_pkey PRIMARY KEY (event_id);


--
-- Name: sync_checkpoints sync_checkpoints_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sync_checkpoints
    ADD CONSTRAINT sync_checkpoints_pkey PRIMARY KEY (name);


--
-- Name: sync_outbox sync_outbox_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sync_outbox
    ADD CONSTRAINT sync_outbox_pkey PRIMARY KEY (seq);


--
-- Name: system_network_audit system_network_audit_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.system_network_audit
    ADD CONSTRAINT system_network_audit_pkey PRIMARY KEY (id);


--
-- Name: tenant_effective_limits tenant_effective_limits_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tenant_effective_limits
    ADD CONSTRAINT tenant_effective_limits_pkey PRIMARY KEY (tenant_id, key);


--
-- Name: tenants tenants_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tenants
    ADD CONSTRAINT tenants_pkey PRIMARY KEY (id);


--
-- Name: tenants tenants_slug_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tenants
    ADD CONSTRAINT tenants_slug_key UNIQUE (slug);


--
-- Name: walled_garden_rules walled_garden_rules_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.walled_garden_rules
    ADD CONSTRAINT walled_garden_rules_pkey PRIMARY KEY (id);


--
-- Name: ac_one_live_per_resolution; Type: INDEX; Schema: iam_v2; Owner: -
--

CREATE UNIQUE INDEX ac_one_live_per_resolution ON iam_v2.auth_contexts USING btree (tenant_id, site_id, resolution_request_id) WHERE ((resolution_request_id IS NOT NULL) AND (consumed_at IS NULL));


--
-- Name: aco_by_context; Type: INDEX; Schema: iam_v2; Owner: -
--

CREATE INDEX aco_by_context ON iam_v2.auth_context_offers USING btree (auth_context_id);


--
-- Name: aps_changes_lookup; Type: INDEX; Schema: iam_v2; Owner: -
--

CREATE INDEX aps_changes_lookup ON iam_v2.appliance_product_setting_changes USING btree (tenant_id, site_id, appliance_id, changed_at DESC);


--
-- Name: auth_resolutions_req_idem; Type: INDEX; Schema: iam_v2; Owner: -
--

CREATE UNIQUE INDEX auth_resolutions_req_idem ON iam_v2.auth_resolutions USING btree (tenant_id, site_id, resolution_request_id) WHERE (resolution_request_id IS NOT NULL);


--
-- Name: eda_boundary_lookup; Type: INDEX; Schema: iam_v2; Owner: -
--

CREATE INDEX eda_boundary_lookup ON iam_v2.entitlement_device_authorizations USING btree (entitlement_id, device_id, authorized_at);


--
-- Name: ent_live_account; Type: INDEX; Schema: iam_v2; Owner: -
--

CREATE UNIQUE INDEX ent_live_account ON iam_v2.entitlements USING btree (guest_account_id) WHERE (status = ANY (ARRAY['PENDING'::text, 'ACTIVE'::text, 'SUSPENDED'::text]));


--
-- Name: ent_live_principal; Type: INDEX; Schema: iam_v2; Owner: -
--

CREATE UNIQUE INDEX ent_live_principal ON iam_v2.entitlements USING btree (guest_principal_id, site_id) WHERE (status = ANY (ARRAY['PENDING'::text, 'ACTIVE'::text, 'SUSPENDED'::text]));


--
-- Name: ent_live_stay; Type: INDEX; Schema: iam_v2; Owner: -
--

CREATE UNIQUE INDEX ent_live_stay ON iam_v2.entitlements USING btree (stay_id) WHERE (status = ANY (ARRAY['PENDING'::text, 'ACTIVE'::text, 'SUSPENDED'::text]));


--
-- Name: ent_live_voucher; Type: INDEX; Schema: iam_v2; Owner: -
--

CREATE UNIQUE INDEX ent_live_voucher ON iam_v2.entitlements USING btree (voucher_id) WHERE (status = ANY (ARRAY['PENDING'::text, 'ACTIVE'::text, 'SUSPENDED'::text]));


--
-- Name: est_boundary_lookup; Type: INDEX; Schema: iam_v2; Owner: -
--

CREATE INDEX est_boundary_lookup ON iam_v2.entitlement_state_transitions USING btree (entitlement_id, effective_at) WHERE (superseded_by IS NULL);


--
-- Name: fin_epoch_one_open_per_site; Type: INDEX; Schema: iam_v2; Owner: -
--

CREATE UNIQUE INDEX fin_epoch_one_open_per_site ON iam_v2.financial_epochs USING btree (tenant_id, site_id) WHERE (released_at IS NULL);


--
-- Name: fin_holds_open; Type: INDEX; Schema: iam_v2; Owner: -
--

CREATE INDEX fin_holds_open ON iam_v2.financial_recovery_holds USING btree (tenant_id, site_id, epoch) WHERE (resolution IS NULL);


--
-- Name: fin_restore_events_site; Type: INDEX; Schema: iam_v2; Owner: -
--

CREATE INDEX fin_restore_events_site ON iam_v2.financial_restore_events USING btree (tenant_id, site_id, restored_at DESC);


--
-- Name: folio_open_identity; Type: INDEX; Schema: iam_v2; Owner: -
--

CREATE UNIQUE INDEX folio_open_identity ON iam_v2.folios USING btree (tenant_id, site_id, pms_interface_id, external_folio_id) WHERE (status = 'OPEN'::text);


--
-- Name: gaa_username; Type: INDEX; Schema: iam_v2; Owner: -
--

CREATE UNIQUE INDEX gaa_username ON iam_v2.guest_access_accounts USING btree (tenant_id, lower(username));


--
-- Name: gda_lookup; Type: INDEX; Schema: iam_v2; Owner: -
--

CREATE INDEX gda_lookup ON iam_v2.guest_device_actions USING btree (tenant_id, site_id, entitlement_id, acted_at DESC);


--
-- Name: gda_release_rate; Type: INDEX; Schema: iam_v2; Owner: -
--

CREATE INDEX gda_release_rate ON iam_v2.guest_device_actions USING btree (entitlement_id, action, acted_at DESC);


--
-- Name: gnpm_one_default; Type: INDEX; Schema: iam_v2; Owner: -
--

CREATE UNIQUE INDEX gnpm_one_default ON iam_v2.guest_network_pms_map USING btree (guest_network_id) WHERE is_default;


--
-- Name: internet_packages_active_lookup; Type: INDEX; Schema: iam_v2; Owner: -
--

CREATE INDEX internet_packages_active_lookup ON iam_v2.internet_packages USING btree (tenant_id, site_id, active) WHERE active;


--
-- Name: offer_quotes_auth_context; Type: INDEX; Schema: iam_v2; Owner: -
--

CREATE INDEX offer_quotes_auth_context ON iam_v2.offer_quotes USING btree (tenant_id, site_id, auth_context_id);


--
-- Name: offer_quotes_open_expiry; Type: INDEX; Schema: iam_v2; Owner: -
--

CREATE INDEX offer_quotes_open_expiry ON iam_v2.offer_quotes USING btree (expires_at) WHERE (consumed_at IS NULL);


--
-- Name: one_conversion_per_episode; Type: INDEX; Schema: iam_v2; Owner: -
--

CREATE UNIQUE INDEX one_conversion_per_episode ON iam_v2.purchases USING btree (stay_id, checkout_episode) WHERE (trigger = ANY (ARRAY['CHECKOUT_GRACE'::text, 'EMERGENCY_GRACE'::text, 'POST_STAY_CONVERSION'::text]));


--
-- Name: one_primary_guest_per_stay; Type: INDEX; Schema: iam_v2; Owner: -
--

CREATE UNIQUE INDEX one_primary_guest_per_stay ON iam_v2.stay_guests USING btree (stay_id) WHERE is_primary;


--
-- Name: online_time_skipped_intervals_ent_idx; Type: INDEX; Schema: iam_v2; Owner: -
--

CREATE INDEX online_time_skipped_intervals_ent_idx ON iam_v2.online_time_skipped_intervals USING btree (entitlement_id, skipped_from);


--
-- Name: outbox_backlog_age; Type: INDEX; Schema: iam_v2; Owner: -
--

CREATE INDEX outbox_backlog_age ON iam_v2.posting_outbox USING btree (tenant_id, site_id, enqueued_at) WHERE (state = ANY (ARRAY['QUEUED'::text, 'IN_FLIGHT'::text, 'HELD_RECOVERY'::text]));


--
-- Name: outbox_one_active; Type: INDEX; Schema: iam_v2; Owner: -
--

CREATE UNIQUE INDEX outbox_one_active ON iam_v2.posting_outbox USING btree (posting_id) WHERE (state = ANY (ARRAY['QUEUED'::text, 'IN_FLIGHT'::text, 'HELD_RECOVERY'::text]));


--
-- Name: outbox_one_inflight_per_interface; Type: INDEX; Schema: iam_v2; Owner: -
--

CREATE UNIQUE INDEX outbox_one_inflight_per_interface ON iam_v2.posting_outbox USING btree (pms_interface_id) WHERE (state = 'IN_FLIGHT'::text);


--
-- Name: INDEX outbox_one_inflight_per_interface; Type: COMMENT; Schema: iam_v2; Owner: -
--

COMMENT ON INDEX iam_v2.outbox_one_inflight_per_interface IS 'Contract 10: a PMS Interface is ONE serialized financial lane. At most one posting may be IN_FLIGHT on it at a time. Distinct interfaces are unaffected and remain fully independent.';


--
-- Name: package_eligibility_rules_by_revision; Type: INDEX; Schema: iam_v2; Owner: -
--

CREATE INDEX package_eligibility_rules_by_revision ON iam_v2.package_eligibility_rules USING btree (package_revision_id, rule_type);


--
-- Name: package_revision_visibility; Type: INDEX; Schema: iam_v2; Owner: -
--

CREATE INDEX package_revision_visibility ON iam_v2.internet_package_revisions USING btree (tenant_id, site_id, package_id, visible_from, visible_until);


--
-- Name: post_stay_profiles_origin; Type: INDEX; Schema: iam_v2; Owner: -
--

CREATE INDEX post_stay_profiles_origin ON iam_v2.post_stay_profiles USING btree (tenant_id, site_id, origin_stay_id);


--
-- Name: posting_attempts_by_posting; Type: INDEX; Schema: iam_v2; Owner: -
--

CREATE INDEX posting_attempts_by_posting ON iam_v2.posting_attempts USING btree (internal_posting_id, attempt_no DESC);


--
-- Name: ppa_merchant_ref_globally_unique; Type: INDEX; Schema: iam_v2; Owner: -
--

CREATE UNIQUE INDEX ppa_merchant_ref_globally_unique ON iam_v2.payment_provider_accounts USING btree (provider, merchant_account_ref) WHERE ((provenance = 'CONFIGURED'::text) AND (merchant_account_ref IS NOT NULL));


--
-- Name: INDEX ppa_merchant_ref_globally_unique; Type: COMMENT; Schema: iam_v2; Owner: -
--

COMMENT ON INDEX iam_v2.ppa_merchant_ref_globally_unique IS 'C27: one external merchant account belongs to one customer. Global rather than tenant-scoped on purpose -- the hazard is precisely two TENANTS naming the same account.';


--
-- Name: ppa_one_default_per_site; Type: INDEX; Schema: iam_v2; Owner: -
--

CREATE UNIQUE INDEX ppa_one_default_per_site ON iam_v2.payment_provider_accounts USING btree (tenant_id, site_id) WHERE is_default;


--
-- Name: ptx_event_provider_identity; Type: INDEX; Schema: iam_v2; Owner: -
--

CREATE UNIQUE INDEX ptx_event_provider_identity ON iam_v2.payment_transaction_events USING btree (tenant_id, provider, merchant_account_id, provider_event_id);


--
-- Name: INDEX ptx_event_provider_identity; Type: COMMENT; Schema: iam_v2; Owner: -
--

COMMENT ON INDEX iam_v2.ptx_event_provider_identity IS 'A provider event is unique within provider + merchant account. 0014 keyed dedupe on the INTERNAL transaction id, so the same event could be applied to two different internal rows by naming a different one. This is the correlation the provider actually owns.';


--
-- Name: ptx_events_by_txn; Type: INDEX; Schema: iam_v2; Owner: -
--

CREATE INDEX ptx_events_by_txn ON iam_v2.payment_transaction_events USING btree (payment_transaction_id, received_at);


--
-- Name: ptx_one_live_charge_per_settlement; Type: INDEX; Schema: iam_v2; Owner: -
--

CREATE UNIQUE INDEX ptx_one_live_charge_per_settlement ON iam_v2.payment_transactions USING btree (settlement_id) WHERE ((transaction_type = 'CHARGE'::text) AND (status = ANY (ARRAY['CREATED'::text, 'PENDING'::text, 'CAPTURED'::text, 'UNKNOWN'::text])));


--
-- Name: INDEX ptx_one_live_charge_per_settlement; Type: COMMENT; Schema: iam_v2; Owner: -
--

COMMENT ON INDEX iam_v2.ptx_one_live_charge_per_settlement IS 'Concurrency-proof replacement for 0014''s SELECT-then-decide duplicate-charge check. A count() inside a BEFORE INSERT trigger cannot see a concurrent uncommitted sibling; a unique index can.';


--
-- Name: ptx_provider_txn_ref_identity; Type: INDEX; Schema: iam_v2; Owner: -
--

CREATE UNIQUE INDEX ptx_provider_txn_ref_identity ON iam_v2.payment_transactions USING btree (tenant_id, provider, merchant_account_id, provider_txn_ref) WHERE (provider_txn_ref IS NOT NULL);


--
-- Name: purchase_once_per_stay; Type: INDEX; Schema: iam_v2; Owner: -
--

CREATE UNIQUE INDEX purchase_once_per_stay ON iam_v2.purchases USING btree (stay_id, package_revision_id) WHERE ((state = ANY (ARRAY['PENDING'::text, 'AWAITING_SETTLEMENT'::text, 'MANUAL_REVIEW'::text, 'GRANTED'::text])) AND (trigger = 'GUEST_SELECTION'::text));


--
-- Name: purchases_auth_context; Type: INDEX; Schema: iam_v2; Owner: -
--

CREATE INDEX purchases_auth_context ON iam_v2.purchases USING btree (tenant_id, site_id, auth_context_id);


--
-- Name: se_live_identity; Type: INDEX; Schema: iam_v2; Owner: -
--

CREATE UNIQUE INDEX se_live_identity ON iam_v2.stay_events USING btree (tenant_id, site_id, pms_interface_id, external_event_identity) WHERE (admission_kind = 'LIVE'::text);


--
-- Name: se_resync_identity; Type: INDEX; Schema: iam_v2; Owner: -
--

CREATE UNIQUE INDEX se_resync_identity ON iam_v2.stay_events USING btree (tenant_id, site_id, pms_interface_id, resync_generation, external_event_identity) WHERE (admission_kind = 'RESYNC'::text);


--
-- Name: seb_attribution; Type: INDEX; Schema: iam_v2; Owner: -
--

CREATE INDEX seb_attribution ON iam_v2.session_entitlement_bindings USING btree (entitlement_id, bound_from);


--
-- Name: seb_one_open; Type: INDEX; Schema: iam_v2; Owner: -
--

CREATE UNIQUE INDEX seb_one_open ON iam_v2.session_entitlement_bindings USING btree (session_id) WHERE (bound_until IS NULL);


--
-- Name: stay_folio_default; Type: INDEX; Schema: iam_v2; Owner: -
--

CREATE UNIQUE INDEX stay_folio_default ON iam_v2.stay_folios USING btree (stay_id) WHERE is_default_posting_target;


--
-- Name: stays_effective_checkout; Type: INDEX; Schema: iam_v2; Owner: -
--

CREATE INDEX stays_effective_checkout ON iam_v2.stays USING btree (tenant_id, site_id, pms_interface_id, effective_checkout_at) WHERE (effective_checkout_at IS NOT NULL);


--
-- Name: stays_room_lookup; Type: INDEX; Schema: iam_v2; Owner: -
--

CREATE INDEX stays_room_lookup ON iam_v2.stays USING btree (tenant_id, site_id, pms_interface_id, normalized_room_number) WHERE (status = 'IN_HOUSE'::text);


--
-- Name: accounting_records_session_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX accounting_records_session_idx ON public.accounting_records USING btree (session_id, ts DESC);


--
-- Name: accounting_records_tenant_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX accounting_records_tenant_idx ON public.accounting_records USING btree (tenant_id, ts DESC);


--
-- Name: accounting_records_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX accounting_records_ts_idx ON public.accounting_records USING btree (ts DESC);


--
-- Name: appliances_tenant_site_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX appliances_tenant_site_idx ON public.appliances USING btree (tenant_id, site_id);


--
-- Name: appliances_tsi_anchor; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX appliances_tsi_anchor ON public.appliances USING btree (id, tenant_id, site_id);


--
-- Name: INDEX appliances_tsi_anchor; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON INDEX public.appliances_tsi_anchor IS 'created by iam_v2 migration 0030_phase6_foundation';


--
-- Name: audit_log_tenant_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX audit_log_tenant_idx ON public.audit_log USING btree (tenant_id, ts DESC);


--
-- Name: audit_log_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX audit_log_ts_idx ON public.audit_log USING btree (ts DESC);


--
-- Name: auth_otps_dest_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX auth_otps_dest_idx ON public.auth_otps USING btree (tenant_id, channel, lower(destination), issued_at DESC);


--
-- Name: auth_otps_recent_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX auth_otps_recent_idx ON public.auth_otps USING btree (tenant_id, channel, lower(destination), issued_at) WHERE (consumed_at IS NULL);


--
-- Name: auth_throttle_buckets_block; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX auth_throttle_buckets_block ON public.auth_throttle_buckets USING btree (scope_kind, scope_key, method, blocked_until) WHERE (blocked_until IS NOT NULL);


--
-- Name: auth_throttle_buckets_expiry; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX auth_throttle_buckets_expiry ON public.auth_throttle_buckets USING btree (window_start);


--
-- Name: dhcp_pools_network_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX dhcp_pools_network_idx ON public.dhcp_pools USING btree (guest_network_id, sort_order);


--
-- Name: guest_networks_bridge_uniq; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX guest_networks_bridge_uniq ON public.guest_networks USING btree (bridge_name);


--
-- Name: guest_networks_enabled_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX guest_networks_enabled_idx ON public.guest_networks USING btree (enabled);


--
-- Name: guest_networks_tsi_anchor; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX guest_networks_tsi_anchor ON public.guest_networks USING btree (tenant_id, site_id, id);


--
-- Name: guest_networks_untagged_parent_uniq; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX guest_networks_untagged_parent_uniq ON public.guest_networks USING btree (parent_interface) WHERE (enabled AND (network_type = 'untagged'::text));


--
-- Name: guest_networks_vlan_parent_uniq; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX guest_networks_vlan_parent_uniq ON public.guest_networks USING btree (parent_interface, vlan_id) WHERE (enabled AND (vlan_id IS NOT NULL));


--
-- Name: nae_revision_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX nae_revision_idx ON public.network_apply_events USING btree (revision_id, at);


--
-- Name: ncr_single_inflight; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX ncr_single_inflight ON public.network_config_revisions USING btree ((true)) WHERE (state = ANY (ARRAY['applying'::text, 'pending_confirmation'::text]));


--
-- Name: ncr_state_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ncr_state_idx ON public.network_config_revisions USING btree (state, seq DESC);


--
-- Name: nhc_revision_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX nhc_revision_idx ON public.network_health_checks USING btree (revision_id, at);


--
-- Name: notification_providers_tenant_channel_enabled_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX notification_providers_tenant_channel_enabled_idx ON public.notification_providers USING btree (tenant_id, channel) WHERE (enabled = true);


--
-- Name: operator_roles_uniq; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX operator_roles_uniq ON public.operator_roles USING btree (operator_id, COALESCE(tenant_id, '00000000-0000-0000-0000-000000000000'::uuid), role);


--
-- Name: operators_oidc_sub_uniq; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX operators_oidc_sub_uniq ON public.operators USING btree (tenant_id, oidc_sub) WHERE (oidc_sub IS NOT NULL);


--
-- Name: otp_hmac_key_generations_one_active; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX otp_hmac_key_generations_one_active ON public.otp_hmac_key_generations USING btree (active) WHERE active;


--
-- Name: pms_attempts_ip_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX pms_attempts_ip_idx ON public.pms_attempts USING btree (tenant_id, ip, attempted_at DESC);


--
-- Name: pms_attempts_room_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX pms_attempts_room_idx ON public.pms_attempts USING btree (tenant_id, lower(room_number), attempted_at DESC);


--
-- Name: pms_providers_tenant_enabled_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX pms_providers_tenant_enabled_idx ON public.pms_providers USING btree (tenant_id) WHERE (enabled = true);


--
-- Name: pms_providers_tenant_name_global_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX pms_providers_tenant_name_global_idx ON public.pms_providers USING btree (tenant_id, name) WHERE (site_id IS NULL);


--
-- Name: pms_providers_tenant_site_enabled_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX pms_providers_tenant_site_enabled_idx ON public.pms_providers USING btree (tenant_id, site_id) WHERE (enabled = true);


--
-- Name: pms_providers_tenant_site_name_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX pms_providers_tenant_site_name_idx ON public.pms_providers USING btree (tenant_id, site_id, name) WHERE (site_id IS NOT NULL);


--
-- Name: recovery_events_created_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX recovery_events_created_idx ON public.appliance_recovery_events USING btree (created_at DESC);


--
-- Name: recovery_events_service_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX recovery_events_service_idx ON public.appliance_recovery_events USING btree (service, created_at DESC);


--
-- Name: social_oauth_providers_tenant_provider_enabled_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX social_oauth_providers_tenant_provider_enabled_idx ON public.social_oauth_providers USING btree (tenant_id, provider) WHERE (enabled = true);


--
-- Name: social_oauth_states_expiry_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX social_oauth_states_expiry_idx ON public.social_oauth_states USING btree (expires_at) WHERE (consumed_at IS NULL);


--
-- Name: stripe_accounts_tenant_enabled_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX stripe_accounts_tenant_enabled_idx ON public.stripe_accounts USING btree (tenant_id) WHERE (enabled = true);


--
-- Name: stripe_events_received_at_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX stripe_events_received_at_idx ON public.stripe_events USING btree (received_at);


--
-- Name: sync_outbox_pending_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX sync_outbox_pending_idx ON public.sync_outbox USING btree (next_attempt_at) WHERE ((sent_at IS NULL) AND (dead = false));


--
-- Name: system_network_audit_created_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX system_network_audit_created_idx ON public.system_network_audit USING btree (created_at DESC);


--
-- Name: system_network_audit_revision_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX system_network_audit_revision_idx ON public.system_network_audit USING btree (revision_id);


--
-- Name: accounting_records ao_accounting; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER ao_accounting BEFORE DELETE OR UPDATE ON iam_v2.accounting_records FOR EACH ROW EXECUTE FUNCTION iam_v2.trg_reject_update_delete();


--
-- Name: entitlement_adjustments ao_adjust; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER ao_adjust BEFORE DELETE OR UPDATE ON iam_v2.entitlement_adjustments FOR EACH ROW EXECUTE FUNCTION iam_v2.trg_reject_update_delete();


--
-- Name: posting_attempt_events ao_pa_events; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER ao_pa_events BEFORE DELETE OR UPDATE ON iam_v2.posting_attempt_events FOR EACH ROW EXECUTE FUNCTION iam_v2.trg_reject_update_delete();


--
-- Name: pms_postings ao_postings; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER ao_postings BEFORE DELETE OR UPDATE ON iam_v2.pms_postings FOR EACH ROW EXECUTE FUNCTION iam_v2.trg_reject_update_delete();


--
-- Name: payment_transaction_events ao_ptx_events; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER ao_ptx_events BEFORE DELETE OR UPDATE ON iam_v2.payment_transaction_events FOR EACH ROW EXECUTE FUNCTION iam_v2.trg_reject_update_delete_ptx_events();


--
-- Name: financial_recovery_holds ao_recovery_holds; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER ao_recovery_holds BEFORE DELETE OR UPDATE ON iam_v2.financial_recovery_holds FOR EACH ROW EXECUTE FUNCTION iam_v2.p4_recovery_hold_immutable();


--
-- Name: posting_review_actions ao_review; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER ao_review BEFORE DELETE OR UPDATE ON iam_v2.posting_review_actions FOR EACH ROW EXECUTE FUNCTION iam_v2.trg_reject_update_delete();


--
-- Name: pms_postings charge_gate; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER charge_gate BEFORE INSERT ON iam_v2.pms_postings FOR EACH ROW EXECUTE FUNCTION iam_v2.trg_posting_charge_gate();


--
-- Name: entitlements ent_guard; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER ent_guard BEFORE INSERT OR UPDATE ON iam_v2.entitlements FOR EACH ROW EXECUTE FUNCTION iam_v2.trg_entitlement_guard();


--
-- Name: internet_package_revisions imm_pkg_rev; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER imm_pkg_rev BEFORE DELETE OR UPDATE ON iam_v2.internet_package_revisions FOR EACH ROW EXECUTE FUNCTION iam_v2.trg_reject_update_delete();


--
-- Name: service_plan_revisions imm_plan_rev; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER imm_plan_rev BEFORE DELETE OR UPDATE ON iam_v2.service_plan_revisions FOR EACH ROW EXECUTE FUNCTION iam_v2.trg_reject_update_delete();


--
-- Name: pms_interface_revisions imm_pms_rev; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER imm_pms_rev BEFORE DELETE OR UPDATE ON iam_v2.pms_interface_revisions FOR EACH ROW EXECUTE FUNCTION iam_v2.trg_reject_update_delete();


--
-- Name: offer_quotes offer_quote_immutable; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER offer_quote_immutable BEFORE UPDATE ON iam_v2.offer_quotes FOR EACH ROW EXECUTE FUNCTION iam_v2.trg_offer_quote_immutable();


--
-- Name: accounting_checkpoints p3_accounting_checkpoints_controlled_writer; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER p3_accounting_checkpoints_controlled_writer BEFORE INSERT OR DELETE OR UPDATE ON iam_v2.accounting_checkpoints FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_controlled_writer_only();


--
-- Name: accounting_records p3_accounting_needs_binding; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER p3_accounting_needs_binding BEFORE INSERT ON iam_v2.accounting_records FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_accounting_needs_binding();


--
-- Name: accounting_records p3_accounting_records_controlled_writer; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER p3_accounting_records_controlled_writer BEFORE INSERT OR DELETE OR UPDATE ON iam_v2.accounting_records FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_controlled_writer_only();


--
-- Name: checkout_grace_alert_actions p3_alert_action_appendonly; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER p3_alert_action_appendonly BEFORE DELETE OR UPDATE ON iam_v2.checkout_grace_alert_actions FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_alert_action_guard();


--
-- Name: checkout_grace_alert_actions p3_alert_action_controlled_writer; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER p3_alert_action_controlled_writer BEFORE INSERT OR DELETE OR UPDATE ON iam_v2.checkout_grace_alert_actions FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_controlled_writer_only();


--
-- Name: checkout_grace_alert_actions p3_alert_action_insert; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER p3_alert_action_insert BEFORE INSERT ON iam_v2.checkout_grace_alert_actions FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_alert_action_guard();


--
-- Name: checkout_grace_audit p3_alert_open_on_audit; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER p3_alert_open_on_audit AFTER INSERT ON iam_v2.checkout_grace_audit FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_alert_open_on_audit();


--
-- Name: auth_contexts p3_auth_context_controlled_writer; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER p3_auth_context_controlled_writer BEFORE INSERT OR DELETE OR UPDATE ON iam_v2.auth_contexts FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_controlled_writer_only();


--
-- Name: auth_context_offers p3_auth_context_offers_controlled_writer; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER p3_auth_context_offers_controlled_writer BEFORE INSERT OR DELETE OR UPDATE ON iam_v2.auth_context_offers FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_controlled_writer_only();


--
-- Name: auth_resolutions p3_auth_resolution_controlled_writer; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER p3_auth_resolution_controlled_writer BEFORE INSERT OR DELETE OR UPDATE ON iam_v2.auth_resolutions FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_controlled_writer_only();


--
-- Name: entitlement_boundary_watermarks p3_boundary_watermark_controlled_writer; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER p3_boundary_watermark_controlled_writer BEFORE INSERT OR DELETE OR UPDATE ON iam_v2.entitlement_boundary_watermarks FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_controlled_writer_only();


--
-- Name: checkout_grace_audit p3_checkout_audit_provenance; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER p3_checkout_audit_provenance BEFORE INSERT ON iam_v2.checkout_grace_audit FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_checkout_audit_provenance();


--
-- Name: checkout_grace_audit p3_checkout_conversion_controlled_writer; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER p3_checkout_conversion_controlled_writer BEFORE INSERT OR DELETE OR UPDATE ON iam_v2.checkout_grace_audit FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_controlled_writer_only();


--
-- Name: checkout_grace_audit p3_checkout_grace_audit_guard; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER p3_checkout_grace_audit_guard BEFORE DELETE OR UPDATE ON iam_v2.checkout_grace_audit FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_checkout_grace_audit_appendonly();


--
-- Name: appliance_class_generation p3_class_generation_controlled_writer; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER p3_class_generation_controlled_writer BEFORE INSERT OR DELETE OR UPDATE ON iam_v2.appliance_class_generation FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_controlled_writer_only();


--
-- Name: delayed_accounting_records p3_dar_appendonly; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER p3_dar_appendonly BEFORE DELETE OR UPDATE ON iam_v2.delayed_accounting_records FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_history_appendonly();


--
-- Name: delayed_accounting_records p3_delayed_accounting_controlled_writer; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER p3_delayed_accounting_controlled_writer BEFORE INSERT OR DELETE OR UPDATE ON iam_v2.delayed_accounting_records FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_controlled_writer_only();


--
-- Name: accounting_records p3_detect_delayed_accounting; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER p3_detect_delayed_accounting AFTER INSERT ON iam_v2.accounting_records FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_detect_delayed_accounting();


--
-- Name: entitlement_device_authorizations p3_device_auth_controlled_writer; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER p3_device_auth_controlled_writer BEFORE INSERT OR DELETE OR UPDATE ON iam_v2.entitlement_device_authorizations FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_controlled_writer_only();


--
-- Name: entitlement_boundary_watermarks p3_ebw_appendonly; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER p3_ebw_appendonly BEFORE DELETE OR UPDATE ON iam_v2.entitlement_boundary_watermarks FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_history_appendonly();


--
-- Name: entitlement_device_authorizations p3_eda_appendonly; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER p3_eda_appendonly BEFORE DELETE OR UPDATE ON iam_v2.entitlement_device_authorizations FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_history_appendonly();


--
-- Name: entitlement_device_authorizations p3_eda_insert; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER p3_eda_insert BEFORE INSERT ON iam_v2.entitlement_device_authorizations FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_eda_insert_guard();


--
-- Name: entitlements p3_entitlement_controlled_writer; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER p3_entitlement_controlled_writer BEFORE UPDATE ON iam_v2.entitlements FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_controlled_writer_only();


--
-- Name: entitlements p3_entitlement_status_coherent; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE CONSTRAINT TRIGGER p3_entitlement_status_coherent AFTER INSERT OR UPDATE ON iam_v2.entitlements DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_entitlement_status_coherent();


--
-- Name: entitlement_state_transitions p3_est_appendonly; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER p3_est_appendonly BEFORE DELETE OR UPDATE ON iam_v2.entitlement_state_transitions FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_history_appendonly();


--
-- Name: entitlement_state_transitions p3_est_controlled_writer; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER p3_est_controlled_writer BEFORE INSERT OR UPDATE ON iam_v2.entitlement_state_transitions FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_controlled_writer_only();


--
-- Name: entitlement_state_transitions p3_est_insert; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER p3_est_insert BEFORE INSERT ON iam_v2.entitlement_state_transitions FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_est_insert_guard();


--
-- Name: site_checkout_grace_config p3_grace_config_controlled_writer; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER p3_grace_config_controlled_writer BEFORE INSERT OR DELETE OR UPDATE ON iam_v2.site_checkout_grace_config FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_controlled_writer_only();


--
-- Name: site_checkout_grace_config p3_grace_config_version_guard; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER p3_grace_config_version_guard BEFORE UPDATE ON iam_v2.site_checkout_grace_config FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_grace_config_version_guard();


--
-- Name: checkout_grace_policy_publications p3_grace_publication_appendonly; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER p3_grace_publication_appendonly BEFORE DELETE OR UPDATE ON iam_v2.checkout_grace_policy_publications FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_history_appendonly();


--
-- Name: checkout_grace_policy_publications p3_grace_publication_controlled_writer; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER p3_grace_publication_controlled_writer BEFORE INSERT OR DELETE OR UPDATE ON iam_v2.checkout_grace_policy_publications FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_controlled_writer_only();


--
-- Name: controlled_operation_scope p3_operation_scope_controlled_writer; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER p3_operation_scope_controlled_writer BEFORE INSERT OR DELETE OR UPDATE ON iam_v2.controlled_operation_scope FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_controlled_writer_only();


--
-- Name: purchases p3_purchase_controlled_writer; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER p3_purchase_controlled_writer BEFORE INSERT OR DELETE OR UPDATE ON iam_v2.purchases FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_controlled_writer_only();


--
-- Name: offer_quotes p3_quote_controlled_writer; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER p3_quote_controlled_writer BEFORE INSERT OR DELETE OR UPDATE ON iam_v2.offer_quotes FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_controlled_writer_only();


--
-- Name: internet_packages p3_reserved_grace_pkg; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER p3_reserved_grace_pkg BEFORE INSERT OR DELETE OR UPDATE ON iam_v2.internet_packages FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_reserved_grace_codes();


--
-- Name: service_plans p3_reserved_grace_plan; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER p3_reserved_grace_plan BEFORE INSERT OR DELETE OR UPDATE ON iam_v2.service_plans FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_reserved_grace_codes();


--
-- Name: session_entitlement_bindings p3_seb_appendonly; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER p3_seb_appendonly BEFORE DELETE OR UPDATE ON iam_v2.session_entitlement_bindings FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_seb_appendonly();


--
-- Name: session_entitlement_bindings p3_seb_insert; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER p3_seb_insert BEFORE INSERT ON iam_v2.session_entitlement_bindings FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_seb_insert_guard();


--
-- Name: session_entitlement_bindings p3_session_binding_controlled_writer; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER p3_session_binding_controlled_writer BEFORE INSERT OR DELETE OR UPDATE ON iam_v2.session_entitlement_bindings FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_controlled_writer_only();


--
-- Name: sessions p3_session_close_binding; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER p3_session_close_binding AFTER UPDATE ON iam_v2.sessions FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_session_close_binding();


--
-- Name: sessions p3_session_open_binding; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER p3_session_open_binding AFTER INSERT ON iam_v2.sessions FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_session_open_binding();


--
-- Name: sessions p3_session_usage_controlled_writer; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER p3_session_usage_controlled_writer BEFORE UPDATE ON iam_v2.sessions FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_controlled_writer_only();


--
-- Name: pms_source_conflicts p3_source_conflict_controlled_writer; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER p3_source_conflict_controlled_writer BEFORE INSERT OR DELETE OR UPDATE ON iam_v2.pms_source_conflicts FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_controlled_writer_only();


--
-- Name: stays p3_stay_controlled_writer; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER p3_stay_controlled_writer BEFORE INSERT OR DELETE OR UPDATE ON iam_v2.stays FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_controlled_writer_only();


--
-- Name: stay_events p3_stay_event_controlled_writer; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER p3_stay_event_controlled_writer BEFORE INSERT OR DELETE OR UPDATE ON iam_v2.stay_events FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_controlled_writer_only();


--
-- Name: stay_events p3_stay_event_guard; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER p3_stay_event_guard BEFORE INSERT OR DELETE OR UPDATE ON iam_v2.stay_events FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_stay_event_appendonly();


--
-- Name: stays p3_stay_lifecycle_guard; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER p3_stay_lifecycle_guard BEFORE UPDATE ON iam_v2.stays FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_stay_lifecycle_guard();


--
-- Name: posting_attempts p4_attempt_lifecycle_gate; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER p4_attempt_lifecycle_gate BEFORE INSERT ON iam_v2.posting_attempts FOR EACH ROW EXECUTE FUNCTION iam_v2.p4_attempt_lifecycle_gate();


--
-- Name: posting_attempts p4_attempt_retry_gate; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER p4_attempt_retry_gate BEFORE INSERT ON iam_v2.posting_attempts FOR EACH ROW EXECUTE FUNCTION iam_v2.p4_attempt_retry_gate();


--
-- Name: posting_attempts p4_consume_retry_authorization; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER p4_consume_retry_authorization AFTER INSERT ON iam_v2.posting_attempts FOR EACH ROW EXECUTE FUNCTION iam_v2.p4_consume_retry_authorization();


--
-- Name: pms_postings p4_fias_exponent_gate; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER p4_fias_exponent_gate BEFORE INSERT ON iam_v2.pms_postings FOR EACH ROW EXECUTE FUNCTION iam_v2.p4_fias_exponent_gate();


--
-- Name: pms_interfaces p4_interface_decommission_gate; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER p4_interface_decommission_gate BEFORE UPDATE ON iam_v2.pms_interfaces FOR EACH ROW EXECUTE FUNCTION iam_v2.p4_interface_decommission_gate();


--
-- Name: posting_outbox p4_outbox_recovery_gate; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER p4_outbox_recovery_gate BEFORE UPDATE ON iam_v2.posting_outbox FOR EACH ROW EXECUTE FUNCTION iam_v2.p4_outbox_recovery_gate();


--
-- Name: payment_transactions p4_payment_admission_gate; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER p4_payment_admission_gate BEFORE INSERT ON iam_v2.payment_transactions FOR EACH ROW EXECUTE FUNCTION iam_v2.p4_payment_admission_gate();


--
-- Name: payment_transactions p4_payment_creation_gate; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER p4_payment_creation_gate BEFORE INSERT ON iam_v2.payment_transactions FOR EACH ROW EXECUTE FUNCTION iam_v2.p4_payment_creation_gate();


--
-- Name: payment_transactions p4_payment_identity_gate; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER p4_payment_identity_gate BEFORE INSERT ON iam_v2.payment_transactions FOR EACH ROW EXECUTE FUNCTION iam_v2.p4_payment_identity_gate();


--
-- Name: payment_transactions p4_payment_status_machine; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER p4_payment_status_machine BEFORE DELETE OR UPDATE ON iam_v2.payment_transactions FOR EACH ROW EXECUTE FUNCTION iam_v2.p4_payment_status_machine();


--
-- Name: pms_postings p4_posting_currency_gate; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER p4_posting_currency_gate BEFORE INSERT ON iam_v2.pms_postings FOR EACH ROW EXECUTE FUNCTION iam_v2.p4_posting_currency_gate();


--
-- Name: pms_postings p4_posting_lifecycle_gate; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER p4_posting_lifecycle_gate BEFORE INSERT ON iam_v2.pms_postings FOR EACH ROW EXECUTE FUNCTION iam_v2.p4_posting_lifecycle_gate();


--
-- Name: posting_outbox p4_recovery_gate_outbox; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER p4_recovery_gate_outbox BEFORE INSERT ON iam_v2.posting_outbox FOR EACH ROW EXECUTE FUNCTION iam_v2.p4_recovery_gate();


--
-- Name: payment_transactions p4_recovery_gate_payments; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER p4_recovery_gate_payments BEFORE INSERT ON iam_v2.payment_transactions FOR EACH ROW EXECUTE FUNCTION iam_v2.p4_recovery_gate();


--
-- Name: pms_postings p4_reversal_ledger_guard; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER p4_reversal_ledger_guard BEFORE INSERT ON iam_v2.pms_postings FOR EACH ROW EXECUTE FUNCTION iam_v2.p4_reversal_ledger_guard();


--
-- Name: posting_attempts p4_reversal_never_attempted; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER p4_reversal_never_attempted BEFORE INSERT ON iam_v2.posting_attempts FOR EACH ROW EXECUTE FUNCTION iam_v2.p4_reversal_never_executes();


--
-- Name: posting_outbox p4_reversal_never_queued; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER p4_reversal_never_queued BEFORE INSERT ON iam_v2.posting_outbox FOR EACH ROW EXECUTE FUNCTION iam_v2.p4_reversal_never_executes();


--
-- Name: posting_review_actions p4_review_writer_only; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER p4_review_writer_only BEFORE INSERT ON iam_v2.posting_review_actions FOR EACH ROW EXECUTE FUNCTION iam_v2.p4_review_writer_only();


--
-- Name: settlements p4_settlement_state_machine; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER p4_settlement_state_machine BEFORE UPDATE ON iam_v2.settlements FOR EACH ROW EXECUTE FUNCTION iam_v2.p4_settlement_state_machine();


--
-- Name: posting_attempts p4_zz_attempt_freshness_gate; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER p4_zz_attempt_freshness_gate BEFORE INSERT ON iam_v2.posting_attempts FOR EACH ROW EXECUTE FUNCTION iam_v2.p4_attempt_freshness_gate();


--
-- Name: pms_postings p4_zz_posting_freshness_gate; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER p4_zz_posting_freshness_gate BEFORE INSERT ON iam_v2.pms_postings FOR EACH ROW EXECUTE FUNCTION iam_v2.p4_posting_freshness_gate();


--
-- Name: entitlement_transfers p5_entitlement_transfer_controlled_writer; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER p5_entitlement_transfer_controlled_writer BEFORE INSERT OR DELETE OR UPDATE ON iam_v2.entitlement_transfers FOR EACH ROW EXECUTE FUNCTION iam_v2.p5_controlled_writer_only();


--
-- Name: entitlement_transfers p5_entitlement_transfer_guard; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER p5_entitlement_transfer_guard BEFORE INSERT OR DELETE OR UPDATE ON iam_v2.entitlement_transfers FOR EACH ROW EXECUTE FUNCTION iam_v2.p5_entitlement_transfer_guard();


--
-- Name: post_stay_profiles p5_post_stay_profile_controlled_writer; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER p5_post_stay_profile_controlled_writer BEFORE INSERT OR DELETE OR UPDATE ON iam_v2.post_stay_profiles FOR EACH ROW EXECUTE FUNCTION iam_v2.p5_controlled_writer_only();


--
-- Name: post_stay_profiles p5_post_stay_profile_guard; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER p5_post_stay_profile_guard BEFORE INSERT OR DELETE OR UPDATE ON iam_v2.post_stay_profiles FOR EACH ROW EXECUTE FUNCTION iam_v2.p5_post_stay_profile_guard();


--
-- Name: stay_links p5_stay_link_controlled_writer; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER p5_stay_link_controlled_writer BEFORE INSERT OR DELETE OR UPDATE ON iam_v2.stay_links FOR EACH ROW EXECUTE FUNCTION iam_v2.p5_controlled_writer_only();


--
-- Name: stay_links p5_stay_link_guard; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER p5_stay_link_guard BEFORE INSERT OR DELETE OR UPDATE ON iam_v2.stay_links FOR EACH ROW EXECUTE FUNCTION iam_v2.p5_stay_link_guard();


--
-- Name: guest_device_actions p6_guest_device_actions_append_only; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER p6_guest_device_actions_append_only BEFORE DELETE OR UPDATE ON iam_v2.guest_device_actions FOR EACH ROW EXECUTE FUNCTION iam_v2.p6_guest_device_actions_append_only();


--
-- Name: session_online_watermarks p6_online_watermark_monotonic; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER p6_online_watermark_monotonic BEFORE UPDATE ON iam_v2.session_online_watermarks FOR EACH ROW EXECUTE FUNCTION iam_v2.p6_online_watermark_monotonic();


--
-- Name: sessions p6_session_requires_authorized_binding; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER p6_session_requires_authorized_binding BEFORE INSERT OR UPDATE OF state, entitlement_id, device_id ON iam_v2.sessions FOR EACH ROW EXECUTE FUNCTION iam_v2.p6_session_requires_authorized_binding();


--
-- Name: appliance_product_setting_changes p6_setting_changes_append_only; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER p6_setting_changes_append_only BEFORE DELETE OR UPDATE ON iam_v2.appliance_product_setting_changes FOR EACH ROW EXECUTE FUNCTION iam_v2.p6_setting_changes_append_only();


--
-- Name: online_time_skipped_intervals p6_skipped_intervals_append_only; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER p6_skipped_intervals_append_only BEFORE DELETE OR UPDATE ON iam_v2.online_time_skipped_intervals FOR EACH ROW EXECUTE FUNCTION iam_v2.p6_skipped_intervals_append_only();


--
-- Name: entitlement_termination_evidence p6_termination_evidence_append_only; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER p6_termination_evidence_append_only BEFORE DELETE OR UPDATE ON iam_v2.entitlement_termination_evidence FOR EACH ROW EXECUTE FUNCTION iam_v2.p6_termination_evidence_append_only();


--
-- Name: entitlement_termination_evidence p6_termination_evidence_matches_transition; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER p6_termination_evidence_matches_transition BEFORE INSERT ON iam_v2.entitlement_termination_evidence FOR EACH ROW EXECUTE FUNCTION iam_v2.p6_termination_evidence_matches_transition();


--
-- Name: posting_attempts pa_oneway; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER pa_oneway BEFORE DELETE OR UPDATE ON iam_v2.posting_attempts FOR EACH ROW EXECUTE FUNCTION iam_v2.trg_posting_attempt_oneway();


--
-- Name: purchases purchase_quote_pin_equal; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER purchase_quote_pin_equal BEFORE INSERT OR UPDATE ON iam_v2.purchases FOR EACH ROW EXECUTE FUNCTION iam_v2.trg_purchase_quote_pin_equal();


--
-- Name: pms_interface_secret_generations sg_guard; Type: TRIGGER; Schema: iam_v2; Owner: -
--

CREATE TRIGGER sg_guard BEFORE DELETE OR UPDATE ON iam_v2.pms_interface_secret_generations FOR EACH ROW EXECUTE FUNCTION iam_v2.trg_secret_gen_guard();


--
-- Name: accounting_records ts_insert_blocker; Type: TRIGGER; Schema: public; Owner: -
--



--
-- Name: accounting_checkpoints accounting_checkpoints_tenant_id_site_id_session_id_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.accounting_checkpoints
    ADD CONSTRAINT accounting_checkpoints_tenant_id_site_id_session_id_fkey FOREIGN KEY (tenant_id, site_id, session_id) REFERENCES iam_v2.sessions(tenant_id, site_id, id) ON DELETE CASCADE;


--
-- Name: accounting_records accounting_records_tenant_id_site_id_session_id_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.accounting_records
    ADD CONSTRAINT accounting_records_tenant_id_site_id_session_id_fkey FOREIGN KEY (tenant_id, site_id, session_id) REFERENCES iam_v2.sessions(tenant_id, site_id, id);


--
-- Name: appliance_product_setting_changes appliance_product_setting_changes_changed_by_operator_id_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.appliance_product_setting_changes
    ADD CONSTRAINT appliance_product_setting_changes_changed_by_operator_id_fkey FOREIGN KEY (changed_by_operator_id) REFERENCES public.operators(id);


--
-- Name: appliance_product_settings aps_appliance_must_exist; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.appliance_product_settings
    ADD CONSTRAINT aps_appliance_must_exist FOREIGN KEY (appliance_id, tenant_id, site_id) REFERENCES public.appliances(id, tenant_id, site_id) ON DELETE CASCADE;


--
-- Name: appliance_product_setting_changes apsc_appliance_must_exist; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.appliance_product_setting_changes
    ADD CONSTRAINT apsc_appliance_must_exist FOREIGN KEY (appliance_id, tenant_id, site_id) REFERENCES public.appliances(id, tenant_id, site_id) ON DELETE CASCADE;


--
-- Name: auth_context_offers auth_context_offers_tenant_id_site_id_auth_context_id_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.auth_context_offers
    ADD CONSTRAINT auth_context_offers_tenant_id_site_id_auth_context_id_fkey FOREIGN KEY (tenant_id, site_id, auth_context_id) REFERENCES iam_v2.auth_contexts(tenant_id, site_id, id) ON DELETE CASCADE;


--
-- Name: auth_context_offers auth_context_offers_tenant_id_site_id_package_revision_id_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.auth_context_offers
    ADD CONSTRAINT auth_context_offers_tenant_id_site_id_package_revision_id_fkey FOREIGN KEY (tenant_id, site_id, package_revision_id) REFERENCES iam_v2.internet_package_revisions(tenant_id, site_id, id);


--
-- Name: auth_contexts auth_contexts_tenant_id_guest_principal_id_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.auth_contexts
    ADD CONSTRAINT auth_contexts_tenant_id_guest_principal_id_fkey FOREIGN KEY (tenant_id, guest_principal_id) REFERENCES iam_v2.guest_principals(tenant_id, id);


--
-- Name: auth_contexts auth_contexts_tenant_id_site_id_device_id_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.auth_contexts
    ADD CONSTRAINT auth_contexts_tenant_id_site_id_device_id_fkey FOREIGN KEY (tenant_id, site_id, device_id) REFERENCES iam_v2.devices(tenant_id, site_id, id);


--
-- Name: auth_contexts auth_contexts_tenant_id_site_id_guest_account_id_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.auth_contexts
    ADD CONSTRAINT auth_contexts_tenant_id_site_id_guest_account_id_fkey FOREIGN KEY (tenant_id, site_id, guest_account_id) REFERENCES iam_v2.guest_access_accounts(tenant_id, site_id, id);


--
-- Name: auth_contexts auth_contexts_tenant_id_site_id_guest_network_id_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.auth_contexts
    ADD CONSTRAINT auth_contexts_tenant_id_site_id_guest_network_id_fkey FOREIGN KEY (tenant_id, site_id, guest_network_id) REFERENCES public.guest_networks(tenant_id, site_id, id);


--
-- Name: auth_contexts auth_contexts_tenant_id_site_id_pms_interface_id_authentic_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.auth_contexts
    ADD CONSTRAINT auth_contexts_tenant_id_site_id_pms_interface_id_authentic_fkey FOREIGN KEY (tenant_id, site_id, pms_interface_id, authentication_interface_revision_id) REFERENCES iam_v2.pms_interface_revisions(tenant_id, site_id, pms_interface_id, id);


--
-- Name: auth_contexts auth_contexts_tenant_id_site_id_pms_interface_id_stay_id_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.auth_contexts
    ADD CONSTRAINT auth_contexts_tenant_id_site_id_pms_interface_id_stay_id_fkey FOREIGN KEY (tenant_id, site_id, pms_interface_id, stay_id) REFERENCES iam_v2.stays(tenant_id, site_id, pms_interface_id, id);


--
-- Name: auth_contexts auth_contexts_tenant_id_site_id_post_stay_profile_id_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.auth_contexts
    ADD CONSTRAINT auth_contexts_tenant_id_site_id_post_stay_profile_id_fkey FOREIGN KEY (tenant_id, site_id, post_stay_profile_id) REFERENCES iam_v2.post_stay_profiles(tenant_id, site_id, id);


--
-- Name: auth_contexts auth_contexts_tenant_id_site_id_voucher_id_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.auth_contexts
    ADD CONSTRAINT auth_contexts_tenant_id_site_id_voucher_id_fkey FOREIGN KEY (tenant_id, site_id, voucher_id) REFERENCES iam_v2.vouchers(tenant_id, site_id, id);


--
-- Name: auth_resolutions auth_resolutions_tenant_id_site_id_guest_network_id_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.auth_resolutions
    ADD CONSTRAINT auth_resolutions_tenant_id_site_id_guest_network_id_fkey FOREIGN KEY (tenant_id, site_id, guest_network_id) REFERENCES public.guest_networks(tenant_id, site_id, id);


--
-- Name: auth_resolutions auth_resolutions_tenant_id_site_id_resolved_stay_id_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.auth_resolutions
    ADD CONSTRAINT auth_resolutions_tenant_id_site_id_resolved_stay_id_fkey FOREIGN KEY (tenant_id, site_id, resolved_stay_id) REFERENCES iam_v2.stays(tenant_id, site_id, id);


--
-- Name: checkout_grace_alert_actions checkout_grace_alert_actions_tenant_id_site_id_audit_id_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.checkout_grace_alert_actions
    ADD CONSTRAINT checkout_grace_alert_actions_tenant_id_site_id_audit_id_fkey FOREIGN KEY (tenant_id, site_id, audit_id) REFERENCES iam_v2.checkout_grace_audit(tenant_id, site_id, id) ON DELETE CASCADE;


--
-- Name: checkout_grace_audit checkout_grace_audit_tenant_id_site_id_grace_entitlement_i_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.checkout_grace_audit
    ADD CONSTRAINT checkout_grace_audit_tenant_id_site_id_grace_entitlement_i_fkey FOREIGN KEY (tenant_id, site_id, grace_entitlement_id) REFERENCES iam_v2.entitlements(tenant_id, site_id, id);


--
-- Name: checkout_grace_audit checkout_grace_audit_tenant_id_site_id_pms_interface_id_st_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.checkout_grace_audit
    ADD CONSTRAINT checkout_grace_audit_tenant_id_site_id_pms_interface_id_st_fkey FOREIGN KEY (tenant_id, site_id, pms_interface_id, stay_id) REFERENCES iam_v2.stays(tenant_id, site_id, pms_interface_id, id) ON DELETE CASCADE;


--
-- Name: delayed_accounting_records delayed_accounting_records_watermark_id_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.delayed_accounting_records
    ADD CONSTRAINT delayed_accounting_records_watermark_id_fkey FOREIGN KEY (watermark_id) REFERENCES iam_v2.entitlement_boundary_watermarks(id) ON DELETE CASCADE;


--
-- Name: device_network_appearances device_network_appearances_tenant_id_site_id_device_id_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.device_network_appearances
    ADD CONSTRAINT device_network_appearances_tenant_id_site_id_device_id_fkey FOREIGN KEY (tenant_id, site_id, device_id) REFERENCES iam_v2.devices(tenant_id, site_id, id);


--
-- Name: device_network_appearances device_network_appearances_tenant_id_site_id_guest_network_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.device_network_appearances
    ADD CONSTRAINT device_network_appearances_tenant_id_site_id_guest_network_fkey FOREIGN KEY (tenant_id, site_id, guest_network_id) REFERENCES public.guest_networks(tenant_id, site_id, id);


--
-- Name: entitlement_adjustments entitlement_adjustments_tenant_id_site_id_entitlement_id_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.entitlement_adjustments
    ADD CONSTRAINT entitlement_adjustments_tenant_id_site_id_entitlement_id_fkey FOREIGN KEY (tenant_id, site_id, entitlement_id) REFERENCES iam_v2.entitlements(tenant_id, site_id, id);


--
-- Name: entitlement_boundary_watermarks entitlement_boundary_watermar_tenant_id_site_id_entitlemen_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.entitlement_boundary_watermarks
    ADD CONSTRAINT entitlement_boundary_watermar_tenant_id_site_id_entitlemen_fkey FOREIGN KEY (tenant_id, site_id, entitlement_id) REFERENCES iam_v2.entitlements(tenant_id, site_id, id) ON DELETE CASCADE;


--
-- Name: entitlement_device_authorizations entitlement_device_authorizat_tenant_id_site_id_entitlemen_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.entitlement_device_authorizations
    ADD CONSTRAINT entitlement_device_authorizat_tenant_id_site_id_entitlemen_fkey FOREIGN KEY (tenant_id, site_id, entitlement_id) REFERENCES iam_v2.entitlements(tenant_id, site_id, id) ON DELETE CASCADE;


--
-- Name: entitlement_device_authorizations entitlement_device_authorizati_tenant_id_site_id_device_id_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.entitlement_device_authorizations
    ADD CONSTRAINT entitlement_device_authorizati_tenant_id_site_id_device_id_fkey FOREIGN KEY (tenant_id, site_id, device_id) REFERENCES iam_v2.devices(tenant_id, site_id, id);


--
-- Name: entitlement_devices entitlement_devices_tenant_id_site_id_device_id_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.entitlement_devices
    ADD CONSTRAINT entitlement_devices_tenant_id_site_id_device_id_fkey FOREIGN KEY (tenant_id, site_id, device_id) REFERENCES iam_v2.devices(tenant_id, site_id, id);


--
-- Name: entitlement_devices entitlement_devices_tenant_id_site_id_entitlement_id_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.entitlement_devices
    ADD CONSTRAINT entitlement_devices_tenant_id_site_id_entitlement_id_fkey FOREIGN KEY (tenant_id, site_id, entitlement_id) REFERENCES iam_v2.entitlements(tenant_id, site_id, id) ON DELETE CASCADE;


--
-- Name: entitlement_state_transitions entitlement_state_transitions_superseded_by_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.entitlement_state_transitions
    ADD CONSTRAINT entitlement_state_transitions_superseded_by_fkey FOREIGN KEY (superseded_by) REFERENCES iam_v2.entitlement_state_transitions(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: entitlement_state_transitions entitlement_state_transitions_supersedes_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.entitlement_state_transitions
    ADD CONSTRAINT entitlement_state_transitions_supersedes_fkey FOREIGN KEY (supersedes) REFERENCES iam_v2.entitlement_state_transitions(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: entitlement_state_transitions entitlement_state_transitions_tenant_id_site_id_entitlemen_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.entitlement_state_transitions
    ADD CONSTRAINT entitlement_state_transitions_tenant_id_site_id_entitlemen_fkey FOREIGN KEY (tenant_id, site_id, entitlement_id) REFERENCES iam_v2.entitlements(tenant_id, site_id, id) ON DELETE CASCADE;


--
-- Name: entitlement_termination_evidence entitlement_termination_evide_tenant_id_site_id_entitlemen_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.entitlement_termination_evidence
    ADD CONSTRAINT entitlement_termination_evide_tenant_id_site_id_entitlemen_fkey FOREIGN KEY (tenant_id, site_id, entitlement_id) REFERENCES iam_v2.entitlements(tenant_id, site_id, id) ON DELETE CASCADE;


--
-- Name: entitlement_transfers entitlement_transfers_tenant_id_site_id_from_entitlement_i_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.entitlement_transfers
    ADD CONSTRAINT entitlement_transfers_tenant_id_site_id_from_entitlement_i_fkey FOREIGN KEY (tenant_id, site_id, from_entitlement_id) REFERENCES iam_v2.entitlements(tenant_id, site_id, id);


--
-- Name: entitlement_transfers entitlement_transfers_tenant_id_site_id_from_stay_id_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.entitlement_transfers
    ADD CONSTRAINT entitlement_transfers_tenant_id_site_id_from_stay_id_fkey FOREIGN KEY (tenant_id, site_id, from_stay_id) REFERENCES iam_v2.stays(tenant_id, site_id, id);


--
-- Name: entitlement_transfers entitlement_transfers_tenant_id_site_id_to_entitlement_id_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.entitlement_transfers
    ADD CONSTRAINT entitlement_transfers_tenant_id_site_id_to_entitlement_id_fkey FOREIGN KEY (tenant_id, site_id, to_entitlement_id) REFERENCES iam_v2.entitlements(tenant_id, site_id, id);


--
-- Name: entitlement_transfers entitlement_transfers_tenant_id_site_id_to_stay_id_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.entitlement_transfers
    ADD CONSTRAINT entitlement_transfers_tenant_id_site_id_to_stay_id_fkey FOREIGN KEY (tenant_id, site_id, to_stay_id) REFERENCES iam_v2.stays(tenant_id, site_id, id);


--
-- Name: entitlements entitlements_tenant_id_guest_principal_id_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.entitlements
    ADD CONSTRAINT entitlements_tenant_id_guest_principal_id_fkey FOREIGN KEY (tenant_id, guest_principal_id) REFERENCES iam_v2.guest_principals(tenant_id, id) ON DELETE CASCADE;


--
-- Name: entitlements entitlements_tenant_id_site_id_guest_account_id_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.entitlements
    ADD CONSTRAINT entitlements_tenant_id_site_id_guest_account_id_fkey FOREIGN KEY (tenant_id, site_id, guest_account_id) REFERENCES iam_v2.guest_access_accounts(tenant_id, site_id, id) ON DELETE CASCADE;


--
-- Name: entitlements entitlements_tenant_id_site_id_package_revision_id_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.entitlements
    ADD CONSTRAINT entitlements_tenant_id_site_id_package_revision_id_fkey FOREIGN KEY (tenant_id, site_id, package_revision_id) REFERENCES iam_v2.internet_package_revisions(tenant_id, site_id, id);


--
-- Name: entitlements entitlements_tenant_id_site_id_pms_interface_id_stay_id_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.entitlements
    ADD CONSTRAINT entitlements_tenant_id_site_id_pms_interface_id_stay_id_fkey FOREIGN KEY (tenant_id, site_id, pms_interface_id, stay_id) REFERENCES iam_v2.stays(tenant_id, site_id, pms_interface_id, id) ON DELETE CASCADE;


--
-- Name: entitlements entitlements_tenant_id_site_id_purchase_id_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.entitlements
    ADD CONSTRAINT entitlements_tenant_id_site_id_purchase_id_fkey FOREIGN KEY (tenant_id, site_id, purchase_id) REFERENCES iam_v2.purchases(tenant_id, site_id, id);


--
-- Name: entitlements entitlements_tenant_id_site_id_service_plan_revision_id_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.entitlements
    ADD CONSTRAINT entitlements_tenant_id_site_id_service_plan_revision_id_fkey FOREIGN KEY (tenant_id, site_id, service_plan_revision_id) REFERENCES iam_v2.service_plan_revisions(tenant_id, site_id, id);


--
-- Name: entitlements entitlements_tenant_id_site_id_supersedes_entitlement_id_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.entitlements
    ADD CONSTRAINT entitlements_tenant_id_site_id_supersedes_entitlement_id_fkey FOREIGN KEY (tenant_id, site_id, supersedes_entitlement_id) REFERENCES iam_v2.entitlements(tenant_id, site_id, id);


--
-- Name: entitlements entitlements_tenant_id_site_id_voucher_id_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.entitlements
    ADD CONSTRAINT entitlements_tenant_id_site_id_voucher_id_fkey FOREIGN KEY (tenant_id, site_id, voucher_id) REFERENCES iam_v2.vouchers(tenant_id, site_id, id) ON DELETE CASCADE;


--
-- Name: folios folios_tenant_id_site_id_pms_interface_id_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.folios
    ADD CONSTRAINT folios_tenant_id_site_id_pms_interface_id_fkey FOREIGN KEY (tenant_id, site_id, pms_interface_id) REFERENCES iam_v2.pms_interfaces(tenant_id, site_id, id);


--
-- Name: guest_access_accounts guest_access_accounts_tenant_id_site_id_assigned_package_i_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.guest_access_accounts
    ADD CONSTRAINT guest_access_accounts_tenant_id_site_id_assigned_package_i_fkey FOREIGN KEY (tenant_id, site_id, assigned_package_id) REFERENCES iam_v2.internet_packages(tenant_id, site_id, id);


--
-- Name: guest_device_actions guest_device_actions_tenant_id_site_id_entitlement_id_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.guest_device_actions
    ADD CONSTRAINT guest_device_actions_tenant_id_site_id_entitlement_id_fkey FOREIGN KEY (tenant_id, site_id, entitlement_id) REFERENCES iam_v2.entitlements(tenant_id, site_id, id) ON DELETE CASCADE;


--
-- Name: guest_network_pms_map guest_network_pms_map_tenant_id_site_id_guest_network_id_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.guest_network_pms_map
    ADD CONSTRAINT guest_network_pms_map_tenant_id_site_id_guest_network_id_fkey FOREIGN KEY (tenant_id, site_id, guest_network_id) REFERENCES public.guest_networks(tenant_id, site_id, id) ON DELETE CASCADE;


--
-- Name: guest_network_pms_map guest_network_pms_map_tenant_id_site_id_pms_interface_id_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.guest_network_pms_map
    ADD CONSTRAINT guest_network_pms_map_tenant_id_site_id_pms_interface_id_fkey FOREIGN KEY (tenant_id, site_id, pms_interface_id) REFERENCES iam_v2.pms_interfaces(tenant_id, site_id, id);


--
-- Name: guest_principal_identities guest_principal_identities_tenant_id_guest_principal_id_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.guest_principal_identities
    ADD CONSTRAINT guest_principal_identities_tenant_id_guest_principal_id_fkey FOREIGN KEY (tenant_id, guest_principal_id) REFERENCES iam_v2.guest_principals(tenant_id, id) ON DELETE CASCADE;


--
-- Name: internet_package_revisions internet_package_revisions_tenant_id_site_id_package_id_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.internet_package_revisions
    ADD CONSTRAINT internet_package_revisions_tenant_id_site_id_package_id_fkey FOREIGN KEY (tenant_id, site_id, package_id) REFERENCES iam_v2.internet_packages(tenant_id, site_id, id);


--
-- Name: internet_package_revisions internet_package_revisions_tenant_id_site_id_service_plan__fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.internet_package_revisions
    ADD CONSTRAINT internet_package_revisions_tenant_id_site_id_service_plan__fkey FOREIGN KEY (tenant_id, site_id, service_plan_revision_id) REFERENCES iam_v2.service_plan_revisions(tenant_id, site_id, id);


--
-- Name: internet_packages internet_packages_tenant_id_site_id_id_current_revision_id_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.internet_packages
    ADD CONSTRAINT internet_packages_tenant_id_site_id_id_current_revision_id_fkey FOREIGN KEY (tenant_id, site_id, id, current_revision_id) REFERENCES iam_v2.internet_package_revisions(tenant_id, site_id, package_id, id);


--
-- Name: offer_quotes offer_quotes_tenant_id_site_id_auth_context_id_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.offer_quotes
    ADD CONSTRAINT offer_quotes_tenant_id_site_id_auth_context_id_fkey FOREIGN KEY (tenant_id, site_id, auth_context_id) REFERENCES iam_v2.auth_contexts(tenant_id, site_id, id);


--
-- Name: offer_quotes offer_quotes_tenant_id_site_id_package_revision_id_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.offer_quotes
    ADD CONSTRAINT offer_quotes_tenant_id_site_id_package_revision_id_fkey FOREIGN KEY (tenant_id, site_id, package_revision_id) REFERENCES iam_v2.internet_package_revisions(tenant_id, site_id, id);


--
-- Name: offer_quotes offer_quotes_tenant_id_site_id_package_revision_id_pms_int_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.offer_quotes
    ADD CONSTRAINT offer_quotes_tenant_id_site_id_package_revision_id_pms_int_fkey FOREIGN KEY (tenant_id, site_id, package_revision_id, pms_interface_id, settlement_mapping_id) REFERENCES iam_v2.package_settlement_mappings(tenant_id, site_id, package_revision_id, pms_interface_id, id);


--
-- Name: package_eligibility_rules package_eligibility_rules_tenant_id_site_id_package_revisi_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.package_eligibility_rules
    ADD CONSTRAINT package_eligibility_rules_tenant_id_site_id_package_revisi_fkey FOREIGN KEY (tenant_id, site_id, package_revision_id) REFERENCES iam_v2.internet_package_revisions(tenant_id, site_id, id) ON DELETE CASCADE;


--
-- Name: package_grant_tiers package_grant_tiers_tenant_id_site_id_package_revision_id_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.package_grant_tiers
    ADD CONSTRAINT package_grant_tiers_tenant_id_site_id_package_revision_id_fkey FOREIGN KEY (tenant_id, site_id, package_revision_id) REFERENCES iam_v2.internet_package_revisions(tenant_id, site_id, id) ON DELETE CASCADE;


--
-- Name: package_settlement_mappings package_settlement_mappings_tenant_id_site_id_package_revi_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.package_settlement_mappings
    ADD CONSTRAINT package_settlement_mappings_tenant_id_site_id_package_revi_fkey FOREIGN KEY (tenant_id, site_id, package_revision_id) REFERENCES iam_v2.internet_package_revisions(tenant_id, site_id, id);


--
-- Name: package_settlement_mappings package_settlement_mappings_tenant_id_site_id_pms_interfac_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.package_settlement_mappings
    ADD CONSTRAINT package_settlement_mappings_tenant_id_site_id_pms_interfac_fkey FOREIGN KEY (tenant_id, site_id, pms_interface_id) REFERENCES iam_v2.pms_interfaces(tenant_id, site_id, id);


--
-- Name: payment_transaction_events payment_transaction_events_tenant_id_site_id_payment_trans_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.payment_transaction_events
    ADD CONSTRAINT payment_transaction_events_tenant_id_site_id_payment_trans_fkey FOREIGN KEY (tenant_id, site_id, payment_transaction_id) REFERENCES iam_v2.payment_transactions(tenant_id, site_id, id);


--
-- Name: payment_transactions payment_transactions_tenant_id_site_id_settlement_id_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.payment_transactions
    ADD CONSTRAINT payment_transactions_tenant_id_site_id_settlement_id_fkey FOREIGN KEY (tenant_id, site_id, settlement_id) REFERENCES iam_v2.settlements(tenant_id, site_id, id);


--
-- Name: payment_transactions payment_transactions_tenant_id_site_id_settlement_id_paren_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.payment_transactions
    ADD CONSTRAINT payment_transactions_tenant_id_site_id_settlement_id_paren_fkey FOREIGN KEY (tenant_id, site_id, settlement_id, parent_transaction_id) REFERENCES iam_v2.payment_transactions(tenant_id, site_id, settlement_id, id);


--
-- Name: pms_interface_pnumber_seq pms_interface_pnumber_seq_tenant_id_site_id_pms_interface__fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.pms_interface_pnumber_seq
    ADD CONSTRAINT pms_interface_pnumber_seq_tenant_id_site_id_pms_interface__fkey FOREIGN KEY (tenant_id, site_id, pms_interface_id) REFERENCES iam_v2.pms_interfaces(tenant_id, site_id, id);


--
-- Name: pms_interface_revisions pms_interface_revisions_tenant_id_site_id_pms_interface_id_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.pms_interface_revisions
    ADD CONSTRAINT pms_interface_revisions_tenant_id_site_id_pms_interface_id_fkey FOREIGN KEY (tenant_id, site_id, pms_interface_id) REFERENCES iam_v2.pms_interfaces(tenant_id, site_id, id);


--
-- Name: pms_interface_runtime pms_interface_runtime_tenant_id_site_id_pms_interface_id__fkey1; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.pms_interface_runtime
    ADD CONSTRAINT pms_interface_runtime_tenant_id_site_id_pms_interface_id__fkey1 FOREIGN KEY (tenant_id, site_id, pms_interface_id, pinned_secret_generation_id) REFERENCES iam_v2.pms_interface_secret_generations(tenant_id, site_id, pms_interface_id, id);


--
-- Name: pms_interface_runtime pms_interface_runtime_tenant_id_site_id_pms_interface_id_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.pms_interface_runtime
    ADD CONSTRAINT pms_interface_runtime_tenant_id_site_id_pms_interface_id_fkey FOREIGN KEY (tenant_id, site_id, pms_interface_id) REFERENCES iam_v2.pms_interfaces(tenant_id, site_id, id) ON DELETE CASCADE;


--
-- Name: pms_interface_runtime pms_interface_runtime_tenant_id_site_id_pms_interface_id_p_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.pms_interface_runtime
    ADD CONSTRAINT pms_interface_runtime_tenant_id_site_id_pms_interface_id_p_fkey FOREIGN KEY (tenant_id, site_id, pms_interface_id, pinned_revision_id) REFERENCES iam_v2.pms_interface_revisions(tenant_id, site_id, pms_interface_id, id);


--
-- Name: pms_interface_secret_generations pms_interface_secret_generati_tenant_id_site_id_pms_interf_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.pms_interface_secret_generations
    ADD CONSTRAINT pms_interface_secret_generati_tenant_id_site_id_pms_interf_fkey FOREIGN KEY (tenant_id, site_id, pms_interface_id) REFERENCES iam_v2.pms_interfaces(tenant_id, site_id, id);


--
-- Name: pms_interfaces pms_interfaces_tenant_id_site_id_id_current_revision_id_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.pms_interfaces
    ADD CONSTRAINT pms_interfaces_tenant_id_site_id_id_current_revision_id_fkey FOREIGN KEY (tenant_id, site_id, id, current_revision_id) REFERENCES iam_v2.pms_interface_revisions(tenant_id, site_id, pms_interface_id, id);


--
-- Name: pms_postings pms_postings_purchase_id_pms_interface_id_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.pms_postings
    ADD CONSTRAINT pms_postings_purchase_id_pms_interface_id_fkey FOREIGN KEY (purchase_id, pms_interface_id) REFERENCES iam_v2.purchases(id, pms_interface_id);


--
-- Name: pms_postings pms_postings_settlement_id_purchase_id_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.pms_postings
    ADD CONSTRAINT pms_postings_settlement_id_purchase_id_fkey FOREIGN KEY (settlement_id, purchase_id) REFERENCES iam_v2.settlements(id, purchase_id);


--
-- Name: pms_postings pms_postings_tenant_id_site_id_pms_interface_id_folio_id_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.pms_postings
    ADD CONSTRAINT pms_postings_tenant_id_site_id_pms_interface_id_folio_id_fkey FOREIGN KEY (tenant_id, site_id, pms_interface_id, folio_id) REFERENCES iam_v2.folios(tenant_id, site_id, pms_interface_id, id);


--
-- Name: pms_postings pms_postings_tenant_id_site_id_pms_interface_id_posting_in_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.pms_postings
    ADD CONSTRAINT pms_postings_tenant_id_site_id_pms_interface_id_posting_in_fkey FOREIGN KEY (tenant_id, site_id, pms_interface_id, posting_interface_revision_id) REFERENCES iam_v2.pms_interface_revisions(tenant_id, site_id, pms_interface_id, id);


--
-- Name: pms_postings pms_postings_tenant_id_site_id_pms_interface_id_secret_gen_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.pms_postings
    ADD CONSTRAINT pms_postings_tenant_id_site_id_pms_interface_id_secret_gen_fkey FOREIGN KEY (tenant_id, site_id, pms_interface_id, secret_generation_id) REFERENCES iam_v2.pms_interface_secret_generations(tenant_id, site_id, pms_interface_id, id);


--
-- Name: pms_postings pms_postings_tenant_id_site_id_pms_interface_id_stay_id_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.pms_postings
    ADD CONSTRAINT pms_postings_tenant_id_site_id_pms_interface_id_stay_id_fkey FOREIGN KEY (tenant_id, site_id, pms_interface_id, stay_id) REFERENCES iam_v2.stays(tenant_id, site_id, pms_interface_id, id);


--
-- Name: pms_postings pms_postings_tenant_id_site_id_purchase_id_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.pms_postings
    ADD CONSTRAINT pms_postings_tenant_id_site_id_purchase_id_fkey FOREIGN KEY (tenant_id, site_id, purchase_id) REFERENCES iam_v2.purchases(tenant_id, site_id, id);


--
-- Name: pms_postings pms_postings_tenant_id_site_id_settlement_id_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.pms_postings
    ADD CONSTRAINT pms_postings_tenant_id_site_id_settlement_id_fkey FOREIGN KEY (tenant_id, site_id, settlement_id) REFERENCES iam_v2.settlements(tenant_id, site_id, id);


--
-- Name: pms_source_conflicts pms_source_conflicts_tenant_id_site_id_interface_a_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.pms_source_conflicts
    ADD CONSTRAINT pms_source_conflicts_tenant_id_site_id_interface_a_fkey FOREIGN KEY (tenant_id, site_id, interface_a) REFERENCES iam_v2.pms_interfaces(tenant_id, site_id, id);


--
-- Name: pms_source_conflicts pms_source_conflicts_tenant_id_site_id_interface_b_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.pms_source_conflicts
    ADD CONSTRAINT pms_source_conflicts_tenant_id_site_id_interface_b_fkey FOREIGN KEY (tenant_id, site_id, interface_b) REFERENCES iam_v2.pms_interfaces(tenant_id, site_id, id);


--
-- Name: post_stay_profiles post_stay_profiles_tenant_id_site_id_origin_stay_id_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.post_stay_profiles
    ADD CONSTRAINT post_stay_profiles_tenant_id_site_id_origin_stay_id_fkey FOREIGN KEY (tenant_id, site_id, origin_stay_id) REFERENCES iam_v2.stays(tenant_id, site_id, id);


--
-- Name: posting_attempt_events posting_attempt_events_tenant_id_site_id_posting_attempt_i_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.posting_attempt_events
    ADD CONSTRAINT posting_attempt_events_tenant_id_site_id_posting_attempt_i_fkey FOREIGN KEY (tenant_id, site_id, posting_attempt_id) REFERENCES iam_v2.posting_attempts(tenant_id, site_id, id);


--
-- Name: posting_attempts posting_attempts_tenant_id_site_id_internal_posting_id_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.posting_attempts
    ADD CONSTRAINT posting_attempts_tenant_id_site_id_internal_posting_id_fkey FOREIGN KEY (tenant_id, site_id, internal_posting_id) REFERENCES iam_v2.pms_postings(tenant_id, site_id, id);


--
-- Name: posting_attempts posting_attempts_tenant_id_site_id_pms_interface_id_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.posting_attempts
    ADD CONSTRAINT posting_attempts_tenant_id_site_id_pms_interface_id_fkey FOREIGN KEY (tenant_id, site_id, pms_interface_id) REFERENCES iam_v2.pms_interfaces(tenant_id, site_id, id);


--
-- Name: posting_outbox posting_outbox_tenant_id_site_id_pms_interface_id_posting__fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.posting_outbox
    ADD CONSTRAINT posting_outbox_tenant_id_site_id_pms_interface_id_posting__fkey FOREIGN KEY (tenant_id, site_id, pms_interface_id, posting_id) REFERENCES iam_v2.pms_postings(tenant_id, site_id, pms_interface_id, id);


--
-- Name: posting_review_actions posting_review_actions_tenant_id_site_id_posting_id_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.posting_review_actions
    ADD CONSTRAINT posting_review_actions_tenant_id_site_id_posting_id_fkey FOREIGN KEY (tenant_id, site_id, posting_id) REFERENCES iam_v2.pms_postings(tenant_id, site_id, id);


--
-- Name: posting_review_state posting_review_state_tenant_id_site_id_posting_id_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.posting_review_state
    ADD CONSTRAINT posting_review_state_tenant_id_site_id_posting_id_fkey FOREIGN KEY (tenant_id, site_id, posting_id) REFERENCES iam_v2.pms_postings(tenant_id, site_id, id);


--
-- Name: payment_transactions ptx_merchant_account_configured; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.payment_transactions
    ADD CONSTRAINT ptx_merchant_account_configured FOREIGN KEY (tenant_id, site_id, merchant_account_id) REFERENCES iam_v2.payment_provider_accounts(tenant_id, site_id, id);


--
-- Name: purchases purchases_offer_quote_id_auth_context_id_package_revision__fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.purchases
    ADD CONSTRAINT purchases_offer_quote_id_auth_context_id_package_revision__fkey FOREIGN KEY (offer_quote_id, auth_context_id, package_revision_id, pms_interface_id, settlement_mapping_id) REFERENCES iam_v2.offer_quotes(id, auth_context_id, package_revision_id, pms_interface_id, settlement_mapping_id);


--
-- Name: purchases purchases_tenant_id_site_id_package_revision_id_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.purchases
    ADD CONSTRAINT purchases_tenant_id_site_id_package_revision_id_fkey FOREIGN KEY (tenant_id, site_id, package_revision_id) REFERENCES iam_v2.internet_package_revisions(tenant_id, site_id, id);


--
-- Name: purchases purchases_tenant_id_site_id_package_revision_id_pms_interf_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.purchases
    ADD CONSTRAINT purchases_tenant_id_site_id_package_revision_id_pms_interf_fkey FOREIGN KEY (tenant_id, site_id, package_revision_id, pms_interface_id, settlement_mapping_id) REFERENCES iam_v2.package_settlement_mappings(tenant_id, site_id, package_revision_id, pms_interface_id, id);


--
-- Name: purchases purchases_tenant_id_site_id_pms_interface_id_authenticatio_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.purchases
    ADD CONSTRAINT purchases_tenant_id_site_id_pms_interface_id_authenticatio_fkey FOREIGN KEY (tenant_id, site_id, pms_interface_id, authentication_interface_revision_id) REFERENCES iam_v2.pms_interface_revisions(tenant_id, site_id, pms_interface_id, id);


--
-- Name: purchases purchases_tenant_id_site_id_pms_interface_id_stay_id_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.purchases
    ADD CONSTRAINT purchases_tenant_id_site_id_pms_interface_id_stay_id_fkey FOREIGN KEY (tenant_id, site_id, pms_interface_id, stay_id) REFERENCES iam_v2.stays(tenant_id, site_id, pms_interface_id, id);


--
-- Name: service_plan_revisions service_plan_revisions_tenant_id_site_id_service_plan_id_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.service_plan_revisions
    ADD CONSTRAINT service_plan_revisions_tenant_id_site_id_service_plan_id_fkey FOREIGN KEY (tenant_id, site_id, service_plan_id) REFERENCES iam_v2.service_plans(tenant_id, site_id, id);


--
-- Name: service_plans service_plans_tenant_id_site_id_id_current_revision_id_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.service_plans
    ADD CONSTRAINT service_plans_tenant_id_site_id_id_current_revision_id_fkey FOREIGN KEY (tenant_id, site_id, id, current_revision_id) REFERENCES iam_v2.service_plan_revisions(tenant_id, site_id, service_plan_id, id);


--
-- Name: session_counter_watermarks session_counter_watermarks_tenant_id_site_id_session_id_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.session_counter_watermarks
    ADD CONSTRAINT session_counter_watermarks_tenant_id_site_id_session_id_fkey FOREIGN KEY (tenant_id, site_id, session_id) REFERENCES iam_v2.sessions(tenant_id, site_id, id) ON DELETE CASCADE;


--
-- Name: session_entitlement_bindings session_entitlement_bindings_tenant_id_site_id_entitlement_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.session_entitlement_bindings
    ADD CONSTRAINT session_entitlement_bindings_tenant_id_site_id_entitlement_fkey FOREIGN KEY (tenant_id, site_id, entitlement_id) REFERENCES iam_v2.entitlements(tenant_id, site_id, id);


--
-- Name: session_entitlement_bindings session_entitlement_bindings_tenant_id_site_id_session_id_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.session_entitlement_bindings
    ADD CONSTRAINT session_entitlement_bindings_tenant_id_site_id_session_id_fkey FOREIGN KEY (tenant_id, site_id, session_id) REFERENCES iam_v2.sessions(tenant_id, site_id, id) ON DELETE CASCADE;


--
-- Name: session_online_watermarks session_online_watermarks_tenant_id_site_id_session_id_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.session_online_watermarks
    ADD CONSTRAINT session_online_watermarks_tenant_id_site_id_session_id_fkey FOREIGN KEY (tenant_id, site_id, session_id) REFERENCES iam_v2.sessions(tenant_id, site_id, id) ON DELETE CASCADE;


--
-- Name: sessions sessions_tenant_id_site_id_device_id_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.sessions
    ADD CONSTRAINT sessions_tenant_id_site_id_device_id_fkey FOREIGN KEY (tenant_id, site_id, device_id) REFERENCES iam_v2.devices(tenant_id, site_id, id);


--
-- Name: sessions sessions_tenant_id_site_id_entitlement_id_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.sessions
    ADD CONSTRAINT sessions_tenant_id_site_id_entitlement_id_fkey FOREIGN KEY (tenant_id, site_id, entitlement_id) REFERENCES iam_v2.entitlements(tenant_id, site_id, id);


--
-- Name: settlements settlements_tenant_id_site_id_purchase_id_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.settlements
    ADD CONSTRAINT settlements_tenant_id_site_id_purchase_id_fkey FOREIGN KEY (tenant_id, site_id, purchase_id) REFERENCES iam_v2.purchases(tenant_id, site_id, id);


--
-- Name: site_checkout_grace_config site_checkout_grace_config_tenant_id_site_id_grace_package_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.site_checkout_grace_config
    ADD CONSTRAINT site_checkout_grace_config_tenant_id_site_id_grace_package_fkey FOREIGN KEY (tenant_id, site_id, grace_package_revision_id) REFERENCES iam_v2.internet_package_revisions(tenant_id, site_id, id);


--
-- Name: stay_events stay_events_tenant_id_site_id_pms_interface_id_stay_id_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.stay_events
    ADD CONSTRAINT stay_events_tenant_id_site_id_pms_interface_id_stay_id_fkey FOREIGN KEY (tenant_id, site_id, pms_interface_id, stay_id) REFERENCES iam_v2.stays(tenant_id, site_id, pms_interface_id, id);


--
-- Name: stay_folios stay_folios_tenant_id_site_id_pms_interface_id_folio_id_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.stay_folios
    ADD CONSTRAINT stay_folios_tenant_id_site_id_pms_interface_id_folio_id_fkey FOREIGN KEY (tenant_id, site_id, pms_interface_id, folio_id) REFERENCES iam_v2.folios(tenant_id, site_id, pms_interface_id, id);


--
-- Name: stay_folios stay_folios_tenant_id_site_id_pms_interface_id_stay_id_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.stay_folios
    ADD CONSTRAINT stay_folios_tenant_id_site_id_pms_interface_id_stay_id_fkey FOREIGN KEY (tenant_id, site_id, pms_interface_id, stay_id) REFERENCES iam_v2.stays(tenant_id, site_id, pms_interface_id, id);


--
-- Name: stay_guests stay_guests_tenant_id_site_id_pms_interface_id_stay_id_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.stay_guests
    ADD CONSTRAINT stay_guests_tenant_id_site_id_pms_interface_id_stay_id_fkey FOREIGN KEY (tenant_id, site_id, pms_interface_id, stay_id) REFERENCES iam_v2.stays(tenant_id, site_id, pms_interface_id, id) ON DELETE CASCADE;


--
-- Name: stay_links stay_links_tenant_id_site_id_from_stay_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.stay_links
    ADD CONSTRAINT stay_links_tenant_id_site_id_from_stay_fkey FOREIGN KEY (tenant_id, site_id, from_stay) REFERENCES iam_v2.stays(tenant_id, site_id, id);


--
-- Name: stay_links stay_links_tenant_id_site_id_to_stay_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.stay_links
    ADD CONSTRAINT stay_links_tenant_id_site_id_to_stay_fkey FOREIGN KEY (tenant_id, site_id, to_stay) REFERENCES iam_v2.stays(tenant_id, site_id, id);


--
-- Name: stays stays_last_applied_event_scoped; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.stays
    ADD CONSTRAINT stays_last_applied_event_scoped FOREIGN KEY (tenant_id, site_id, pms_interface_id, last_applied_event_id) REFERENCES iam_v2.stay_events(tenant_id, site_id, pms_interface_id, id);


--
-- Name: stays stays_occupancy_revision_fk; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.stays
    ADD CONSTRAINT stays_occupancy_revision_fk FOREIGN KEY (tenant_id, site_id, pms_interface_id, occupancy_revision_id) REFERENCES iam_v2.pms_interface_revisions(tenant_id, site_id, pms_interface_id, id);


--
-- Name: stays stays_tenant_id_site_id_pms_interface_id_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.stays
    ADD CONSTRAINT stays_tenant_id_site_id_pms_interface_id_fkey FOREIGN KEY (tenant_id, site_id, pms_interface_id) REFERENCES iam_v2.pms_interfaces(tenant_id, site_id, id);


--
-- Name: voucher_batches voucher_batches_tenant_id_site_id_package_revision_id_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.voucher_batches
    ADD CONSTRAINT voucher_batches_tenant_id_site_id_package_revision_id_fkey FOREIGN KEY (tenant_id, site_id, package_revision_id) REFERENCES iam_v2.internet_package_revisions(tenant_id, site_id, id);


--
-- Name: vouchers vouchers_tenant_id_site_id_code_key_generation_id_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.vouchers
    ADD CONSTRAINT vouchers_tenant_id_site_id_code_key_generation_id_fkey FOREIGN KEY (tenant_id, site_id, code_key_generation_id) REFERENCES iam_v2.voucher_code_key_generations(tenant_id, site_id, id);


--
-- Name: vouchers vouchers_tenant_id_site_id_package_revision_id_fkey; Type: FK CONSTRAINT; Schema: iam_v2; Owner: -
--

ALTER TABLE ONLY iam_v2.vouchers
    ADD CONSTRAINT vouchers_tenant_id_site_id_package_revision_id_fkey FOREIGN KEY (tenant_id, site_id, package_revision_id) REFERENCES iam_v2.internet_package_revisions(tenant_id, site_id, id);


--
-- Name: appliances appliances_site_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.appliances
    ADD CONSTRAINT appliances_site_id_fkey FOREIGN KEY (site_id) REFERENCES public.sites(id) ON DELETE CASCADE;


--
-- Name: appliances appliances_tenant_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.appliances
    ADD CONSTRAINT appliances_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES public.tenants(id) ON DELETE CASCADE;


--
-- Name: auth_otps auth_otps_appliance_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.auth_otps
    ADD CONSTRAINT auth_otps_appliance_id_fkey FOREIGN KEY (appliance_id) REFERENCES public.appliances(id) ON DELETE SET NULL;


--
-- Name: auth_otps auth_otps_otp_key_generation_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.auth_otps
    ADD CONSTRAINT auth_otps_otp_key_generation_fkey FOREIGN KEY (otp_key_generation) REFERENCES public.otp_hmac_key_generations(generation);


--
-- Name: auth_otps auth_otps_tenant_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.auth_otps
    ADD CONSTRAINT auth_otps_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES public.tenants(id) ON DELETE CASCADE;


--
-- Name: dhcp_pools dhcp_pools_guest_network_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.dhcp_pools
    ADD CONSTRAINT dhcp_pools_guest_network_id_fkey FOREIGN KEY (guest_network_id) REFERENCES public.guest_networks(id) ON DELETE CASCADE;


--
-- Name: dhcp_reservations dhcp_reservations_guest_network_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.dhcp_reservations
    ADD CONSTRAINT dhcp_reservations_guest_network_id_fkey FOREIGN KEY (guest_network_id) REFERENCES public.guest_networks(id) ON DELETE CASCADE;


--
-- Name: network_apply_events network_apply_events_revision_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.network_apply_events
    ADD CONSTRAINT network_apply_events_revision_id_fkey FOREIGN KEY (revision_id) REFERENCES public.network_config_revisions(id) ON DELETE CASCADE;


--
-- Name: network_health_checks network_health_checks_revision_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.network_health_checks
    ADD CONSTRAINT network_health_checks_revision_id_fkey FOREIGN KEY (revision_id) REFERENCES public.network_config_revisions(id) ON DELETE CASCADE;


--
-- Name: notification_providers notification_providers_tenant_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.notification_providers
    ADD CONSTRAINT notification_providers_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES public.tenants(id) ON DELETE CASCADE;


--
-- Name: operator_roles operator_roles_operator_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.operator_roles
    ADD CONSTRAINT operator_roles_operator_id_fkey FOREIGN KEY (operator_id) REFERENCES public.operators(id) ON DELETE CASCADE;


--
-- Name: operator_roles operator_roles_tenant_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.operator_roles
    ADD CONSTRAINT operator_roles_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES public.tenants(id) ON DELETE CASCADE;


--
-- Name: operators operators_tenant_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.operators
    ADD CONSTRAINT operators_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES public.tenants(id) ON DELETE CASCADE;


--
-- Name: pms_attempts pms_attempts_appliance_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pms_attempts
    ADD CONSTRAINT pms_attempts_appliance_id_fkey FOREIGN KEY (appliance_id) REFERENCES public.appliances(id) ON DELETE SET NULL;


--
-- Name: pms_attempts pms_attempts_tenant_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pms_attempts
    ADD CONSTRAINT pms_attempts_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES public.tenants(id) ON DELETE CASCADE;


--
-- Name: pms_providers pms_providers_site_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pms_providers
    ADD CONSTRAINT pms_providers_site_id_fkey FOREIGN KEY (site_id) REFERENCES public.sites(id) ON DELETE CASCADE;


--
-- Name: pms_providers pms_providers_tenant_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pms_providers
    ADD CONSTRAINT pms_providers_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES public.tenants(id) ON DELETE CASCADE;


--
-- Name: sites sites_tenant_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sites
    ADD CONSTRAINT sites_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES public.tenants(id) ON DELETE CASCADE;


--
-- Name: social_oauth_providers social_oauth_providers_tenant_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.social_oauth_providers
    ADD CONSTRAINT social_oauth_providers_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES public.tenants(id) ON DELETE CASCADE;


--
-- Name: social_oauth_states social_oauth_states_appliance_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.social_oauth_states
    ADD CONSTRAINT social_oauth_states_appliance_id_fkey FOREIGN KEY (appliance_id) REFERENCES public.appliances(id) ON DELETE SET NULL;


--
-- Name: social_oauth_states social_oauth_states_tenant_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.social_oauth_states
    ADD CONSTRAINT social_oauth_states_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES public.tenants(id) ON DELETE CASCADE;


--
-- Name: stripe_accounts stripe_accounts_tenant_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.stripe_accounts
    ADD CONSTRAINT stripe_accounts_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES public.tenants(id) ON DELETE CASCADE;


--
-- Name: stripe_events stripe_events_tenant_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.stripe_events
    ADD CONSTRAINT stripe_events_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES public.tenants(id) ON DELETE CASCADE;


--
-- Name: walled_garden_rules walled_garden_rules_site_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.walled_garden_rules
    ADD CONSTRAINT walled_garden_rules_site_id_fkey FOREIGN KEY (site_id) REFERENCES public.sites(id) ON DELETE CASCADE;


--
-- Name: walled_garden_rules walled_garden_rules_tenant_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.walled_garden_rules
    ADD CONSTRAINT walled_garden_rules_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES public.tenants(id) ON DELETE CASCADE;


--
-- Name: SCHEMA public; Type: ACL; Schema: -; Owner: -
--

GRANT USAGE ON SCHEMA public TO svc_scd;
GRANT USAGE ON SCHEMA public TO svc_edged;
GRANT USAGE ON SCHEMA public TO svc_acctd;
GRANT USAGE ON SCHEMA public TO svc_netd;


--
-- Name: SCHEMA iam_v2; Type: ACL; Schema: -; Owner: -
--

GRANT USAGE ON SCHEMA iam_v2 TO sc_payment_runtime;
GRANT USAGE ON SCHEMA iam_v2 TO sc_financial_operator;
GRANT USAGE ON SCHEMA iam_v2 TO sc_financial_readonly;
GRANT USAGE ON SCHEMA iam_v2 TO sc_commerce_runtime;
GRANT USAGE ON SCHEMA iam_v2 TO sc_payment_outcome;
GRANT USAGE ON SCHEMA iam_v2 TO svc_scd;
GRANT USAGE ON SCHEMA iam_v2 TO svc_netd;
GRANT USAGE ON SCHEMA iam_v2 TO svc_acctd;
GRANT USAGE ON SCHEMA iam_v2 TO svc_edged;


--
-- Name: FUNCTION activate_session_enforcement(p_tenant uuid, p_site uuid, p_session uuid, p_bridge text, p_class_minor integer, p_epoch bigint); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.activate_session_enforcement(p_tenant uuid, p_site uuid, p_session uuid, p_bridge text, p_class_minor integer, p_epoch bigint) FROM PUBLIC;
GRANT ALL ON FUNCTION iam_v2.activate_session_enforcement(p_tenant uuid, p_site uuid, p_session uuid, p_bridge text, p_class_minor integer, p_epoch bigint) TO svc_netd;


--
-- Name: FUNCTION allocate_class_generation(p_tenant uuid, p_site uuid, p_appliance uuid); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.allocate_class_generation(p_tenant uuid, p_site uuid, p_appliance uuid) FROM PUBLIC;
GRANT ALL ON FUNCTION iam_v2.allocate_class_generation(p_tenant uuid, p_site uuid, p_appliance uuid) TO svc_netd;


--
-- Name: FUNCTION allocate_p_number(p_tenant uuid, p_site uuid, p_interface uuid); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.allocate_p_number(p_tenant uuid, p_site uuid, p_interface uuid) FROM PUBLIC;


--
-- Name: FUNCTION apply_entitlement_transition(p_ent uuid, p_to text, p_at timestamp with time zone, p_reason text); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.apply_entitlement_transition(p_ent uuid, p_to text, p_at timestamp with time zone, p_reason text) FROM PUBLIC;
GRANT ALL ON FUNCTION iam_v2.apply_entitlement_transition(p_ent uuid, p_to text, p_at timestamp with time zone, p_reason text) TO svc_netd;
GRANT ALL ON FUNCTION iam_v2.apply_entitlement_transition(p_ent uuid, p_to text, p_at timestamp with time zone, p_reason text) TO svc_acctd;


--
-- Name: FUNCTION apply_payment_callback_v2(p_tenant uuid, p_provider text, p_merchant uuid, p_client_ref text, p_provider_event_id text, p_event_type text, p_asserted_status text, p_provider_txn_ref text, p_evidence jsonb); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.apply_payment_callback_v2(p_tenant uuid, p_provider text, p_merchant uuid, p_client_ref text, p_provider_event_id text, p_event_type text, p_asserted_status text, p_provider_txn_ref text, p_evidence jsonb) FROM PUBLIC;


--
-- Name: FUNCTION authorize_entitlement_device(p_ent uuid, p_device uuid, p_at timestamp with time zone); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.authorize_entitlement_device(p_ent uuid, p_device uuid, p_at timestamp with time zone) FROM PUBLIC;
GRANT ALL ON FUNCTION iam_v2.authorize_entitlement_device(p_ent uuid, p_device uuid, p_at timestamp with time zone) TO svc_acctd;


--
-- Name: FUNCTION begin_controlled_operation(p_family text); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.begin_controlled_operation(p_family text) FROM PUBLIC;
GRANT ALL ON FUNCTION iam_v2.begin_controlled_operation(p_family text) TO sc_payment_runtime;
GRANT ALL ON FUNCTION iam_v2.begin_controlled_operation(p_family text) TO svc_edged;
GRANT ALL ON FUNCTION iam_v2.begin_controlled_operation(p_family text) TO svc_acctd;
GRANT ALL ON FUNCTION iam_v2.begin_controlled_operation(p_family text) TO svc_scd;
GRANT ALL ON FUNCTION iam_v2.begin_controlled_operation(p_family text) TO svc_netd;


--
-- Name: FUNCTION begin_payment_execution(p_txn uuid); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.begin_payment_execution(p_txn uuid) FROM PUBLIC;
GRANT ALL ON FUNCTION iam_v2.begin_payment_execution(p_txn uuid) TO sc_payment_runtime;


--
-- Name: FUNCTION bootstrap_emergency_grace(p_tenant uuid, p_site uuid); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.bootstrap_emergency_grace(p_tenant uuid, p_site uuid) FROM PUBLIC;


--
-- Name: FUNCTION deauthorize_entitlement_device(p_ent uuid, p_device uuid, p_at timestamp with time zone, p_reason text); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.deauthorize_entitlement_device(p_ent uuid, p_device uuid, p_at timestamp with time zone, p_reason text) FROM PUBLIC;


--
-- Name: FUNCTION emergency_grace_health(p_tenant uuid, p_site uuid); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.emergency_grace_health(p_tenant uuid, p_site uuid) FROM PUBLIC;


--
-- Name: FUNCTION end_session_enforcement(p_tenant uuid, p_site uuid, p_session uuid, p_reason text); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.end_session_enforcement(p_tenant uuid, p_site uuid, p_session uuid, p_reason text) FROM PUBLIC;
GRANT ALL ON FUNCTION iam_v2.end_session_enforcement(p_tenant uuid, p_site uuid, p_session uuid, p_reason text) TO svc_netd;


--
-- Name: FUNCTION entitlement_usage_bytes(p_ent uuid, p_at timestamp with time zone); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.entitlement_usage_bytes(p_ent uuid, p_at timestamp with time zone) FROM PUBLIC;
GRANT ALL ON FUNCTION iam_v2.entitlement_usage_bytes(p_ent uuid, p_at timestamp with time zone) TO svc_acctd;


--
-- Name: FUNCTION grace_package_matches_policy(p_tenant uuid, p_site uuid, p_pkg_rev uuid, p_duration integer, p_down integer, p_up integer, p_quota bigint, p_dev_limit integer, p_dev_policy text); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.grace_package_matches_policy(p_tenant uuid, p_site uuid, p_pkg_rev uuid, p_duration integer, p_down integer, p_up integer, p_quota bigint, p_dev_limit integer, p_dev_policy text) FROM PUBLIC;


--
-- Name: FUNCTION grace_package_mismatch_reason(p_tenant uuid, p_site uuid, p_pkg_rev uuid, p_duration integer, p_down integer, p_up integer, p_quota bigint, p_dev_limit integer, p_dev_policy text); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.grace_package_mismatch_reason(p_tenant uuid, p_site uuid, p_pkg_rev uuid, p_duration integer, p_down integer, p_up integer, p_quota bigint, p_dev_limit integer, p_dev_policy text) FROM PUBLIC;
GRANT ALL ON FUNCTION iam_v2.grace_package_mismatch_reason(p_tenant uuid, p_site uuid, p_pkg_rev uuid, p_duration integer, p_down integer, p_up integer, p_quota bigint, p_dev_limit integer, p_dev_policy text) TO svc_edged;


--
-- Name: FUNCTION ingest_absolute_counters(p_tenant uuid, p_site uuid, p_session uuid, p_source_device uuid, p_bridge text, p_class_minor integer, p_epoch bigint, p_abs_up bigint, p_abs_down bigint, p_sampled_at timestamp with time zone); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.ingest_absolute_counters(p_tenant uuid, p_site uuid, p_session uuid, p_source_device uuid, p_bridge text, p_class_minor integer, p_epoch bigint, p_abs_up bigint, p_abs_down bigint, p_sampled_at timestamp with time zone) FROM PUBLIC;
GRANT ALL ON FUNCTION iam_v2.ingest_absolute_counters(p_tenant uuid, p_site uuid, p_session uuid, p_source_device uuid, p_bridge text, p_class_minor integer, p_epoch bigint, p_abs_up bigint, p_abs_down bigint, p_sampled_at timestamp with time zone) TO svc_netd;
GRANT ALL ON FUNCTION iam_v2.ingest_absolute_counters(p_tenant uuid, p_site uuid, p_session uuid, p_source_device uuid, p_bridge text, p_class_minor integer, p_epoch bigint, p_abs_up bigint, p_abs_down bigint, p_sampled_at timestamp with time zone) TO svc_acctd;


--
-- Name: FUNCTION issue_or_return_pms_context(p_tenant uuid, p_site uuid, p_interface uuid, p_revision uuid, p_stay uuid, p_device uuid, p_guest_network uuid, p_request uuid, p_ttl_seconds integer); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.issue_or_return_pms_context(p_tenant uuid, p_site uuid, p_interface uuid, p_revision uuid, p_stay uuid, p_device uuid, p_guest_network uuid, p_request uuid, p_ttl_seconds integer) FROM PUBLIC;


--
-- Name: FUNCTION p3_accounting_needs_binding(); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.p3_accounting_needs_binding() FROM PUBLIC;


--
-- Name: FUNCTION p3_alert_action_guard(); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.p3_alert_action_guard() FROM PUBLIC;


--
-- Name: FUNCTION p3_alert_open_on_audit(); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.p3_alert_open_on_audit() FROM PUBLIC;


--
-- Name: FUNCTION p3_checkout_audit_provenance(); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.p3_checkout_audit_provenance() FROM PUBLIC;


--
-- Name: FUNCTION p3_checkout_grace_audit_appendonly(); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.p3_checkout_grace_audit_appendonly() FROM PUBLIC;


--
-- Name: FUNCTION p3_controlled_operation_open(p_family text); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.p3_controlled_operation_open(p_family text) FROM PUBLIC;
GRANT ALL ON FUNCTION iam_v2.p3_controlled_operation_open(p_family text) TO svc_scd;
GRANT ALL ON FUNCTION iam_v2.p3_controlled_operation_open(p_family text) TO svc_netd;
GRANT ALL ON FUNCTION iam_v2.p3_controlled_operation_open(p_family text) TO svc_acctd;


--
-- Name: FUNCTION p3_controlled_writer_only(); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.p3_controlled_writer_only() FROM PUBLIC;


--
-- Name: FUNCTION p3_controlled_writer_owner(p_family text); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.p3_controlled_writer_owner(p_family text) FROM PUBLIC;


--
-- Name: FUNCTION p3_detect_delayed_accounting(); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.p3_detect_delayed_accounting() FROM PUBLIC;


--
-- Name: FUNCTION p3_eda_insert_guard(); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.p3_eda_insert_guard() FROM PUBLIC;


--
-- Name: FUNCTION p3_entitlement_at(p_session uuid, p_at timestamp with time zone); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.p3_entitlement_at(p_session uuid, p_at timestamp with time zone) FROM PUBLIC;


--
-- Name: FUNCTION p3_entitlement_status_coherent(); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.p3_entitlement_status_coherent() FROM PUBLIC;


--
-- Name: FUNCTION p3_est_insert_guard(); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.p3_est_insert_guard() FROM PUBLIC;


--
-- Name: FUNCTION p3_expected_class_minor(p_ip inet); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.p3_expected_class_minor(p_ip inet) FROM PUBLIC;


--
-- Name: FUNCTION p3_grace_config_version_guard(); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.p3_grace_config_version_guard() FROM PUBLIC;


--
-- Name: FUNCTION p3_history_appendonly(); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.p3_history_appendonly() FROM PUBLIC;


--
-- Name: FUNCTION p3_rederive_entitlement_times(p_ent uuid); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.p3_rederive_entitlement_times(p_ent uuid) FROM PUBLIC;


--
-- Name: FUNCTION p3_reserved_grace_codes(); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.p3_reserved_grace_codes() FROM PUBLIC;


--
-- Name: FUNCTION p3_seb_appendonly(); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.p3_seb_appendonly() FROM PUBLIC;


--
-- Name: FUNCTION p3_session_close_binding(); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.p3_session_close_binding() FROM PUBLIC;


--
-- Name: FUNCTION p3_session_open_binding(); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.p3_session_open_binding() FROM PUBLIC;


--
-- Name: FUNCTION p3_stay_event_appendonly(); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.p3_stay_event_appendonly() FROM PUBLIC;


--
-- Name: FUNCTION p3_stay_lifecycle_guard(); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.p3_stay_lifecycle_guard() FROM PUBLIC;


--
-- Name: FUNCTION p4_apply_provider_outcome(p_client_ref text, p_provider_event_id text, p_event_type text, p_outcome text, p_provider_txn_ref text, p_evidence jsonb); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.p4_apply_provider_outcome(p_client_ref text, p_provider_event_id text, p_event_type text, p_outcome text, p_provider_txn_ref text, p_evidence jsonb) FROM PUBLIC;
GRANT ALL ON FUNCTION iam_v2.p4_apply_provider_outcome(p_client_ref text, p_provider_event_id text, p_event_type text, p_outcome text, p_provider_txn_ref text, p_evidence jsonb) TO sc_payment_outcome;


--
-- Name: FUNCTION p4_assert_compliance_archived(p_tenant uuid); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.p4_assert_compliance_archived(p_tenant uuid) FROM PUBLIC;


--
-- Name: FUNCTION p4_assert_financial_actor(p_tenant uuid, p_actor uuid); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.p4_assert_financial_actor(p_tenant uuid, p_actor uuid) FROM PUBLIC;


--
-- Name: FUNCTION p4_authorize_zero_attempt_retry(p_posting uuid, p_actor uuid, p_reason text, p_evidence jsonb); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.p4_authorize_zero_attempt_retry(p_posting uuid, p_actor uuid, p_reason text, p_evidence jsonb) FROM PUBLIC;
GRANT ALL ON FUNCTION iam_v2.p4_authorize_zero_attempt_retry(p_posting uuid, p_actor uuid, p_reason text, p_evidence jsonb) TO sc_financial_operator;


--
-- Name: FUNCTION p4_current_restore_generation(p_tenant uuid, p_site uuid); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.p4_current_restore_generation(p_tenant uuid, p_site uuid) FROM PUBLIC;
GRANT ALL ON FUNCTION iam_v2.p4_current_restore_generation(p_tenant uuid, p_site uuid) TO sc_payment_runtime;
GRANT ALL ON FUNCTION iam_v2.p4_current_restore_generation(p_tenant uuid, p_site uuid) TO sc_financial_operator;


--
-- Name: FUNCTION p4_declare_financial_recovery(p_tenant uuid, p_site uuid, p_actor uuid, p_reason text); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.p4_declare_financial_recovery(p_tenant uuid, p_site uuid, p_actor uuid, p_reason text) FROM PUBLIC;
GRANT ALL ON FUNCTION iam_v2.p4_declare_financial_recovery(p_tenant uuid, p_site uuid, p_actor uuid, p_reason text) TO sc_financial_operator;


--
-- Name: FUNCTION p4_entitlement_grant_kernel(p_tenant uuid, p_site uuid, p_purchase uuid, p_voucher uuid, p_account uuid, p_principal uuid, p_snapshot jsonb, p_plan_rev uuid, p_pkg_rev uuid); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.p4_entitlement_grant_kernel(p_tenant uuid, p_site uuid, p_purchase uuid, p_voucher uuid, p_account uuid, p_principal uuid, p_snapshot jsonb, p_plan_rev uuid, p_pkg_rev uuid) FROM PUBLIC;


--
-- Name: FUNCTION p4_financial_recovery_active(p_tenant uuid, p_site uuid); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.p4_financial_recovery_active(p_tenant uuid, p_site uuid) FROM PUBLIC;
GRANT ALL ON FUNCTION iam_v2.p4_financial_recovery_active(p_tenant uuid, p_site uuid) TO sc_payment_runtime;
GRANT ALL ON FUNCTION iam_v2.p4_financial_recovery_active(p_tenant uuid, p_site uuid) TO sc_financial_operator;


--
-- Name: FUNCTION p4_grant_paid_entitlement(p_tenant uuid, p_site uuid, p_settlement uuid); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.p4_grant_paid_entitlement(p_tenant uuid, p_site uuid, p_settlement uuid) FROM PUBLIC;
GRANT ALL ON FUNCTION iam_v2.p4_grant_paid_entitlement(p_tenant uuid, p_site uuid, p_settlement uuid) TO sc_payment_runtime;


--
-- Name: FUNCTION p4_grant_quoted_entitlement(p_tenant uuid, p_site uuid, p_purchase uuid); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.p4_grant_quoted_entitlement(p_tenant uuid, p_site uuid, p_purchase uuid) FROM PUBLIC;
GRANT ALL ON FUNCTION iam_v2.p4_grant_quoted_entitlement(p_tenant uuid, p_site uuid, p_purchase uuid) TO sc_commerce_runtime;
GRANT ALL ON FUNCTION iam_v2.p4_grant_quoted_entitlement(p_tenant uuid, p_site uuid, p_purchase uuid) TO svc_scd;


--
-- Name: FUNCTION p4_hold_financial_rails(p_tenant uuid, p_site uuid, p_epoch bigint); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.p4_hold_financial_rails(p_tenant uuid, p_site uuid, p_epoch bigint) FROM PUBLIC;


--
-- Name: FUNCTION p4_insert_entitlement(p_tenant uuid, p_site uuid, p_voucher uuid, p_account uuid, p_principal uuid, p_purchase uuid, p_policy jsonb, p_plan_rev uuid, p_pkg_rev uuid, p_time_mode text, p_end_mode text, p_window_ends timestamp with time zone, p_supersedes uuid); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.p4_insert_entitlement(p_tenant uuid, p_site uuid, p_voucher uuid, p_account uuid, p_principal uuid, p_purchase uuid, p_policy jsonb, p_plan_rev uuid, p_pkg_rev uuid, p_time_mode text, p_end_mode text, p_window_ends timestamp with time zone, p_supersedes uuid) FROM PUBLIC;
GRANT ALL ON FUNCTION iam_v2.p4_insert_entitlement(p_tenant uuid, p_site uuid, p_voucher uuid, p_account uuid, p_principal uuid, p_purchase uuid, p_policy jsonb, p_plan_rev uuid, p_pkg_rev uuid, p_time_mode text, p_end_mode text, p_window_ends timestamp with time zone, p_supersedes uuid) TO svc_scd;


--
-- Name: FUNCTION p4_mark_purchase_granted(p_purchase uuid); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.p4_mark_purchase_granted(p_purchase uuid) FROM PUBLIC;
GRANT ALL ON FUNCTION iam_v2.p4_mark_purchase_granted(p_purchase uuid) TO svc_scd;


--
-- Name: FUNCTION p4_outbox_recovery_gate(); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.p4_outbox_recovery_gate() FROM PUBLIC;


--
-- Name: FUNCTION p4_payment_admission_gate(); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.p4_payment_admission_gate() FROM PUBLIC;


--
-- Name: FUNCTION p4_payment_identity_gate(); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.p4_payment_identity_gate() FROM PUBLIC;


--
-- Name: FUNCTION p4_reconcile_financial_epoch(p_tenant uuid, p_site uuid, p_system_identity text); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.p4_reconcile_financial_epoch(p_tenant uuid, p_site uuid, p_system_identity text) FROM PUBLIC;


--
-- Name: FUNCTION p4_reconcile_financial_epoch_v2(p_tenant uuid, p_site uuid, p_system_identity text, p_marker_generation bigint, p_marker_present boolean); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.p4_reconcile_financial_epoch_v2(p_tenant uuid, p_site uuid, p_system_identity text, p_marker_generation bigint, p_marker_present boolean) FROM PUBLIC;
GRANT ALL ON FUNCTION iam_v2.p4_reconcile_financial_epoch_v2(p_tenant uuid, p_site uuid, p_system_identity text, p_marker_generation bigint, p_marker_present boolean) TO sc_payment_runtime;


--
-- Name: FUNCTION p4_record_compliance_archive(p_tenant uuid, p_site uuid, p_manifest_sha text, p_artifact_path text, p_row_counts jsonb); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.p4_record_compliance_archive(p_tenant uuid, p_site uuid, p_manifest_sha text, p_artifact_path text, p_row_counts jsonb) FROM PUBLIC;


--
-- Name: FUNCTION p4_record_compliance_receipt(p_archive uuid, p_authority text, p_reference text); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.p4_record_compliance_receipt(p_archive uuid, p_authority text, p_reference text) FROM PUBLIC;


--
-- Name: FUNCTION p4_record_supported_restore(p_tenant uuid, p_site uuid, p_generation bigint, p_manifest_sha text, p_backup_taken_at timestamp with time zone, p_restored_by text); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.p4_record_supported_restore(p_tenant uuid, p_site uuid, p_generation bigint, p_manifest_sha text, p_backup_taken_at timestamp with time zone, p_restored_by text) FROM PUBLIC;


--
-- Name: FUNCTION p4_recovery_gate(); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.p4_recovery_gate() FROM PUBLIC;


--
-- Name: FUNCTION p4_release_financial_recovery(p_tenant uuid, p_site uuid, p_actor uuid, p_note text); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.p4_release_financial_recovery(p_tenant uuid, p_site uuid, p_actor uuid, p_note text) FROM PUBLIC;
GRANT ALL ON FUNCTION iam_v2.p4_release_financial_recovery(p_tenant uuid, p_site uuid, p_actor uuid, p_note text) TO sc_financial_operator;


--
-- Name: FUNCTION p4_resolve_payment_account(p_tenant uuid, p_site uuid); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.p4_resolve_payment_account(p_tenant uuid, p_site uuid) FROM PUBLIC;
GRANT ALL ON FUNCTION iam_v2.p4_resolve_payment_account(p_tenant uuid, p_site uuid) TO sc_payment_runtime;


--
-- Name: FUNCTION p4_resolve_recovery_hold(p_hold uuid, p_resolution text, p_actor uuid, p_note text); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.p4_resolve_recovery_hold(p_hold uuid, p_resolution text, p_actor uuid, p_note text) FROM PUBLIC;
GRANT ALL ON FUNCTION iam_v2.p4_resolve_recovery_hold(p_hold uuid, p_resolution text, p_actor uuid, p_note text) TO sc_financial_operator;


--
-- Name: FUNCTION p4_terminate_live_entitlement_for_subject(p_tenant uuid, p_site uuid, p_voucher uuid, p_account uuid, p_principal uuid); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.p4_terminate_live_entitlement_for_subject(p_tenant uuid, p_site uuid, p_voucher uuid, p_account uuid, p_principal uuid) FROM PUBLIC;
GRANT ALL ON FUNCTION iam_v2.p4_terminate_live_entitlement_for_subject(p_tenant uuid, p_site uuid, p_voucher uuid, p_account uuid, p_principal uuid) TO svc_scd;


--
-- Name: FUNCTION p5_begin_controlled_operation(p_family text); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.p5_begin_controlled_operation(p_family text) FROM PUBLIC;
GRANT ALL ON FUNCTION iam_v2.p5_begin_controlled_operation(p_family text) TO svc_scd;


--
-- Name: FUNCTION p5_controlled_operation_open(p_family text); Type: ACL; Schema: iam_v2; Owner: -
--

GRANT ALL ON FUNCTION iam_v2.p5_controlled_operation_open(p_family text) TO svc_scd;
GRANT ALL ON FUNCTION iam_v2.p5_controlled_operation_open(p_family text) TO svc_netd;
GRANT ALL ON FUNCTION iam_v2.p5_controlled_operation_open(p_family text) TO svc_acctd;


--
-- Name: FUNCTION p5_controlled_writer_only(); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.p5_controlled_writer_only() FROM PUBLIC;


--
-- Name: FUNCTION p5_entitlement_transfer_guard(); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.p5_entitlement_transfer_guard() FROM PUBLIC;


--
-- Name: FUNCTION p5_post_stay_authenticable(p_tenant uuid, p_site uuid, p_profile uuid); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.p5_post_stay_authenticable(p_tenant uuid, p_site uuid, p_profile uuid) FROM PUBLIC;


--
-- Name: FUNCTION p5_post_stay_profile_guard(); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.p5_post_stay_profile_guard() FROM PUBLIC;


--
-- Name: FUNCTION p5_stay_link_guard(); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.p5_stay_link_guard() FROM PUBLIC;


--
-- Name: FUNCTION p6_data_crossing(p_entitlement uuid); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.p6_data_crossing(p_entitlement uuid) FROM PUBLIC;


--
-- Name: FUNCTION p6_due_terminal(p_entitlement uuid); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.p6_due_terminal(p_entitlement uuid) FROM PUBLIC;


--
-- Name: FUNCTION p6_exhaustion_instant(p_entitlement uuid); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.p6_exhaustion_instant(p_entitlement uuid) FROM PUBLIC;


--
-- Name: FUNCTION p6_expire_entitlement(p_entitlement uuid); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.p6_expire_entitlement(p_entitlement uuid) FROM PUBLIC;


--
-- Name: FUNCTION p6_guest_device_actions_append_only(); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.p6_guest_device_actions_append_only() FROM PUBLIC;


--
-- Name: FUNCTION p6_guest_release_device(p_entitlement uuid, p_device uuid, p_max_releases_per_hour integer); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.p6_guest_release_device(p_entitlement uuid, p_device uuid, p_max_releases_per_hour integer) FROM PUBLIC;


--
-- Name: FUNCTION p6_guest_release_device_policy(p_entitlement uuid, p_device uuid); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.p6_guest_release_device_policy(p_entitlement uuid, p_device uuid) FROM PUBLIC;


--
-- Name: FUNCTION p6_online_watermark_monotonic(); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.p6_online_watermark_monotonic() FROM PUBLIC;


--
-- Name: FUNCTION p6_over_budget_now(p_entitlement uuid); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.p6_over_budget_now(p_entitlement uuid) FROM PUBLIC;


--
-- Name: FUNCTION p6_record_time_termination(p_entitlement uuid, p_cause text); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.p6_record_time_termination(p_entitlement uuid, p_cause text) FROM PUBLIC;


--
-- Name: FUNCTION p6_session_requires_authorized_binding(); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.p6_session_requires_authorized_binding() FROM PUBLIC;


--
-- Name: FUNCTION p6_set_guest_device_self_service(p_tenant uuid, p_site uuid, p_appliance uuid, p_on boolean, p_operator uuid, p_operator_label text, p_reason text); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.p6_set_guest_device_self_service(p_tenant uuid, p_site uuid, p_appliance uuid, p_on boolean, p_operator uuid, p_operator_label text, p_reason text) FROM PUBLIC;


--
-- Name: FUNCTION p6_setting_changes_append_only(); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.p6_setting_changes_append_only() FROM PUBLIC;


--
-- Name: FUNCTION p6_skipped_intervals_append_only(); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.p6_skipped_intervals_append_only() FROM PUBLIC;


--
-- Name: FUNCTION p6_suspend_over_budget(p_tenant uuid, p_site uuid); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.p6_suspend_over_budget(p_tenant uuid, p_site uuid) FROM PUBLIC;


--
-- Name: FUNCTION p6_termination_evidence_append_only(); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.p6_termination_evidence_append_only() FROM PUBLIC;


--
-- Name: FUNCTION p6_termination_evidence_matches_transition(); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.p6_termination_evidence_matches_transition() FROM PUBLIC;


--
-- Name: FUNCTION p6_tick_online_time(p_tenant uuid, p_site uuid, p_now timestamp with time zone, p_max_charge_seconds integer, p_capped_entitlements uuid[], p_caps timestamp with time zone[]); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.p6_tick_online_time(p_tenant uuid, p_site uuid, p_now timestamp with time zone, p_max_charge_seconds integer, p_capped_entitlements uuid[], p_caps timestamp with time zone[]) FROM PUBLIC;


--
-- Name: FUNCTION publish_checkout_grace_config(p_tenant uuid, p_site uuid, p_pkg_rev uuid, p_duration integer, p_down integer, p_up integer, p_quota bigint, p_dev_limit integer, p_dev_policy text, p_eligibility integer); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.publish_checkout_grace_config(p_tenant uuid, p_site uuid, p_pkg_rev uuid, p_duration integer, p_down integer, p_up integer, p_quota bigint, p_dev_limit integer, p_dev_policy text, p_eligibility integer) FROM PUBLIC;


--
-- Name: FUNCTION publish_checkout_grace_policy(p_tenant uuid, p_site uuid, p_pkg_rev uuid, p_duration integer, p_down integer, p_up integer, p_quota bigint, p_dev_limit integer, p_dev_policy text, p_eligibility integer, p_expected_version integer, p_actor uuid, p_reason text); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.publish_checkout_grace_policy(p_tenant uuid, p_site uuid, p_pkg_rev uuid, p_duration integer, p_down integer, p_up integer, p_quota bigint, p_dev_limit integer, p_dev_policy text, p_eligibility integer, p_expected_version integer, p_actor uuid, p_reason text) FROM PUBLIC;
GRANT ALL ON FUNCTION iam_v2.publish_checkout_grace_policy(p_tenant uuid, p_site uuid, p_pkg_rev uuid, p_duration integer, p_down integer, p_up integer, p_quota bigint, p_dev_limit integer, p_dev_policy text, p_eligibility integer, p_expected_version integer, p_actor uuid, p_reason text) TO svc_edged;


--
-- Name: FUNCTION rebind_session_entitlement(p_session uuid, p_ent uuid, p_at timestamp with time zone); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.rebind_session_entitlement(p_session uuid, p_ent uuid, p_at timestamp with time zone) FROM PUBLIC;
GRANT ALL ON FUNCTION iam_v2.rebind_session_entitlement(p_session uuid, p_ent uuid, p_at timestamp with time zone) TO svc_acctd;


--
-- Name: FUNCTION record_alert_action(p_tenant uuid, p_site uuid, p_audit uuid, p_action text, p_actor uuid, p_reason text, p_expected_state text); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.record_alert_action(p_tenant uuid, p_site uuid, p_audit uuid, p_action text, p_actor uuid, p_reason text, p_expected_state text) FROM PUBLIC;


--
-- Name: FUNCTION record_auth_context_offer(p_tenant uuid, p_site uuid, p_auth_context uuid, p_package_revision uuid, p_tier integer, p_evidence_version bigint, p_expires_at timestamp with time zone); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.record_auth_context_offer(p_tenant uuid, p_site uuid, p_auth_context uuid, p_package_revision uuid, p_tier integer, p_evidence_version bigint, p_expires_at timestamp with time zone) FROM PUBLIC;
GRANT ALL ON FUNCTION iam_v2.record_auth_context_offer(p_tenant uuid, p_site uuid, p_auth_context uuid, p_package_revision uuid, p_tier integer, p_evidence_version bigint, p_expires_at timestamp with time zone) TO svc_netd;


--
-- Name: FUNCTION record_posting_review_action(p_posting uuid, p_action text, p_actor uuid, p_reason text, p_evidence jsonb, p_expected_version integer, p_reversal_amount bigint); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.record_posting_review_action(p_posting uuid, p_action text, p_actor uuid, p_reason text, p_evidence jsonb, p_expected_version integer, p_reversal_amount bigint) FROM PUBLIC;
GRANT ALL ON FUNCTION iam_v2.record_posting_review_action(p_posting uuid, p_action text, p_actor uuid, p_reason text, p_evidence jsonb, p_expected_version integer, p_reversal_amount bigint) TO sc_financial_operator;


--
-- Name: FUNCTION register_class_origin(p_tenant uuid, p_site uuid, p_session uuid, p_source_device uuid, p_bridge text, p_class_minor integer, p_epoch bigint, p_origin_up bigint, p_origin_down bigint, p_created_at timestamp with time zone); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.register_class_origin(p_tenant uuid, p_site uuid, p_session uuid, p_source_device uuid, p_bridge text, p_class_minor integer, p_epoch bigint, p_origin_up bigint, p_origin_down bigint, p_created_at timestamp with time zone) FROM PUBLIC;
GRANT ALL ON FUNCTION iam_v2.register_class_origin(p_tenant uuid, p_site uuid, p_session uuid, p_source_device uuid, p_bridge text, p_class_minor integer, p_epoch bigint, p_origin_up bigint, p_origin_down bigint, p_created_at timestamp with time zone) TO svc_netd;
GRANT ALL ON FUNCTION iam_v2.register_class_origin(p_tenant uuid, p_site uuid, p_session uuid, p_source_device uuid, p_bridge text, p_class_minor integer, p_epoch bigint, p_origin_up bigint, p_origin_down bigint, p_created_at timestamp with time zone) TO svc_acctd;


--
-- Name: FUNCTION selectable_grace_packages(p_tenant uuid, p_site uuid); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.selectable_grace_packages(p_tenant uuid, p_site uuid) FROM PUBLIC;


--
-- Name: FUNCTION supersede_entitlement_transition(p_target uuid, p_to text, p_at timestamp with time zone, p_reason text); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.supersede_entitlement_transition(p_target uuid, p_to text, p_at timestamp with time zone, p_reason text) FROM PUBLIC;


--
-- Name: FUNCTION terminate_entitlement_at_boundary(p_ent uuid, p_at timestamp with time zone, p_reason text); Type: ACL; Schema: iam_v2; Owner: -
--

REVOKE ALL ON FUNCTION iam_v2.terminate_entitlement_at_boundary(p_ent uuid, p_at timestamp with time zone, p_reason text) FROM PUBLIC;
GRANT ALL ON FUNCTION iam_v2.terminate_entitlement_at_boundary(p_ent uuid, p_at timestamp with time zone, p_reason text) TO svc_acctd;


--
-- Name: TABLE accounting_checkpoints; Type: ACL; Schema: iam_v2; Owner: -
--

GRANT SELECT,INSERT,UPDATE ON TABLE iam_v2.accounting_checkpoints TO svc_acctd;


--
-- Name: TABLE accounting_records; Type: ACL; Schema: iam_v2; Owner: -
--

GRANT SELECT,INSERT,UPDATE ON TABLE iam_v2.accounting_records TO svc_acctd;


--
-- Name: TABLE active_operational_alerts; Type: ACL; Schema: iam_v2; Owner: -
--

GRANT SELECT ON TABLE iam_v2.active_operational_alerts TO svc_edged;


--
-- Name: TABLE appliance_product_settings; Type: ACL; Schema: iam_v2; Owner: -
--

GRANT SELECT ON TABLE iam_v2.appliance_product_settings TO svc_edged;


--
-- Name: TABLE auth_contexts; Type: ACL; Schema: iam_v2; Owner: -
--

GRANT SELECT ON TABLE iam_v2.auth_contexts TO sc_payment_runtime;
GRANT SELECT,INSERT,UPDATE ON TABLE iam_v2.auth_contexts TO svc_scd;


--
-- Name: TABLE auth_resolutions; Type: ACL; Schema: iam_v2; Owner: -
--

GRANT SELECT ON TABLE iam_v2.auth_resolutions TO svc_edged;


--
-- Name: TABLE delayed_accounting_records; Type: ACL; Schema: iam_v2; Owner: -
--

GRANT SELECT,INSERT,UPDATE ON TABLE iam_v2.delayed_accounting_records TO svc_acctd;


--
-- Name: TABLE device_network_appearances; Type: ACL; Schema: iam_v2; Owner: -
--

GRANT SELECT,INSERT,UPDATE ON TABLE iam_v2.device_network_appearances TO svc_scd;


--
-- Name: TABLE devices; Type: ACL; Schema: iam_v2; Owner: -
--

GRANT SELECT,INSERT,UPDATE ON TABLE iam_v2.devices TO svc_scd;
GRANT SELECT ON TABLE iam_v2.devices TO svc_netd;
GRANT SELECT ON TABLE iam_v2.devices TO svc_acctd;


--
-- Name: TABLE entitlement_boundary_watermarks; Type: ACL; Schema: iam_v2; Owner: -
--

GRANT SELECT,INSERT,UPDATE ON TABLE iam_v2.entitlement_boundary_watermarks TO svc_acctd;


--
-- Name: TABLE entitlement_devices; Type: ACL; Schema: iam_v2; Owner: -
--

GRANT SELECT ON TABLE iam_v2.entitlement_devices TO svc_edged;


--
-- Name: TABLE entitlement_state_transitions; Type: ACL; Schema: iam_v2; Owner: -
--

GRANT SELECT ON TABLE iam_v2.entitlement_state_transitions TO sc_payment_runtime;


--
-- Name: TABLE entitlement_termination_evidence; Type: ACL; Schema: iam_v2; Owner: -
--

GRANT SELECT ON TABLE iam_v2.entitlement_termination_evidence TO svc_edged;


--
-- Name: TABLE entitlement_transfers; Type: ACL; Schema: iam_v2; Owner: -
--

GRANT SELECT ON TABLE iam_v2.entitlement_transfers TO svc_edged;


--
-- Name: TABLE entitlements; Type: ACL; Schema: iam_v2; Owner: -
--

GRANT SELECT ON TABLE iam_v2.entitlements TO sc_payment_runtime;
GRANT SELECT,INSERT,UPDATE ON TABLE iam_v2.entitlements TO svc_scd;
GRANT SELECT ON TABLE iam_v2.entitlements TO svc_acctd;
GRANT SELECT ON TABLE iam_v2.entitlements TO svc_edged;


--
-- Name: TABLE financial_epochs; Type: ACL; Schema: iam_v2; Owner: -
--

GRANT SELECT ON TABLE iam_v2.financial_epochs TO sc_financial_operator;


--
-- Name: TABLE financial_recovery_holds; Type: ACL; Schema: iam_v2; Owner: -
--

GRANT SELECT ON TABLE iam_v2.financial_recovery_holds TO sc_financial_operator;


--
-- Name: TABLE financial_restore_events; Type: ACL; Schema: iam_v2; Owner: -
--

GRANT SELECT ON TABLE iam_v2.financial_restore_events TO sc_financial_operator;


--
-- Name: TABLE folios; Type: ACL; Schema: iam_v2; Owner: -
--

GRANT SELECT ON TABLE iam_v2.folios TO svc_edged;


--
-- Name: TABLE guest_access_accounts; Type: ACL; Schema: iam_v2; Owner: -
--

GRANT SELECT ON TABLE iam_v2.guest_access_accounts TO svc_scd;
GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE iam_v2.guest_access_accounts TO svc_edged;


--
-- Name: TABLE guest_network_pms_map; Type: ACL; Schema: iam_v2; Owner: -
--

GRANT SELECT ON TABLE iam_v2.guest_network_pms_map TO svc_edged;


--
-- Name: TABLE guest_principal_identities; Type: ACL; Schema: iam_v2; Owner: -
--

GRANT SELECT,INSERT ON TABLE iam_v2.guest_principal_identities TO svc_scd;


--
-- Name: TABLE guest_principals; Type: ACL; Schema: iam_v2; Owner: -
--

GRANT SELECT,INSERT ON TABLE iam_v2.guest_principals TO svc_scd;


--
-- Name: TABLE internet_package_revisions; Type: ACL; Schema: iam_v2; Owner: -
--

GRANT SELECT ON TABLE iam_v2.internet_package_revisions TO sc_payment_runtime;
GRANT SELECT ON TABLE iam_v2.internet_package_revisions TO svc_scd;
GRANT SELECT ON TABLE iam_v2.internet_package_revisions TO svc_acctd;
GRANT SELECT,INSERT ON TABLE iam_v2.internet_package_revisions TO svc_edged;


--
-- Name: TABLE internet_packages; Type: ACL; Schema: iam_v2; Owner: -
--

GRANT SELECT ON TABLE iam_v2.internet_packages TO svc_scd;
GRANT SELECT ON TABLE iam_v2.internet_packages TO svc_acctd;
GRANT SELECT,INSERT,UPDATE ON TABLE iam_v2.internet_packages TO svc_edged;


--
-- Name: TABLE offer_quotes; Type: ACL; Schema: iam_v2; Owner: -
--

GRANT SELECT ON TABLE iam_v2.offer_quotes TO sc_payment_runtime;
GRANT SELECT,INSERT,UPDATE ON TABLE iam_v2.offer_quotes TO svc_scd;
GRANT SELECT ON TABLE iam_v2.offer_quotes TO svc_edged;


--
-- Name: TABLE package_eligibility_rules; Type: ACL; Schema: iam_v2; Owner: -
--

GRANT SELECT ON TABLE iam_v2.package_eligibility_rules TO svc_scd;
GRANT INSERT ON TABLE iam_v2.package_eligibility_rules TO svc_edged;


--
-- Name: TABLE package_grant_tiers; Type: ACL; Schema: iam_v2; Owner: -
--

GRANT SELECT ON TABLE iam_v2.package_grant_tiers TO svc_scd;
GRANT INSERT ON TABLE iam_v2.package_grant_tiers TO svc_edged;


--
-- Name: TABLE payment_provider_accounts; Type: ACL; Schema: iam_v2; Owner: -
--

GRANT SELECT ON TABLE iam_v2.payment_provider_accounts TO sc_payment_runtime;


--
-- Name: TABLE payment_transaction_events; Type: ACL; Schema: iam_v2; Owner: -
--

GRANT SELECT ON TABLE iam_v2.payment_transaction_events TO sc_payment_runtime;
GRANT SELECT ON TABLE iam_v2.payment_transaction_events TO sc_financial_operator;


--
-- Name: TABLE payment_transactions; Type: ACL; Schema: iam_v2; Owner: -
--

GRANT SELECT,INSERT ON TABLE iam_v2.payment_transactions TO sc_payment_runtime;
GRANT SELECT ON TABLE iam_v2.payment_transactions TO sc_financial_operator;
GRANT SELECT ON TABLE iam_v2.payment_transactions TO sc_payment_outcome;


--
-- Name: TABLE pms_interface_revisions; Type: ACL; Schema: iam_v2; Owner: -
--

GRANT SELECT,INSERT ON TABLE iam_v2.pms_interface_revisions TO svc_edged;


--
-- Name: TABLE pms_interface_runtime; Type: ACL; Schema: iam_v2; Owner: -
--

GRANT SELECT ON TABLE iam_v2.pms_interface_runtime TO svc_edged;


--
-- Name: TABLE pms_interface_secret_generations; Type: ACL; Schema: iam_v2; Owner: -
--

GRANT SELECT,INSERT,UPDATE ON TABLE iam_v2.pms_interface_secret_generations TO svc_edged;


--
-- Name: TABLE pms_interfaces; Type: ACL; Schema: iam_v2; Owner: -
--

GRANT SELECT ON TABLE iam_v2.pms_interfaces TO svc_acctd;
GRANT SELECT,INSERT,UPDATE ON TABLE iam_v2.pms_interfaces TO svc_edged;


--
-- Name: TABLE pms_postings; Type: ACL; Schema: iam_v2; Owner: -
--

GRANT SELECT ON TABLE iam_v2.pms_postings TO sc_financial_operator;
GRANT SELECT ON TABLE iam_v2.pms_postings TO svc_edged;


--
-- Name: TABLE pms_source_conflicts; Type: ACL; Schema: iam_v2; Owner: -
--

GRANT SELECT ON TABLE iam_v2.pms_source_conflicts TO svc_edged;


--
-- Name: TABLE post_stay_profiles; Type: ACL; Schema: iam_v2; Owner: -
--

GRANT SELECT ON TABLE iam_v2.post_stay_profiles TO svc_edged;


--
-- Name: TABLE posting_attempts; Type: ACL; Schema: iam_v2; Owner: -
--

GRANT SELECT ON TABLE iam_v2.posting_attempts TO svc_edged;


--
-- Name: TABLE posting_outbox; Type: ACL; Schema: iam_v2; Owner: -
--

GRANT SELECT ON TABLE iam_v2.posting_outbox TO sc_financial_operator;


--
-- Name: TABLE posting_review_state; Type: ACL; Schema: iam_v2; Owner: -
--

GRANT SELECT ON TABLE iam_v2.posting_review_state TO sc_financial_operator;
GRANT SELECT ON TABLE iam_v2.posting_review_state TO svc_edged;


--
-- Name: TABLE posting_execution_state; Type: ACL; Schema: iam_v2; Owner: -
--

GRANT SELECT ON TABLE iam_v2.posting_execution_state TO svc_edged;


--
-- Name: TABLE posting_review_actions; Type: ACL; Schema: iam_v2; Owner: -
--

GRANT SELECT ON TABLE iam_v2.posting_review_actions TO sc_financial_operator;
GRANT SELECT ON TABLE iam_v2.posting_review_actions TO svc_edged;


--
-- Name: TABLE purchases; Type: ACL; Schema: iam_v2; Owner: -
--

GRANT SELECT ON TABLE iam_v2.purchases TO sc_payment_runtime;
GRANT SELECT ON TABLE iam_v2.purchases TO sc_financial_operator;
GRANT SELECT,INSERT ON TABLE iam_v2.purchases TO svc_scd;
GRANT SELECT ON TABLE iam_v2.purchases TO svc_acctd;
GRANT SELECT ON TABLE iam_v2.purchases TO svc_edged;


--
-- Name: TABLE service_plan_revisions; Type: ACL; Schema: iam_v2; Owner: -
--

GRANT SELECT ON TABLE iam_v2.service_plan_revisions TO sc_payment_runtime;
GRANT SELECT ON TABLE iam_v2.service_plan_revisions TO svc_scd;
GRANT SELECT ON TABLE iam_v2.service_plan_revisions TO svc_acctd;
GRANT SELECT,INSERT ON TABLE iam_v2.service_plan_revisions TO svc_edged;


--
-- Name: TABLE service_plans; Type: ACL; Schema: iam_v2; Owner: -
--

GRANT SELECT ON TABLE iam_v2.service_plans TO svc_acctd;
GRANT SELECT,INSERT,UPDATE ON TABLE iam_v2.service_plans TO svc_edged;


--
-- Name: TABLE session_entitlement_bindings; Type: ACL; Schema: iam_v2; Owner: -
--

GRANT SELECT,INSERT,UPDATE ON TABLE iam_v2.session_entitlement_bindings TO svc_acctd;


--
-- Name: TABLE sessions; Type: ACL; Schema: iam_v2; Owner: -
--

GRANT SELECT,INSERT,UPDATE ON TABLE iam_v2.sessions TO svc_scd;
GRANT SELECT ON TABLE iam_v2.sessions TO svc_netd;
GRANT SELECT ON TABLE iam_v2.sessions TO svc_acctd;
GRANT SELECT,UPDATE ON TABLE iam_v2.sessions TO svc_edged;


--
-- Name: TABLE settlements; Type: ACL; Schema: iam_v2; Owner: -
--

GRANT SELECT ON TABLE iam_v2.settlements TO sc_payment_runtime;
GRANT SELECT ON TABLE iam_v2.settlements TO sc_financial_operator;
GRANT SELECT ON TABLE iam_v2.settlements TO sc_payment_outcome;
GRANT SELECT,INSERT ON TABLE iam_v2.settlements TO svc_scd;
GRANT SELECT ON TABLE iam_v2.settlements TO svc_edged;


--
-- Name: TABLE site_checkout_grace_config; Type: ACL; Schema: iam_v2; Owner: -
--

GRANT SELECT ON TABLE iam_v2.site_checkout_grace_config TO svc_edged;


--
-- Name: TABLE stay_events; Type: ACL; Schema: iam_v2; Owner: -
--

GRANT SELECT ON TABLE iam_v2.stay_events TO svc_edged;


--
-- Name: TABLE stay_folios; Type: ACL; Schema: iam_v2; Owner: -
--

GRANT SELECT ON TABLE iam_v2.stay_folios TO svc_edged;


--
-- Name: TABLE stay_guests; Type: ACL; Schema: iam_v2; Owner: -
--

GRANT SELECT ON TABLE iam_v2.stay_guests TO svc_edged;


--
-- Name: TABLE stays; Type: ACL; Schema: iam_v2; Owner: -
--

GRANT SELECT ON TABLE iam_v2.stays TO svc_acctd;
GRANT SELECT ON TABLE iam_v2.stays TO svc_edged;


--
-- Name: TABLE v_financial_payments; Type: ACL; Schema: iam_v2; Owner: -
--

GRANT SELECT ON TABLE iam_v2.v_financial_payments TO sc_financial_readonly;
GRANT SELECT ON TABLE iam_v2.v_financial_payments TO sc_financial_operator;
GRANT SELECT ON TABLE iam_v2.v_financial_payments TO svc_edged;


--
-- Name: TABLE v_financial_recovery; Type: ACL; Schema: iam_v2; Owner: -
--

GRANT SELECT ON TABLE iam_v2.v_financial_recovery TO sc_financial_readonly;
GRANT SELECT ON TABLE iam_v2.v_financial_recovery TO sc_financial_operator;
GRANT SELECT ON TABLE iam_v2.v_financial_recovery TO sc_payment_runtime;


--
-- Name: TABLE v_financial_review_queue; Type: ACL; Schema: iam_v2; Owner: -
--

GRANT SELECT ON TABLE iam_v2.v_financial_review_queue TO sc_financial_readonly;


--
-- Name: TABLE v_financial_settlements; Type: ACL; Schema: iam_v2; Owner: -
--

GRANT SELECT ON TABLE iam_v2.v_financial_settlements TO sc_financial_readonly;
GRANT SELECT ON TABLE iam_v2.v_financial_settlements TO sc_financial_operator;
GRANT SELECT ON TABLE iam_v2.v_financial_settlements TO svc_edged;


--
-- Name: TABLE v_zero_attempt_recovery_queue; Type: ACL; Schema: iam_v2; Owner: -
--

GRANT SELECT ON TABLE iam_v2.v_zero_attempt_recovery_queue TO sc_financial_operator;
GRANT SELECT ON TABLE iam_v2.v_zero_attempt_recovery_queue TO svc_edged;


--
-- Name: TABLE voucher_code_key_generations; Type: ACL; Schema: iam_v2; Owner: -
--

GRANT SELECT,INSERT ON TABLE iam_v2.voucher_code_key_generations TO svc_scd;


--
-- Name: TABLE vouchers; Type: ACL; Schema: iam_v2; Owner: -
--

GRANT SELECT,INSERT ON TABLE iam_v2.vouchers TO svc_scd;


--
-- Name: TABLE accounting_records; Type: ACL; Schema: public; Owner: -
--

GRANT SELECT,DELETE ON TABLE public.accounting_records TO svc_scd;
GRANT INSERT ON TABLE public.accounting_records TO svc_acctd;


--
-- Name: TABLE appliance_boot_convergence; Type: ACL; Schema: public; Owner: -
--

GRANT SELECT,INSERT,UPDATE ON TABLE public.appliance_boot_convergence TO svc_edged;


--
-- Name: TABLE appliance_recovery_events; Type: ACL; Schema: public; Owner: -
--

GRANT SELECT,INSERT,DELETE ON TABLE public.appliance_recovery_events TO svc_edged;


--
-- Name: SEQUENCE appliance_recovery_events_id_seq; Type: ACL; Schema: public; Owner: -
--

GRANT SELECT,USAGE ON SEQUENCE public.appliance_recovery_events_id_seq TO svc_edged;


--
-- Name: TABLE appliance_service_health; Type: ACL; Schema: public; Owner: -
--

GRANT SELECT,INSERT,UPDATE ON TABLE public.appliance_service_health TO svc_edged;
GRANT SELECT,INSERT,UPDATE ON TABLE public.appliance_service_health TO svc_scd;
GRANT SELECT,INSERT,UPDATE ON TABLE public.appliance_service_health TO svc_acctd;
GRANT SELECT,INSERT,UPDATE ON TABLE public.appliance_service_health TO svc_netd;


--
-- Name: TABLE appliances; Type: ACL; Schema: public; Owner: -
--

GRANT SELECT,INSERT,UPDATE ON TABLE public.appliances TO svc_scd;


--
-- Name: TABLE audit_log; Type: ACL; Schema: public; Owner: -
--

GRANT INSERT ON TABLE public.audit_log TO svc_scd;
GRANT INSERT ON TABLE public.audit_log TO svc_edged;


--
-- Name: TABLE auth_otps; Type: ACL; Schema: public; Owner: -
--

GRANT SELECT,INSERT,UPDATE ON TABLE public.auth_otps TO svc_scd;


--
-- Name: TABLE auth_throttle_buckets; Type: ACL; Schema: public; Owner: -
--

GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE public.auth_throttle_buckets TO svc_scd;


--
-- Name: TABLE backup_records; Type: ACL; Schema: public; Owner: -
--

GRANT SELECT ON TABLE public.backup_records TO svc_edged;


--
-- Name: TABLE dhcp_pools; Type: ACL; Schema: public; Owner: -
--

GRANT SELECT,INSERT,DELETE ON TABLE public.dhcp_pools TO svc_edged;
GRANT SELECT ON TABLE public.dhcp_pools TO svc_netd;


--
-- Name: TABLE dhcp_reservations; Type: ACL; Schema: public; Owner: -
--

GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE public.dhcp_reservations TO svc_edged;
GRANT SELECT ON TABLE public.dhcp_reservations TO svc_netd;


--
-- Name: TABLE edge_executed_commands; Type: ACL; Schema: public; Owner: -
--

GRANT SELECT,INSERT ON TABLE public.edge_executed_commands TO svc_scd;


--
-- Name: TABLE edge_installed_updates; Type: ACL; Schema: public; Owner: -
--

GRANT SELECT,INSERT ON TABLE public.edge_installed_updates TO svc_scd;


--
-- Name: TABLE edge_offline_packages; Type: ACL; Schema: public; Owner: -
--

GRANT SELECT,INSERT,UPDATE ON TABLE public.edge_offline_packages TO svc_scd;


--
-- Name: TABLE guest_networks; Type: ACL; Schema: public; Owner: -
--

GRANT REFERENCES ON TABLE public.guest_networks TO iam_v2_owner;
GRANT SELECT,UPDATE ON TABLE public.guest_networks TO svc_scd;
GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE public.guest_networks TO svc_edged;
GRANT SELECT ON TABLE public.guest_networks TO svc_netd;
GRANT SELECT ON TABLE public.guest_networks TO svc_acctd;


--
-- Name: TABLE network_apply_events; Type: ACL; Schema: public; Owner: -
--

GRANT SELECT,INSERT ON TABLE public.network_apply_events TO svc_edged;
GRANT SELECT,INSERT ON TABLE public.network_apply_events TO svc_netd;


--
-- Name: TABLE network_config_revisions; Type: ACL; Schema: public; Owner: -
--

GRANT SELECT,INSERT,UPDATE ON TABLE public.network_config_revisions TO svc_edged;
GRANT SELECT,INSERT,UPDATE ON TABLE public.network_config_revisions TO svc_netd;


--
-- Name: SEQUENCE network_config_revisions_seq_seq; Type: ACL; Schema: public; Owner: -
--

GRANT SELECT,USAGE ON SEQUENCE public.network_config_revisions_seq_seq TO svc_netd;
GRANT SELECT,USAGE ON SEQUENCE public.network_config_revisions_seq_seq TO svc_edged;


--
-- Name: TABLE network_health_checks; Type: ACL; Schema: public; Owner: -
--

GRANT SELECT,INSERT ON TABLE public.network_health_checks TO svc_edged;
GRANT SELECT,INSERT ON TABLE public.network_health_checks TO svc_netd;


--
-- Name: TABLE network_interfaces; Type: ACL; Schema: public; Owner: -
--

GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE public.network_interfaces TO svc_edged;
GRANT SELECT,INSERT ON TABLE public.network_interfaces TO svc_netd;


--
-- Name: TABLE notification_providers; Type: ACL; Schema: public; Owner: -
--

GRANT SELECT,DELETE,UPDATE ON TABLE public.notification_providers TO svc_scd;
GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE public.notification_providers TO svc_edged;


--
-- Name: TABLE operator_roles; Type: ACL; Schema: public; Owner: -
--

GRANT SELECT,DELETE ON TABLE public.operator_roles TO svc_scd;
GRANT SELECT,INSERT,DELETE ON TABLE public.operator_roles TO svc_edged;


--
-- Name: TABLE operators; Type: ACL; Schema: public; Owner: -
--

GRANT SELECT,DELETE ON TABLE public.operators TO svc_scd;
GRANT SELECT,INSERT,UPDATE ON TABLE public.operators TO svc_edged;
GRANT SELECT ON TABLE public.operators TO iam_v2_owner;


--
-- Name: TABLE otp_hmac_key_generations; Type: ACL; Schema: public; Owner: -
--

GRANT SELECT ON TABLE public.otp_hmac_key_generations TO svc_scd;


--
-- Name: TABLE pms_attempts; Type: ACL; Schema: public; Owner: -
--

GRANT SELECT,INSERT,DELETE ON TABLE public.pms_attempts TO svc_scd;


--
-- Name: TABLE pms_providers; Type: ACL; Schema: public; Owner: -
--

GRANT SELECT,DELETE,UPDATE ON TABLE public.pms_providers TO svc_scd;
GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE public.pms_providers TO svc_edged;


--
-- Name: TABLE sites; Type: ACL; Schema: public; Owner: -
--

GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE public.sites TO svc_scd;


--
-- Name: TABLE social_oauth_providers; Type: ACL; Schema: public; Owner: -
--

GRANT SELECT,DELETE ON TABLE public.social_oauth_providers TO svc_scd;
GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE public.social_oauth_providers TO svc_edged;


--
-- Name: TABLE social_oauth_states; Type: ACL; Schema: public; Owner: -
--

GRANT SELECT,INSERT,UPDATE ON TABLE public.social_oauth_states TO svc_scd;


--
-- Name: TABLE stripe_accounts; Type: ACL; Schema: public; Owner: -
--

GRANT SELECT,DELETE ON TABLE public.stripe_accounts TO svc_scd;
GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE public.stripe_accounts TO svc_edged;


--
-- Name: TABLE stripe_events; Type: ACL; Schema: public; Owner: -
--

GRANT SELECT,DELETE ON TABLE public.stripe_events TO svc_scd;


--
-- Name: TABLE sync_checkpoints; Type: ACL; Schema: public; Owner: -
--

GRANT SELECT,INSERT,UPDATE ON TABLE public.sync_checkpoints TO svc_scd;
GRANT SELECT,INSERT ON TABLE public.sync_checkpoints TO svc_edged;


--
-- Name: TABLE sync_outbox; Type: ACL; Schema: public; Owner: -
--

GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE public.sync_outbox TO svc_scd;
GRANT SELECT,INSERT,UPDATE ON TABLE public.sync_outbox TO svc_edged;


--
-- Name: SEQUENCE sync_outbox_seq_seq; Type: ACL; Schema: public; Owner: -
--

GRANT SELECT,USAGE ON SEQUENCE public.sync_outbox_seq_seq TO svc_edged;
GRANT SELECT,USAGE ON SEQUENCE public.sync_outbox_seq_seq TO svc_scd;


--
-- Name: TABLE system_network_audit; Type: ACL; Schema: public; Owner: -
--

GRANT INSERT ON TABLE public.system_network_audit TO svc_netd;


--
-- Name: TABLE tenant_effective_limits; Type: ACL; Schema: public; Owner: -
--

GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE public.tenant_effective_limits TO svc_scd;
GRANT SELECT,INSERT,UPDATE ON TABLE public.tenant_effective_limits TO svc_edged;


--
-- Name: TABLE tenants; Type: ACL; Schema: public; Owner: -
--

GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE public.tenants TO svc_scd;
GRANT SELECT,UPDATE ON TABLE public.tenants TO svc_edged;


--
-- Name: TABLE walled_garden_rules; Type: ACL; Schema: public; Owner: -
--

GRANT SELECT,DELETE ON TABLE public.walled_garden_rules TO svc_scd;
GRANT SELECT,INSERT,DELETE ON TABLE public.walled_garden_rules TO svc_edged;


--
-- PostgreSQL database dump complete
--


-- TimescaleDB hypertable registration. A --schema-only dump does not carry it, and without this the
-- tables exist, match every catalog comparison, and refuse every INSERT.
SELECT public.create_hypertable('public.accounting_records', 'ts', chunk_time_interval => INTERVAL '1 day', if_not_exists => TRUE, migrate_data => TRUE);
SELECT public.create_hypertable('public.audit_log', 'ts', chunk_time_interval => INTERVAL '7 days', if_not_exists => TRUE, migrate_data => TRUE);
