-- 0009 rollback — drop the Phase 2 commerce hardening (pin-equality trigger + indexes). Additive-only.
BEGIN;

DROP TRIGGER IF EXISTS purchase_quote_pin_equal ON iam_v2.purchases;
DROP FUNCTION IF EXISTS iam_v2.trg_purchase_quote_pin_equal();

DROP INDEX IF EXISTS iam_v2.internet_packages_active_lookup;
DROP INDEX IF EXISTS iam_v2.package_revision_visibility;
DROP INDEX IF EXISTS iam_v2.offer_quotes_auth_context;
DROP INDEX IF EXISTS iam_v2.offer_quotes_open_expiry;
DROP INDEX IF EXISTS iam_v2.purchases_auth_context;
DROP INDEX IF EXISTS iam_v2.package_eligibility_rules_by_revision;

DELETE FROM public.schema_migrations WHERE version = '0009_phase2_commerce';

COMMIT;
