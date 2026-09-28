-- A ROOM CHARGE IS POSTED TO THE ROOM, AND ONLY THE PMS SAYS IT WAS POSTED.
--
-- PMS Room Charge (docs/architecture/ONEGATE_MODULES_AND_ACQUISITION.md, section 6.4) reuses the accepted Phase-4
-- posting machinery unchanged -- the posting ledger, the outbox and its lanes, P# allocation, the attempt
-- lifecycle, every insert-time gate (folio UNSET, IN_HOUSE, currency three-way, freshness, FIAS exponent) and
-- the audited manual review. What was missing, and is added here:
--
--   1. PER-INTERFACE FINANCIAL ONBOARDING. A folio identity strategy is a financial determination made from
--      observed PMS behaviour, never a form choice (the authoring form still accepts only UNSET). Onboarding is
--      its own audited act: site admin, password step-up, an explicit attestation and a reason. It publishes a
--      new interface revision carrying the concrete strategy and the financial base currency, and records an
--      append-only approval. Re-authoring the connection resets the strategy to UNSET (fail closed) and needs
--      onboarding again.
--   2. THE PACKAGE -> POSTING CODE MAPPING WRITER. A room-charge package names, per PMS interface, the posting
--      code (FIAS CT) its charge is booked under. Bounded and wire-safe by CHECK.
--   3. ROOM-CHARGE POSTING CREATION re-derives its own evidence from the purchase -- stay, the stay's default
--      posting folio, the pinned mapping, the amount -- so the caller supplies only the settlement. The insert
--      triggers are the gate; the settlement moves REQUIRED -> IN_PROGRESS in the same transaction.
--   4. THE SETTLEMENT FOLLOWS THE PMS. PA=OK -> SETTLED and the paid grant; any other PA -> FAILED; UNKNOWN ->
--      MANUAL_REVIEW. Nothing is granted because a posting was sent.
--   5. THE ACCEPTED REVIEW ACTIONS TAKE EFFECT: CONFIRM_POSTED settles and grants (the state machine now accepts
--      that reviewed evidence), CONFIRM_NOT_POSTED_ABANDON fails the settlement, CONFIRM_NOT_POSTED_RETRY requeues
--      the posting for exactly the one attempt the review authorised. UNKNOWN is never retried automatically.
--   6. A DEDICATED POSTING RUNTIME ROLE (sc_posting_runtime; login svc_posting via Gate-P) for the posting worker.
--
-- Real posting stays impossible without the transmit ceiling (STAYCONNECT_PHASE4_PMS_TRANSMIT), which requires a
-- separate Product-Owner authorisation.

BEGIN;

