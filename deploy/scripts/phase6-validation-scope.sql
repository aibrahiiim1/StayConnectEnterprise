-- PHASE-6 CONTROLLED VALIDATION -- THE SYNTHETIC SCOPE, SEEDED AT RESERVED IDENTITIES.
--
-- How controlled state is identified is a safety property, not a convenience. The earlier harness tagged its
-- rows with a marker written into purchases.trigger, which is wrong twice over: that column carries a CHECK
-- listing the ten real commercial triggers, so the write would simply have failed -- and had it succeeded, it
-- would have widened a business vocabulary to describe a test.
--
-- What identifies controlled state instead is RESERVED IDENTITY. Every row created here has a fixed uuid from
-- one reserved block (6d5f0000-...), and the two guest devices carry reserved MAC addresses from the
-- locally-administered range. Nothing is invented, nothing is added to any constrained vocabulary, and every
-- row is schema-valid under the ordinary product constraints -- including purchases.trigger = 'ADMIN_GRANT',
-- which is the literal truth of what a zero-price administrative grant is.
--
-- Because the identities are FIXED rather than generated, teardown works from a cold start: a run killed
-- before it could record anything still leaves state that cleanup can find and end. And because teardown
-- names those exact ids, it cannot reach a real guest's row even if it runs twice, concurrently, or years
-- later.
--
-- WHY IT SITS IN THE REAL TENANT AND SITE. It has to. scd resolves a requesting device against its OWN
-- appliance identity -- `INSERT INTO iam_v2.devices ... VALUES (p.srv.tenID, p.srv.siteID, p.srv.applID, mac)`
-- -- and refuses any source address that is not on a mapped guest network. A fixture in a tenant of its own
-- would therefore be unreachable through the real guest route, and the validation would prove the handler
-- works against a fixture rather than against the appliance. The devices are pre-created at the reserved MACs
-- so that scd's own upsert resolves to exactly these rows.
--
--   psql -v ten=<tenant> -v site=<site> -v appl=<appliance> -v gip=<an address on a mapped guest network>
--
-- Re-running is safe: the file tears its own identities down first.
\set ON_ERROR_STOP on

-- NOTE ON RE-RUNS. This file does NOT \i the teardown. psql runs inside the database container, so a \i of a
-- host path resolves against the container's filesystem and fails there -- silently turning "seeded from a
-- clean slate" into "seeded on top of whatever the last run left". The harness runs the teardown as its own
-- statement, on the host side, before calling this.
BEGIN;

-- THE CONTROLLED-OPERATION SCOPES THIS TRANSACTION NEEDS. Phase 3's writer guard refuses any write to the
-- stay and commerce families unless the transaction has declared it is performing one -- which is why a
-- direct INSERT into stays was rejected with "writes to the stay family require an open controlled
-- operation". That guard is doing its job; the fixture opens the scopes properly rather than being exempted.
SELECT iam_v2.begin_controlled_operation('stay');
SELECT iam_v2.begin_controlled_operation('commerce_intent');

-- ITS OWN PMS INTERFACE, AND DELIBERATELY AUTH_DISABLED. The appliance has no PMS interface row at all -- it
-- is a development appliance that has never been pointed at a property management system -- so selecting "the
-- site's interface" silently returned nothing and every dependent insert below vanished with it. The fixture
-- therefore brings its own, at a reserved id.
--
-- AUTH_DISABLED rather than ACTIVE is the point, not a detail. This validation must never cause PMS traffic;
-- pmsd is enabled on this appliance even though it is not currently running, and an ACTIVE interface is an
-- invitation for it to try. AUTH_DISABLED is an existing lifecycle state that says exactly what is true here:
-- the interface exists to hang a stay off, and it authenticates nobody.
INSERT INTO iam_v2.pms_interfaces (id, tenant_id, site_id, connector_kind, display_label, lifecycle_state)
VALUES ('6d5f0000-0000-4000-8000-000000000101', :'ten'::uuid, :'site'::uuid, 'protel-fias',
        'Phase-6 controlled validation (no PMS)', 'AUTH_DISABLED')
ON CONFLICT (id) DO UPDATE SET lifecycle_state = 'AUTH_DISABLED';

-- A stay, so the entitlement hangs off something real-shaped. No PMS is contacted and no PMS event is
-- produced: the row is written directly, exactly as an administrative grant does.
-- THE SKELETON IS FIXED AND REUSED; THE GRANT IS FRESH EVERY RUN. An entitlement cannot be reset and used
-- again: its state transitions and its termination evidence are append-only by trigger, and the evidence is
-- keyed on the entitlement, so a second exhaustion would collide with the first run's row. Teardown ends
-- entitlements rather than deleting them, so re-seeding on the same id fails on the second run -- which is
-- exactly what happened: "duplicate key value violates unique constraint stays_pkey".
--
-- So the durable skeleton (interface, stay, plan, package) is created once and reused, and the purchase,
-- entitlement and session are NEW each run. They are still unambiguously identifiable, and by something
-- stronger than a marker: everything hangs off the reserved stay, and teardown works from that.
INSERT INTO iam_v2.stays
  (id, tenant_id, site_id, pms_interface_id, external_reservation_id, external_stay_identity,
   status, lifecycle_version, last_applied_event_version)
