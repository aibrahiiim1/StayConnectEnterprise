-- Reverse of 0032. Restores 0031's release body and removes the session guard.
--
-- Rolling this back REINSTATES the defect it fixed: the release goes back to its own deauthorization and the
-- forbidden state becomes representable again. That is what a faithful reversal means here, and it is stated
-- rather than quietly softened -- a down migration that "improved" on the version it restores would make the
-- pair untrustworthy in both directions.
BEGIN;

DROP TRIGGER IF EXISTS p6_session_requires_authorized_binding ON iam_v2.sessions;
DROP FUNCTION IF EXISTS iam_v2.p6_session_requires_authorized_binding();

CREATE OR REPLACE FUNCTION iam_v2.p6_guest_release_device(
  p_entitlement uuid, p_device uuid, p_max_releases_per_hour int DEFAULT 20)
RETURNS text LANGUAGE plpgsql AS $$
DECLARE e record; b record; live_sessions int; recent int;
BEGIN
  SELECT id, tenant_id, site_id, status INTO e FROM iam_v2.entitlements WHERE id = p_entitlement FOR UPDATE;
  IF NOT FOUND THEN RAISE EXCEPTION 'no such entitlement' USING ERRCODE = 'foreign_key_violation'; END IF;
  SELECT count(*) INTO recent FROM iam_v2.guest_device_actions
   WHERE entitlement_id = p_entitlement AND action = 'RELEASE' AND acted_at > now() - interval '1 hour';
  IF recent >= p_max_releases_per_hour THEN
    INSERT INTO iam_v2.guest_device_actions (tenant_id, site_id, entitlement_id, device_id, action, outcome, detail)
      VALUES (e.tenant_id, e.site_id, e.id, p_device, 'RELEASE', 'REFUSED_THROTTLED', format('%s release attempts in the last hour', recent));
    RETURN 'REFUSED_THROTTLED';
  END IF;
  SELECT entitlement_id, device_id, status INTO b FROM iam_v2.entitlement_devices
   WHERE entitlement_id = p_entitlement AND device_id = p_device FOR UPDATE;
  IF NOT FOUND THEN
    INSERT INTO iam_v2.guest_device_actions (tenant_id, site_id, entitlement_id, device_id, action, outcome, detail)
      VALUES (e.tenant_id, e.site_id, e.id, p_device, 'RELEASE', 'REFUSED_NOT_FOUND', 'the device is not bound to this entitlement');
    RETURN 'REFUSED_NOT_FOUND';
  END IF;
  IF b.status <> 'AUTHORIZED' THEN
    INSERT INTO iam_v2.guest_device_actions (tenant_id, site_id, entitlement_id, device_id, action, outcome, detail)
      VALUES (e.tenant_id, e.site_id, e.id, p_device, 'RELEASE', 'REFUSED_ALREADY_RELEASED', format('binding is already %s', b.status));
    RETURN 'REFUSED_ALREADY_RELEASED';
  END IF;
  PERFORM 1 FROM iam_v2.sessions WHERE entitlement_id = p_entitlement AND device_id = p_device
     AND state IN ('active', 'PENDING_ENFORCEMENT') FOR UPDATE;
  GET DIAGNOSTICS live_sessions = ROW_COUNT;
  IF live_sessions > 0 THEN
    INSERT INTO iam_v2.guest_device_actions (tenant_id, site_id, entitlement_id, device_id, action, outcome, detail)
      VALUES (e.tenant_id, e.site_id, e.id, p_device, 'RELEASE', 'REFUSED_ONLINE', format('%s live session(s) on this device', live_sessions));
    RETURN 'REFUSED_ONLINE';
  END IF;
  UPDATE iam_v2.entitlement_devices SET status = 'DISCONNECTED', disconnected_reason = 'GUEST_SELF_SERVICE'
   WHERE entitlement_id = p_entitlement AND device_id = p_device AND status = 'AUTHORIZED';
  UPDATE iam_v2.entitlement_device_authorizations a SET deauthorized_at = GREATEST(now(), a.authorized_at)
   WHERE a.entitlement_id = p_entitlement AND a.device_id = p_device AND a.deauthorized_at IS NULL;
  INSERT INTO iam_v2.guest_device_actions (tenant_id, site_id, entitlement_id, device_id, action, outcome, detail)
    VALUES (e.tenant_id, e.site_id, e.id, p_device, 'RELEASE', 'OK', 'slot released by guest self-service');
  RETURN 'OK';
END $$;
REVOKE EXECUTE ON FUNCTION iam_v2.p6_guest_release_device(uuid, uuid, int) FROM PUBLIC;

DELETE FROM public.schema_migrations WHERE version = '0032_phase6_release_admission_serialization';

COMMIT;
