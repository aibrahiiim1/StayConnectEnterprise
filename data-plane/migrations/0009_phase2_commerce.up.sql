-- 0009 — Phase 2 commercial-packages hardening (iam_v2). ADDITIVE. Dark; no data.
-- The canonical Phase-1A schema (mg1..mg9) already provides append-only immutability triggers on
-- service_plan_revisions + internet_package_revisions (imm_plan_rev / imm_pkg_rev), one-live-
-- entitlement-per-subject partial uniques, the entitlement guard, and the device/session/adjustment
-- engine functions. This migration adds ONLY the Phase-2 free-purchase invariants the base schema
-- cannot express:
--   1. Purchase<->Quote exactness across EVERY money/settlement/tax pin (null-safe IS NOT DISTINCT
--      FROM), enforced even in the FREE case where pms_interface_id/settlement_mapping_id are NULL and
--      the composite FK (MATCH SIMPLE) is not enforced;
--   2. Offer-quote immutability — a quote's pins/price/tax/expiry/grant_snapshot are frozen after
--      creation; the ONLY permitted mutation is the one-time consume (consumed_at NULL -> timestamp);
--   3. supporting lookup indexes.
-- Free-only / NOT_REQUIRED enforcement stays a Phase-2 DOMAIN + UI gate (NOT a schema trigger) so the
-- schema stays forward-compatible with Phase-3 paid / PMS settlement.

BEGIN;

-- 1. Purchase<->Quote pin equality (null-safe, all money pins). A Purchase that references an Offer
--    Quote must pin the SAME tenant/site/package_revision/auth_context/pms_interface/settlement_mapping
--    AND the same amount/currency/exponent/tax_code/tax_rate/tax_amount as that quote.
CREATE OR REPLACE FUNCTION iam_v2.trg_purchase_quote_pin_equal() RETURNS trigger AS $$
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
END $$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS purchase_quote_pin_equal ON iam_v2.purchases;
CREATE TRIGGER purchase_quote_pin_equal
  BEFORE INSERT OR UPDATE ON iam_v2.purchases
  FOR EACH ROW EXECUTE FUNCTION iam_v2.trg_purchase_quote_pin_equal();

-- 2. Offer-quote immutability except one-time consumption. Every pin/price/tax/expiry/grant_snapshot is
--    frozen after INSERT; the only legal UPDATE sets consumed_at from NULL to a timestamp exactly once.
CREATE OR REPLACE FUNCTION iam_v2.trg_offer_quote_immutable() RETURNS trigger AS $$
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
END $$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS offer_quote_immutable ON iam_v2.offer_quotes;
CREATE TRIGGER offer_quote_immutable
  BEFORE UPDATE ON iam_v2.offer_quotes
  FOR EACH ROW EXECUTE FUNCTION iam_v2.trg_offer_quote_immutable();

-- 3. Lookup indexes (additive; IF NOT EXISTS keeps re-apply a no-op).
CREATE INDEX IF NOT EXISTS internet_packages_active_lookup
  ON iam_v2.internet_packages (tenant_id, site_id, active) WHERE active;
CREATE INDEX IF NOT EXISTS package_revision_visibility
  ON iam_v2.internet_package_revisions (tenant_id, site_id, package_id, visible_from, visible_until);
CREATE INDEX IF NOT EXISTS offer_quotes_auth_context
  ON iam_v2.offer_quotes (tenant_id, site_id, auth_context_id);
CREATE INDEX IF NOT EXISTS offer_quotes_open_expiry
  ON iam_v2.offer_quotes (expires_at) WHERE consumed_at IS NULL;
CREATE INDEX IF NOT EXISTS purchases_auth_context
  ON iam_v2.purchases (tenant_id, site_id, auth_context_id);
CREATE INDEX IF NOT EXISTS package_eligibility_rules_by_revision
  ON iam_v2.package_eligibility_rules (package_revision_id, rule_type);

INSERT INTO public.schema_migrations (version) VALUES ('0009_phase2_commerce')
  ON CONFLICT (version) DO NOTHING;

COMMIT;
