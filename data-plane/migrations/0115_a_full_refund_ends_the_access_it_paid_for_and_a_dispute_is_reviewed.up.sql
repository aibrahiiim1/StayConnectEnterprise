-- 0115 — a confirmed FULL provider refund ends the access it paid for; a dispute or chargeback is reviewed
-- (Product-Owner policy 2026-10-12; contract docs/architecture/ONEGATE_MODULES_AND_ACQUISITION.md §card, and
-- ONEGATE_CLIENT_IDENTITY_AND_ACCESS_POLICY.md §9).
--
--   * A full refund (refunds recorded CAPTURED against a charge >= the charge) terminates ONLY the entitlement
--     that charge's purchase funded (purchase -> settlement -> entitlement is 1:1:1), with reason REVOKED; its
--     devices are disconnected and its sessions ended exactly as the Phase-6 expiry writer does; one append-only
--     evidence row records the amounts and the transition. Idempotent.
--   * A partial refund changes nothing about access.
--   * A chargeback, or a dispute the provider reports, NEVER ends access automatically: it opens a review case
--     that an operator resolves explicitly (KEEP_ACCESS or END_ACCESS) with a reason. If a charge has both a
--     chargeback and refunds, the review decides.
--   * Financial UNKNOWN is untouched by all of this: only CAPTURED reversal rows are counted.
--
-- Nothing here creates, retries or reverses money. Additive; the .down drops what this adds.
BEGIN;

CREATE TABLE IF NOT EXISTS iam_v2.card_reversal_reviews (
  id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id             uuid NOT NULL,
  site_id               uuid NOT NULL,
  settlement_id         uuid NOT NULL,
  charge_transaction_id uuid NOT NULL,
  kind                  text NOT NULL CHECK (kind IN ('DISPUTE','CHARGEBACK')),
  provider_ref          text NOT NULL CHECK (length(provider_ref) BETWEEN 1 AND 200),
  opened_at             timestamptz NOT NULL DEFAULT now(),
  resolution            text CHECK (resolution IN ('KEEP_ACCESS','END_ACCESS')),
  resolved_at           timestamptz,
  resolved_by           text CHECK (resolved_by IS NULL OR length(resolved_by) BETWEEN 1 AND 200),
  resolution_reason     text CHECK (resolution_reason IS NULL OR length(resolution_reason) BETWEEN 1 AND 500),
  UNIQUE (charge_transaction_id, kind, provider_ref),
  CHECK ((resolution IS NULL) = (resolved_at IS NULL)),
  CHECK ((resolution IS NULL) = (resolved_by IS NULL)),
  CHECK ((resolution IS NULL) = (resolution_reason IS NULL))
);
CREATE INDEX IF NOT EXISTS card_reversal_reviews_open ON iam_v2.card_reversal_reviews (tenant_id, site_id) WHERE resolution IS NULL;
COMMENT ON TABLE iam_v2.card_reversal_reviews IS
  'Disputes and chargebacks on card charges, each a case an operator resolves explicitly (0115). Never deleted; '
  'a resolution is written once.';

-- A case is opened once and resolved once; nothing else may change and nothing may be deleted.
CREATE OR REPLACE FUNCTION iam_v2.card_reversal_reviews_guard() RETURNS trigger
  LANGUAGE plpgsql SET search_path = iam_v2, pg_temp AS $f$
BEGIN
  IF TG_OP = 'DELETE' THEN
    RAISE EXCEPTION 'card reversal reviews are never deleted' USING ERRCODE = 'insufficient_privilege';
  END IF;
  IF OLD.resolution IS NOT NULL THEN
    RAISE EXCEPTION 'a resolved review is final' USING ERRCODE = 'insufficient_privilege';
  END IF;
  IF (NEW.id, NEW.tenant_id, NEW.site_id, NEW.settlement_id, NEW.charge_transaction_id, NEW.kind, NEW.provider_ref, NEW.opened_at)
     IS DISTINCT FROM (OLD.id, OLD.tenant_id, OLD.site_id, OLD.settlement_id, OLD.charge_transaction_id, OLD.kind, OLD.provider_ref, OLD.opened_at) THEN
    RAISE EXCEPTION 'only the resolution of a review may be written' USING ERRCODE = 'insufficient_privilege';
  END IF;
  RETURN NEW;
