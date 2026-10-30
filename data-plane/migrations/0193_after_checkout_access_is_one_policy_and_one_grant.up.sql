-- 0193 -- AFTER CHECK-OUT ACCESS IS ONE POLICY AND ONE GRANT (Product Owner decision, 2026-10-30).
--
-- Checkout Grace and Post-Stay access are one feature. The authoritative, versioned policy is the existing site grace
-- configuration (iam_v2.site_checkout_grace_config, published through iam_v2.publish_checkout_grace_policy, history in
-- iam_v2.checkout_grace_policy_publications); there is no second duration, package or default. This migration adds only
-- what the one feature still lacked and closes the paths that could still make a second benefit. See
-- docs/architecture/ONEGATE_AFTER_CHECKOUT_ACCESS.md.
--
--   1. site_checkout_grace_config.after_checkout_enabled (default true: every existing site keeps today's behaviour, which
--      is a grant at every checkout, from the published policy or from the built-in fallback). Changed only by
--      iam_v2.set_after_checkout_enabled (operator, reason code, expected version, history row, version bump).
--      iam_v2.p3_after_checkout_enabled reads it for the checkout conversion, inside the transaction that already holds the
--      configuration row lock.
--   2. A device may enter a GRACE_AFTER_CHECKOUT entitlement only if the checkout conversion carried it over (a device that
--      was authorised on the Stay at the boundary, online or offline). A device that is not on it cannot be added by any
--      route; a device that left it (staff release, guest self-service, transfer) cannot come back. Only a system
--      suspension may be undone.
--   3. No new purchase can carry the trigger POST_STAY_CONVERSION: the separate Post-Stay benefit is retired. Existing rows
--      (history, and entitlements already issued) are untouched and keep working until they end.
--
-- post_stay_signin_days (0179) is no longer read: the sign-in window is the After Check-out entitlement's own window. The
-- column, its function column and its change log are left in place as history (nothing is dropped or rewritten).
--
-- Additive, idempotent, reversible (the .down.sql removes the triggers, the functions and the column).

BEGIN;

ALTER TABLE iam_v2.site_checkout_grace_config
  ADD COLUMN IF NOT EXISTS after_checkout_enabled boolean NOT NULL DEFAULT true;

-- ---------------------------------------------------------------------------------------------------------
-- 1. The enable switch.
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION iam_v2.p3_after_checkout_enabled(p_tenant uuid, p_site uuid) RETURNS boolean
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
  SELECT COALESCE((SELECT g.after_checkout_enabled FROM iam_v2.site_checkout_grace_config g
                    WHERE g.tenant_id = p_tenant AND g.site_id = p_site), true);
$fn$;
REVOKE ALL ON FUNCTION iam_v2.p3_after_checkout_enabled(uuid,uuid) FROM PUBLIC;

-- The switch is one more policy field of the existing configuration, so it follows the existing version rule: a change
-- moves config_version by exactly one, and nothing moves it otherwise.
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
    OR NEW.eligibility_window_seconds IS DISTINCT FROM OLD.eligibility_window_seconds
    OR NEW.after_checkout_enabled IS DISTINCT FROM OLD.after_checkout_enabled);
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

-- The switch may be changed only inside the open grace_publication operation (set_after_checkout_enabled opens it).
CREATE OR REPLACE FUNCTION iam_v2.after_checkout_switch_guard() RETURNS trigger
    LANGUAGE plpgsql SET search_path TO 'iam_v2', 'pg_temp' AS $$
BEGIN
  IF NEW.after_checkout_enabled IS DISTINCT FROM OLD.after_checkout_enabled
     AND NOT iam_v2.p3_controlled_operation_open('grace_publication') THEN
    RAISE EXCEPTION 'after_checkout_enabled changes only through iam_v2.set_after_checkout_enabled (caller %)', current_user;
  END IF;
  RETURN NEW;
