-- Reverse of 0030_phase6_foundation. Restores the exact pre-0030 structure.
--
-- The terminal_reason CHECK is restored to its pre-Phase-6 value WITHOUT 'AGGREGATE_TIME'. That is only safe
-- while no row uses it, which is the case for a DARK deployment and is asserted here rather than assumed: a
-- rollback that silently dropped a constraint value in use would leave rows the restored constraint forbids.
BEGIN;

DO $$
DECLARE n bigint;
BEGIN
  SELECT count(*) INTO n FROM iam_v2.entitlements WHERE terminal_reason = 'AGGREGATE_TIME';
  IF n > 0 THEN
    RAISE EXCEPTION 'refusing to roll back 0030: % entitlement(s) are terminated with reason AGGREGATE_TIME, '
      'which the pre-Phase-6 constraint does not admit', n USING ERRCODE = 'restrict_violation';
  END IF;
END $$;

ALTER TABLE iam_v2.entitlements DROP CONSTRAINT IF EXISTS entitlements_terminal_reason_check;
ALTER TABLE iam_v2.entitlements ADD CONSTRAINT entitlements_terminal_reason_check
  CHECK (terminal_reason IN ('TIME','DATA','HARD_EXPIRY','CHECKOUT','ADMIN','REVOKED','SUPERSEDED',
                             'CONVERTED','TRANSFERRED','CANCELLED','OTHER'));

COMMENT ON COLUMN iam_v2.entitlement_devices.disconnected_reason IS NULL;

DROP TRIGGER IF EXISTS p6_online_watermark_monotonic ON iam_v2.session_online_watermarks;
DROP FUNCTION IF EXISTS iam_v2.p6_online_watermark_monotonic();
DROP TABLE IF EXISTS iam_v2.session_online_watermarks;

DROP TRIGGER IF EXISTS p6_setting_changes_append_only ON iam_v2.appliance_product_setting_changes;
DROP FUNCTION IF EXISTS iam_v2.p6_setting_changes_append_only();
DROP TABLE IF EXISTS iam_v2.appliance_product_setting_changes;

DROP TABLE IF EXISTS iam_v2.appliance_product_settings;

DELETE FROM public.schema_migrations WHERE version = '0030_phase6_foundation';

COMMIT;
