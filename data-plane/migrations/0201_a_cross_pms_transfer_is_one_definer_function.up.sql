-- 0201 -- A CROSS-PMS TRANSFER IS ONE DEFINER FUNCTION (final engineering round, 2026-10-31).
--
-- WHY. The staff-confirmed Cross-PMS Transfer (internal/transfer, Hotel Admin "stay-transfers") ran every one of its writes as the
-- role edged connects as, svc_edged: apply_entitlement_transition, INSERT on purchases, entitlements, entitlement_devices,
-- entitlement_device_authorizations, entitlement_transfers and stay_links, and a column UPDATE on sessions. Under the production
-- Gate-P grants svc_edged holds none of those (the entitlement state writer is scd's and pmsd's alone, and edged was deliberately
-- given no commerce write), so the transfer could not complete on a real appliance: it failed closed, in one transaction, with
-- nothing applied. The integration tests ran as the database owner and could not see it.
--
-- WHAT. iam_v2.cross_pms_transfer(...) performs the whole operation in one transaction with the same preconditions, the same
-- lock order (both stays in id order, then the source entitlement), the same landing-package choice, the same lineage and the same
-- session rebinding (rebind_session_entitlement, so accounting follows the guest) the Go code had. It is SECURITY DEFINER, owned by
-- iam_v2_owner, and is the only thing svc_edged may EXECUTE for this: no table privilege is added to svc_edged, and the column UPDATE
-- on sessions.entitlement_id it held for the old code is withdrawn (deploy/gatep). Refusals are raised as 'XFER_<CODE>: ...' and
-- mapped back to the same operator-facing errors by the caller. The function stores the operator's id and the bounded reason exactly
-- as before; it reads and writes no guest name, room or reservation text.
--
-- Additive and reversible: the .down.sql drops the function. No data is touched.
BEGIN;

CREATE OR REPLACE FUNCTION iam_v2.cross_pms_transfer(
    p_tenant uuid, p_site uuid, p_from_stay uuid, p_to_stay uuid,
    p_operator uuid, p_reason text, p_grace_seconds integer)
RETURNS TABLE(transfer_id uuid, from_entitlement uuid, to_entitlement uuid, to_package uuid,
              devices_rebound integer, sessions_rebound integer, window_ends timestamptz)
LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE
  n integer;
  v_from_iface uuid; v_to_iface uuid; v_from_status text; v_to_status text;
  v_from_ent uuid; v_to_ent uuid; v_purchase uuid; v_transfer uuid;
  v_pkg_rev uuid; v_plan_rev uuid; v_window timestamptz;
  v_now timestamptz; v_devices integer := 0; v_sessions integer := 0;
  c record; r record; v_ok boolean; v_sid uuid;