END $f$;
DROP TRIGGER IF EXISTS card_reversal_reviews_guard ON iam_v2.card_reversal_reviews;
CREATE TRIGGER card_reversal_reviews_guard BEFORE UPDATE OR DELETE ON iam_v2.card_reversal_reviews
  FOR EACH ROW EXECUTE FUNCTION iam_v2.card_reversal_reviews_guard();

CREATE TABLE IF NOT EXISTS iam_v2.refund_access_terminations (
  entitlement_id        uuid PRIMARY KEY,
  tenant_id             uuid NOT NULL,
  site_id               uuid NOT NULL,
  settlement_id         uuid NOT NULL,
  charge_transaction_id uuid NOT NULL,
  captured_minor        bigint NOT NULL CHECK (captured_minor > 0),
  refunded_minor        bigint NOT NULL CHECK (refunded_minor >= captured_minor),
  transition_id         uuid,
  terminated_at         timestamptz NOT NULL DEFAULT now()
);
COMMENT ON TABLE iam_v2.refund_access_terminations IS
  'Evidence: an entitlement ended because the provider fully refunded the charge that funded it (0115). Append-only.';
CREATE OR REPLACE FUNCTION iam_v2.refund_access_terminations_append_only() RETURNS trigger
  LANGUAGE plpgsql SET search_path = iam_v2, pg_temp AS $f$
BEGIN
  RAISE EXCEPTION 'refund termination evidence is append-only' USING ERRCODE = 'insufficient_privilege';
END $f$;
DROP TRIGGER IF EXISTS refund_access_terminations_append_only ON iam_v2.refund_access_terminations;
CREATE TRIGGER refund_access_terminations_append_only BEFORE UPDATE OR DELETE ON iam_v2.refund_access_terminations
  FOR EACH ROW EXECUTE FUNCTION iam_v2.refund_access_terminations_append_only();

-- The one way access paid for by a charge is ended here: the boundary termination, then devices and sessions
-- closed as the Phase-6 writer closes them. Returns the transition id (NULL if already terminated).
CREATE OR REPLACE FUNCTION iam_v2.p4_end_access_funded_by(p_entitlement uuid, p_reason text) RETURNS uuid
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $f$
DECLARE v_tr uuid; v_status text;
BEGIN
  SELECT status INTO v_status FROM iam_v2.entitlements WHERE id = p_entitlement FOR UPDATE;
  IF v_status IS NULL OR v_status = 'TERMINATED' THEN
    RETURN NULL;
  END IF;
  v_tr := iam_v2.terminate_entitlement_at_boundary(p_entitlement, now(), p_reason);
  PERFORM iam_v2.begin_controlled_operation('device_auth');
  WITH closed AS (
    UPDATE iam_v2.entitlement_device_authorizations a
       SET deauthorized_at = GREATEST(now(), a.authorized_at)
     WHERE a.entitlement_id = p_entitlement AND a.deauthorized_at IS NULL
     RETURNING a.entitlement_id, a.device_id)
  UPDATE iam_v2.entitlement_devices ed
     SET status = 'DISCONNECTED', disconnected_reason = 'ENTITLEMENT_ENDED'
    FROM closed
   WHERE ed.entitlement_id = closed.entitlement_id AND ed.device_id = closed.device_id;
  UPDATE iam_v2.sessions
     SET state = 'ended', ended = GREATEST(now(), started), end_reason = 'ENTITLEMENT_ENDED'
   WHERE entitlement_id = p_entitlement AND state IN ('active', 'PENDING_ENFORCEMENT');
  RETURN v_tr;
