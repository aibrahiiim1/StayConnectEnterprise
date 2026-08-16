-- PHASE 6 — close the remaining privilege bypasses, and remove the speculative grants.
--
-- 0033 put the release behind a SECURITY DEFINER policy function, which closed the big hole. Three smaller
-- ones survived it, and each has the same shape: a capability that LOOKS like it goes through the policy but
-- does not have to.
--
--   1. p6_guest_release_device takes p_max_releases_per_hour. A runtime role holding EXECUTE can pass
--      2147483647 and walk straight past the throttle while still calling the approved function. A policy
--      control the caller chooses is not a policy control.
--
--   2. svc_edged held INSERT/UPDATE directly on appliance_product_settings and INSERT on its audit. The Go
--      service writes both in one transaction, but the ROLE could write either alone -- so the audit was
--      mandatory by convention and optional by privilege. An operator could flip a guest-facing capability
--      with no trace, and nothing would refuse it.
--
--   3. Several 0033 grants were speculative: svc_scd received SELECT/INSERT on guest_device_actions and
--      SELECT on entitlement_device_authorizations, and svc_edged SELECT on the setting audit. No implemented
--      route reads or writes any of them -- the audit rows are written by the definer functions, under the
--      definer's rights. A grant kept "in case" is a privilege nobody is accounting for.
BEGIN;

-- ---------------------------------------------------------------------------------------------------------
-- 1. A release the caller cannot parameterize
-- ---------------------------------------------------------------------------------------------------------
-- The runtime gets a TWO-argument entry point. The throttle is not an argument of it at all, so there is no
-- value a caller can choose. The three-argument form survives as the internal/test primitive: same body,
-- owner-only, never granted to a runtime role.
CREATE OR REPLACE FUNCTION iam_v2.p6_guest_release_device_policy(p_entitlement uuid, p_device uuid)
RETURNS text LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $$
DECLARE
  -- The policy limit lives HERE, on the server, next to the operation it governs. It is a constant rather
  -- than a setting because a per-appliance throttle would be a per-appliance way to disable the throttle,
  -- and nothing in the product needs that.
  c_max_releases_per_hour CONSTANT int := 20;
BEGIN
  RETURN iam_v2.p6_guest_release_device(p_entitlement, p_device, c_max_releases_per_hour);
END $$;

COMMENT ON FUNCTION iam_v2.p6_guest_release_device_policy(uuid, uuid) IS
  'THE ONLY release entry point a runtime role may hold. Its security policy is derived server-side: the '
  'hourly throttle is not a parameter, so no caller can choose it. The three-argument form is the internal '
  'test primitive and is never granted to a runtime role -- a role that can pass its own limit can pass '
  '2147483647 and use the approved function to bypass the approved policy.';

REVOKE EXECUTE ON FUNCTION iam_v2.p6_guest_release_device_policy(uuid, uuid) FROM PUBLIC;
-- The parameterized primitive is withdrawn from the runtime. It keeps its owner-only posture.
REVOKE EXECUTE ON FUNCTION iam_v2.p6_guest_release_device(uuid, uuid, int) FROM svc_scd;
GRANT  EXECUTE ON FUNCTION iam_v2.p6_guest_release_device_policy(uuid, uuid) TO svc_scd;

-- ---------------------------------------------------------------------------------------------------------
-- 2. The setting change and its audit, as ONE operation
-- ---------------------------------------------------------------------------------------------------------
-- Atomic by construction rather than by the caller's good behaviour: there is no privilege that writes the
-- setting without also writing the audit, because the only privilege granted is this function.
--
-- Scope and actor remain the SERVER'S. The parameters exist because the function has to be told which
-- appliance and which operator, but every one of them is filled from trusted local assignment and
-- authenticated operator context by the layer above, and the FKs behind them refuse anything invented: the
-- appliance must exist under that exact tenant and site, and the operator must be a real operator record.
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

  SELECT guest_device_self_service INTO v_old
    FROM iam_v2.appliance_product_settings
   WHERE tenant_id = p_tenant AND site_id = p_site AND appliance_id = p_appliance
   FOR UPDATE;

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

