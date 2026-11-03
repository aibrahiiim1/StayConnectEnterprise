-- ONE-TIME (Product Owner, 2026-11-03): stays that paid for Full Stay on the hotel's previous system receive Full Stay here as a
-- zero-price administrative grant. Not a feature: the function lives in pg_temp, so it exists only for the psql session that
-- loads this file, and nothing is added to the schema.
--
-- It is staff Reset Package (0202, iam_v2.admin_reset_package) with one difference: the new entitlement is of another package
-- (Full Stay) instead of a new cycle of the same one. Everything else is Reset's: the Stay then the entitlement are locked in that
-- order; a live Free entitlement is terminated SUPERSEDED in the same transaction; its authorised devices and live sessions move to
-- the new entitlement (rebind_session_entitlement), so nobody is disconnected; the purchase is ADMIN_GRANT, amount 0, GRANTED, with
-- no quote and no settlement mapping, so no posting, payment or charge path reads it (they start from a quote or a settlement).
-- A stay with no live access gets the entitlement without devices; its first room sign-in joins it (live_access) with no choice.
--
-- Refused (raised, nothing written) unless: the Stay is IN_HOUSE on the given PMS interface; it holds no live access, or exactly
-- one ACTIVE entitlement of the given Free package revision's package; the target revision is the package's current revision.
-- Repeating it for a stay that already holds the target package changes nothing (outcome ALREADY_GRANTED).
-- No guest name, room or reservation text is written: the audit row carries ids and the operator's reason.

CREATE OR REPLACE FUNCTION pg_temp.onetime_fullstay_grant(p_tenant uuid, p_site uuid, p_stay uuid, p_iface uuid, p_free_pkg uuid,
                                                          p_target_rev uuid, p_operator uuid, p_reason text)
RETURNS TABLE(outcome text, old_entitlement uuid, new_entitlement uuid, purchase uuid, devices integer, sessions integer)
LANGUAGE plpgsql AS $fn$
DECLARE o record; t record; v_live int; v_now timestamptz; v_purchase uuid; v_new uuid; v_devices int := 0;
        v_sessions uuid[] := '{}'; v_sid uuid; v_ctx uuid;
