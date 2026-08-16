-- PHASE 6 — order the first setting write, and stop claiming LIST is audited.
--
-- 1. THE FIRST WRITE HAD NOTHING TO LOCK.
--
--    p6_set_guest_device_self_service takes the settings row FOR UPDATE before writing it. On an appliance
--    that has never been configured there IS no settings row, so the lock locks nothing and two concurrent
--    first-time changes are unordered: both read old_value as NULL, both upsert, and the audit ends up
--    claiming two independent transitions from "unset" when only one of them can have been first. The
--    setting itself converges -- the upsert is idempotent -- but the HISTORY is wrong, and the history is the
--    only reason the audit exists.
--
--    The anchor is the APPLIANCE, which always exists: every settings row is foreign-keyed to a real enrolled
--    appliance under its exact tenant and site, so there is always an identity to serialize on even when the
--    child row is absent. A transaction-scoped advisory lock keyed on that identity orders mutations per
--    appliance without taking a row lock on a platform table that enrollment also writes.
--
-- 2. LIST WAS NEVER AUDITED, AND THE SCHEMA SAID IT WAS.
--
--    guest_device_actions.action admitted 'LIST', but no code path has ever written one -- and after 0034
--    none can, because svc_scd holds no INSERT on that table at all (the audit rows are written by the
--    definer functions under their own rights). A schema that names an action nobody records is a standing
--    claim that listing is audited, and anybody reading it would reasonably believe a guest's list requests
--    are investigable. They are not.
--
--    The claim is narrowed to the truth rather than the implementation stretched to meet it: RELEASE is the
--    action that changes durable state, it is audited in every outcome including refusals, and that is what
--    the approved requirement is about. Auditing every read would also mean granting the guest surface a
--    write on its own audit table, which is exactly the privilege 0034 removed on purpose.
BEGIN;

-- ---------------------------------------------------------------------------------------------------------
-- 1. Serialize per appliance, on an identity that always exists
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION iam_v2.p6_set_guest_device_self_service(
  p_tenant uuid, p_site uuid, p_appliance uuid, p_on boolean,
  p_operator uuid, p_operator_label text, p_reason text DEFAULT NULL)
RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $$
DECLARE
  v_old boolean;
  -- Advisory-lock namespace for Phase-6 per-appliance product settings, following the existing convention of
  -- a fixed namespace per contended resource (Phase 1A recorded LN_DEVICE_SLOT=11 and LN_CAPACITY=7).
  c_ln_appliance_setting CONSTANT int := 61;
BEGIN
  IF p_operator_label IS NULL OR btrim(p_operator_label) = '' THEN
    RAISE EXCEPTION 'an operator label is required: "somebody changed it" is not an audit record'
      USING ERRCODE = 'invalid_parameter_value';
  END IF;

  -- THE APPLIANCE IS THE ANCHOR. The settings row may not exist yet, so locking it would order nothing; the
  -- appliance identity always exists because the settings row is foreign-keyed to it. Transaction-scoped, so
  -- it is released at commit or rollback without any explicit unlock path to forget.
  PERFORM pg_advisory_xact_lock(c_ln_appliance_setting, hashtext(p_appliance::text));

  -- The row lock is still taken when the row DOES exist: the advisory lock orders the writers, and this keeps
  -- the ordinary row-level contract for anything else that touches the row.
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
  'The ONLY way a runtime role may change the per-appliance setting. Change and audit are one operation, so '
  'the audit is mandatory by PRIVILEGE rather than convention. Mutations are serialized per APPLIANCE on a '
  'transaction advisory lock, because the settings row may not exist yet and a lock on an absent row orders '
  'nothing -- two concurrent first writes would each record a transition from "unset".';

REVOKE EXECUTE ON FUNCTION iam_v2.p6_set_guest_device_self_service(uuid, uuid, uuid, boolean, uuid, text, text) FROM PUBLIC;
GRANT  EXECUTE ON FUNCTION iam_v2.p6_set_guest_device_self_service(uuid, uuid, uuid, boolean, uuid, text, text) TO svc_edged;

-- ---------------------------------------------------------------------------------------------------------
-- 2. The action set now says what is actually recorded
-- ---------------------------------------------------------------------------------------------------------
-- No row has ever carried 'LIST', so narrowing the CHECK removes a claim rather than data. Asserted rather
-- than assumed, because a constraint narrowed over live rows is a failed migration at best.
DO $$
DECLARE n bigint;
BEGIN
  SELECT count(*) INTO n FROM iam_v2.guest_device_actions WHERE action = 'LIST';
  IF n > 0 THEN
    RAISE EXCEPTION 'refusing to narrow the action set: % LIST row(s) exist', n
      USING ERRCODE = 'restrict_violation';
  END IF;
END $$;

ALTER TABLE iam_v2.guest_device_actions DROP CONSTRAINT IF EXISTS guest_device_actions_action_check;
ALTER TABLE iam_v2.guest_device_actions ADD CONSTRAINT guest_device_actions_action_check
  CHECK (action IN ('RELEASE'));

COMMENT ON TABLE iam_v2.guest_device_actions IS
  'Every guest-initiated action that CHANGES durable device state, refusals included. RELEASE is the only '
  'such action: listing reads and changes nothing. An earlier version of this schema also named LIST, which '
  'was a standing claim that a guest''s list requests were investigable when no path recorded one -- and '
  'after the Phase-6 privilege audit no path can, because the guest surface holds no write on this table.';

INSERT INTO public.schema_migrations (version) VALUES ('0035_phase6_setting_serialization_and_list_audit')
  ON CONFLICT (version) DO NOTHING;

COMMIT;
