-- 0009 — Phase 2 commercial-packages hardening (iam_v2). ADDITIVE. Dark; no data.
-- The canonical Phase-1A schema (mg1..mg9) already provides: append-only immutability triggers on
-- service_plan_revisions + internet_package_revisions (imm_plan_rev / imm_pkg_rev), one-live-
-- entitlement-per-subject partial uniques, the entitlement guard, and the device/session/adjustment
-- engine functions. This migration adds ONLY what is missing for the Phase-2 free-purchase path:
--   1. Purchase<->Quote pin equality enforced even in the FREE case (pms_interface_id /
--      settlement_mapping_id NULL) — the purchase<->quote composite FK is MATCH SIMPLE and is not
--      enforced when a pinned column is NULL;
--   2. supporting lookup indexes for eligible-package / active-revision / auth-context queries.
-- Free-only / NOT_REQUIRED enforcement is a Phase-2 DOMAIN + UI gate (NOT a schema trigger) so the
-- schema stays forward-compatible with Phase-3 paid / PMS settlement.

BEGIN;

-- 1. Purchase<->Quote pin equality (null-safe): a Purchase that references an Offer Quote must pin the
--    SAME tenant/site/package_revision_id/auth_context_id as that quote — enforced even when
--    pms_interface_id / settlement_mapping_id are NULL (the free path the composite FK cannot cover).
CREATE OR REPLACE FUNCTION iam_v2.trg_purchase_quote_pin_equal() RETURNS trigger AS $$
DECLARE q RECORD;
BEGIN
  IF NEW.offer_quote_id IS NULL THEN
    RETURN NEW;  -- non-quote triggers (voucher/account auto-grant etc.) are pinned by their own paths
  END IF;
  SELECT tenant_id, site_id, package_revision_id, auth_context_id
    INTO q FROM iam_v2.offer_quotes WHERE id = NEW.offer_quote_id;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'purchase references unknown offer_quote %', NEW.offer_quote_id USING ERRCODE = 'foreign_key_violation';
  END IF;
  IF NEW.tenant_id IS DISTINCT FROM q.tenant_id
     OR NEW.site_id IS DISTINCT FROM q.site_id
     OR NEW.package_revision_id IS DISTINCT FROM q.package_revision_id
     OR NEW.auth_context_id IS DISTINCT FROM q.auth_context_id THEN
    RAISE EXCEPTION 'purchase pins (tenant/site/package_revision/auth_context) must match its offer_quote %', NEW.offer_quote_id
      USING ERRCODE = 'restrict_violation';
  END IF;
  RETURN NEW;
END $$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS purchase_quote_pin_equal ON iam_v2.purchases;
CREATE TRIGGER purchase_quote_pin_equal
  BEFORE INSERT OR UPDATE ON iam_v2.purchases
  FOR EACH ROW EXECUTE FUNCTION iam_v2.trg_purchase_quote_pin_equal();

-- 2. Lookup indexes (additive; IF NOT EXISTS keeps re-apply a no-op).
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
