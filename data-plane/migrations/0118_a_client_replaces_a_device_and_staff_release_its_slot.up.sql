-- 0118 -- A CLIENT REPLACES A DEVICE, AND STAFF RELEASE ITS SLOT
-- (contract docs/architecture/ONEGATE_DEVICE_MANAGEMENT.md; migration approved by the Product Owner on 2026-10-15,
-- limited to device management).
--
-- A package allows max_concurrent_devices devices (service_plan_revisions). A device that has used a package holds
-- a slot on it -- an open interval in entitlement_device_authorizations -- and keeps it after it signs out, so a
-- client cannot hand the package to another device by signing out (the device limit would mean nothing). Until now
-- nothing could move a slot except the dark Phase-6 guest release, which only works from a device that already
-- holds one: the new device a client actually wants to use was simply refused. The database primitive that closes
-- an interval (deauthorize_entitlement_device) is, deliberately, unreachable by every runtime role.
--
-- This migration adds the three SCOPED, AUDITED ways a slot may move, and nothing else:
--
--   1. iam_v2.client_device_action  scd, for the client on the portal.
--        REPLACE  the device the client just signed in on takes the slot of one of their other devices, in ONE
--                 transaction (the freed slot cannot be taken by anyone else in between). Proof: an unexpired
--                 auth context issued to THIS device for the subject that owns the package.
--        RELEASE  from a device that is online on the package, free the slot of another, offline device.
--        Both: the target must be offline (scd ends its session through enforcement first), and at most
--        site_device_settings.client_device_changes_per_day successful changes per package per 24 h.
--   2. iam_v2.staff_release_device_slot  edged, for staff: free one device's slot on one package. Offline only;
--        an operator and a reason are required.
--   3. iam_v2.staff_require_device_signin  edged, for staff: forget the device's remembered sign-in ("Welcome
--        back"), so its next visit shows the full sign-in page.
--
-- None of them touches an entitlement's window, consumption, quota or ownership; every attempt (refusals too) is a
-- row in the append-only iam_v2.device_slot_actions. Sign-out still does not free a slot.
--
-- Plus the setting (docs/OPERATIONAL_SETTINGS_POLICY.md): iam_v2.site_device_settings, read and written only through
-- definer functions, with an append-only change log -- the 0117 shape.
--
-- Additive and idempotent. No existing row is changed.

BEGIN;

-- ---------------------------------------------------------------------------------------------------------
-- 1. The setting: how many device changes a client may make per package per day.
-- ---------------------------------------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS iam_v2.site_device_settings (
  tenant_id                     uuid NOT NULL,
  site_id                       uuid NOT NULL,
  client_device_changes_per_day integer NOT NULL DEFAULT 3
    CONSTRAINT sds_changes_per_day CHECK (client_device_changes_per_day BETWEEN 1 AND 10),
  config_version                bigint  NOT NULL DEFAULT 1 CONSTRAINT sds_version CHECK (config_version >= 1),
  updated_at                    timestamptz NOT NULL DEFAULT now(),
  updated_by                    text,
  PRIMARY KEY (tenant_id, site_id)
);
COMMENT ON TABLE iam_v2.site_device_settings IS
  'Per-site client device management (0118): how many times per package per 24 hours a client may replace or '
  'release one of their devices on the portal (default 3, hard bounds 1-10: the protection against passing one '
  'package around cannot be switched off). A site with no row reads the default. Written only through '
  'iam_v2.device_settings_set.';