END $$;
REVOKE ALL ON FUNCTION iam_v2.after_checkout_switch_guard() FROM PUBLIC;
DROP TRIGGER IF EXISTS after_checkout_switch_guard ON iam_v2.site_checkout_grace_config;
CREATE TRIGGER after_checkout_switch_guard BEFORE UPDATE ON iam_v2.site_checkout_grace_config
  FOR EACH ROW EXECUTE FUNCTION iam_v2.after_checkout_switch_guard();

CREATE OR REPLACE FUNCTION iam_v2.set_after_checkout_enabled(
    p_tenant uuid, p_site uuid, p_enabled boolean, p_expected_version integer, p_actor uuid, p_reason text)
  RETURNS integer
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE v_current int; v_new int; v_now boolean;
BEGIN
  PERFORM iam_v2.begin_controlled_operation('grace_publication');
  IF p_actor IS NULL THEN RAISE EXCEPTION 'GRACE_ACTOR_INVALID: an actor is required'; END IF;
  PERFORM 1 FROM public.operators o
    WHERE o.id = p_actor AND o.status = 'active' AND (o.tenant_id IS NULL OR o.tenant_id = p_tenant);
  IF NOT FOUND THEN
    RAISE EXCEPTION 'GRACE_ACTOR_INVALID: actor % is not an active operator of this tenant', p_actor;
  END IF;
  IF p_reason IS NULL OR p_reason !~ '^[A-Z][A-Z0-9_]{0,63}$' THEN
    RAISE EXCEPTION 'GRACE_ACTOR_INVALID: a bounded machine reason code is required';
  END IF;
  IF p_enabled IS NULL THEN RAISE EXCEPTION 'GRACE_POLICY_UNSUPPORTED: enabled must be true or false'; END IF;
  IF p_expected_version IS NULL OR p_expected_version < 0 THEN
    RAISE EXCEPTION 'GRACE_VERSION_CONFLICT: an expected config_version (>= 0) is required';
  END IF;
  PERFORM pg_advisory_xact_lock(hashtextextended(p_tenant::text || ':' || p_site::text || ':grace-config', 0));
  SELECT config_version, after_checkout_enabled INTO v_current, v_now FROM iam_v2.site_checkout_grace_config
    WHERE tenant_id = p_tenant AND site_id = p_site FOR UPDATE;
  IF COALESCE(v_current, 0) <> p_expected_version THEN
    RAISE EXCEPTION 'GRACE_VERSION_CONFLICT: current version is % (caller expected %)', COALESCE(v_current, 0), p_expected_version;
  END IF;
  IF v_current IS NULL THEN
    -- Nothing published yet: the row holds only the switch (the typed policy fields are all NULL, which the table allows).
    IF p_enabled THEN RETURN 0; END IF;   -- the default already is enabled: nothing to record
    INSERT INTO iam_v2.site_checkout_grace_config (tenant_id, site_id, after_checkout_enabled, config_version)
      VALUES (p_tenant, p_site, p_enabled, 1);
    v_new := 1;
  ELSE
    IF v_now = p_enabled THEN RETURN v_current; END IF;   -- idempotent: no new version, no history row
    UPDATE iam_v2.site_checkout_grace_config SET after_checkout_enabled = p_enabled, config_version = v_current + 1
      WHERE tenant_id = p_tenant AND site_id = p_site;
    v_new := v_current + 1;
  END IF;
  INSERT INTO iam_v2.checkout_grace_policy_publications
    (tenant_id, site_id, config_version, actor, reason_code, grace_package_revision_id, policy_snapshot)
    SELECT p_tenant, p_site, v_new, p_actor, p_reason, g.grace_package_revision_id,
           jsonb_build_object('change', 'AFTER_CHECKOUT_ENABLED', 'after_checkout_enabled', p_enabled,
                              'grace_duration_seconds', g.grace_duration_seconds, 'grace_down_kbps', g.grace_down_kbps,
                              'grace_up_kbps', g.grace_up_kbps, 'grace_data_quota_bytes', g.grace_data_quota_bytes,
                              'grace_device_limit', g.grace_device_limit,
                              'grace_device_limit_policy', g.grace_device_limit_policy,
                              'eligibility_window_seconds', g.eligibility_window_seconds)
      FROM iam_v2.site_checkout_grace_config g WHERE g.tenant_id = p_tenant AND g.site_id = p_site;
  RETURN v_new;
