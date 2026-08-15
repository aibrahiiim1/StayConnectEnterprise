-- 0047 — THE GUEST SURFACE HAS TO BE ABLE TO RESOLVE THE DEVICE THAT IS ASKING.
--
-- Found by running the Phase-6 guest route on the development appliance, under the real service role, against
-- the real database — which is what live-dark validation is for. Every unit and integration test connects as a
-- role that owns the schema, so none of them could ever have seen this:
--
--   phase6 device self-service: unavailable
--   reason: device_identity: ERROR: permission denied for table devices (SQLSTATE 42501)
--
-- 0033 enumerated what svc_scd needs for the guest surface and granted SELECT on iam_v2.devices, on the
-- reasoning that listing a guest's devices is a read. That is true of the listing and false of the request:
-- the FIRST thing either handler does is resolve the caller to a durable identity, and that resolution is an
-- upsert —
--
--   INSERT INTO iam_v2.devices (tenant_id, site_id, appliance_id, mac)
--   VALUES (...) ON CONFLICT (tenant_id, site_id, appliance_id, mac) DO UPDATE SET mac = EXCLUDED.mac
--
-- — because a device's row is created the first time the appliance sees it. A read-only grant therefore makes
-- the whole capability fail closed on its first line, for every guest, in a way that looks like the feature
-- being switched off. Fail-closed is the right direction to fail in; being unable to work at all is not a
-- design, it is an omission.
--
-- WHAT THIS GRANTS, AND WHAT IT STILL REFUSES. INSERT, so a device can appear. UPDATE on THREE COLUMNS ONLY —
-- mac, last_seen, last_ip — because those are the only ones the two upsert paths touch (phase3_auth.device
-- rewrites mac with the value already there; UpsertDevice advances last_seen and last_ip). Column-level rather
-- than table-level: the guest surface has no business changing which tenant, site or appliance a device
-- belongs to, and a whole-table UPDATE would hand it exactly that. DELETE is not granted and is asserted
-- against in the privilege gate — devices are durable identity, and the surface that lets a guest release a
-- BINDING must never be able to make the device itself disappear.
--
-- Nothing about the release path changes: releasing a device is still p6_guest_release_device, which svc_scd
-- may EXECUTE and cannot bypass, and it operates on entitlement_devices rather than on devices.
\set ON_ERROR_STOP on

GRANT INSERT ON iam_v2.devices TO svc_scd;
GRANT UPDATE (mac, last_seen, last_ip) ON iam_v2.devices TO svc_scd;

-- AND THE TWO READS THE SAME PATH MAKES, missing for the same reason. Immediately after resolving the device
-- the handler resolves that device's entitlement, and the listing reports remaining time from the pinned plan
-- revision:
--
--   phase6 device self-service: unavailable
--   reason: no_entitlement_for_device: ERROR: permission denied for table entitlements (SQLSTATE 42501)
--
-- READ ONLY, and that is the whole point. svc_scd never writes an entitlement: creating, suspending and
-- terminating them belong to the grant kernel and the enforcement owner, and the release path reaches
-- entitlement_devices through p6_guest_release_device rather than touching either table directly. The
-- assertions below check the refusals as well as the grants, because a privilege model is defined by what it
-- withholds.
GRANT SELECT ON iam_v2.entitlements TO svc_scd;
GRANT SELECT ON iam_v2.service_plan_revisions TO svc_scd;

COMMENT ON TABLE iam_v2.devices IS
  'Durable device identity. svc_scd may INSERT (a device appears the first time the appliance sees it) and may '
  'UPDATE only mac, last_seen and last_ip. It may not DELETE, and it may not move a device between tenant, '
  'site or appliance: those columns are outside its column-level grant.';

DO $$
BEGIN
  IF NOT has_table_privilege('svc_scd', 'iam_v2.devices', 'INSERT') THEN
    RAISE EXCEPTION 'svc_scd still cannot insert a device; the guest surface would fail on its first line';
  END IF;
  IF NOT has_column_privilege('svc_scd', 'iam_v2.devices', 'last_seen', 'UPDATE') THEN
    RAISE EXCEPTION 'svc_scd cannot advance last_seen; the device upsert would be refused';
  END IF;
  -- The refusals matter as much as the grants, so they are checked here rather than assumed from the absence
  -- of a GRANT statement.
  IF has_table_privilege('svc_scd', 'iam_v2.devices', 'DELETE') THEN
    RAISE EXCEPTION 'svc_scd can delete devices; durable identity must not be removable by the guest surface';
  END IF;
  IF has_column_privilege('svc_scd', 'iam_v2.devices', 'tenant_id', 'UPDATE') THEN
    RAISE EXCEPTION 'svc_scd can move a device between tenants';
  END IF;
  IF has_column_privilege('svc_scd', 'iam_v2.devices', 'appliance_id', 'UPDATE') THEN
    RAISE EXCEPTION 'svc_scd can move a device between appliances';
  END IF;
  IF NOT has_table_privilege('svc_scd', 'iam_v2.entitlements', 'SELECT') THEN
    RAISE EXCEPTION 'svc_scd cannot read entitlements; the guest surface cannot resolve its own subject';
  END IF;
  IF has_table_privilege('svc_scd', 'iam_v2.entitlements', 'INSERT')
     OR has_table_privilege('svc_scd', 'iam_v2.entitlements', 'UPDATE')
     OR has_table_privilege('svc_scd', 'iam_v2.entitlements', 'DELETE') THEN
    RAISE EXCEPTION 'svc_scd can write entitlements; the guest surface reads them and nothing more';
  END IF;
  IF has_table_privilege('svc_scd', 'iam_v2.service_plan_revisions', 'UPDATE') THEN
    RAISE EXCEPTION 'svc_scd can rewrite a plan revision; revisions are immutable';
  END IF;
END $$;