END $f$;
REVOKE EXECUTE ON FUNCTION iam_v2.p4_end_access_funded_by(uuid, text) FROM PUBLIC;

-- THE REFUND POLICY, applied after the provider's reversals of one charge have been recorded.
CREATE OR REPLACE FUNCTION iam_v2.p4_apply_refund_policy(p_tenant uuid, p_site uuid, p_charge uuid) RETURNS text
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $f$
DECLARE
  ch record; se record; v_captured bigint; v_refunded bigint; v_charged_back bigint; v_ent uuid; v_status text; v_tr uuid;
BEGIN
  SELECT * INTO ch FROM iam_v2.payment_transactions
   WHERE id = p_charge AND tenant_id = p_tenant AND site_id = p_site AND transaction_type = 'CHARGE';
  IF NOT FOUND OR ch.status <> 'CAPTURED' THEN
    RETURN 'NOT_A_CAPTURED_CHARGE';
  END IF;
  SELECT * INTO se FROM iam_v2.settlements WHERE id = ch.settlement_id FOR UPDATE;
  v_captured := ch.amount_minor;
  SELECT COALESCE(sum(amount_minor) FILTER (WHERE transaction_type = 'REFUND'), 0),
         COALESCE(sum(amount_minor) FILTER (WHERE transaction_type = 'CHARGEBACK'), 0)
    INTO v_refunded, v_charged_back
    FROM iam_v2.payment_transactions WHERE parent_transaction_id = p_charge AND status = 'CAPTURED';

  IF v_charged_back > 0 THEN
    INSERT INTO iam_v2.card_reversal_reviews (tenant_id, site_id, settlement_id, charge_transaction_id, kind, provider_ref)
    VALUES (p_tenant, p_site, se.id, p_charge, 'CHARGEBACK', 'ledger')
    ON CONFLICT (charge_transaction_id, kind, provider_ref) DO NOTHING;
    RETURN 'CHARGEBACK_REVIEW';
  END IF;
  IF v_refunded <= 0 THEN
    RETURN 'NO_REFUND';
  END IF;
  IF v_refunded < v_captured THEN
    RETURN 'PARTIAL_NO_ACTION';
  END IF;

  SELECT id, status INTO v_ent, v_status FROM iam_v2.entitlements
   WHERE tenant_id = p_tenant AND site_id = p_site AND purchase_id = se.purchase_id;
  IF v_ent IS NULL THEN
    RETURN 'NO_ENTITLEMENT';
  END IF;
  IF EXISTS (SELECT 1 FROM iam_v2.refund_access_terminations WHERE entitlement_id = v_ent) THEN
    RETURN 'ALREADY_ENDED';
  END IF;
  IF v_status = 'TERMINATED' THEN
    RETURN 'ALREADY_TERMINATED'; -- ended for another reason before the refund; nothing to record
  END IF;
  v_tr := iam_v2.p4_end_access_funded_by(v_ent, 'REVOKED');
  INSERT INTO iam_v2.refund_access_terminations
    (entitlement_id, tenant_id, site_id, settlement_id, charge_transaction_id, captured_minor, refunded_minor, transition_id)
  VALUES (v_ent, p_tenant, p_site, se.id, p_charge, v_captured, v_refunded, v_tr);
  RETURN 'ACCESS_ENDED';
END $f$;
REVOKE EXECUTE ON FUNCTION iam_v2.p4_apply_refund_policy(uuid, uuid, uuid) FROM PUBLIC;

