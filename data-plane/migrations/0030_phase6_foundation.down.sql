-- Reverse of 0030_phase6_foundation. Restores the exact pre-0030 structure.
--
-- The contract's terminal_reason set was never modified by 0030, so there is nothing to restore about it and
-- no risk of a rollback leaving rows a narrowed constraint forbids. That is a direct consequence of not
-- having widened it in the first place: vocabulary you never changed needs no reversal.
BEGIN;

DROP TRIGGER IF EXISTS p6_termination_evidence_matches_transition ON iam_v2.entitlement_termination_evidence;
DROP FUNCTION IF EXISTS iam_v2.p6_termination_evidence_matches_transition();
DROP TRIGGER IF EXISTS p6_termination_evidence_append_only ON iam_v2.entitlement_termination_evidence;
DROP FUNCTION IF EXISTS iam_v2.p6_termination_evidence_append_only();
DROP TABLE IF EXISTS iam_v2.entitlement_termination_evidence;

COMMENT ON COLUMN iam_v2.entitlement_devices.disconnected_reason IS NULL;

DROP TRIGGER IF EXISTS p6_online_watermark_monotonic ON iam_v2.session_online_watermarks;
DROP FUNCTION IF EXISTS iam_v2.p6_online_watermark_monotonic();
DROP TABLE IF EXISTS iam_v2.session_online_watermarks;

DROP TRIGGER IF EXISTS p6_setting_changes_append_only ON iam_v2.appliance_product_setting_changes;
DROP FUNCTION IF EXISTS iam_v2.p6_setting_changes_append_only();
DROP TABLE IF EXISTS iam_v2.appliance_product_setting_changes;

DROP TABLE IF EXISTS iam_v2.appliance_product_settings;

-- The scope anchor is dropped last, because the settings tables reference it.
DROP INDEX IF EXISTS public.appliances_tsi_anchor;

DELETE FROM public.schema_migrations WHERE version = '0030_phase6_foundation';

COMMIT;
