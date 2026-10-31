-- 0202 down: removes staff End/Reset Package and restores the 0193 After Check-out device guard. The action rows are dropped with
-- their table; the entitlements and purchases a reset created stay (they are ordinary entitlement history).
BEGIN;

DROP FUNCTION IF EXISTS iam_v2.admin_reset_package(uuid, uuid, uuid, uuid, text, uuid);
DROP FUNCTION IF EXISTS iam_v2.admin_end_package(uuid, uuid, uuid, uuid, text, uuid);
DROP FUNCTION IF EXISTS iam_v2.package_admin_history(uuid, uuid, uuid);
DROP FUNCTION IF EXISTS iam_v2.package_control_list(uuid, uuid, text, uuid);
DROP FUNCTION IF EXISTS iam_v2.package_control_preview(uuid, uuid, uuid);
DROP FUNCTION IF EXISTS iam_v2.package_reset_window(uuid, timestamptz);
DROP FUNCTION IF EXISTS iam_v2.package_reset_refusal(uuid);
DROP FUNCTION IF EXISTS iam_v2.package_admin_check_actor(uuid, uuid, text);
DROP TABLE IF EXISTS iam_v2.package_admin_actions;
DROP FUNCTION IF EXISTS iam_v2.package_admin_actions_append_only();

CREATE OR REPLACE FUNCTION iam_v2.after_checkout_device_guard() RETURNS trigger
    LANGUAGE plpgsql SET search_path TO 'iam_v2', 'pg_temp' AS $$
DECLARE v_mode text;
BEGIN
  SELECT e.end_mode INTO v_mode FROM iam_v2.entitlements e WHERE e.id = NEW.entitlement_id;
  IF v_mode IS DISTINCT FROM 'GRACE_AFTER_CHECKOUT' THEN RETURN NEW; END IF;
  IF TG_OP = 'INSERT' THEN
    IF NEW.grandfathered THEN RETURN NEW; END IF;
    IF EXISTS (SELECT 1 FROM iam_v2.entitlement_devices d
                WHERE d.entitlement_id = NEW.entitlement_id AND d.device_id = NEW.device_id) THEN
      RETURN NEW;
    END IF;
    RAISE EXCEPTION 'AFTER_CHECKOUT_NEW_DEVICE_REJECTED: a device that was not authorised on the stay before check-out cannot be added to its After Check-out entitlement'
      USING ERRCODE = 'check_violation';
  END IF;
  IF OLD.status IS DISTINCT FROM 'AUTHORIZED' AND NEW.status = 'AUTHORIZED'
     AND COALESCE(OLD.disconnected_reason, '') <> 'ENTITLEMENT_SUSPENDED' THEN
    RAISE EXCEPTION 'AFTER_CHECKOUT_NEW_DEVICE_REJECTED: a device that left an After Check-out entitlement cannot rejoin it'
      USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END $$;
REVOKE ALL ON FUNCTION iam_v2.after_checkout_device_guard() FROM PUBLIC;

COMMIT;