BEGIN
  IF p_operator IS NULL OR p_reason IS NULL OR length(btrim(p_reason)) < 4
     OR p_grace_seconds IS NULL OR p_grace_seconds <= 0 OR p_grace_seconds > 7 * 24 * 3600 THEN
    RAISE EXCEPTION 'XFER_NOT_AUTHORIZED: an operator and a bounded reason are required' USING ERRCODE = 'P0001';
  END IF;

  -- Both stays, in id order (caller order would deadlock two opposite-direction transfers between the same pair).
  SELECT count(*) INTO n FROM (SELECT id FROM iam_v2.stays
     WHERE tenant_id = p_tenant AND site_id = p_site AND id IN (p_from_stay, p_to_stay) ORDER BY id FOR UPDATE) l;
  IF n <> 2 THEN
    RAISE EXCEPTION 'XFER_DESTINATION_MISSING: the destination stay does not exist yet from verified PMS state' USING ERRCODE = 'P0001';
  END IF;

  SELECT f.pms_interface_id, t.pms_interface_id, f.status, t.status
    INTO v_from_iface, v_to_iface, v_from_status, v_to_status
    FROM iam_v2.stays f, iam_v2.stays t
   WHERE f.tenant_id = p_tenant AND f.site_id = p_site AND f.id = p_from_stay
     AND t.tenant_id = p_tenant AND t.site_id = p_site AND t.id = p_to_stay;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'XFER_DESTINATION_MISSING: the destination stay does not exist yet from verified PMS state' USING ERRCODE = 'P0001';
  END IF;
  IF v_from_iface = v_to_iface THEN
    RAISE EXCEPTION 'XFER_SAME_INTERFACE: both stays are on the same PMS interface; this is a room move, not a transfer' USING ERRCODE = 'P0001';
  END IF;
  IF v_from_status <> 'IN_HOUSE' THEN
    RAISE EXCEPTION 'XFER_SOURCE_NOT_IN_HOUSE: the source stay has already checked out; grace access is not transferable' USING ERRCODE = 'P0001';
  END IF;
  IF v_to_status <> 'IN_HOUSE' THEN
    RAISE EXCEPTION 'XFER_DESTINATION_NOT_ELIGIBLE: the destination stay is not currently in house' USING ERRCODE = 'P0001';
  END IF;

  SELECT e.id INTO v_from_ent FROM iam_v2.entitlements e
   WHERE e.tenant_id = p_tenant AND e.site_id = p_site AND e.stay_id = p_from_stay
     AND e.status IN ('PENDING','ACTIVE','SUSPENDED')
   LIMIT 1 FOR UPDATE;
  IF v_from_ent IS NULL THEN
    RAISE EXCEPTION 'XFER_SOURCE_NOT_TRANSFERABLE: the source stay has no transferable live access' USING ERRCODE = 'P0001';
  END IF;
  IF EXISTS (SELECT 1 FROM iam_v2.entitlement_transfers t WHERE t.from_entitlement_id = v_from_ent) THEN
    RAISE EXCEPTION 'XFER_ALREADY_TRANSFERRED: this access has already been transferred' USING ERRCODE = 'P0001';
  END IF;
  IF EXISTS (SELECT 1 FROM iam_v2.entitlements e
              WHERE e.tenant_id = p_tenant AND e.site_id = p_site AND e.stay_id = p_to_stay
                AND e.status IN ('PENDING','ACTIVE','SUSPENDED')) THEN
    RAISE EXCEPTION 'XFER_DESTINATION_OCCUPIED: the destination stay already holds live access' USING ERRCODE = 'P0001';
  END IF;

  -- The landing package: zero price, no posting, deterministic order; a package whose current revision is restricted by a
  -- PMS_INTERFACE rule to other interfaces is not offered at the destination (an unreadable restriction is refused, never guessed).
  FOR c IN
    SELECT ipr.id AS pkg_rev, ipr.service_plan_revision_id AS plan_rev, ip.id AS pkg, ip.is_system AS is_system
      FROM iam_v2.internet_package_revisions ipr
      JOIN iam_v2.internet_packages ip
        ON ip.tenant_id = ipr.tenant_id AND ip.site_id = ipr.site_id AND ip.id = ipr.package_id
     WHERE ipr.tenant_id = p_tenant AND ipr.site_id = p_site AND ip.active
       AND ip.current_revision_id = ipr.id
       AND ipr.price_minor = 0
       AND ipr.settlement_methods = ARRAY['NOT_REQUIRED']::text[]
       AND ipr.package_type IN ('CHECKOUT_GRACE','FREE_STAY','GENERAL')
     ORDER BY CASE ipr.package_type WHEN 'CHECKOUT_GRACE' THEN 0 WHEN 'FREE_STAY' THEN 1 ELSE 2 END, ip.code, ipr.id
     LIMIT 50
  LOOP
    v_ok := true;
    IF NOT c.is_system THEN
      FOR r IN SELECT value FROM iam_v2.p2_package_current_conditions(p_tenant, p_site, c.pkg)
                WHERE kind = 'RULE' AND rule_type = 'PMS_INTERFACE'
      LOOP
        BEGIN
          IF NOT EXISTS (SELECT 1 FROM jsonb_array_elements_text(r.value -> 'pms_interface_ids') x
                          WHERE lower(x) = lower(v_to_iface::text)) THEN
            v_ok := false;
          END IF;
        EXCEPTION WHEN others THEN
          v_ok := false;
        END;
      END LOOP;
    END IF;
    IF v_ok THEN
      v_pkg_rev := c.pkg_rev; v_plan_rev := c.plan_rev;
      EXIT;
    END IF;
  END LOOP;
  IF v_pkg_rev IS NULL THEN
    RAISE EXCEPTION 'XFER_NO_LANDING_PACKAGE: no eligible no-posting package at the destination' USING ERRCODE = 'P0001';
  END IF;

  -- After every lock: the instant the devices and sessions move (a session opened while this waited for the lock started earlier).
  v_now := clock_timestamp();

  PERFORM iam_v2.apply_entitlement_transition(v_from_ent, 'TERMINATED', v_now, 'TRANSFERRED');

  PERFORM iam_v2.begin_controlled_operation('commerce_intent');
  INSERT INTO iam_v2.purchases (tenant_id, site_id, package_revision_id, stay_id, trigger, amount_minor, state)
  VALUES (p_tenant, p_site, v_pkg_rev, p_to_stay, 'CROSS_PMS_TRANSFER', 0, 'GRANTED')
  RETURNING id INTO v_purchase;

  -- supersedes_entitlement_id stays unset: a transfer crosses subjects, a supersession does not.
  INSERT INTO iam_v2.entitlements (tenant_id, site_id, stay_id, purchase_id, policy_snapshot, service_plan_revision_id,
                                   package_revision_id, time_accounting_mode, end_mode, window_ends_at, status)
  VALUES (p_tenant, p_site, p_to_stay, v_purchase, '{}'::jsonb, v_plan_rev, v_pkg_rev, 'VALIDITY_WINDOW', 'VALIDITY_WINDOW',
          v_now + make_interval(secs => p_grace_seconds), 'PENDING')
  RETURNING id, window_ends_at INTO v_to_ent, v_window;
  PERFORM iam_v2.apply_entitlement_transition(v_to_ent, 'ACTIVE', v_now, NULL);

  -- Devices and live sessions rebind in place. The authorization history is an append-only interval log that accounting
  -- attribution reads, so the source's open intervals close at the instant the destination's open.
  PERFORM iam_v2.begin_controlled_operation('device_auth');
  INSERT INTO iam_v2.entitlement_devices (tenant_id, site_id, entitlement_id, device_id, status, grandfathered,
                                          first_authorized, last_authorized)
  SELECT p_tenant, p_site, v_to_ent, ed.device_id, 'AUTHORIZED', ed.grandfathered, ed.first_authorized, v_now
    FROM iam_v2.entitlement_devices ed
   WHERE ed.entitlement_id = v_from_ent AND ed.status = 'AUTHORIZED';
  GET DIAGNOSTICS v_devices = ROW_COUNT;

  INSERT INTO iam_v2.entitlement_device_authorizations (tenant_id, site_id, entitlement_id, device_id, seq, authorized_at)
  SELECT p_tenant, p_site, v_to_ent, ed.device_id, 1, v_now
    FROM iam_v2.entitlement_devices ed WHERE ed.entitlement_id = v_to_ent;

  WITH closed AS (
    UPDATE iam_v2.entitlement_device_authorizations a
       SET deauthorized_at = GREATEST(v_now, a.authorized_at)
     WHERE a.entitlement_id = v_from_ent AND a.deauthorized_at IS NULL
     RETURNING a.entitlement_id, a.device_id)
  UPDATE iam_v2.entitlement_devices ed
     SET status = 'DISCONNECTED', disconnected_reason = 'CROSS_PMS_TRANSFER'
    FROM closed WHERE ed.entitlement_id = closed.entitlement_id AND ed.device_id = closed.device_id;

  -- Sessions move through the binding writer, never by rewriting sessions.entitlement_id, so each sample is charged to the
  -- entitlement the session was bound to at its instant. Active and pending sessions both move.
  FOR v_sid IN SELECT s.id FROM iam_v2.sessions s
                WHERE s.tenant_id = p_tenant AND s.site_id = p_site AND s.entitlement_id = v_from_ent AND s.ended IS NULL
                  AND s.state IN ('active','PENDING_ENFORCEMENT') ORDER BY s.id
  LOOP
    PERFORM iam_v2.rebind_session_entitlement(v_sid, v_to_ent, v_now);
    v_sessions := v_sessions + 1;
  END LOOP;

  PERFORM iam_v2.p5_begin_controlled_operation('entitlement_transfer');
  INSERT INTO iam_v2.entitlement_transfers (tenant_id, site_id, from_entitlement_id, to_entitlement_id, from_stay_id, to_stay_id,
                                            actor, operator_reason)
  VALUES (p_tenant, p_site, v_from_ent, v_to_ent, p_from_stay, p_to_stay, p_operator, left(btrim(p_reason), 500))
  RETURNING id INTO v_transfer;
  INSERT INTO iam_v2.stay_links (tenant_id, site_id, from_stay, to_stay, reason)
  VALUES (p_tenant, p_site, p_from_stay, p_to_stay, 'CROSS_PMS_TRANSFER') ON CONFLICT DO NOTHING;

  RETURN QUERY SELECT v_transfer, v_from_ent, v_to_ent, v_pkg_rev, v_devices, v_sessions, v_window;
END $fn$;

COMMENT ON FUNCTION iam_v2.cross_pms_transfer(uuid, uuid, uuid, uuid, uuid, text, integer) IS
  'The staff-confirmed Cross-PMS Transfer in one transaction (0201). The only thing the Hotel Admin login may execute for it.';

REVOKE ALL ON FUNCTION iam_v2.cross_pms_transfer(uuid, uuid, uuid, uuid, uuid, text, integer) FROM PUBLIC;

DO $own$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
    EXECUTE 'ALTER FUNCTION iam_v2.cross_pms_transfer(uuid, uuid, uuid, uuid, uuid, text, integer) OWNER TO iam_v2_owner';
  END IF;
END $own$;

DO $g$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
    GRANT EXECUTE ON FUNCTION iam_v2.cross_pms_transfer(uuid, uuid, uuid, uuid, uuid, text, integer) TO svc_edged;
  END IF;
END $g$;

COMMIT;