VALUES ('6d5f0000-0000-4000-8000-000000000102', :'ten'::uuid, :'site'::uuid,
        '6d5f0000-0000-4000-8000-000000000101', 'P6-VALIDATION', 'P6-VALIDATION', 'IN_HOUSE', 1, 0)
ON CONFLICT (id) DO NOTHING;

INSERT INTO iam_v2.service_plans (id, tenant_id, site_id, code, enabled)
VALUES ('6d5f0000-0000-4000-8000-000000000103', :'ten'::uuid, :'site'::uuid, 'p6-validation', true)
ON CONFLICT (id) DO NOTHING;

-- Revision 1 is the ORDINARY mode and revision 2 is the aggregate one. Both exist because the validation has
-- to show that publishing the new mode leaves the old one behaving exactly as before. Plan revisions are
-- immutable, so an existing revision is never reinterpreted; having both here is what lets that be tested
-- rather than asserted.
INSERT INTO iam_v2.service_plan_revisions
  (id, tenant_id, site_id, service_plan_id, revision_no, down_kbps, up_kbps, max_concurrent_devices,
   device_limit_policy, time_accounting_mode, data_quota_bytes)
VALUES ('6d5f0000-0000-4000-8000-000000000104', :'ten'::uuid, :'site'::uuid,
        '6d5f0000-0000-4000-8000-000000000103', 1, 4000, 2000, 4, 'REJECT_NEW_DEVICE', 'VALIDITY_WINDOW', 0)
ON CONFLICT (id) DO NOTHING;

-- 600 seconds of budget: long enough that ordinary use does not exhaust it by accident, short enough that the
-- exhaustion proof can reach it by moving the watermark rather than by waiting.
INSERT INTO iam_v2.service_plan_revisions
  (id, tenant_id, site_id, service_plan_id, revision_no, down_kbps, up_kbps, max_concurrent_devices,
   device_limit_policy, time_accounting_mode, time_quota_seconds)
VALUES ('6d5f0000-0000-4000-8000-000000000105', :'ten'::uuid, :'site'::uuid,
        '6d5f0000-0000-4000-8000-000000000103', 2, 4000, 2000, 4, 'REJECT_NEW_DEVICE',
        'AGGREGATE_ONLINE_TIME', 600)
ON CONFLICT (id) DO NOTHING;

INSERT INTO iam_v2.internet_packages (id, tenant_id, site_id, code, is_system)
VALUES ('6d5f0000-0000-4000-8000-000000000106', :'ten'::uuid, :'site'::uuid, 'p6-validation', false)
ON CONFLICT (id) DO NOTHING;

INSERT INTO iam_v2.internet_package_revisions
  (id, tenant_id, site_id, package_id, revision_no, service_plan_revision_id, package_type, price_minor,
   settlement_methods, duration_policy)
VALUES ('6d5f0000-0000-4000-8000-000000000107', :'ten'::uuid, :'site'::uuid,
        '6d5f0000-0000-4000-8000-000000000106', 1, '6d5f0000-0000-4000-8000-000000000105',
        'FREE_STAY', 0, ARRAY['NOT_REQUIRED']::text[], '{}'::jsonb)
ON CONFLICT (id) DO NOTHING;

-- THE TWO DEVICES, at reserved locally-administered MACs. scd's own resolution upserts on
-- (tenant, site, appliance, mac), so a guest request arriving from these addresses resolves to exactly these
-- rows -- which is what makes the guest route exercise the real path instead of a fixture.
INSERT INTO iam_v2.devices (id, tenant_id, site_id, appliance_id, mac, last_seen)
VALUES ('6d5f0000-0000-4000-8000-000000000201', :'ten'::uuid, :'site'::uuid, :'appl'::uuid,
        '02:00:00:60:00:01'::macaddr, now()),
       ('6d5f0000-0000-4000-8000-000000000202', :'ten'::uuid, :'site'::uuid, :'appl'::uuid,
        '02:00:00:60:00:02'::macaddr, now() - interval '30 minutes')
ON CONFLICT (tenant_id, site_id, appliance_id, mac) DO NOTHING;