-- ---------------------------------------------------------------------------------------------------------
-- 1. Financial onboarding.
-- ---------------------------------------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS iam_v2.pms_financial_onboardings (
  id                      uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id               uuid NOT NULL,
  site_id                 uuid NOT NULL,
  pms_interface_id        uuid NOT NULL,
  revision_id             uuid NOT NULL UNIQUE,
  previous_revision_id    uuid,
  folio_identity_strategy text NOT NULL CHECK (folio_identity_strategy IN ('GLOBALLY_UNIQUE','UNIQUE_PER_STAY','REUSED_SEQUENTIAL')),
  currency                char(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  currency_exponent       smallint NOT NULL CHECK (currency_exponent BETWEEN 0 AND 4),
  attestation             text NOT NULL CHECK (length(btrim(attestation)) BETWEEN 20 AND 2000),
  reason                  text NOT NULL CHECK (length(btrim(reason)) BETWEEN 4 AND 500),
  approved_by             uuid NOT NULL,
  approved_at             timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS pms_financial_onboardings_iface
  ON iam_v2.pms_financial_onboardings (tenant_id, site_id, pms_interface_id, approved_at DESC);
CREATE OR REPLACE FUNCTION iam_v2.pms_financial_onboardings_append_only() RETURNS trigger
  LANGUAGE plpgsql AS $fn$
BEGIN
  RAISE EXCEPTION 'iam_v2.pms_financial_onboardings is append-only: % refused', TG_OP USING ERRCODE = 'restrict_violation';
END $fn$;
REVOKE ALL ON FUNCTION iam_v2.pms_financial_onboardings_append_only() FROM PUBLIC;
DROP TRIGGER IF EXISTS pms_financial_onboardings_append_only ON iam_v2.pms_financial_onboardings;
CREATE TRIGGER pms_financial_onboardings_append_only BEFORE UPDATE OR DELETE ON iam_v2.pms_financial_onboardings
  FOR EACH ROW EXECUTE FUNCTION iam_v2.pms_financial_onboardings_append_only();

CREATE OR REPLACE FUNCTION iam_v2.pms_interface_financial_onboard(
    p_tenant uuid, p_site uuid, p_iface uuid, p_expected_revision uuid, p_strategy text, p_currency text,
    p_exponent smallint, p_attestation text, p_reason text, p_operator uuid)
  RETURNS uuid
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, public, pg_temp AS $fn$
DECLARE v_current uuid; v_kind text; v_new uuid; v_status text;
BEGIN
  IF p_strategy NOT IN ('GLOBALLY_UNIQUE','UNIQUE_PER_STAY','REUSED_SEQUENTIAL') THEN
    RAISE EXCEPTION 'ONBOARDING_STRATEGY: a concrete folio identity strategy is required' USING ERRCODE = 'check_violation';
  END IF;
  IF p_currency IS NULL OR p_currency !~ '^[A-Z]{3}$' OR p_exponent IS NULL OR p_exponent NOT BETWEEN 0 AND 4 THEN
    RAISE EXCEPTION 'ONBOARDING_CURRENCY: a three-letter currency and an exponent 0..4 are required' USING ERRCODE = 'check_violation';
  END IF;
  IF length(btrim(coalesce(p_attestation,''))) < 20 THEN
    RAISE EXCEPTION 'ONBOARDING_ATTESTATION: state what was observed about how this PMS identifies folios' USING ERRCODE = 'check_violation';
  END IF;
  IF length(btrim(coalesce(p_reason,''))) < 4 THEN
    RAISE EXCEPTION 'ONBOARDING_REASON: a reason is required' USING ERRCODE = 'check_violation';
  END IF;
  SELECT status INTO v_status FROM public.operators WHERE id = p_operator AND tenant_id = p_tenant;
  IF v_status IS DISTINCT FROM 'active' THEN
    RAISE EXCEPTION 'ONBOARDING_ACTOR: an active operator of this tenant must approve onboarding' USING ERRCODE = 'check_violation';
  END IF;
  SELECT current_revision_id, connector_kind INTO v_current, v_kind FROM iam_v2.pms_interfaces
   WHERE tenant_id = p_tenant AND site_id = p_site AND id = p_iface FOR UPDATE;
  IF v_current IS NULL THEN
    RAISE EXCEPTION 'ONBOARDING_INTERFACE: no published revision for this interface' USING ERRCODE = 'no_data_found';
  END IF;
  IF v_current IS DISTINCT FROM p_expected_revision THEN
    RAISE EXCEPTION 'ONBOARDING_REVISION_CONFLICT: the interface changed while this form was open' USING ERRCODE = 'serialization_failure';
  END IF;
  IF v_kind <> 'protel-fias' THEN
    RAISE EXCEPTION 'ONBOARDING_CONNECTOR: room charge is supported for FIAS interfaces only' USING ERRCODE = 'check_violation';
  END IF;
  IF p_exponent <> 2 THEN
    RAISE EXCEPTION 'ONBOARDING_EXPONENT: FIAS transmits amounts with exponent 2' USING ERRCODE = 'check_violation';
  END IF;
  INSERT INTO iam_v2.pms_interface_revisions
    (tenant_id, site_id, pms_interface_id, revision_no, source_timezone, folio_identity_strategy, config,
     normalization_version, source_fingerprint, financial_base_currency, financial_base_currency_exponent)
  SELECT r.tenant_id, r.site_id, r.pms_interface_id,
         (SELECT COALESCE(MAX(x.revision_no),0)+1 FROM iam_v2.pms_interface_revisions x
           WHERE x.tenant_id = r.tenant_id AND x.site_id = r.site_id AND x.pms_interface_id = r.pms_interface_id),
         r.source_timezone, p_strategy, r.config, r.normalization_version, r.source_fingerprint, p_currency, p_exponent
    FROM iam_v2.pms_interface_revisions r WHERE r.id = v_current
  RETURNING id INTO v_new;
  UPDATE iam_v2.pms_interfaces SET current_revision_id = v_new
   WHERE tenant_id = p_tenant AND site_id = p_site AND id = p_iface;
  INSERT INTO iam_v2.pms_financial_onboardings
    (tenant_id, site_id, pms_interface_id, revision_id, previous_revision_id, folio_identity_strategy, currency,
     currency_exponent, attestation, reason, approved_by)
  VALUES (p_tenant, p_site, p_iface, v_new, v_current, p_strategy, p_currency, p_exponent, btrim(p_attestation),
          btrim(p_reason), p_operator);
  RETURN v_new;
END $fn$;

-- Is this interface's CURRENT revision financially onboarded (approved, concrete strategy, currency)?
CREATE OR REPLACE FUNCTION iam_v2.pms_interface_financially_ready(p_tenant uuid, p_site uuid, p_iface uuid)
  RETURNS TABLE (ready boolean, reason text, currency char(3), currency_exponent smallint)
  LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE i record; r record;
BEGIN
  SELECT * INTO i FROM iam_v2.pms_interfaces WHERE tenant_id = p_tenant AND site_id = p_site AND id = p_iface;
  IF i.id IS NULL THEN ready := false; reason := 'INTERFACE_UNKNOWN'; RETURN NEXT; RETURN; END IF;
  IF i.connector_kind <> 'protel-fias' THEN ready := false; reason := 'CONNECTOR_NOT_FINANCIAL'; RETURN NEXT; RETURN; END IF;
  IF i.lifecycle_state NOT IN ('ACTIVE','AUTH_DISABLED') THEN ready := false; reason := 'INTERFACE_NOT_ACTIVE'; RETURN NEXT; RETURN; END IF;
  SELECT * INTO r FROM iam_v2.pms_interface_revisions WHERE id = i.current_revision_id;
  currency := r.financial_base_currency; currency_exponent := r.financial_base_currency_exponent;
  IF r.folio_identity_strategy = 'UNSET' OR r.financial_base_currency IS NULL THEN
    ready := false; reason := 'NOT_ONBOARDED'; RETURN NEXT; RETURN;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM iam_v2.pms_financial_onboardings o WHERE o.revision_id = r.id) THEN
    ready := false; reason := 'ONBOARDING_NOT_APPROVED'; RETURN NEXT; RETURN;
  END IF;
  ready := true; reason := NULL; RETURN NEXT;
END $fn$;

-- ---------------------------------------------------------------------------------------------------------
-- 2. The package -> posting code mapping.
-- ---------------------------------------------------------------------------------------------------------
ALTER TABLE iam_v2.package_settlement_mappings DROP CONSTRAINT IF EXISTS psm_posting_code_wire_safe;
ALTER TABLE iam_v2.package_settlement_mappings ADD CONSTRAINT psm_posting_code_wire_safe CHECK (
  length(btrim(posting_code)) BETWEEN 1 AND 20 AND posting_code !~ '[|\x00-\x1f\x7f]'
  AND (tax_rate_bp IS NULL OR tax_rate_bp BETWEEN 0 AND 10000));
CREATE UNIQUE INDEX IF NOT EXISTS psm_one_live_per_interface
  ON iam_v2.package_settlement_mappings (package_revision_id, pms_interface_id) WHERE retired_at IS NULL;

-- ---------------------------------------------------------------------------------------------------------
-- 3. Room-charge posting creation: the caller names the settlement; everything else is re-derived.
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION iam_v2.p4_create_room_charge_posting(p_tenant uuid, p_site uuid, p_settlement uuid)
  RETURNS uuid
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE se record; pu record; ac record; i record; rdy record; v_folio uuid; v_posting uuid;
BEGIN
  SELECT * INTO se FROM iam_v2.settlements WHERE tenant_id = p_tenant AND site_id = p_site AND id = p_settlement FOR UPDATE;
  IF se.id IS NULL OR se.method <> 'PMS_POSTING' OR se.status <> 'REQUIRED' THEN
    RAISE EXCEPTION 'ROOM_CHARGE_SETTLEMENT: not a REQUIRED room-charge settlement' USING ERRCODE = 'check_violation';
  END IF;
  SELECT * INTO pu FROM iam_v2.purchases WHERE id = se.purchase_id FOR UPDATE;
  IF pu.state <> 'AWAITING_SETTLEMENT' OR pu.stay_id IS NULL OR pu.pms_interface_id IS NULL OR pu.settlement_mapping_id IS NULL THEN
    RAISE EXCEPTION 'ROOM_CHARGE_PURCHASE: the purchase does not pin a stay, interface and mapping' USING ERRCODE = 'check_violation';
  END IF;
  SELECT * INTO ac FROM iam_v2.auth_contexts WHERE id = pu.auth_context_id;
  IF ac.method <> 'PMS' OR ac.stay_id IS DISTINCT FROM pu.stay_id THEN
    RAISE EXCEPTION 'ROOM_CHARGE_AUTHENTICATION: a room charge needs a verified Room sign-in for the same stay' USING ERRCODE = 'check_violation';
  END IF;
  SELECT * INTO rdy FROM iam_v2.pms_interface_financially_ready(p_tenant, p_site, pu.pms_interface_id);
  IF NOT rdy.ready THEN
    RAISE EXCEPTION 'ROOM_CHARGE_NOT_READY: %', rdy.reason USING ERRCODE = 'check_violation';
  END IF;
  SELECT * INTO i FROM iam_v2.pms_interfaces WHERE id = pu.pms_interface_id;
  SELECT sf.folio_id INTO v_folio FROM iam_v2.stay_folios sf
   WHERE sf.tenant_id = p_tenant AND sf.site_id = p_site AND sf.pms_interface_id = pu.pms_interface_id
     AND sf.stay_id = pu.stay_id AND sf.is_default_posting_target;
  IF v_folio IS NULL THEN
    RAISE EXCEPTION 'ROOM_CHARGE_NO_FOLIO: the stay has no default posting folio' USING ERRCODE = 'check_violation';
  END IF;
  INSERT INTO iam_v2.pms_postings
    (tenant_id, site_id, pms_interface_id, settlement_id, purchase_id, stay_id, folio_id,
     posting_interface_revision_id, posting_type, amount_minor, currency, currency_exponent, idempotency_key)
  VALUES (p_tenant, p_site, pu.pms_interface_id, se.id, pu.id, pu.stay_id, v_folio,
          i.current_revision_id, 'CHARGE', pu.amount_minor, pu.currency, pu.currency_exponent,
          'room-charge:' || se.id::text)
  RETURNING id INTO v_posting;
  INSERT INTO iam_v2.posting_outbox (tenant_id, site_id, pms_interface_id, posting_id, state)
  VALUES (p_tenant, p_site, pu.pms_interface_id, v_posting, 'QUEUED');
  UPDATE iam_v2.settlements SET status = 'IN_PROGRESS' WHERE id = se.id;
  RETURN v_posting;
END $fn$;

-- ---------------------------------------------------------------------------------------------------------
-- 4. The settlement follows the PMS, and a PA=OK grants.
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION iam_v2.p4_settlement_state_machine() RETURNS trigger
  LANGUAGE plpgsql SET search_path = iam_v2, pg_temp AS $fn$
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
      -- The PMS acknowledged the charge (PA=OK), OR an authorised reviewer recorded CONFIRM_POSTED after
      -- verifying from external evidence that an UNKNOWN attempt did post (the accepted Phase-0 review action).
      IF NOT EXISTS (SELECT 1 FROM iam_v2.pms_postings p
                       JOIN iam_v2.posting_attempts a ON a.internal_posting_id = p.id
                      WHERE p.settlement_id = NEW.id AND p.posting_type = 'CHARGE'
                        AND a.outcome = 'ACKED' AND a.pa_as_status = 'OK')
         AND NOT EXISTS (SELECT 1 FROM iam_v2.pms_postings p
                           JOIN iam_v2.posting_review_state rs ON rs.posting_id = p.id
                          WHERE p.settlement_id = NEW.id AND p.posting_type = 'CHARGE'
                            AND rs.terminal_action = 'CONFIRM_POSTED') THEN
        RAISE EXCEPTION 'SETTLEMENT_NOT_EVIDENCED: PMS_POSTING settles only on a posting the PMS ACKed OK '
                        'or a reviewed CONFIRM_POSTED'
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
END $fn$;


CREATE OR REPLACE FUNCTION iam_v2.p4_posting_settlement_outcome(p_posting uuid)
  RETURNS text
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE p record; se record; a record; v_target text; v_ent uuid;
BEGIN
  SELECT * INTO p FROM iam_v2.pms_postings WHERE id = p_posting AND posting_type = 'CHARGE';
  IF p.id IS NULL THEN RAISE EXCEPTION 'POSTING_UNKNOWN' USING ERRCODE = 'no_data_found'; END IF;
  SELECT * INTO se FROM iam_v2.settlements WHERE id = p.settlement_id FOR UPDATE;
  IF se.method <> 'PMS_POSTING' THEN RETURN 'NOT_A_ROOM_CHARGE'; END IF;
  SELECT * INTO a FROM iam_v2.posting_attempts WHERE internal_posting_id = p.id ORDER BY attempt_no DESC LIMIT 1;
  IF a.id IS NULL THEN RETURN 'NO_ATTEMPT'; END IF;
  IF a.outcome = 'ACKED' AND a.pa_as_status = 'OK' THEN
    v_target := 'SETTLED';
  ELSIF a.outcome = 'ACKED' THEN
    v_target := 'FAILED';
  ELSIF a.outcome = 'UNKNOWN' THEN
    v_target := 'MANUAL_REVIEW';
  ELSE
    RETURN 'NOT_CONCLUSIVE';
  END IF;
  IF se.status = v_target OR se.status IN ('FAILED','PARTIALLY_REVERSED','REVERSED') THEN
    NULL;
  ELSIF se.status IN ('IN_PROGRESS','MANUAL_REVIEW') THEN
    IF NOT (se.status = 'MANUAL_REVIEW' AND v_target = 'MANUAL_REVIEW') THEN
      UPDATE iam_v2.settlements SET status = v_target WHERE id = se.id;
    END IF;
  ELSE
    RAISE EXCEPTION 'POSTING_SETTLEMENT_STATE: settlement is %', se.status USING ERRCODE = 'check_violation';
  END IF;
  IF v_target = 'SETTLED' THEN
    SELECT entitlement_id INTO v_ent FROM iam_v2.p4_grant_paid_entitlement(p.tenant_id, p.site_id, se.id);
    RETURN 'GRANTED';
  END IF;
  RETURN v_target;
END $fn$;

-- ---------------------------------------------------------------------------------------------------------
-- 5. The accepted review actions take effect on the settlement (and a retry is requeued, once).
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION iam_v2.p4_posting_review_apply(p_tenant uuid, p_site uuid, p_posting uuid)
  RETURNS text
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE p record; se record; rs record; v_ent uuid;
BEGIN
  SELECT * INTO p FROM iam_v2.pms_postings WHERE tenant_id = p_tenant AND site_id = p_site AND id = p_posting AND posting_type = 'CHARGE';
  IF p.id IS NULL THEN RAISE EXCEPTION 'POSTING_UNKNOWN' USING ERRCODE = 'no_data_found'; END IF;
  SELECT * INTO rs FROM iam_v2.posting_review_state WHERE posting_id = p.id;
  IF rs.terminal_action IS NULL THEN RETURN 'NO_DECISION'; END IF;
  SELECT * INTO se FROM iam_v2.settlements WHERE id = p.settlement_id FOR UPDATE;
  IF rs.terminal_action = 'CONFIRM_NOT_POSTED_RETRY' THEN
    UPDATE iam_v2.posting_outbox SET state = 'QUEUED'
     WHERE posting_id = p.id AND state = 'HELD_RECOVERY' AND rs.retry_authorized_attempt_no IS NOT NULL
       AND rs.retry_authorization_consumed_at IS NULL;
    RETURN 'REQUEUED';
  END IF;
  IF se.method <> 'PMS_POSTING' THEN RETURN 'NOT_A_ROOM_CHARGE'; END IF;
  IF rs.terminal_action = 'CONFIRM_POSTED' THEN
    IF se.status IN ('IN_PROGRESS','MANUAL_REVIEW') THEN
      UPDATE iam_v2.settlements SET status = 'SETTLED' WHERE id = se.id;
    END IF;
    IF (SELECT status FROM iam_v2.settlements WHERE id = se.id) = 'SETTLED' THEN
      SELECT entitlement_id INTO v_ent FROM iam_v2.p4_grant_paid_entitlement(p.tenant_id, p.site_id, se.id);
      RETURN 'GRANTED';
    END IF;
    RETURN 'NOT_SETTLED';
  END IF;
  IF rs.terminal_action = 'CONFIRM_NOT_POSTED_ABANDON' THEN
    IF se.status IN ('IN_PROGRESS','MANUAL_REVIEW') THEN
      UPDATE iam_v2.settlements SET status = 'FAILED' WHERE id = se.id;
    END IF;
    RETURN 'FAILED';
  END IF;
  RETURN 'NO_EFFECT';
END $fn$;

-- ---------------------------------------------------------------------------------------------------------
-- 6. The posting runtime role.
-- ---------------------------------------------------------------------------------------------------------
DO $role$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'sc_posting_runtime') THEN
    CREATE ROLE sc_posting_runtime NOLOGIN;
  END IF;
