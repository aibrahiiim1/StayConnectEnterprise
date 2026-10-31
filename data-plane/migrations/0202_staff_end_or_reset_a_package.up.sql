-- 0202 -- STAFF END OR RESET A PACKAGE (Product Owner mission, 2026-10-31).
--
-- Two administrative operations on any Internet Entitlement (a PMS Stay / room package, After Check-out Access, a Client Account
-- package, a free, voucher-acquired, cash, card or PMS-settled package):
--
--   END PACKAGE   iam_v2.admin_end_package ends the WHOLE entitlement with reason ADMIN through the one end-access primitive that
--                 already exists (iam_v2.p4_end_access_funded_by: boundary termination, every open device interval closed, every
--                 active or pending session ended). It returns the sessions it ended so the caller can withdraw their kernel
--                 authorisation at once (scd /v1/sessions/withdraw); enforcement also converges from these rows on its own.
--   RESET PACKAGE iam_v2.admin_reset_package grants a NEW cycle of the same package as a new, explicitly linked entitlement
--                 (supersedes_entitlement_id) funded by a ZERO-PRICE purchase with trigger ADMIN_GRANT. It never zeroes a counter
--                 and never reopens a terminal row: the original keeps its usage, its purchase and its settlement exactly as they
--                 are. A live original is terminated SUPERSEDED in the same transaction and its authorised devices and live
--                 sessions move to the new entitlement through rebind_session_entitlement (accounting follows the instant).
--
-- MONEY. Nothing here creates, retries or reverses a payment, a charge, a PMS posting, a refund or a voucher redemption. The reset
-- purchase is amount_minor = 0, state GRANTED, no quote, no settlement mapping, no currency: no settlement or posting path reads
-- it (they start from a settlement or a quote, neither of which exists for it).
--
-- WHAT A RESET RESTORES. The pinned package and service-plan revisions (speed, device limit and policy, time mode, quota), the
-- frozen data allowance (data_quota_bytes, NULL meaning the plan's), the end mode and the policy snapshot. A duration-based window
-- (VALIDITY_WINDOW, GRACE_AFTER_CHECKOUT) restarts at the reset instant with the original length; a window fixed to an instant
-- (FIXED_AT, AT_CHECKOUT, EARLIEST_OF_FIXED_AND_CHECKOUT) keeps that instant and is refused once it has passed; no window stays no
-- window (non-expiring).
--
-- WHAT A RESET NEVER OVERRIDES. An entitlement SUSPENDED for an independent reason (a disabled Client Account, a system suspension),
-- one ended REVOKED (a refund or security revocation), TRANSFERRED, CONVERTED, CANCELLED or ended by CHECKOUT (the PMS boundary), a
-- disabled Client Account, a voucher that is REVOKED, a room package whose Stay is no longer IN_HOUSE, an entitlement already
-- superseded (reset or renewed: reset the current one), or a subject that already holds other live access. Licence enforcement
-- stays in admission (scd), which a reset does not bypass: it admits nobody new.
--
-- AFTER CHECK-OUT. The eligible-device cohort is fixed. A reset of a GRACE_AFTER_CHECKOUT entitlement carries only the devices the
-- checkout carried over (grandfathered rows); a device that left the original by a deliberate act (staff release, guest
-- self-service, transfer, a newer device) is not carried. A carried device that is not online is recorded DISCONNECTED with reason
-- PACKAGE_RESET_COHORT, and after_checkout_device_guard is extended to let exactly such a row return when the device signs in; a
-- new device, or a rotated MAC without that lineage, is still refused. The reset is an ADMIN_GRANT purchase, so it is not another
-- automatic After Check-out grant; the checkout event still makes at most one.
--
-- CONCURRENCY AND IDEMPOTENCY. Every request carries a client-generated request id (unique in iam_v2.package_admin_actions): a
-- repeated request returns the recorded outcome and changes nothing; a request id reused for a different action or entitlement is
-- refused. The request id is serialised with an advisory lock, then the Stay (if any) and the entitlement are locked in that order,
-- the order every other entitlement writer uses. Two different requests to reset the same entitlement: the second finds it
-- superseded and is refused. Refusals are raised as 'PKG_<CODE>: ...' and mapped to operator messages by the caller.
--
-- NO AUTOMATIC RE-ACQUISITION AFTER AN END (Product Owner, 2026-11-01). A free benefit staff ended is not taken again by the
-- same subject through an automatic or self-service free acquisition (GUEST_SELECTION, ACCOUNT_AUTO_GRANT, OTP_SOCIAL_DEFAULT,
-- VOUCHER_REDEMPTION at price 0): iam_v2.package_ended_by_staff is the one rule, enforced by the entitlement guard
-- staff_ended_benefit_guard and read by the offer list and the quote so a guest is never shown what the grant refuses.
--   * the same package (any revision), the same subject; an anonymous subject is matched by the devices the ended package had;
--   * a room package: for the rest of that Stay; a later, independent Stay is a different subject and is not affected;
--   * any other subject: until the moment the ended package would have ended anyway (its window when staff ended it); one
--     without a window stays ended until staff reset it;
--   * a priced purchase is never blocked, nor a staff grant, After Check-out, transfer, conversion or renewal;
--   * a staff Reset of the ended package lifts the block (Reset is the deliberate way back).
--
-- The operator id and the bounded reason are stored; no guest name, room or reservation text is written by this code. The reason
-- is staff free text, so it is a registered staff note (0147 scrub registry): the staff-notes period removes it, and the three
-- erasure functions (anonymise_guest_account, anonymise_guest_principal, anonymise_stay, re-declared here from their 0147 text
-- with one statement added) remove it with the guest's other staff notes. The operator id is not a person's text.
--
-- Additive and reversible: the .down.sql drops what this adds and restores after_checkout_device_guard. No data is touched.
BEGIN;

CREATE TABLE IF NOT EXISTS iam_v2.package_admin_actions (
  id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id          uuid NOT NULL,
  site_id            uuid NOT NULL,
  request_id         uuid NOT NULL UNIQUE,
  action             text NOT NULL CHECK (action IN ('END','RESET')),
  entitlement_id     uuid NOT NULL,
  new_entitlement_id uuid,
  purchase_id        uuid,
  outcome            text NOT NULL CHECK (outcome IN ('ENDED','ALREADY_ENDED','RESET')),
  actor              uuid NOT NULL,
  reason             text NOT NULL CHECK (length(reason) BETWEEN 4 AND 500),
  sessions           uuid[] NOT NULL DEFAULT '{}',
  devices            integer NOT NULL DEFAULT 0,
  window_ends_at     timestamptz,
  created_at         timestamptz NOT NULL DEFAULT now(),
  CHECK ((action = 'RESET') = (new_entitlement_id IS NOT NULL)),
  CHECK ((action = 'RESET') = (purchase_id IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS package_admin_actions_entitlement ON iam_v2.package_admin_actions (entitlement_id);
CREATE INDEX IF NOT EXISTS package_admin_actions_new_entitlement ON iam_v2.package_admin_actions (new_entitlement_id)
  WHERE new_entitlement_id IS NOT NULL;
COMMENT ON TABLE iam_v2.package_admin_actions IS
  'Staff End Package / Reset Package (0202): one append-only row per request, keyed by the request id (idempotency).';

CREATE OR REPLACE FUNCTION iam_v2.package_admin_actions_append_only() RETURNS trigger
  LANGUAGE plpgsql SET search_path = iam_v2, pg_temp AS $f$
BEGIN
  RAISE EXCEPTION 'package admin actions are append-only' USING ERRCODE = 'insufficient_privilege';
END $f$;
-- Append-only, except the registered staff note scrub (0147): while the transaction-local scrub is on, zz_scrub_guard admits an
-- UPDATE of the reason alone, made by the table's owner, and nothing else.
DROP TRIGGER IF EXISTS package_admin_actions_append_only ON iam_v2.package_admin_actions;
CREATE TRIGGER package_admin_actions_append_only BEFORE UPDATE OR DELETE ON iam_v2.package_admin_actions
  FOR EACH ROW WHEN (current_setting('iam_v2.scrub', true) IS DISTINCT FROM 'on')
  EXECUTE FUNCTION iam_v2.package_admin_actions_append_only();
DROP TRIGGER IF EXISTS zz_scrub_guard ON iam_v2.package_admin_actions;
CREATE TRIGGER zz_scrub_guard BEFORE UPDATE OR DELETE ON iam_v2.package_admin_actions
  FOR EACH ROW WHEN (current_setting('iam_v2.scrub', true) = 'on') EXECUTE FUNCTION iam_v2.scrub_guard();
INSERT INTO iam_v2.scrub_registry (table_name, column_name, kind, ts_column, handled, jsonb_key) VALUES
  ('iam_v2.package_admin_actions', 'reason', 'note', 'created_at', false, NULL)
ON CONFLICT (table_name, column_name) DO NOTHING;

-- ---------------------------------------------------------------------------------------------------------
-- The one eligibility rule for a reset, read by the preview and re-applied under the locks by the reset.
-- Returns NULL when a reset is allowed, otherwise 'CODE: explanation'.
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION iam_v2.package_reset_refusal(p_ent uuid) RETURNS text
  LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $f$
DECLARE e record; v_stay text; v_enabled boolean; v_voucher text;
BEGIN
  SELECT * INTO e FROM iam_v2.entitlements WHERE id = p_ent;
  IF NOT FOUND THEN
    RETURN 'NOT_FOUND: the package does not exist on this site';
  END IF;
  IF EXISTS (SELECT 1 FROM iam_v2.entitlements s WHERE s.supersedes_entitlement_id = p_ent) THEN
    RETURN 'SUPERSEDED: this package was already reset or renewed; reset the current package instead';
  END IF;
  IF e.status = 'PENDING' OR e.activated_at IS NULL THEN
    RETURN 'NOT_STARTED: this package has not started yet, so there is nothing to reset';
  END IF;
  IF e.status = 'SUSPENDED' THEN
    RETURN 'SUSPENDED: this package is suspended for another reason (for example a disabled Client Account); resolve that first';
  END IF;
  IF e.status = 'TERMINATED' AND e.terminal_reason NOT IN ('TIME','DATA','HARD_EXPIRY','ADMIN') THEN
    RETURN 'NOT_RESETTABLE: a package that ended by ' || e.terminal_reason || ' cannot be reset (only one that expired, ran out or was ended by staff)';
  END IF;
  IF e.stay_id IS NOT NULL THEN
    SELECT status INTO v_stay FROM iam_v2.stays WHERE id = e.stay_id;
    IF e.end_mode = 'GRACE_AFTER_CHECKOUT' THEN
      IF v_stay IS DISTINCT FROM 'CHECKED_OUT' AND v_stay IS DISTINCT FROM 'POST_STAY_ACTIVE' THEN
        RETURN 'STAY_NOT_CHECKED_OUT: After Check-out Access can be reset only for a stay that has checked out';
      END IF;
    ELSIF v_stay IS DISTINCT FROM 'IN_HOUSE' THEN
      RETURN 'STAY_NOT_IN_HOUSE: a room package can be reset only while its stay is in house';
    END IF;
  END IF;
  IF e.guest_account_id IS NOT NULL THEN
    SELECT enabled INTO v_enabled FROM iam_v2.guest_access_accounts WHERE id = e.guest_account_id;
    IF v_enabled IS DISTINCT FROM true THEN
      RETURN 'ACCOUNT_DISABLED: the Client Account is disabled or removed';
    END IF;
  END IF;
  IF e.voucher_id IS NOT NULL THEN
    SELECT state INTO v_voucher FROM iam_v2.vouchers WHERE id = e.voucher_id;
    IF v_voucher IS DISTINCT FROM 'REDEEMED' THEN
      RETURN 'VOUCHER_NOT_REDEEMED: the voucher is revoked or no longer valid';
    END IF;
  END IF;
  IF e.end_mode IN ('FIXED_AT','AT_CHECKOUT','EARLIEST_OF_FIXED_AND_CHECKOUT')
     AND e.window_ends_at IS NOT NULL AND e.window_ends_at <= now() THEN
    RETURN 'WINDOW_PASSED: this package ends at a fixed time that has already passed';
  END IF;
  IF e.status = 'TERMINATED' AND EXISTS (
       SELECT 1 FROM iam_v2.entitlements o
        WHERE o.id <> e.id AND o.status IN ('PENDING','ACTIVE','SUSPENDED')
          AND o.tenant_id = e.tenant_id AND o.site_id = e.site_id
          AND (o.stay_id = e.stay_id OR o.guest_account_id = e.guest_account_id OR o.voucher_id = e.voucher_id
               OR (o.guest_principal_id = e.guest_principal_id AND o.site_id = e.site_id)
               OR o.anonymous_subject_id = e.anonymous_subject_id)) THEN
    RETURN 'SUBJECT_HAS_LIVE_ACCESS: this guest already holds other live access; end or reset that one';
  END IF;
  RETURN NULL;
END $f$;
REVOKE ALL ON FUNCTION iam_v2.package_reset_refusal(uuid) FROM PUBLIC;

-- ---------------------------------------------------------------------------------------------------------
-- Preview: exactly what an operator is about to end or reset, and whether each action is allowed.
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION iam_v2.package_control_preview(p_tenant uuid, p_site uuid, p_ent uuid) RETURNS jsonb
  LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $f$
DECLARE e record; v jsonb; u record; v_refusal text; v_window timestamptz; v_successor uuid; v_quota bigint;
        v_devices int; v_sessions int; v_subject text; v_subject_id uuid;
BEGIN
  SELECT en.*, ipr.package_type, COALESCE(ipr.display ->> 'name', ip.code) AS package_name, ip.code AS package_code,
         spr.name AS plan_name, spr.down_kbps, spr.up_kbps, spr.max_concurrent_devices, spr.data_quota_bytes AS plan_quota,
         spr.time_quota_seconds, pu."trigger" AS purchase_trigger, pu.amount_minor, pu.currency
    INTO e
    FROM iam_v2.entitlements en
    JOIN iam_v2.internet_package_revisions ipr ON ipr.id = en.package_revision_id
    JOIN iam_v2.internet_packages ip ON ip.id = ipr.package_id
    JOIN iam_v2.service_plan_revisions spr ON spr.id = en.service_plan_revision_id
    LEFT JOIN iam_v2.purchases pu ON pu.id = en.purchase_id
   WHERE en.id = p_ent AND en.tenant_id = p_tenant AND en.site_id = p_site;
  IF NOT FOUND THEN
    RETURN NULL;
  END IF;
  SELECT * INTO u FROM iam_v2.entitlement_usage_bytes(p_ent, now());
  SELECT s.id INTO v_successor FROM iam_v2.entitlements s WHERE s.supersedes_entitlement_id = p_ent LIMIT 1;
  SELECT count(*) INTO v_devices FROM iam_v2.entitlement_device_authorizations a
   WHERE a.entitlement_id = p_ent AND a.deauthorized_at IS NULL;
  SELECT count(*) INTO v_sessions FROM iam_v2.sessions s
   WHERE s.entitlement_id = p_ent AND s.ended IS NULL AND s.state IN ('active','PENDING_ENFORCEMENT');
  v_quota := COALESCE(e.data_quota_bytes, e.plan_quota);
  v_subject := CASE WHEN e.stay_id IS NOT NULL THEN 'STAY' WHEN e.guest_account_id IS NOT NULL THEN 'CLIENT_ACCOUNT'
                    WHEN e.voucher_id IS NOT NULL THEN 'VOUCHER' WHEN e.guest_principal_id IS NOT NULL THEN 'CLIENT'
                    ELSE 'ANONYMOUS' END;
  v_subject_id := COALESCE(e.stay_id, e.guest_account_id, e.voucher_id, e.guest_principal_id, e.anonymous_subject_id);
  v_refusal := iam_v2.package_reset_refusal(p_ent);
  v_window := iam_v2.package_reset_window(p_ent, now());
  v := jsonb_build_object(
    'entitlement_id', e.id, 'status', e.status, 'terminal_reason', e.terminal_reason,
    'subject_kind', v_subject, 'subject_id', v_subject_id,
    'stay_id', e.stay_id, 'pms_interface_id', e.pms_interface_id,
    'package_revision_id', e.package_revision_id, 'package_name', e.package_name, 'package_code', e.package_code,
    'package_type', e.package_type, 'plan_name', e.plan_name, 'down_kbps', e.down_kbps, 'up_kbps', e.up_kbps,
    'max_devices', e.max_concurrent_devices, 'end_mode', e.end_mode, 'time_mode', e.time_accounting_mode,
    'time_quota_seconds', e.time_quota_seconds, 'online_seconds_used', e.consumed_online_seconds,
    'data_quota_bytes', v_quota, 'bytes_used', COALESCE(u.bytes_up, 0) + COALESCE(u.bytes_down, 0),
    'activated_at', e.activated_at, 'window_ends_at', e.window_ends_at, 'terminated_at', e.terminated_at,
    'devices_online', v_devices, 'sessions_live', v_sessions,
    'purchase_trigger', e.purchase_trigger, 'paid_minor', e.amount_minor, 'currency', e.currency,
    'supersedes', e.supersedes_entitlement_id, 'superseded_by', v_successor,
    'end', jsonb_build_object('allowed', e.status <> 'TERMINATED',
                              'reason', CASE WHEN e.status = 'TERMINATED' THEN 'ALREADY_ENDED: this package has already ended' END),
    'reset', jsonb_build_object('allowed', v_refusal IS NULL, 'reason', v_refusal,
                                'window_ends_at', CASE WHEN v_refusal IS NULL THEN v_window END,
                                'data_quota_bytes', v_quota, 'price_minor', 0));
  IF e.stay_id IS NOT NULL THEN
    v := v || (SELECT jsonb_build_object('stay', jsonb_build_object('status', st.status, 'room', st.normalized_room_number,
                                          'arrival', st.arrival, 'departure', st.departure,
                                          'checked_out_at', st.effective_checkout_at,
                                          'pms_interface', pi.display_label))
                 FROM iam_v2.stays st LEFT JOIN iam_v2.pms_interfaces pi ON pi.id = st.pms_interface_id
                WHERE st.id = e.stay_id);
  END IF;
  RETURN v;
END $f$;
REVOKE ALL ON FUNCTION iam_v2.package_control_preview(uuid, uuid, uuid) FROM PUBLIC;

-- The window a reset at p_at would grant (NULL: no window). Duration-based windows restart; fixed instants are kept.
CREATE OR REPLACE FUNCTION iam_v2.package_reset_window(p_ent uuid, p_at timestamptz) RETURNS timestamptz
  LANGUAGE sql STABLE SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $f$
  SELECT CASE
           WHEN e.window_ends_at IS NULL THEN NULL
           WHEN e.end_mode IN ('VALIDITY_WINDOW','GRACE_AFTER_CHECKOUT','MANUAL_END') AND e.activated_at IS NOT NULL
             THEN p_at + greatest(e.window_ends_at - e.activated_at, interval '1 minute')
           ELSE e.window_ends_at
         END
    FROM iam_v2.entitlements e WHERE e.id = p_ent;
$f$;
REVOKE ALL ON FUNCTION iam_v2.package_reset_window(uuid, timestamptz) FROM PUBLIC;

-- The operator and the reason every staff action needs; raised the same way by both operations.
CREATE OR REPLACE FUNCTION iam_v2.package_admin_check_actor(p_tenant uuid, p_operator uuid, p_reason text) RETURNS void
  LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $f$
BEGIN
  IF p_operator IS NULL OR p_reason IS NULL OR length(btrim(p_reason)) < 4 OR length(btrim(p_reason)) > 500 THEN
    RAISE EXCEPTION 'PKG_NOT_AUTHORIZED: an operator and a reason of 4 to 500 characters are required' USING ERRCODE = 'P0001';
  END IF;
  PERFORM 1 FROM public.operators o
    WHERE o.id = p_operator AND o.status = 'active' AND (o.tenant_id IS NULL OR o.tenant_id = p_tenant);
  IF NOT FOUND THEN
    RAISE EXCEPTION 'PKG_NOT_AUTHORIZED: the operator is not an active operator of this tenant' USING ERRCODE = 'P0001';
  END IF;
END $f$;
REVOKE ALL ON FUNCTION iam_v2.package_admin_check_actor(uuid, uuid, text) FROM PUBLIC;

-- ---------------------------------------------------------------------------------------------------------
-- END PACKAGE
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION iam_v2.admin_end_package(p_tenant uuid, p_site uuid, p_ent uuid, p_operator uuid, p_reason text,
                                                    p_request uuid)
RETURNS TABLE(action_id uuid, outcome text, sessions uuid[], devices integer)
LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE a record; v_status text; v_window timestamptz; v_sessions uuid[]; v_devices int; v_id uuid; v_stay uuid;
BEGIN
  IF p_request IS NULL THEN
    RAISE EXCEPTION 'PKG_NOT_AUTHORIZED: a request id is required' USING ERRCODE = 'P0001';
  END IF;
  PERFORM pg_advisory_xact_lock(hashtextextended('package-admin:' || p_request::text, 0));
  SELECT * INTO a FROM iam_v2.package_admin_actions WHERE request_id = p_request;
  IF FOUND THEN
    IF a.action <> 'END' OR a.entitlement_id <> p_ent OR a.tenant_id <> p_tenant OR a.site_id <> p_site THEN
      RAISE EXCEPTION 'PKG_REQUEST_REUSED: this request id was already used for another action' USING ERRCODE = 'P0001';
    END IF;
    RETURN QUERY SELECT a.id, a.outcome, a.sessions, a.devices;
    RETURN;
  END IF;
  PERFORM iam_v2.package_admin_check_actor(p_tenant, p_operator, p_reason);

  SELECT e.stay_id INTO v_stay FROM iam_v2.entitlements e WHERE e.id = p_ent AND e.tenant_id = p_tenant AND e.site_id = p_site;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'PKG_NOT_FOUND: the package does not exist on this site' USING ERRCODE = 'P0001';
  END IF;
  IF v_stay IS NOT NULL THEN
    PERFORM 1 FROM iam_v2.stays WHERE id = v_stay FOR UPDATE;
  END IF;
  SELECT e.status, e.window_ends_at INTO v_status, v_window FROM iam_v2.entitlements e WHERE e.id = p_ent FOR UPDATE;

  IF v_status = 'TERMINATED' THEN
    INSERT INTO iam_v2.package_admin_actions (tenant_id, site_id, request_id, action, entitlement_id, outcome, actor, reason)
    VALUES (p_tenant, p_site, p_request, 'END', p_ent, 'ALREADY_ENDED', p_operator, left(btrim(p_reason), 500))
    RETURNING id INTO v_id;
    RETURN QUERY SELECT v_id, 'ALREADY_ENDED'::text, '{}'::uuid[], 0;
    RETURN;
  END IF;

  SELECT COALESCE(array_agg(s.id ORDER BY s.id), '{}') INTO v_sessions FROM iam_v2.sessions s
   WHERE s.entitlement_id = p_ent AND s.ended IS NULL AND s.state IN ('active','PENDING_ENFORCEMENT');
  SELECT count(*) INTO v_devices FROM iam_v2.entitlement_device_authorizations da
   WHERE da.entitlement_id = p_ent AND da.deauthorized_at IS NULL;

  PERFORM iam_v2.p4_end_access_funded_by(p_ent, 'ADMIN');

  -- window_ends_at of an END is the window the package had when it was ended: how long the free benefit stays ended for a
  -- subject that is not a Stay (package_ended_by_staff).
  INSERT INTO iam_v2.package_admin_actions (tenant_id, site_id, request_id, action, entitlement_id, outcome, actor, reason,
                                            sessions, devices, window_ends_at)
  VALUES (p_tenant, p_site, p_request, 'END', p_ent, 'ENDED', p_operator, left(btrim(p_reason), 500), v_sessions, v_devices,
          v_window)
  RETURNING id INTO v_id;
  RETURN QUERY SELECT v_id, 'ENDED'::text, v_sessions, v_devices;
END $fn$;
COMMENT ON FUNCTION iam_v2.admin_end_package(uuid, uuid, uuid, uuid, text, uuid) IS
  'Staff End Package (0202): ends the whole entitlement (reason ADMIN), its devices and sessions; idempotent per request id.';
REVOKE ALL ON FUNCTION iam_v2.admin_end_package(uuid, uuid, uuid, uuid, text, uuid) FROM PUBLIC;

-- ---------------------------------------------------------------------------------------------------------
-- RESET PACKAGE
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION iam_v2.admin_reset_package(p_tenant uuid, p_site uuid, p_ent uuid, p_operator uuid, p_reason text,
                                                      p_request uuid)
RETURNS TABLE(action_id uuid, new_entitlement uuid, purchase uuid, sessions_rebound integer, devices integer,
              window_ends timestamptz)
LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE a record; o record; v_refusal text; v_now timestamptz; v_window timestamptz; v_purchase uuid; v_new uuid;
        v_live boolean; v_devices int := 0; v_cohort int := 0; v_sessions uuid[] := '{}'; v_sid uuid; v_id uuid;
        v_stay uuid; v_grace boolean;
BEGIN
  IF p_request IS NULL THEN
    RAISE EXCEPTION 'PKG_NOT_AUTHORIZED: a request id is required' USING ERRCODE = 'P0001';
  END IF;
  PERFORM pg_advisory_xact_lock(hashtextextended('package-admin:' || p_request::text, 0));
  SELECT * INTO a FROM iam_v2.package_admin_actions WHERE request_id = p_request;
  IF FOUND THEN
    IF a.action <> 'RESET' OR a.entitlement_id <> p_ent OR a.tenant_id <> p_tenant OR a.site_id <> p_site THEN
      RAISE EXCEPTION 'PKG_REQUEST_REUSED: this request id was already used for another action' USING ERRCODE = 'P0001';
    END IF;
    RETURN QUERY SELECT a.id, a.new_entitlement_id, a.purchase_id, cardinality(a.sessions), a.devices, a.window_ends_at;
    RETURN;
  END IF;
  PERFORM iam_v2.package_admin_check_actor(p_tenant, p_operator, p_reason);

  -- Lock order: the Stay, then the entitlement (as checkout, transfer and admission do).
  SELECT e.stay_id INTO v_stay FROM iam_v2.entitlements e WHERE e.id = p_ent AND e.tenant_id = p_tenant AND e.site_id = p_site;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'PKG_NOT_FOUND: the package does not exist on this site' USING ERRCODE = 'P0001';
  END IF;
  IF v_stay IS NOT NULL THEN
    PERFORM 1 FROM iam_v2.stays WHERE id = v_stay FOR UPDATE;
  END IF;
  SELECT * INTO o FROM iam_v2.entitlements e WHERE e.id = p_ent FOR UPDATE;

  v_refusal := iam_v2.package_reset_refusal(p_ent);
  IF v_refusal IS NOT NULL THEN
    RAISE EXCEPTION 'PKG_%', v_refusal USING ERRCODE = 'P0001';
  END IF;

  v_now := clock_timestamp();
  v_window := iam_v2.package_reset_window(p_ent, v_now);
  v_live := o.status IN ('ACTIVE');
  v_grace := o.end_mode = 'GRACE_AFTER_CHECKOUT';

  IF v_live THEN
    PERFORM iam_v2.terminate_entitlement_at_boundary(p_ent, v_now, 'SUPERSEDED');
  END IF;

  -- The new cycle: a zero-price administrative purchase, never a payment, posting or redemption.
  PERFORM iam_v2.begin_controlled_operation('commerce_intent');
  INSERT INTO iam_v2.purchases (tenant_id, site_id, package_revision_id, pms_interface_id, stay_id, trigger, amount_minor, state)
  VALUES (p_tenant, p_site, o.package_revision_id, o.pms_interface_id, o.stay_id, 'ADMIN_GRANT', 0, 'GRANTED')
  RETURNING id INTO v_purchase;

  INSERT INTO iam_v2.entitlements (tenant_id, site_id, stay_id, guest_account_id, voucher_id, guest_principal_id,
                                   anonymous_subject_id, pms_interface_id, purchase_id, policy_snapshot, snapshot_version,
                                   service_plan_revision_id, package_revision_id, time_accounting_mode, end_mode,
                                   window_ends_at, status, renewal_number, supersedes_entitlement_id, is_emergency_grace,
                                   data_quota_bytes)
  VALUES (p_tenant, p_site, o.stay_id, o.guest_account_id, o.voucher_id, o.guest_principal_id, o.anonymous_subject_id,
          o.pms_interface_id, v_purchase,
          o.policy_snapshot || jsonb_build_object('granted_from', 'ADMIN_RESET', 'reset_of', o.id::text),
          o.snapshot_version, o.service_plan_revision_id, o.package_revision_id, o.time_accounting_mode, o.end_mode,
          v_window, 'PENDING', o.renewal_number + 1, o.id, o.is_emergency_grace, o.data_quota_bytes)
  RETURNING id INTO v_new;
  PERFORM iam_v2.apply_entitlement_transition(v_new, 'ACTIVE', v_now, 'ADMIN_RESET');

  PERFORM iam_v2.begin_controlled_operation('device_auth');
  IF v_live THEN
    -- Devices online on the original move with it (the transfer's interval rule: the old interval closes when the new opens).
    INSERT INTO iam_v2.entitlement_devices (tenant_id, site_id, entitlement_id, device_id, status, grandfathered,
                                            first_authorized, last_authorized)
    SELECT p_tenant, p_site, v_new, ed.device_id, 'AUTHORIZED', ed.grandfathered, ed.first_authorized, v_now
      FROM iam_v2.entitlement_devices ed
     WHERE ed.entitlement_id = p_ent AND ed.status = 'AUTHORIZED'
       AND (NOT v_grace OR ed.grandfathered);
    GET DIAGNOSTICS v_devices = ROW_COUNT;
    INSERT INTO iam_v2.entitlement_device_authorizations (tenant_id, site_id, entitlement_id, device_id, seq, authorized_at)
    SELECT p_tenant, p_site, v_new, ed.device_id, 1, v_now
      FROM iam_v2.entitlement_devices ed WHERE ed.entitlement_id = v_new AND ed.status = 'AUTHORIZED';
    WITH closed AS (
      UPDATE iam_v2.entitlement_device_authorizations da
         SET deauthorized_at = GREATEST(v_now, da.authorized_at)
       WHERE da.entitlement_id = p_ent AND da.deauthorized_at IS NULL
       RETURNING da.entitlement_id, da.device_id)
    UPDATE iam_v2.entitlement_devices ed
       SET status = 'DISCONNECTED', disconnected_reason = 'PACKAGE_RESET'
      FROM closed WHERE ed.entitlement_id = closed.entitlement_id AND ed.device_id = closed.device_id;
  END IF;
  IF v_grace THEN
    -- The fixed cohort: devices the checkout carried over that are not online now may return; nobody else.
    INSERT INTO iam_v2.entitlement_devices (tenant_id, site_id, entitlement_id, device_id, status, grandfathered,
                                            disconnected_reason, first_authorized, last_authorized)
    SELECT p_tenant, p_site, v_new, ed.device_id, 'DISCONNECTED', true, 'PACKAGE_RESET_COHORT', ed.first_authorized,
           ed.last_authorized
      FROM iam_v2.entitlement_devices ed
     WHERE ed.entitlement_id = p_ent AND ed.grandfathered
       AND COALESCE(ed.disconnected_reason, 'ENTITLEMENT_ENDED')
           IN ('ENTITLEMENT_ENDED','ENTITLEMENT_SUSPENDED','PACKAGE_RESET','PACKAGE_RESET_COHORT')
    ON CONFLICT (entitlement_id, device_id) DO NOTHING;
    GET DIAGNOSTICS v_cohort = ROW_COUNT;
  END IF;

  IF v_live THEN
    FOR v_sid IN SELECT s.id FROM iam_v2.sessions s
                  WHERE s.tenant_id = p_tenant AND s.site_id = p_site AND s.entitlement_id = p_ent AND s.ended IS NULL
                    AND s.state IN ('active','PENDING_ENFORCEMENT')
                    AND EXISTS (SELECT 1 FROM iam_v2.entitlement_devices nd
                                 WHERE nd.entitlement_id = v_new AND nd.device_id = s.device_id AND nd.status = 'AUTHORIZED')
                  ORDER BY s.id
    LOOP
      PERFORM iam_v2.rebind_session_entitlement(v_sid, v_new, v_now);
      v_sessions := v_sessions || v_sid;
    END LOOP;
    -- A session on the original whose device did not move (After Check-out: not in the cohort) ends with the original.
    UPDATE iam_v2.sessions s
       SET state = 'ended', ended = GREATEST(v_now, s.started), end_reason = 'ENTITLEMENT_ENDED'
     WHERE s.entitlement_id = p_ent AND s.ended IS NULL AND s.state IN ('active','PENDING_ENFORCEMENT');
  END IF;

  INSERT INTO iam_v2.package_admin_actions (tenant_id, site_id, request_id, action, entitlement_id, new_entitlement_id,
                                            purchase_id, outcome, actor, reason, sessions, devices, window_ends_at)
  VALUES (p_tenant, p_site, p_request, 'RESET', p_ent, v_new, v_purchase, 'RESET', p_operator, left(btrim(p_reason), 500),
          v_sessions, v_devices + v_cohort, v_window)
  RETURNING id INTO v_id;
  RETURN QUERY SELECT v_id, v_new, v_purchase, cardinality(v_sessions), v_devices + v_cohort, v_window;
END $fn$;
COMMENT ON FUNCTION iam_v2.admin_reset_package(uuid, uuid, uuid, uuid, text, uuid) IS
  'Staff Reset Package (0202): a new, linked, zero-price ADMIN_GRANT cycle of the same package; idempotent per request id.';
REVOKE ALL ON FUNCTION iam_v2.admin_reset_package(uuid, uuid, uuid, uuid, text, uuid) FROM PUBLIC;

-- The packages of one subject, newest first, for the Client, Stay, session and device views: a session or a device resolves to
-- the subject of the entitlement it is (or was last) on. A stay is named by its id, never by its room number.
CREATE OR REPLACE FUNCTION iam_v2.package_control_list(p_tenant uuid, p_site uuid, p_kind text, p_id uuid) RETURNS jsonb
  LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $f$
DECLARE r record; v_ent uuid;
BEGIN
  IF p_kind = 'session' THEN
    SELECT s.entitlement_id INTO v_ent FROM iam_v2.sessions s WHERE s.id = p_id AND s.tenant_id = p_tenant AND s.site_id = p_site;
  ELSIF p_kind = 'device' THEN
    SELECT ed.entitlement_id INTO v_ent FROM iam_v2.entitlement_devices ed
     WHERE ed.device_id = p_id AND ed.tenant_id = p_tenant AND ed.site_id = p_site
     ORDER BY ed.last_authorized DESC NULLS LAST LIMIT 1;
  ELSIF p_kind = 'entitlement' THEN
    v_ent := p_id;
  END IF;
  IF p_kind IN ('session','device','entitlement') THEN
    SELECT stay_id, guest_account_id, voucher_id, guest_principal_id, anonymous_subject_id INTO r
      FROM iam_v2.entitlements WHERE id = v_ent AND tenant_id = p_tenant AND site_id = p_site;
    IF NOT FOUND THEN RETURN '[]'::jsonb; END IF;
  ELSE
    SELECT CASE WHEN p_kind = 'stay' THEN p_id END AS stay_id, CASE WHEN p_kind = 'account' THEN p_id END AS guest_account_id,
           CASE WHEN p_kind = 'voucher' THEN p_id END AS voucher_id, CASE WHEN p_kind = 'principal' THEN p_id END AS guest_principal_id,
           NULL::uuid AS anonymous_subject_id INTO r;
  END IF;
  RETURN COALESCE((
    SELECT jsonb_agg(jsonb_build_object('entitlement_id', e.id, 'status', e.status, 'terminal_reason', e.terminal_reason,
                                        'package_name', COALESCE(ipr.display ->> 'name', ip.code), 'end_mode', e.end_mode,
                                        'activated_at', e.activated_at, 'window_ends_at', e.window_ends_at,
                                        'terminated_at', e.terminated_at, 'supersedes', e.supersedes_entitlement_id,
                                        'purchase_trigger', pu."trigger")
                     ORDER BY (e.status IN ('PENDING','ACTIVE','SUSPENDED')) DESC, COALESCE(e.activated_at, e.terminated_at) DESC NULLS LAST)
      FROM (SELECT * FROM iam_v2.entitlements en
             WHERE en.tenant_id = p_tenant AND en.site_id = p_site
               AND ((r.stay_id IS NOT NULL AND en.stay_id = r.stay_id)
                 OR (r.guest_account_id IS NOT NULL AND en.guest_account_id = r.guest_account_id)
                 OR (r.voucher_id IS NOT NULL AND en.voucher_id = r.voucher_id)
                 OR (r.guest_principal_id IS NOT NULL AND en.guest_principal_id = r.guest_principal_id)
                 OR (r.anonymous_subject_id IS NOT NULL AND en.anonymous_subject_id = r.anonymous_subject_id))
             ORDER BY (en.status IN ('PENDING','ACTIVE','SUSPENDED')) DESC, COALESCE(en.activated_at, en.terminated_at) DESC NULLS LAST
             LIMIT 10) e
      JOIN iam_v2.internet_package_revisions ipr ON ipr.id = e.package_revision_id
      JOIN iam_v2.internet_packages ip ON ip.id = ipr.package_id
      LEFT JOIN iam_v2.purchases pu ON pu.id = e.purchase_id), '[]'::jsonb);
END $f$;
REVOKE ALL ON FUNCTION iam_v2.package_control_list(uuid, uuid, text, uuid) FROM PUBLIC;

-- Staff history for one entitlement's lineage, newest first (ids, codes and the bounded reason only).
CREATE OR REPLACE FUNCTION iam_v2.package_admin_history(p_tenant uuid, p_site uuid, p_ent uuid) RETURNS jsonb
  LANGUAGE sql STABLE SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $f$
  SELECT COALESCE(jsonb_agg(jsonb_build_object('id', a.id, 'action', a.action, 'outcome', a.outcome,
                                               'entitlement_id', a.entitlement_id, 'new_entitlement_id', a.new_entitlement_id,
                                               'actor', a.actor, 'reason', a.reason, 'sessions', cardinality(a.sessions),
                                               'devices', a.devices, 'window_ends_at', a.window_ends_at, 'at', a.created_at)
                            ORDER BY a.created_at DESC), '[]'::jsonb)
    FROM iam_v2.package_admin_actions a
   WHERE a.tenant_id = p_tenant AND a.site_id = p_site AND (a.entitlement_id = p_ent OR a.new_entitlement_id = p_ent);
$f$;
REVOKE ALL ON FUNCTION iam_v2.package_admin_history(uuid, uuid, uuid) FROM PUBLIC;

-- ---------------------------------------------------------------------------------------------------------
-- After Check-out: a cohort device a reset carried over may return; nothing else changes in the 0193 rule.
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION iam_v2.after_checkout_device_guard() RETURNS trigger
    LANGUAGE plpgsql SET search_path TO 'iam_v2', 'pg_temp' AS $$
DECLARE v_mode text;
BEGIN
  SELECT e.end_mode INTO v_mode FROM iam_v2.entitlements e WHERE e.id = NEW.entitlement_id;
  IF v_mode IS DISTINCT FROM 'GRACE_AFTER_CHECKOUT' THEN RETURN NEW; END IF;
  IF TG_OP = 'INSERT' THEN
    -- The checkout conversion (and a reset, from the same cohort) marks the devices it carries over as grandfathered.
    -- authorize_entitlement_device inserts with ON CONFLICT DO UPDATE, so an existing row reaches the UPDATE rule below.
    IF NEW.grandfathered THEN RETURN NEW; END IF;
    IF EXISTS (SELECT 1 FROM iam_v2.entitlement_devices d
                WHERE d.entitlement_id = NEW.entitlement_id AND d.device_id = NEW.device_id) THEN
      RETURN NEW;
    END IF;
    RAISE EXCEPTION 'AFTER_CHECKOUT_NEW_DEVICE_REJECTED: a device that was not authorised on the stay before check-out cannot be added to its After Check-out entitlement'
      USING ERRCODE = 'check_violation';
  END IF;
  IF OLD.status IS DISTINCT FROM 'AUTHORIZED' AND NEW.status = 'AUTHORIZED'
     AND COALESCE(OLD.disconnected_reason, '') NOT IN ('ENTITLEMENT_SUSPENDED', 'PACKAGE_RESET_COHORT') THEN
    RAISE EXCEPTION 'AFTER_CHECKOUT_NEW_DEVICE_REJECTED: a device that left an After Check-out entitlement cannot rejoin it'
      USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END $$;
REVOKE ALL ON FUNCTION iam_v2.after_checkout_device_guard() FROM PUBLIC;

-- ---------------------------------------------------------------------------------------------------------
-- No automatic re-acquisition of a free benefit staff ended (see the header).
CREATE OR REPLACE FUNCTION iam_v2.package_ended_by_staff(p_tenant uuid, p_site uuid, p_package_revision uuid, p_stay uuid,
                                                         p_account uuid, p_voucher uuid, p_principal uuid, p_device uuid)
RETURNS boolean LANGUAGE sql STABLE SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
  SELECT EXISTS (
    SELECT 1
      FROM iam_v2.package_admin_actions a
      JOIN iam_v2.entitlements x ON x.id = a.entitlement_id
      JOIN iam_v2.internet_package_revisions rx ON rx.id = x.package_revision_id
      JOIN iam_v2.internet_package_revisions rn ON rn.id = p_package_revision AND rn.package_id = rx.package_id
     WHERE a.tenant_id = p_tenant AND a.site_id = p_site AND a.action = 'END' AND a.outcome = 'ENDED'
       AND (   (p_stay IS NOT NULL AND x.stay_id = p_stay)
            OR (x.stay_id IS NULL AND (a.window_ends_at IS NULL OR a.window_ends_at > now())
                AND (   (p_account   IS NOT NULL AND x.guest_account_id   = p_account)
                     OR (p_voucher   IS NOT NULL AND x.voucher_id         = p_voucher)
                     OR (p_principal IS NOT NULL AND x.guest_principal_id = p_principal)
                     OR (p_device IS NOT NULL AND x.anonymous_subject_id IS NOT NULL
                         AND EXISTS (SELECT 1 FROM iam_v2.entitlement_device_authorizations da
                                      WHERE da.entitlement_id = x.id AND da.device_id = p_device)))))
       AND NOT EXISTS (SELECT 1 FROM iam_v2.package_admin_actions r
                        WHERE r.action = 'RESET' AND r.entitlement_id = a.entitlement_id AND r.created_at >= a.created_at))
$fn$;
COMMENT ON FUNCTION iam_v2.package_ended_by_staff(uuid, uuid, uuid, uuid, uuid, uuid, uuid, uuid) IS
  'Staff End Package (0202): true while a free benefit staff ended may not be acquired again automatically by this subject.';
REVOKE ALL ON FUNCTION iam_v2.package_ended_by_staff(uuid, uuid, uuid, uuid, uuid, uuid, uuid, uuid) FROM PUBLIC;

CREATE OR REPLACE FUNCTION iam_v2.staff_ended_benefit_guard() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE v_trigger text; v_amount bigint; v_device uuid;
BEGIN
  IF NEW.purchase_id IS NULL THEN RETURN NEW; END IF;
  SELECT pu.trigger, pu.amount_minor, ac.device_id INTO v_trigger, v_amount, v_device
    FROM iam_v2.purchases pu LEFT JOIN iam_v2.auth_contexts ac ON ac.id = pu.auth_context_id
   WHERE pu.id = NEW.purchase_id;
  IF NOT FOUND OR v_amount <> 0
     OR v_trigger NOT IN ('GUEST_SELECTION', 'ACCOUNT_AUTO_GRANT', 'OTP_SOCIAL_DEFAULT', 'VOUCHER_REDEMPTION') THEN
    RETURN NEW;
  END IF;
  IF iam_v2.package_ended_by_staff(NEW.tenant_id, NEW.site_id, NEW.package_revision_id, NEW.stay_id, NEW.guest_account_id,
                                   NEW.voucher_id, NEW.guest_principal_id,
                                   CASE WHEN NEW.anonymous_subject_id IS NOT NULL THEN v_device END) THEN
    RAISE EXCEPTION 'PKG_ENDED_BY_STAFF: staff ended this free package; it is not acquired again automatically'
      USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END $fn$;
REVOKE ALL ON FUNCTION iam_v2.staff_ended_benefit_guard() FROM PUBLIC;
DROP TRIGGER IF EXISTS staff_ended_benefit_guard ON iam_v2.entitlements;
CREATE TRIGGER staff_ended_benefit_guard BEFORE INSERT ON iam_v2.entitlements
  FOR EACH ROW EXECUTE FUNCTION iam_v2.staff_ended_benefit_guard();

-- ---------------------------------------------------------------------------------------------------------
-- Erasure removes what staff typed here with the guest's other staff notes. The bodies are 0147's, unchanged but for the
-- package_admin_actions statement before the scrub is switched off.
CREATE OR REPLACE FUNCTION iam_v2.anonymise_guest_account(p_account uuid) RETURNS void
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
BEGIN
  PERFORM set_config('iam_v2.anonymise', 'retention', true);
  UPDATE iam_v2.account_email_tokens
     SET last_send_error = NULL, token_hmac = CASE WHEN token_hmac IS NOT NULL THEN public.gen_random_bytes(32) END,
         superseded_at = CASE WHEN used_at IS NULL AND superseded_at IS NULL THEN now() ELSE superseded_at END
   WHERE account_id = p_account;
  UPDATE iam_v2.guest_access_accounts
     SET username = iam_v2.anonymised_token() || '@anonymised.removed-' || left(replace(id::text, '-', ''), 8) || '.invalid',
         display_name = NULL, notes = NULL, created_by_device_mac = NULL, enabled = false,
         -- The link to the reservation, the password hash, the sign-in history and the dates of the account go with the name:
         -- the reservation the account was made for would otherwise name the person a join away.
         stay_id = NULL, password_hash = '$argon2id$v=19$m=65536,t=1,p=4$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA', failed_attempts = 0, locked_until = NULL, last_login_at = NULL,
         login_count = 0, created_at = date_trunc('month', created_at), email_verified_at = NULL, verification_deadline = NULL,
         verification_reminder_sent_at = NULL, grace_until = NULL, valid_from = NULL, valid_until = NULL
   WHERE id = p_account AND username NOT LIKE 'anon-%@anonymised.removed-%';
  -- What staff typed about this account's access.
  PERFORM set_config('iam_v2.scrub', 'on', true);
  UPDATE iam_v2.entitlement_adjustments SET reason = 'Removed after the staff-notes period'
   WHERE entitlement_id IN (SELECT e.id FROM iam_v2.entitlements e WHERE e.guest_account_id = p_account) AND reason <> 'Removed after the staff-notes period';
  UPDATE iam_v2.device_slot_actions SET reason = 'Removed after the staff-notes period', detail = CASE WHEN detail IS NULL THEN NULL ELSE 'Removed after the staff-notes period' END
   WHERE entitlement_id IN (SELECT e.id FROM iam_v2.entitlements e WHERE e.guest_account_id = p_account)
     AND (reason <> 'Removed after the staff-notes period' OR (detail IS NOT NULL AND detail <> 'Removed after the staff-notes period'));
  UPDATE iam_v2.guest_device_actions SET detail = 'Removed after the staff-notes period'
   WHERE entitlement_id IN (SELECT e.id FROM iam_v2.entitlements e WHERE e.guest_account_id = p_account)
     AND detail IS NOT NULL AND detail <> 'Removed after the staff-notes period';
  -- What staff typed when they ended or reset this guest's package (0202).
  UPDATE iam_v2.package_admin_actions SET reason = 'Removed after the staff-notes period'
   WHERE entitlement_id IN (SELECT e.id FROM iam_v2.entitlements e WHERE e.guest_account_id = p_account) AND reason <> 'Removed after the staff-notes period';
  PERFORM set_config('iam_v2.scrub', '', true);
END $$;

CREATE OR REPLACE FUNCTION iam_v2.anonymise_guest_principal(p_principal uuid) RETURNS void
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
BEGIN
  PERFORM set_config('iam_v2.anonymise', 'retention', true);
  UPDATE iam_v2.guest_principals SET display_name = NULL WHERE id = p_principal AND display_name IS NOT NULL;
  UPDATE iam_v2.guest_principal_identities SET factor_value_norm = iam_v2.anonymised_token(), attrs = '{}'::jsonb
   WHERE guest_principal_id = p_principal AND NOT iam_v2.is_anon_token(factor_value_norm);
  UPDATE iam_v2.guest_principal_identities SET verified_at = date_trunc('month', verified_at)
   WHERE guest_principal_id = p_principal AND verified_at IS NOT NULL AND verified_at <> date_trunc('month', verified_at);
  -- A remembered-device secret is revoked AND its value replaced: a revoked row must not keep a usable shape of it.
  UPDATE iam_v2.principal_device_credentials
     SET revoked_at = COALESCE(revoked_at, now()), secret_hmac = public.gen_random_bytes(32), last_used_at = NULL
   WHERE guest_principal_id = p_principal AND (revoked_at IS NULL OR last_used_at IS NOT NULL);
  PERFORM set_config('iam_v2.scrub', 'on', true);
  UPDATE iam_v2.entitlement_adjustments SET reason = 'Removed after the staff-notes period'
   WHERE entitlement_id IN (SELECT e.id FROM iam_v2.entitlements e WHERE e.guest_principal_id = p_principal) AND reason <> 'Removed after the staff-notes period';
  UPDATE iam_v2.device_slot_actions SET reason = 'Removed after the staff-notes period', detail = CASE WHEN detail IS NULL THEN NULL ELSE 'Removed after the staff-notes period' END
   WHERE entitlement_id IN (SELECT e.id FROM iam_v2.entitlements e WHERE e.guest_principal_id = p_principal)
     AND (reason <> 'Removed after the staff-notes period' OR (detail IS NOT NULL AND detail <> 'Removed after the staff-notes period'));
  UPDATE iam_v2.guest_device_actions SET detail = 'Removed after the staff-notes period'
   WHERE entitlement_id IN (SELECT e.id FROM iam_v2.entitlements e WHERE e.guest_principal_id = p_principal)
     AND detail IS NOT NULL AND detail <> 'Removed after the staff-notes period';
  -- What staff typed when they ended or reset this guest's package (0202).
  UPDATE iam_v2.package_admin_actions SET reason = 'Removed after the staff-notes period'
   WHERE entitlement_id IN (SELECT e.id FROM iam_v2.entitlements e WHERE e.guest_principal_id = p_principal) AND reason <> 'Removed after the staff-notes period';
  PERFORM set_config('iam_v2.scrub', '', true);
END $$;

CREATE OR REPLACE FUNCTION iam_v2.anonymise_stay(p_stay uuid) RETURNS void
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
BEGIN
  PERFORM set_config('iam_v2.anonymise', 'retention', true);
  PERFORM iam_v2.begin_controlled_operation('stay');
  UPDATE iam_v2.stays
     SET external_reservation_id = iam_v2.anonymised_token(), external_stay_identity = iam_v2.anonymised_token(),
         normalized_room_number = NULL, vip = NULL, travel_agent = NULL, rate_plan = NULL, room_type = NULL,
         -- The dates stay as months and the times as days: enough for a report, not enough to find the booking in the PMS.
         arrival = date_trunc('month', arrival)::date, departure = date_trunc('month', departure)::date,
         -- (only on a finished stay: an open one is still governed by the occupancy-evidence rules)
         effective_checkout_at = CASE WHEN status IN ('CHECKED_OUT', 'POST_STAY_ACTIVE', 'CANCELLED', 'NO_SHOW')
                                      THEN date_trunc('day', effective_checkout_at) ELSE effective_checkout_at END,
         occupancy_evidence_at = CASE WHEN status IN ('CHECKED_OUT', 'POST_STAY_ACTIVE', 'CANCELLED', 'NO_SHOW')
                                      THEN date_trunc('day', occupancy_evidence_at) ELSE occupancy_evidence_at END,
         occupancy_ingested_at = CASE WHEN status IN ('CHECKED_OUT', 'POST_STAY_ACTIVE', 'CANCELLED', 'NO_SHOW')
                                      THEN date_trunc('day', occupancy_ingested_at) ELSE occupancy_ingested_at END
   WHERE id = p_stay AND NOT iam_v2.is_anon_token(external_reservation_id);
  -- What staff typed about the stay, and the post-stay secret.
  PERFORM set_config('iam_v2.scrub', 'on', true);
  UPDATE iam_v2.stay_posting_blocks SET note = CASE WHEN note IS NULL THEN NULL ELSE 'Removed after the staff-notes period' END,
         cleared_reason = CASE WHEN cleared_reason IS NULL THEN NULL ELSE 'Removed after the staff-notes period' END
   WHERE stay_id = p_stay AND ((note IS NOT NULL AND note <> 'Removed after the staff-notes period') OR (cleared_reason IS NOT NULL AND cleared_reason <> 'Removed after the staff-notes period'));
  UPDATE iam_v2.pms_case_resolutions SET note = 'Removed after the staff-notes period', evidence_ref = CASE WHEN evidence_ref IS NULL THEN NULL ELSE 'Removed after the staff-notes period' END
   WHERE stay_id = p_stay AND ((note IS NOT NULL AND note <> 'Removed after the staff-notes period') OR (evidence_ref IS NOT NULL AND evidence_ref <> 'Removed after the staff-notes period'));
  UPDATE iam_v2.entitlement_transfers SET reason = 'Removed after the staff-notes period'
   WHERE (from_stay_id = p_stay OR to_stay_id = p_stay) AND reason IS NOT NULL AND reason <> 'Removed after the staff-notes period';
  UPDATE iam_v2.entitlement_adjustments SET reason = 'Removed after the staff-notes period'
   WHERE entitlement_id IN (SELECT e.id FROM iam_v2.entitlements e WHERE e.stay_id = p_stay) AND reason <> 'Removed after the staff-notes period';
  UPDATE iam_v2.device_slot_actions SET reason = 'Removed after the staff-notes period', detail = CASE WHEN detail IS NULL THEN NULL ELSE 'Removed after the staff-notes period' END
   WHERE entitlement_id IN (SELECT e.id FROM iam_v2.entitlements e WHERE e.stay_id = p_stay)
     AND (reason <> 'Removed after the staff-notes period' OR (detail IS NOT NULL AND detail <> 'Removed after the staff-notes period'));
  UPDATE iam_v2.guest_device_actions SET detail = 'Removed after the staff-notes period'
   WHERE entitlement_id IN (SELECT e.id FROM iam_v2.entitlements e WHERE e.stay_id = p_stay)
     AND detail IS NOT NULL AND detail <> 'Removed after the staff-notes period';
  UPDATE iam_v2.post_stay_profiles
     SET pin_hash = '$argon2id$v=19$m=65536,t=1,p=4$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA', revoke_reason = CASE WHEN revoke_reason IS NULL THEN NULL ELSE 'Removed after the staff-notes period' END
   WHERE origin_stay_id = p_stay AND (pin_hash <> '$argon2id$v=19$m=65536,t=1,p=4$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA' OR (revoke_reason IS NOT NULL AND revoke_reason <> 'Removed after the staff-notes period'));
  -- What staff typed when they ended or reset this guest's package (0202).
  UPDATE iam_v2.package_admin_actions SET reason = 'Removed after the staff-notes period'
   WHERE entitlement_id IN (SELECT e.id FROM iam_v2.entitlements e WHERE e.stay_id = p_stay) AND reason <> 'Removed after the staff-notes period';
  PERFORM set_config('iam_v2.scrub', '', true);
  UPDATE iam_v2.stay_guests
     SET external_guest_id = NULL, first_name_norm = NULL, last_name_norm = NULL, display_name = NULL,
         pin_hash = NULL
   WHERE stay_id = p_stay
     AND num_nonnulls(external_guest_id, first_name_norm, last_name_norm, display_name, pin_hash) > 0;
  UPDATE iam_v2.stay_events
     SET payload = '{"anonymised": true}'::jsonb,
         external_event_identity = 'anon-' || encode(public.gen_random_bytes(32), 'hex')
   WHERE stay_id = p_stay AND processing_status <> 'PENDING'
     AND payload IS DISTINCT FROM '{"anonymised": true}'::jsonb;
  -- The copy of the names and room migration 0066 kept as rollback evidence goes with the stay
  -- (GDPR audit 2026-10-19, A6). Not every appliance has the table, so it is looked up first.
  IF to_regclass('iam_v2.backfill_0066_identity_text') IS NOT NULL THEN
    EXECUTE $q$
      UPDATE iam_v2.backfill_0066_identity_text x
         SET prior_first_name = NULL, prior_last_name = NULL, prior_room = NULL
       WHERE (x.row_id = $1 OR x.row_id IN (SELECT sg.id FROM iam_v2.stay_guests sg WHERE sg.stay_id = $1))
         AND num_nonnulls(x.prior_first_name, x.prior_last_name, x.prior_room) > 0
    $q$ USING p_stay;
  END IF;
END $$;

DO $own$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
    EXECUTE 'ALTER TABLE iam_v2.package_admin_actions OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.package_admin_actions_append_only() OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.package_reset_refusal(uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.package_reset_window(uuid, timestamptz) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.package_control_preview(uuid, uuid, uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.package_admin_check_actor(uuid, uuid, text) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.admin_end_package(uuid, uuid, uuid, uuid, text, uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.admin_reset_package(uuid, uuid, uuid, uuid, text, uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.package_admin_history(uuid, uuid, uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.package_control_list(uuid, uuid, text, uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.after_checkout_device_guard() OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.package_ended_by_staff(uuid, uuid, uuid, uuid, uuid, uuid, uuid, uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.staff_ended_benefit_guard() OWNER TO iam_v2_owner';
  END IF;
END $own$;

-- The grants are made again by deploy/gatep/svc-package-controls-grants.sql (a Gate-P reconcile revokes and re-grants); they are
-- repeated here only so a database migrated without a reconcile behaves the same.
DO $g$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
    GRANT EXECUTE ON FUNCTION iam_v2.package_control_preview(uuid, uuid, uuid) TO svc_edged;
    GRANT EXECUTE ON FUNCTION iam_v2.package_admin_history(uuid, uuid, uuid) TO svc_edged;
    GRANT EXECUTE ON FUNCTION iam_v2.package_control_list(uuid, uuid, text, uuid) TO svc_edged;
    GRANT EXECUTE ON FUNCTION iam_v2.admin_end_package(uuid, uuid, uuid, uuid, text, uuid) TO svc_edged;
    GRANT EXECUTE ON FUNCTION iam_v2.admin_reset_package(uuid, uuid, uuid, uuid, text, uuid) TO svc_edged;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_scd') THEN
    GRANT EXECUTE ON FUNCTION iam_v2.package_ended_by_staff(uuid, uuid, uuid, uuid, uuid, uuid, uuid, uuid) TO svc_scd;
  END IF;
END $g$;

COMMIT;