CREATE TABLE IF NOT EXISTS iam_v2.device_settings_changes (
  id                                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id                         uuid NOT NULL,
  site_id                           uuid NOT NULL,
  changed_by                        text NOT NULL
    CONSTRAINT device_settings_changes_actor CHECK (length(btrim(changed_by)) > 0),
  change_reason                     text NOT NULL
    CONSTRAINT device_settings_changes_reason CHECK (length(btrim(change_reason)) > 0),
  old_client_device_changes_per_day integer,
  new_client_device_changes_per_day integer NOT NULL,
  new_config_version                bigint NOT NULL,
  changed_at                        timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS device_settings_changes_lookup
  ON iam_v2.device_settings_changes (tenant_id, site_id, changed_at DESC);

CREATE OR REPLACE FUNCTION iam_v2.device_settings_changes_append_only() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'iam_v2.device_settings_changes is append-only: % refused', TG_OP
    USING ERRCODE = 'restrict_violation';
END $$;
REVOKE EXECUTE ON FUNCTION iam_v2.device_settings_changes_append_only() FROM PUBLIC;
DROP TRIGGER IF EXISTS device_settings_changes_append_only ON iam_v2.device_settings_changes;
CREATE TRIGGER device_settings_changes_append_only
  BEFORE UPDATE OR DELETE ON iam_v2.device_settings_changes
  FOR EACH ROW EXECUTE FUNCTION iam_v2.device_settings_changes_append_only();

CREATE OR REPLACE FUNCTION iam_v2.device_settings_get(p_tenant uuid, p_site uuid)
  RETURNS TABLE (client_device_changes_per_day integer, config_version bigint, updated_at timestamptz,
                 updated_by text)
  LANGUAGE sql STABLE SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
  SELECT COALESCE(s.client_device_changes_per_day, 3), COALESCE(s.config_version, 0), s.updated_at, s.updated_by
    FROM (SELECT 1) one
    LEFT JOIN iam_v2.site_device_settings s ON s.tenant_id = p_tenant AND s.site_id = p_site;
$fn$;
REVOKE EXECUTE ON FUNCTION iam_v2.device_settings_get(uuid,uuid) FROM PUBLIC;

CREATE OR REPLACE FUNCTION iam_v2.device_settings_set(
    p_tenant uuid, p_site uuid, p_client_device_changes_per_day integer, p_operator text, p_reason text)
  RETURNS bigint
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE v_version bigint; v_old integer;
BEGIN
  IF p_operator IS NULL OR btrim(p_operator) = '' THEN
    RAISE EXCEPTION 'an operator label is required' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  IF p_reason IS NULL OR length(btrim(p_reason)) < 4 OR length(btrim(p_reason)) > 500 THEN
    RAISE EXCEPTION 'a reason of 4-500 characters is required to change the device settings'
      USING ERRCODE = 'invalid_parameter_value';
  END IF;
  IF p_client_device_changes_per_day IS NULL OR p_client_device_changes_per_day NOT BETWEEN 1 AND 10 THEN
    RAISE EXCEPTION 'client_device_changes_per_day must be 1-10 (got %)', p_client_device_changes_per_day
      USING ERRCODE = 'invalid_parameter_value';
  END IF;
  PERFORM pg_advisory_xact_lock(hashtext('device_settings'), hashtext(p_site::text));
  SELECT s.client_device_changes_per_day INTO v_old
    FROM iam_v2.site_device_settings s WHERE s.tenant_id = p_tenant AND s.site_id = p_site FOR UPDATE;
  INSERT INTO iam_v2.site_device_settings AS s (tenant_id, site_id, client_device_changes_per_day, updated_by)
  VALUES (p_tenant, p_site, p_client_device_changes_per_day, btrim(p_operator))
  ON CONFLICT (tenant_id, site_id) DO UPDATE
     SET client_device_changes_per_day = EXCLUDED.client_device_changes_per_day,
         updated_by = EXCLUDED.updated_by, config_version = s.config_version + 1, updated_at = now()
  RETURNING s.config_version INTO v_version;
  INSERT INTO iam_v2.device_settings_changes
    (tenant_id, site_id, changed_by, change_reason, old_client_device_changes_per_day,
     new_client_device_changes_per_day, new_config_version)
  VALUES (p_tenant, p_site, btrim(p_operator), btrim(p_reason), v_old, p_client_device_changes_per_day, v_version);
  RETURN v_version;
END $fn$;
REVOKE EXECUTE ON FUNCTION iam_v2.device_settings_set(uuid,uuid,integer,text,text) FROM PUBLIC;

-- ---------------------------------------------------------------------------------------------------------
-- 2. Every attempt to move a slot or forget a device's sign-in, refusals included. Append-only.
-- ---------------------------------------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS iam_v2.device_slot_actions (
  id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id             uuid NOT NULL,
  site_id               uuid NOT NULL,
  entitlement_id        uuid,
  -- The device acted on: the one whose slot is freed (REPLACE, RELEASE) or whose sign-in is forgotten.
  device_id             uuid,
  -- REPLACE: the device that took the slot.
  replacement_device_id uuid,
  actor                 text NOT NULL CONSTRAINT dsa_actor CHECK (actor IN ('CLIENT','STAFF')),
  operator_id           uuid,
  operator_label        text,
  action                text NOT NULL CONSTRAINT dsa_action CHECK (action IN ('REPLACE','RELEASE','REQUIRE_SIGNIN')),
  outcome               text NOT NULL CONSTRAINT dsa_outcome CHECK (outcome IN (
                          'OK', 'REFUSED_SIGNIN_EXPIRED', 'REFUSED_NO_PACKAGE', 'REFUSED_NOT_FOUND',
                          'REFUSED_ALREADY_RELEASED', 'REFUSED_ONLINE', 'REFUSED_THROTTLED', 'REFUSED_SAME_DEVICE',
                          'REFUSED_LIMIT_NOT_REACHED')),
  reason                text CONSTRAINT dsa_reason_len CHECK (reason IS NULL OR length(reason) <= 500),
  detail                text CONSTRAINT dsa_detail_len CHECK (detail IS NULL OR length(detail) <= 200),
  acted_at              timestamptz NOT NULL DEFAULT now(),
  -- A staff action names its operator and gives a reason; a client action has no operator.
  CONSTRAINT dsa_staff_named CHECK ((actor = 'STAFF') = (operator_label IS NOT NULL)),
  CONSTRAINT dsa_staff_reason CHECK (actor <> 'STAFF' OR reason IS NOT NULL)
);
CREATE INDEX IF NOT EXISTS dsa_entitlement ON iam_v2.device_slot_actions (entitlement_id, actor, acted_at DESC);
CREATE INDEX IF NOT EXISTS dsa_device ON iam_v2.device_slot_actions (tenant_id, site_id, device_id, acted_at DESC);
COMMENT ON TABLE iam_v2.device_slot_actions IS
  'Every client or staff attempt to replace or release a device slot, or to forget a device''s remembered '
  'sign-in (0118), with its outcome. Append-only; refusals are recorded too.';

CREATE OR REPLACE FUNCTION iam_v2.device_slot_actions_append_only() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'iam_v2.device_slot_actions is append-only: % refused', TG_OP USING ERRCODE = 'restrict_violation';
END $$;
REVOKE EXECUTE ON FUNCTION iam_v2.device_slot_actions_append_only() FROM PUBLIC;
DROP TRIGGER IF EXISTS device_slot_actions_append_only ON iam_v2.device_slot_actions;
CREATE TRIGGER device_slot_actions_append_only
  BEFORE UPDATE OR DELETE ON iam_v2.device_slot_actions
  FOR EACH ROW EXECUTE FUNCTION iam_v2.device_slot_actions_append_only();

-- ---------------------------------------------------------------------------------------------------------
-- 3. The client's action (scd).
-- ---------------------------------------------------------------------------------------------------------
-- p_auth_context  REPLACE: the unexpired auth context issued to p_device (the device asking) for the package's
--                 subject. RELEASE: NULL -- the package is the one p_device is online on.
-- p_target        the device whose slot is freed.
-- Returns the outcome and the package it applied to.
CREATE OR REPLACE FUNCTION iam_v2.client_device_action(
    p_tenant uuid, p_site uuid, p_action text, p_device uuid, p_auth_context uuid, p_target uuid)
  RETURNS TABLE (outcome text, entitlement_id uuid)
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE
  v_ac iam_v2.auth_contexts%ROWTYPE; v_ent uuid; v_limit int; v_open int; v_used int; v_allowed int;
  v_status text; v_online int; v_out text;
BEGIN
  IF p_action NOT IN ('REPLACE','RELEASE') THEN
    RAISE EXCEPTION 'action must be REPLACE or RELEASE' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  IF p_device IS NULL OR p_target IS NULL THEN
    RAISE EXCEPTION 'both devices are required' USING ERRCODE = 'invalid_parameter_value';
  END IF;

  -- WHICH PACKAGE. Never named by the caller: derived from proof the device holds.
  IF p_action = 'REPLACE' THEN
    SELECT * INTO v_ac FROM iam_v2.auth_contexts a
     WHERE a.id = p_auth_context AND a.tenant_id = p_tenant AND a.site_id = p_site;
    IF NOT FOUND OR v_ac.device_id <> p_device OR v_ac.expires_at <= now() THEN
      v_out := 'REFUSED_SIGNIN_EXPIRED';
    ELSE
      SELECT e.id INTO v_ent FROM iam_v2.entitlements e
       WHERE e.tenant_id = p_tenant AND e.site_id = p_site AND e.status = 'ACTIVE'
         AND ((v_ac.guest_account_id IS NOT NULL AND e.guest_account_id = v_ac.guest_account_id)
           OR (v_ac.voucher_id IS NOT NULL AND e.voucher_id = v_ac.voucher_id)
           OR (v_ac.stay_id IS NOT NULL AND e.stay_id = v_ac.stay_id)
           OR (v_ac.guest_principal_id IS NOT NULL AND e.guest_principal_id = v_ac.guest_principal_id)
           OR (v_ac.anonymous_subject_id IS NOT NULL AND e.anonymous_subject_id = v_ac.anonymous_subject_id))
       LIMIT 1;
      IF v_ent IS NULL THEN v_out := 'REFUSED_NO_PACKAGE'; END IF;
    END IF;
  ELSE
    SELECT s.entitlement_id INTO v_ent FROM iam_v2.sessions s
      JOIN iam_v2.entitlements e ON e.id = s.entitlement_id
     WHERE s.tenant_id = p_tenant AND s.site_id = p_site AND s.device_id = p_device
       AND s.state = 'active' AND s.ended IS NULL AND e.status = 'ACTIVE'
     ORDER BY s.started DESC LIMIT 1;
    IF v_ent IS NULL THEN v_out := 'REFUSED_NO_PACKAGE'; END IF;
  END IF;

  IF v_out IS NULL AND p_target = p_device THEN v_out := 'REFUSED_SAME_DEVICE'; END IF;

  IF v_out IS NULL THEN
    -- L3 lock first, as authorize/deauthorize take it: the count below and the move cannot interleave with
    -- another activation on this package.
    PERFORM 1 FROM iam_v2.entitlements WHERE id = v_ent FOR UPDATE;

    SELECT g.client_device_changes_per_day INTO v_allowed FROM iam_v2.device_settings_get(p_tenant, p_site) g;
    SELECT count(*) INTO v_used FROM iam_v2.device_slot_actions d
     WHERE d.entitlement_id = v_ent AND d.actor = 'CLIENT' AND d.outcome = 'OK'
       AND d.acted_at > now() - interval '24 hours';
    IF v_used >= v_allowed THEN v_out := 'REFUSED_THROTTLED'; END IF;
  END IF;

  IF v_out IS NULL AND p_action = 'REPLACE' THEN
    SELECT spr.max_concurrent_devices INTO v_limit
      FROM iam_v2.entitlements e JOIN iam_v2.service_plan_revisions spr ON spr.id = e.service_plan_revision_id
     WHERE e.id = v_ent;
    SELECT count(*) INTO v_open FROM iam_v2.entitlement_device_authorizations x
     WHERE x.entitlement_id = v_ent AND x.deauthorized_at IS NULL;
    -- A free slot needs no replacement (the device simply joins), and a device already holding a slot has
    -- nothing to take.
    IF v_limit IS NULL OR v_open < v_limit
       OR EXISTS (SELECT 1 FROM iam_v2.entitlement_device_authorizations x
                   WHERE x.entitlement_id = v_ent AND x.device_id = p_device AND x.deauthorized_at IS NULL) THEN
      v_out := 'REFUSED_LIMIT_NOT_REACHED';
    END IF;
  END IF;

  IF v_out IS NULL THEN
    SELECT ed.status INTO v_status FROM iam_v2.entitlement_devices ed
     WHERE ed.entitlement_id = v_ent AND ed.device_id = p_target;
    IF v_status IS NULL THEN v_out := 'REFUSED_NOT_FOUND';
    ELSIF v_status <> 'AUTHORIZED' THEN v_out := 'REFUSED_ALREADY_RELEASED';
    END IF;
  END IF;

  IF v_out IS NULL THEN
    -- Offline only. scd ends the target's session through enforcement (nft deny, session ended) before calling;
    -- a session still live here is one that came back in between, and the client is asked to try again.
    SELECT count(*) INTO v_online FROM iam_v2.sessions s
     WHERE s.entitlement_id = v_ent AND s.device_id = p_target AND s.state IN ('active','PENDING_ENFORCEMENT');
    IF v_online > 0 THEN v_out := 'REFUSED_ONLINE'; END IF;
  END IF;

  IF v_out IS NULL THEN
    IF NOT iam_v2.deauthorize_entitlement_device(v_ent, p_target, now(),
                                                 CASE p_action WHEN 'REPLACE' THEN 'CLIENT_REPLACED' ELSE 'CLIENT_RELEASED' END) THEN
      v_out := 'REFUSED_ALREADY_RELEASED';
    ELSE
      IF p_action = 'REPLACE' THEN
        -- The freed slot goes to the device that asked, in this transaction: nobody else can take it first.
        PERFORM iam_v2.authorize_entitlement_device(v_ent, p_device, now());
      END IF;
      v_out := 'OK';
    END IF;
  END IF;

  INSERT INTO iam_v2.device_slot_actions
    (tenant_id, site_id, entitlement_id, device_id, replacement_device_id, actor, action, outcome)
  VALUES (p_tenant, p_site, v_ent, p_target, CASE WHEN p_action = 'REPLACE' THEN p_device END, 'CLIENT',
          p_action, v_out);
  RETURN QUERY SELECT v_out, v_ent;
END $fn$;
REVOKE EXECUTE ON FUNCTION iam_v2.client_device_action(uuid,uuid,text,uuid,uuid,uuid) FROM PUBLIC;
COMMENT ON FUNCTION iam_v2.client_device_action(uuid,uuid,text,uuid,uuid,uuid) IS
  'A client replaces (REPLACE, from a new device with a fresh sign-in) or releases (RELEASE, from a device online on '
  'the package) one of their devices'' slots (0118). Offline targets only; limited per package per day by '
  'site_device_settings; every attempt recorded in device_slot_actions. Never changes the package''s window, '
  'consumption or ownership.';

-- ---------------------------------------------------------------------------------------------------------
-- 4. Staff actions (edged).
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION iam_v2.staff_release_device_slot(
    p_tenant uuid, p_site uuid, p_entitlement uuid, p_device uuid, p_operator uuid, p_operator_label text,
    p_reason text)
  RETURNS text
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE v_status text; v_online int; v_out text;
BEGIN
  IF p_operator_label IS NULL OR btrim(p_operator_label) = '' THEN
    RAISE EXCEPTION 'an operator label is required' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  IF p_reason IS NULL OR length(btrim(p_reason)) < 4 OR length(btrim(p_reason)) > 500 THEN
    RAISE EXCEPTION 'a reason of 4-500 characters is required' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  PERFORM 1 FROM iam_v2.entitlements e
   WHERE e.id = p_entitlement AND e.tenant_id = p_tenant AND e.site_id = p_site FOR UPDATE;
  IF NOT FOUND THEN
    v_out := 'REFUSED_NO_PACKAGE';
  ELSE
    SELECT ed.status INTO v_status FROM iam_v2.entitlement_devices ed
     WHERE ed.entitlement_id = p_entitlement AND ed.device_id = p_device;
    IF v_status IS NULL THEN v_out := 'REFUSED_NOT_FOUND';
    ELSIF v_status <> 'AUTHORIZED' THEN v_out := 'REFUSED_ALREADY_RELEASED';
    ELSE
      SELECT count(*) INTO v_online FROM iam_v2.sessions s
       WHERE s.entitlement_id = p_entitlement AND s.device_id = p_device
         AND s.state IN ('active','PENDING_ENFORCEMENT');
      IF v_online > 0 THEN v_out := 'REFUSED_ONLINE';
      ELSIF iam_v2.deauthorize_entitlement_device(p_entitlement, p_device, now(), 'STAFF_RELEASED') THEN
        v_out := 'OK';
      ELSE v_out := 'REFUSED_ALREADY_RELEASED';
      END IF;
    END IF;
  END IF;
  INSERT INTO iam_v2.device_slot_actions
    (tenant_id, site_id, entitlement_id, device_id, actor, operator_id, operator_label, action, outcome, reason)
  VALUES (p_tenant, p_site, CASE WHEN v_out <> 'REFUSED_NO_PACKAGE' THEN p_entitlement END, p_device, 'STAFF',
          p_operator, btrim(p_operator_label), 'RELEASE', v_out, btrim(p_reason));
  RETURN v_out;
END $fn$;
REVOKE EXECUTE ON FUNCTION iam_v2.staff_release_device_slot(uuid,uuid,uuid,uuid,uuid,text,text) FROM PUBLIC;
COMMENT ON FUNCTION iam_v2.staff_release_device_slot(uuid,uuid,uuid,uuid,uuid,text,text) IS
  'Staff free one device''s slot on one package (0118). Offline devices only (edged disconnects first); reason '
  'required; recorded in device_slot_actions. The package''s window, consumption and ownership are untouched.';

CREATE OR REPLACE FUNCTION iam_v2.staff_require_device_signin(
    p_tenant uuid, p_site uuid, p_device uuid, p_operator uuid, p_operator_label text, p_reason text)
  RETURNS integer
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE v_n integer;
BEGIN
  IF p_operator_label IS NULL OR btrim(p_operator_label) = '' THEN
    RAISE EXCEPTION 'an operator label is required' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  IF p_reason IS NULL OR length(btrim(p_reason)) < 4 OR length(btrim(p_reason)) > 500 THEN
    RAISE EXCEPTION 'a reason of 4-500 characters is required' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  PERFORM 1 FROM iam_v2.devices d WHERE d.id = p_device AND d.tenant_id = p_tenant AND d.site_id = p_site;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'device % is not at this site', p_device USING ERRCODE = 'foreign_key_violation';
  END IF;
  -- The device-bound remembered sign-in ("Welcome back"). Revoked, never deleted.
  UPDATE iam_v2.principal_device_credentials c SET revoked_at = now()
   WHERE c.tenant_id = p_tenant AND c.site_id = p_site AND c.device_id = p_device AND c.revoked_at IS NULL;
  GET DIAGNOSTICS v_n = ROW_COUNT;
  INSERT INTO iam_v2.device_slot_actions
    (tenant_id, site_id, device_id, actor, operator_id, operator_label, action, outcome, reason, detail)
  VALUES (p_tenant, p_site, p_device, 'STAFF', p_operator, btrim(p_operator_label), 'REQUIRE_SIGNIN', 'OK',
          btrim(p_reason), v_n || ' remembered sign-in(s) revoked');
  RETURN v_n;
END $fn$;
REVOKE EXECUTE ON FUNCTION iam_v2.staff_require_device_signin(uuid,uuid,uuid,uuid,text,text) FROM PUBLIC;
COMMENT ON FUNCTION iam_v2.staff_require_device_signin(uuid,uuid,uuid,uuid,text,text) IS
  'Staff make a device sign in afresh (0118): its remembered sign-in is revoked (edged also ends its sessions '
  'through enforcement). Slots and packages are untouched; recorded in device_slot_actions.';

-- ---------------------------------------------------------------------------------------------------------
-- 5. Least privilege, mirrored in deploy/gatep/svc-device-management-grants.sql.
-- ---------------------------------------------------------------------------------------------------------
DO $g$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_scd') THEN
    GRANT EXECUTE ON FUNCTION iam_v2.client_device_action(uuid,uuid,text,uuid,uuid,uuid) TO svc_scd;
    GRANT EXECUTE ON FUNCTION iam_v2.device_settings_get(uuid,uuid) TO svc_scd;
    -- To tell the client how many changes are left today, from the same rows the function counts.
    GRANT SELECT (entitlement_id, actor, outcome, acted_at) ON iam_v2.device_slot_actions TO svc_scd;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
    GRANT EXECUTE ON FUNCTION iam_v2.device_settings_get(uuid,uuid) TO svc_edged;
    GRANT EXECUTE ON FUNCTION iam_v2.device_settings_set(uuid,uuid,integer,text,text) TO svc_edged;
    GRANT EXECUTE ON FUNCTION iam_v2.staff_release_device_slot(uuid,uuid,uuid,uuid,uuid,text,text) TO svc_edged;
    GRANT EXECUTE ON FUNCTION iam_v2.staff_require_device_signin(uuid,uuid,uuid,uuid,text,text) TO svc_edged;
    GRANT SELECT ON iam_v2.device_slot_actions, iam_v2.device_settings_changes TO svc_edged;
    -- Whether a device has a remembered sign-in, never the secret.
    GRANT SELECT (id, tenant_id, site_id, device_id, method, created_at, expires_at, last_used_at, revoked_at)
      ON iam_v2.principal_device_credentials TO svc_edged;
  END IF;
END $g$;

COMMIT;
