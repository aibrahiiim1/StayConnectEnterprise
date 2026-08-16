-- Reverse of 0035: restore the unserialized first write and the LIST action value.
--
-- Rolling back reinstates the unordered-first-write defect. Same rule as 0032 and 0034: a down migration
-- restores what it replaced rather than quietly keeping the improvement.
BEGIN;

ALTER TABLE iam_v2.guest_device_actions DROP CONSTRAINT IF EXISTS guest_device_actions_action_check;
ALTER TABLE iam_v2.guest_device_actions ADD CONSTRAINT guest_device_actions_action_check
  CHECK (action IN ('LIST','RELEASE'));
COMMENT ON TABLE iam_v2.guest_device_actions IS NULL;

CREATE OR REPLACE FUNCTION iam_v2.p6_set_guest_device_self_service(
  p_tenant uuid, p_site uuid, p_appliance uuid, p_on boolean,
  p_operator uuid, p_operator_label text, p_reason text DEFAULT NULL)
RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $$
DECLARE v_old boolean;
BEGIN
  IF p_operator_label IS NULL OR btrim(p_operator_label) = '' THEN
    RAISE EXCEPTION 'an operator label is required: "somebody changed it" is not an audit record'
      USING ERRCODE = 'invalid_parameter_value';
  END IF;
  SELECT guest_device_self_service INTO v_old FROM iam_v2.appliance_product_settings
   WHERE tenant_id = p_tenant AND site_id = p_site AND appliance_id = p_appliance FOR UPDATE;
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
REVOKE EXECUTE ON FUNCTION iam_v2.p6_set_guest_device_self_service(uuid, uuid, uuid, boolean, uuid, text, text) FROM PUBLIC;
GRANT  EXECUTE ON FUNCTION iam_v2.p6_set_guest_device_self_service(uuid, uuid, uuid, boolean, uuid, text, text) TO svc_edged;

DELETE FROM public.schema_migrations WHERE version = '0035_phase6_setting_serialization_and_list_audit';

COMMIT;