END $fn$;
REVOKE ALL ON FUNCTION iam_v2.set_after_checkout_enabled(uuid,uuid,boolean,integer,uuid,text) FROM PUBLIC;

-- ---------------------------------------------------------------------------------------------------------
-- 2. Only a device the checkout carried over may be on an After Check-out entitlement.
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION iam_v2.after_checkout_device_guard() RETURNS trigger
    LANGUAGE plpgsql SET search_path TO 'iam_v2', 'pg_temp' AS $$
DECLARE v_mode text;
BEGIN
  SELECT e.end_mode INTO v_mode FROM iam_v2.entitlements e WHERE e.id = NEW.entitlement_id;
  IF v_mode IS DISTINCT FROM 'GRACE_AFTER_CHECKOUT' THEN RETURN NEW; END IF;
  IF TG_OP = 'INSERT' THEN
    -- The checkout conversion marks the devices it carries over as grandfathered. authorize_entitlement_device inserts with
    -- ON CONFLICT DO UPDATE, so an existing row reaches the UPDATE rule below instead of failing here.
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
DROP TRIGGER IF EXISTS after_checkout_device_guard ON iam_v2.entitlement_devices;
CREATE TRIGGER after_checkout_device_guard BEFORE INSERT OR UPDATE ON iam_v2.entitlement_devices
  FOR EACH ROW EXECUTE FUNCTION iam_v2.after_checkout_device_guard();

-- ---------------------------------------------------------------------------------------------------------
-- 3. The separate Post-Stay benefit is retired: no new purchase may carry it.
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION iam_v2.post_stay_conversion_retired() RETURNS trigger
    LANGUAGE plpgsql SET search_path TO 'iam_v2', 'pg_temp' AS $$
BEGIN
  IF NEW."trigger" = 'POST_STAY_CONVERSION' THEN
    RAISE EXCEPTION 'POST_STAY_CONVERSION_RETIRED: the separate post-stay benefit no longer exists; After Check-out Access is granted once, at check-out'
      USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END $$;
REVOKE ALL ON FUNCTION iam_v2.post_stay_conversion_retired() FROM PUBLIC;
DROP TRIGGER IF EXISTS post_stay_conversion_retired ON iam_v2.purchases;
CREATE TRIGGER post_stay_conversion_retired BEFORE INSERT ON iam_v2.purchases
  FOR EACH ROW EXECUTE FUNCTION iam_v2.post_stay_conversion_retired();

DO $own$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
    EXECUTE 'ALTER FUNCTION iam_v2.p3_after_checkout_enabled(uuid,uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.set_after_checkout_enabled(uuid,uuid,boolean,integer,uuid,text) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.after_checkout_switch_guard() OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.after_checkout_device_guard() OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.post_stay_conversion_retired() OWNER TO iam_v2_owner';
  END IF;
END $own$;

DO $g$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_pmsd') THEN
    GRANT EXECUTE ON FUNCTION iam_v2.p3_after_checkout_enabled(uuid,uuid) TO svc_pmsd;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
    GRANT EXECUTE ON FUNCTION iam_v2.p3_after_checkout_enabled(uuid,uuid) TO svc_edged;
    GRANT EXECUTE ON FUNCTION iam_v2.set_after_checkout_enabled(uuid,uuid,boolean,integer,uuid,text) TO svc_edged;
  END IF;
END $g$;

COMMIT;