COMMENT ON FUNCTION iam_v2.p6_set_guest_device_self_service(uuid, uuid, uuid, boolean, uuid, text, text) IS
  'The ONLY way a runtime role may change the per-appliance setting. The change and its audit are one '
  'operation, so the audit is mandatory by PRIVILEGE rather than by convention: svc_edged holds no direct '
  'write on either table, and therefore cannot flip a guest-facing capability without leaving a trace.';

REVOKE EXECUTE ON FUNCTION iam_v2.p6_set_guest_device_self_service(uuid, uuid, uuid, boolean, uuid, text, text) FROM PUBLIC;
GRANT  EXECUTE ON FUNCTION iam_v2.p6_set_guest_device_self_service(uuid, uuid, uuid, boolean, uuid, text, text) TO svc_edged;

-- The direct write bypass is withdrawn.
REVOKE INSERT, UPDATE, DELETE ON iam_v2.appliance_product_settings FROM svc_edged;
REVOKE INSERT, UPDATE, DELETE ON iam_v2.appliance_product_setting_changes FROM svc_edged;

-- ---------------------------------------------------------------------------------------------------------
-- 3. The speculative grants, withdrawn
-- ---------------------------------------------------------------------------------------------------------
-- Audited against what the implemented routes actually query. The audit rows are written by the definer
-- functions under the definer's rights, and the throttle counts them inside the definer -- so the guest
-- surface needs no access to that table at all. The listing reads entitlement_devices, devices and sessions,
-- and nothing else.
REVOKE SELECT, INSERT ON iam_v2.guest_device_actions FROM svc_scd;
REVOKE SELECT ON iam_v2.entitlement_device_authorizations FROM svc_scd;
REVOKE SELECT ON iam_v2.appliance_product_setting_changes FROM svc_edged;

-- ---------------------------------------------------------------------------------------------------------
-- 4. The resulting shape, asserted rather than described
-- ---------------------------------------------------------------------------------------------------------
DO $$
DECLARE bad text;
BEGIN
  IF has_function_privilege('svc_scd', 'iam_v2.p6_guest_release_device(uuid,uuid,int)', 'EXECUTE') THEN
    RAISE EXCEPTION 'svc_scd can still call the parameterized release and choose its own throttle';
  END IF;
  IF NOT has_function_privilege('svc_scd', 'iam_v2.p6_guest_release_device_policy(uuid,uuid)', 'EXECUTE') THEN
    RAISE EXCEPTION 'svc_scd cannot call the policy release, so the guest surface cannot work';
  END IF;
  IF NOT has_table_privilege('svc_scd', 'iam_v2.sessions', 'INSERT') THEN
    RAISE EXCEPTION 'svc_scd cannot INSERT sessions, so the real admission path cannot work';
  END IF;
  IF has_table_privilege('svc_scd', 'iam_v2.sessions', 'UPDATE') THEN
    RAISE EXCEPTION 'svc_scd can UPDATE sessions, which includes promoting one to active';
  END IF;

  SELECT string_agg(format('%s %s', pr.priv, t.tbl), ', ') INTO bad
    FROM (VALUES ('appliance_product_settings'), ('appliance_product_setting_changes')) AS t(tbl),
         (VALUES ('INSERT'), ('UPDATE'), ('DELETE')) AS pr(priv)
   WHERE has_table_privilege('svc_edged', 'iam_v2.' || t.tbl, pr.priv);
  IF bad IS NOT NULL THEN
    RAISE EXCEPTION 'svc_edged retains a direct unaudited setting write: %', bad;
  END IF;
  IF NOT has_function_privilege('svc_edged', 'iam_v2.p6_set_guest_device_self_service(uuid,uuid,uuid,boolean,uuid,text,text)', 'EXECUTE') THEN
    RAISE EXCEPTION 'svc_edged cannot call the controlled setting operation, so the operator surface cannot work';
  END IF;

  IF has_table_privilege('svc_scd', 'iam_v2.guest_device_actions', 'SELECT')
     OR has_table_privilege('svc_scd', 'iam_v2.guest_device_actions', 'INSERT') THEN
    RAISE EXCEPTION 'svc_scd retains a speculative grant on guest_device_actions';
  END IF;
END $$;

INSERT INTO public.schema_migrations (version) VALUES ('0034_phase6_policy_boundaries')
  ON CONFLICT (version) DO NOTHING;

COMMIT;