-- ADMIN_GRANT, price zero, settlement not required: the free administrative grant this genuinely is. No paid
-- access is created, no provider or PMS is contacted, and no financial record of any kind is produced.
--
-- Purchase and entitlement are created in ONE statement, and the entitlement takes the purchase id straight
-- out of the CTE. The first attempt looked the id up afterwards with ORDER BY created_at -- a column neither
-- table has. Chaining them removes the lookup rather than fixing it: there is no ordering question if the
-- value is never searched for.
WITH pur AS (
  INSERT INTO iam_v2.purchases
    (id, tenant_id, site_id, package_revision_id, pms_interface_id, stay_id, trigger, amount_minor, state)
  SELECT gen_random_uuid(), :'ten'::uuid, :'site'::uuid,
         '6d5f0000-0000-4000-8000-000000000107', s.pms_interface_id, s.id, 'ADMIN_GRANT', 0, 'GRANTED'
    FROM iam_v2.stays s WHERE s.id = '6d5f0000-0000-4000-8000-000000000102'
  RETURNING id, tenant_id, site_id, stay_id, pms_interface_id
)
INSERT INTO iam_v2.entitlements
  (id, tenant_id, site_id, stay_id, pms_interface_id, purchase_id, policy_snapshot,
   service_plan_revision_id, package_revision_id, time_accounting_mode, end_mode, status, window_ends_at)
SELECT gen_random_uuid(), pur.tenant_id, pur.site_id, pur.stay_id, pur.pms_interface_id, pur.id, '{}'::jsonb,
       '6d5f0000-0000-4000-8000-000000000105', '6d5f0000-0000-4000-8000-000000000107',
       'AGGREGATE_ONLINE_TIME', 'VALIDITY_WINDOW', 'ACTIVE', now() + interval '1 day'
  FROM pur;

-- FROM HERE ON, "THE ENTITLEMENT OF THIS RUN" IS THE ONE THAT IS NOT TERMINATED. Teardown ran first and ended
-- every earlier grant on this stay, so exactly one lives -- which is a definition that needs no timestamp
-- column and cannot pick up a previous run's row.
SELECT iam_v2.apply_entitlement_transition(
  (SELECT id FROM iam_v2.entitlements
    WHERE stay_id = '6d5f0000-0000-4000-8000-000000000102' AND status <> 'TERMINATED'),
  'ACTIVE', now() - interval '10 minutes', 'GRANT');

-- Device A is the one the guest is holding and is online; device B was last seen half an hour ago, so it is
-- the one the release proof may remove. That asymmetry is the product rule, not a fixture convenience.
SELECT iam_v2.authorize_entitlement_device(
  (SELECT id FROM iam_v2.entitlements
    WHERE stay_id = '6d5f0000-0000-4000-8000-000000000102' AND status <> 'TERMINATED'),
  d.id, now() - interval '10 minutes')
  FROM iam_v2.devices d
 WHERE d.tenant_id = :'ten'::uuid AND d.site_id = :'site'::uuid AND d.appliance_id = :'appl'::uuid
   AND d.mac = '02:00:00:60:00:01'::macaddr;

SELECT iam_v2.authorize_entitlement_device(
  (SELECT id FROM iam_v2.entitlements
    WHERE stay_id = '6d5f0000-0000-4000-8000-000000000102' AND status <> 'TERMINATED'),
  d.id, now() - interval '30 minutes')
  FROM iam_v2.devices d
 WHERE d.tenant_id = :'ten'::uuid AND d.site_id = :'site'::uuid AND d.appliance_id = :'appl'::uuid
   AND d.mac = '02:00:00:60:00:02'::macaddr;

INSERT INTO iam_v2.sessions (id, tenant_id, site_id, entitlement_id, device_id, state, started, ip, mac)
SELECT gen_random_uuid(), :'ten'::uuid, :'site'::uuid,
       (SELECT id FROM iam_v2.entitlements
         WHERE stay_id = '6d5f0000-0000-4000-8000-000000000102' AND status <> 'TERMINATED'),
       d.id, 'active', now() - interval '10 minutes', :'gip'::inet, '02:00:00:60:00:01'::macaddr
  FROM iam_v2.devices d
 WHERE d.tenant_id = :'ten'::uuid AND d.site_id = :'site'::uuid AND d.appliance_id = :'appl'::uuid
   AND d.mac = '02:00:00:60:00:01'::macaddr;

INSERT INTO iam_v2.session_online_watermarks
  (tenant_id, site_id, session_id, accounted_through, accounted_seconds)
SELECT :'ten'::uuid, :'site'::uuid, se.id, now() - interval '10 minutes', 0
  FROM iam_v2.sessions se
 WHERE se.entitlement_id = (SELECT id FROM iam_v2.entitlements
                             WHERE stay_id = '6d5f0000-0000-4000-8000-000000000102'
                               AND status <> 'TERMINATED');

COMMIT;

SELECT 'P6_SCOPE_SEEDED entitlement=' || e.id || ' status=' || e.status || ' devices=' ||
       (SELECT count(*) FROM iam_v2.entitlement_devices
         WHERE entitlement_id = e.id AND status = 'AUTHORIZED') AS seeded
  FROM iam_v2.entitlements e
 WHERE e.stay_id = '6d5f0000-0000-4000-8000-000000000102' AND e.status <> 'TERMINATED';