END $role$;
GRANT USAGE ON SCHEMA iam_v2 TO sc_posting_runtime;
GRANT SELECT ON iam_v2.pms_interfaces, iam_v2.pms_interface_revisions, iam_v2.purchases,
                iam_v2.internet_package_revisions, iam_v2.stays, iam_v2.folios, iam_v2.stay_folios,
                iam_v2.package_settlement_mappings, iam_v2.settlements, iam_v2.pms_postings,
                iam_v2.posting_outbox, iam_v2.posting_attempts, iam_v2.posting_review_state,
                iam_v2.posting_execution_state, iam_v2.pms_interface_runtime TO sc_posting_runtime;
GRANT UPDATE (state) ON iam_v2.posting_outbox TO sc_posting_runtime;
GRANT INSERT, UPDATE ON iam_v2.posting_attempts TO sc_posting_runtime;
GRANT INSERT ON iam_v2.posting_attempt_events TO sc_posting_runtime;
GRANT EXECUTE ON FUNCTION iam_v2.allocate_p_number(uuid,uuid,uuid) TO sc_posting_runtime;
GRANT EXECUTE ON FUNCTION iam_v2.p4_interface_freshness_block(uuid,uuid,uuid,uuid,timestamptz) TO sc_posting_runtime;
GRANT EXECUTE ON FUNCTION iam_v2.p4_posting_settlement_outcome(uuid) TO sc_posting_runtime;
-- Consuming a retry authorisation is part of inserting the authorised attempt. It runs as the owner, so the
-- worker needs no write privilege on review decisions at all.
ALTER FUNCTION iam_v2.p4_consume_retry_authorization() SECURITY DEFINER;
REVOKE ALL ON FUNCTION iam_v2.p4_consume_retry_authorization() FROM PUBLIC;