BEGIN
  IF p_operator IS NULL OR p_reason IS NULL OR length(btrim(p_reason)) < 4 THEN
    RAISE EXCEPTION 'ONETIME_REFUSED: an operator and a reason are required';
  END IF;
  PERFORM 1 FROM public.operators op WHERE op.id = p_operator AND op.status = 'active' AND (op.tenant_id IS NULL OR op.tenant_id = p_tenant);
  IF NOT FOUND THEN
    RAISE EXCEPTION 'ONETIME_REFUSED: the operator is not an active operator of this tenant';
  END IF;

  -- Lock order: the Stay, then its entitlements (as checkout, transfer, admission and Reset do).
  PERFORM 1 FROM iam_v2.stays st
   WHERE st.id = p_stay AND st.tenant_id = p_tenant AND st.site_id = p_site AND st.pms_interface_id = p_iface
     AND st.status = 'IN_HOUSE'
   FOR UPDATE;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'ONETIME_REFUSED: the stay is not in house on this PMS interface';
  END IF;

  SELECT r.id, r.package_id, r.service_plan_revision_id, spr.time_accounting_mode,
         COALESCE(r.duration_policy->>'end_mode', 'MANUAL_END') AS end_mode
    INTO t
    FROM iam_v2.internet_package_revisions r
    JOIN iam_v2.internet_packages p ON p.id = r.package_id AND p.current_revision_id = r.id
    JOIN iam_v2.service_plan_revisions spr ON spr.id = r.service_plan_revision_id
   WHERE r.id = p_target_rev AND r.tenant_id = p_tenant AND r.site_id = p_site AND p.is_system IS NOT TRUE;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'ONETIME_REFUSED: the target is not the current revision of a site package';
  END IF;
  IF t.end_mode <> 'MANUAL_END' THEN
    RAISE EXCEPTION 'ONETIME_REFUSED: the target package must last until the stay ends (MANUAL_END), found %', t.end_mode;
  END IF;

  SELECT count(*) INTO v_live FROM iam_v2.entitlements e
   WHERE e.stay_id = p_stay AND e.status IN ('PENDING','ACTIVE','SUSPENDED');
  IF v_live > 1 THEN
    RAISE EXCEPTION 'ONETIME_REFUSED: the stay holds % live entitlements', v_live;
  END IF;
  IF v_live = 1 THEN
    SELECT e.* INTO o FROM iam_v2.entitlements e
     WHERE e.stay_id = p_stay AND e.status IN ('PENDING','ACTIVE','SUSPENDED') FOR UPDATE;
    IF o.package_revision_id = p_target_rev THEN
      RETURN QUERY SELECT 'ALREADY_GRANTED'::text, NULL::uuid, o.id, o.purchase_id, 0, 0;
      RETURN;
    END IF;
    IF o.status <> 'ACTIVE' OR NOT EXISTS (SELECT 1 FROM iam_v2.internet_package_revisions r
                                            WHERE r.id = o.package_revision_id AND r.package_id = p_free_pkg) THEN
      RAISE EXCEPTION 'ONETIME_REFUSED: the stay''s live access is not an ACTIVE Free entitlement';
    END IF;
    IF EXISTS (SELECT 1 FROM iam_v2.entitlements s WHERE s.supersedes_entitlement_id = o.id) THEN
      RAISE EXCEPTION 'ONETIME_REFUSED: the Free entitlement was already superseded';
    END IF;
  END IF;

  v_now := clock_timestamp();
  IF v_live = 1 THEN
    PERFORM iam_v2.terminate_entitlement_at_boundary(o.id, v_now, 'SUPERSEDED');
    SELECT pu.auth_context_id INTO v_ctx FROM iam_v2.purchases pu WHERE pu.id = o.purchase_id;
  END IF;

  -- A zero-price administrative purchase: never a payment, a posting, a quote or a redemption.
  PERFORM iam_v2.begin_controlled_operation('commerce_intent');
  INSERT INTO iam_v2.purchases (tenant_id, site_id, package_revision_id, pms_interface_id, stay_id, auth_context_id, trigger,
                                amount_minor, state)
  VALUES (p_tenant, p_site, p_target_rev, p_iface, p_stay, v_ctx, 'ADMIN_GRANT', 0, 'GRANTED')
  RETURNING id INTO v_purchase;

  -- Full Stay's own terms: its revisions, end mode and the plan's allowance (data_quota_bytes NULL = the plan's).
  INSERT INTO iam_v2.entitlements (tenant_id, site_id, stay_id, guest_principal_id, anonymous_subject_id, pms_interface_id,
                                   purchase_id, policy_snapshot, service_plan_revision_id, package_revision_id,
                                   time_accounting_mode, end_mode, window_ends_at, status, supersedes_entitlement_id,
                                   data_quota_bytes)
  VALUES (p_tenant, p_site, p_stay, CASE WHEN v_live = 1 THEN o.guest_principal_id END,
          CASE WHEN v_live = 1 THEN o.anonymous_subject_id END, p_iface, v_purchase,
          jsonb_build_object('package_revision_id', p_target_rev::text, 'service_plan_revision_id', t.service_plan_revision_id::text,
                             'granted_from', 'ADMIN_ONETIME_MIGRATION',
                             'migrated_from', CASE WHEN v_live = 1 THEN o.id::text END),
          t.service_plan_revision_id, p_target_rev, t.time_accounting_mode, 'MANUAL_END', NULL, 'PENDING',
          CASE WHEN v_live = 1 THEN o.id END, NULL)
  RETURNING id INTO v_new;
  PERFORM iam_v2.apply_entitlement_transition(v_new, 'ACTIVE', v_now, 'GRANT');

  IF v_live = 1 THEN
    -- Reset's device move: the old interval closes when the new opens.
    PERFORM iam_v2.begin_controlled_operation('device_auth');
    INSERT INTO iam_v2.entitlement_devices (tenant_id, site_id, entitlement_id, device_id, status, grandfathered,
                                            first_authorized, last_authorized)
    SELECT p_tenant, p_site, v_new, ed.device_id, 'AUTHORIZED', ed.grandfathered, ed.first_authorized, v_now
      FROM iam_v2.entitlement_devices ed
     WHERE ed.entitlement_id = o.id AND ed.status = 'AUTHORIZED';
    GET DIAGNOSTICS v_devices = ROW_COUNT;
    INSERT INTO iam_v2.entitlement_device_authorizations (tenant_id, site_id, entitlement_id, device_id, seq, authorized_at)
    SELECT p_tenant, p_site, v_new, ed.device_id, 1, v_now
      FROM iam_v2.entitlement_devices ed WHERE ed.entitlement_id = v_new AND ed.status = 'AUTHORIZED';
    WITH closed AS (
      UPDATE iam_v2.entitlement_device_authorizations da
         SET deauthorized_at = GREATEST(v_now, da.authorized_at)
       WHERE da.entitlement_id = o.id AND da.deauthorized_at IS NULL
       RETURNING da.entitlement_id, da.device_id)
    UPDATE iam_v2.entitlement_devices ed
       SET status = 'DISCONNECTED', disconnected_reason = 'PACKAGE_RESET'
      FROM closed WHERE ed.entitlement_id = closed.entitlement_id AND ed.device_id = closed.device_id;

    FOR v_sid IN SELECT s.id FROM iam_v2.sessions s
                  WHERE s.tenant_id = p_tenant AND s.site_id = p_site AND s.entitlement_id = o.id AND s.ended IS NULL
                    AND s.state IN ('active','PENDING_ENFORCEMENT')
                    AND EXISTS (SELECT 1 FROM iam_v2.entitlement_devices nd
                                 WHERE nd.entitlement_id = v_new AND nd.device_id = s.device_id AND nd.status = 'AUTHORIZED')
                  ORDER BY s.id
    LOOP
      PERFORM iam_v2.rebind_session_entitlement(v_sid, v_new, v_now);
      v_sessions := v_sessions || v_sid;
    END LOOP;
    UPDATE iam_v2.sessions s
       SET state = 'ended', ended = GREATEST(v_now, s.started), end_reason = 'ENTITLEMENT_ENDED'
     WHERE s.entitlement_id = o.id AND s.ended IS NULL AND s.state IN ('active','PENDING_ENFORCEMENT');
  END IF;

  INSERT INTO public.audit_log (tenant_id, actor_type, actor_id, action, target_type, target_id, payload)
  VALUES (p_tenant, 'operator', p_operator::text, 'package.onetime_migration_granted', 'stay', p_stay::text,
          jsonb_build_object('old_entitlement_id', CASE WHEN v_live = 1 THEN o.id::text END, 'new_entitlement_id', v_new::text,
                             'purchase_id', v_purchase::text, 'amount_minor', 0, 'devices_moved', v_devices,
                             'sessions_moved', cardinality(v_sessions), 'reason', left(btrim(p_reason), 500)));

  RETURN QUERY SELECT CASE WHEN v_live = 1 THEN 'SWITCHED' ELSE 'GRANTED' END, CASE WHEN v_live = 1 THEN o.id END,
                      v_new, v_purchase, v_devices, cardinality(v_sessions);
END $fn$;
