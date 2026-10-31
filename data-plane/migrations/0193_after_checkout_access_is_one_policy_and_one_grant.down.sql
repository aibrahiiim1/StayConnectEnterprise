-- Down for 0193. Removes the triggers, the functions and the after_checkout_enabled column, and restores the 0010 body of
-- the version guard. Every site then grants at every checkout again (the behaviour before 0193); history rows written by
-- set_after_checkout_enabled stay in checkout_grace_policy_publications (append-only) and remain readable.
-- Note: it does not bring back the separate post-stay benefit; that is code, not schema.
BEGIN;

DROP TRIGGER IF EXISTS post_stay_conversion_retired ON iam_v2.purchases;
DROP FUNCTION IF EXISTS iam_v2.post_stay_conversion_retired();

DROP TRIGGER IF EXISTS after_checkout_device_guard ON iam_v2.entitlement_devices;
DROP FUNCTION IF EXISTS iam_v2.after_checkout_device_guard();

DROP TRIGGER IF EXISTS after_checkout_switch_guard ON iam_v2.site_checkout_grace_config;
DROP FUNCTION IF EXISTS iam_v2.after_checkout_switch_guard();

DROP FUNCTION IF EXISTS iam_v2.set_after_checkout_enabled(uuid,uuid,boolean,integer,uuid,text);
DROP FUNCTION IF EXISTS iam_v2.p3_after_checkout_enabled(uuid,uuid);

CREATE OR REPLACE FUNCTION iam_v2.p3_grace_config_version_guard() RETURNS trigger
    LANGUAGE plpgsql SET search_path TO 'iam_v2', 'pg_temp' AS $$
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

ALTER TABLE iam_v2.site_checkout_grace_config DROP COLUMN IF EXISTS after_checkout_enabled;

COMMIT;
