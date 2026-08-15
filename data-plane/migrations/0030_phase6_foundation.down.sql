-- Reverse of 0030_phase6_foundation. Restores the exact pre-0030 structure.
--
-- The contract's terminal_reason set was never modified by 0030, so there is nothing to restore about it and
-- no risk of a rollback leaving rows a narrowed constraint forbids. That is a direct consequence of not
-- having widened it in the first place: vocabulary you never changed needs no reversal.
BEGIN;

DROP TRIGGER IF EXISTS p6_termination_evidence_matches_transition ON iam_v2.entitlement_termination_evidence;
DROP FUNCTION IF EXISTS iam_v2.p6_record_time_termination(uuid, text);
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

-- The scope anchor is dropped last, because the settings tables reference it -- and ONLY IF 0030 CREATED IT.
-- `IF NOT EXISTS` on the way up means the index may have pre-existed as a platform object, and a rollback
-- that removed it would be deleting something it never made. The COMMENT written by the up migration is the
-- ownership record, so an index without that exact marker is left exactly where it is.
DO $$
DECLARE owned boolean;
BEGIN
  SELECT obj_description(c.oid, 'pg_class') = 'created by iam_v2 migration 0030_phase6_foundation'
    INTO owned
    FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
   WHERE n.nspname = 'public' AND c.relname = 'appliances_tsi_anchor' AND c.relkind = 'i';
  IF owned IS TRUE THEN
    DROP INDEX public.appliances_tsi_anchor;
  ELSIF owned IS FALSE THEN
    RAISE NOTICE 'public.appliances_tsi_anchor exists but was not created by 0030; leaving it in place';
  END IF;
END $$;

DELETE FROM public.schema_migrations WHERE version = '0030_phase6_foundation';

COMMIT;