REVOKE ALL ON iam_v2.pms_financial_onboardings FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.pms_interface_financial_onboard(uuid,uuid,uuid,uuid,text,text,smallint,text,text,uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.pms_interface_financially_ready(uuid,uuid,uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.p4_create_room_charge_posting(uuid,uuid,uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.p4_posting_settlement_outcome(uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.p4_posting_review_apply(uuid,uuid,uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION iam_v2.p4_posting_settlement_outcome(uuid) TO sc_posting_runtime;

DO $grant$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_scd') THEN
    GRANT EXECUTE ON FUNCTION iam_v2.p4_create_room_charge_posting(uuid,uuid,uuid) TO svc_scd;
    GRANT EXECUTE ON FUNCTION iam_v2.pms_interface_financially_ready(uuid,uuid,uuid) TO svc_scd;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
    GRANT SELECT ON iam_v2.pms_financial_onboardings TO svc_edged;
    GRANT SELECT, INSERT ON iam_v2.package_settlement_mappings TO svc_edged;
    GRANT EXECUTE ON FUNCTION iam_v2.pms_interface_financial_onboard(uuid,uuid,uuid,uuid,text,text,smallint,text,text,uuid) TO svc_edged;
    GRANT EXECUTE ON FUNCTION iam_v2.pms_interface_financially_ready(uuid,uuid,uuid) TO svc_edged;
    GRANT EXECUTE ON FUNCTION iam_v2.p4_posting_review_apply(uuid,uuid,uuid) TO svc_edged;
  END IF;
END
$grant$;

DO $own$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
    EXECUTE 'ALTER TABLE iam_v2.pms_financial_onboardings OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.pms_financial_onboardings_append_only() OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.pms_interface_financial_onboard(uuid,uuid,uuid,uuid,text,text,smallint,text,text,uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.pms_interface_financially_ready(uuid,uuid,uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.p4_create_room_charge_posting(uuid,uuid,uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.p4_posting_settlement_outcome(uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.p4_posting_review_apply(uuid,uuid,uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.p4_settlement_state_machine() OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.p4_consume_retry_authorization() OWNER TO iam_v2_owner';
  END IF;
END $own$;

COMMIT;