-- A dispute the provider reports (not yet money) opens a review case once.
CREATE OR REPLACE FUNCTION iam_v2.p4_open_dispute_review(p_tenant uuid, p_site uuid, p_charge uuid, p_provider_ref text) RETURNS text
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $f$
DECLARE ch record; n int;
BEGIN
  IF p_provider_ref IS NULL OR length(p_provider_ref) NOT BETWEEN 1 AND 200 THEN
    RAISE EXCEPTION 'a dispute needs the provider reference' USING ERRCODE = 'check_violation';
  END IF;
  SELECT * INTO ch FROM iam_v2.payment_transactions
   WHERE id = p_charge AND tenant_id = p_tenant AND site_id = p_site AND transaction_type = 'CHARGE' AND status = 'CAPTURED';
  IF NOT FOUND THEN
    RETURN 'NOT_A_CAPTURED_CHARGE';
  END IF;
  INSERT INTO iam_v2.card_reversal_reviews (tenant_id, site_id, settlement_id, charge_transaction_id, kind, provider_ref)
  VALUES (p_tenant, p_site, ch.settlement_id, p_charge, 'DISPUTE', p_provider_ref)
  ON CONFLICT (charge_transaction_id, kind, provider_ref) DO NOTHING;
  GET DIAGNOSTICS n = ROW_COUNT;
  RETURN CASE WHEN n = 1 THEN 'OPENED' ELSE 'ALREADY_OPEN' END;
END $f$;
REVOKE EXECUTE ON FUNCTION iam_v2.p4_open_dispute_review(uuid, uuid, uuid, text) FROM PUBLIC;

-- An operator resolves a case: KEEP_ACCESS changes nothing else; END_ACCESS ends the access the charge funded.
CREATE OR REPLACE FUNCTION iam_v2.p4_resolve_reversal_review(p_tenant uuid, p_site uuid, p_review uuid, p_resolution text,
                                                           p_actor text, p_reason text) RETURNS text
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $f$
DECLARE rv record; v_ent uuid;
BEGIN
  IF p_resolution NOT IN ('KEEP_ACCESS','END_ACCESS') THEN
    RAISE EXCEPTION 'resolution must be KEEP_ACCESS or END_ACCESS' USING ERRCODE = 'check_violation';
  END IF;
  IF COALESCE(btrim(p_actor), '') = '' OR COALESCE(btrim(p_reason), '') = '' THEN
    RAISE EXCEPTION 'a resolution needs an operator and a reason' USING ERRCODE = 'check_violation';
  END IF;
  SELECT * INTO rv FROM iam_v2.card_reversal_reviews
   WHERE id = p_review AND tenant_id = p_tenant AND site_id = p_site FOR UPDATE;
  IF NOT FOUND THEN
    RETURN 'NOT_FOUND';
  END IF;
  IF rv.resolution IS NOT NULL THEN
    RETURN 'ALREADY_RESOLVED';
  END IF;
  IF p_resolution = 'END_ACCESS' THEN
    SELECT e.id INTO v_ent FROM iam_v2.settlements se JOIN iam_v2.entitlements e ON e.purchase_id = se.purchase_id
     WHERE se.id = rv.settlement_id;
    IF v_ent IS NOT NULL THEN
      PERFORM iam_v2.p4_end_access_funded_by(v_ent, 'REVOKED');
    END IF;
  END IF;
  UPDATE iam_v2.card_reversal_reviews
     SET resolution = p_resolution, resolved_at = now(), resolved_by = btrim(p_actor), resolution_reason = btrim(p_reason)
   WHERE id = p_review;
  RETURN p_resolution;
END $f$;
REVOKE EXECUTE ON FUNCTION iam_v2.p4_resolve_reversal_review(uuid, uuid, uuid, text, text, text) FROM PUBLIC;

DO $g$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'sc_payment_outcome') THEN
    GRANT EXECUTE ON FUNCTION iam_v2.p4_apply_refund_policy(uuid, uuid, uuid) TO sc_payment_outcome;
    GRANT EXECUTE ON FUNCTION iam_v2.p4_open_dispute_review(uuid, uuid, uuid, text) TO sc_payment_outcome;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
    GRANT SELECT ON iam_v2.card_reversal_reviews, iam_v2.refund_access_terminations TO svc_edged;
    GRANT EXECUTE ON FUNCTION iam_v2.p4_resolve_reversal_review(uuid, uuid, uuid, text, text, text) TO svc_edged;
  END IF;
END $g$;

COMMIT;
