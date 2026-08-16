-- PHASE-7 — THE DETERMINISTIC TEST FIXTURE, FOR THE ACCEPTED SCHEMA.
--
-- THIS IS TEST DATA. It is not, and must never be quoted as, a statement about what the DEVELOPMENT appliance
-- or any production system contains. The accepted schema carries no rows; these are the rows the GATES need.
--
-- WHY IT IS NOT 00_platform_fixture.sql. That fixture was written against the SCRATCH `public` schema -- the
-- file says so in its own first line, "minimal stand-ins for EXISTING live platform tables" -- and it does
-- not satisfy the real one: the accepted `public.tenants` has a NOT NULL `slug`, `sites` needs `code` and
-- `name`, `guest_networks` needs its addressing columns, and applying the scratch fixture to the accepted
-- schema fails on the first INSERT. Two lineages again, and the same lesson as mg1-mg9: a scratch stand-in is
-- not the accepted thing.
--
-- WHAT IS PRESERVED is the part the gates actually depend on -- the canonical IDENTIFIERS. Eleven gates
-- address these uuids literally and seed none of them:
--
--   tenant        11111111-1111-1111-1111-111111111111
--   site          22222222-2222-2222-2222-222222222222
--   guest network 33333333-3333-3333-3333-333333333333
--   appliance     44444444-4444-4444-4444-444444444444
--
-- A gate that fails for want of these rows has a MISSING FIXTURE, not a product regression. Keeping that
-- distinction is the whole reason this file is separate from the schema restore.
\set ON_ERROR_STOP on
BEGIN;

INSERT INTO public.tenants(id, slug, name)
VALUES ('11111111-1111-1111-1111-111111111111', 'p7-fixture', 'Phase-7 fixture tenant')
ON CONFLICT (id) DO NOTHING;

INSERT INTO public.sites(id, tenant_id, code, name)
VALUES ('22222222-2222-2222-2222-222222222222', '11111111-1111-1111-1111-111111111111',
        'P7SITE', 'Phase-7 fixture site')
ON CONFLICT (id) DO NOTHING;

-- The addressing is deliberately inside a documentation range and unroutable: a fixture must never describe a
-- network somebody could mistake for real, and the gates only care that a mapped guest network exists.
INSERT INTO public.guest_networks(id, tenant_id, site_id, name, parent_interface, bridge_name,
                                  gateway_cidr, gateway_ip, subnet_cidr, enabled)
VALUES ('33333333-3333-3333-3333-333333333333', '11111111-1111-1111-1111-111111111111',
        '22222222-2222-2222-2222-222222222222', 'p7-fixture-net', 'ens192', 'br-p7',
        '198.51.100.1/24'::inet, '198.51.100.1'::inet, '198.51.100.0/24'::cidr, true)
ON CONFLICT (id) DO NOTHING;

INSERT INTO public.appliances(id, tenant_id, site_id, serial, name)
VALUES ('44444444-4444-4444-4444-444444444444', '11111111-1111-1111-1111-111111111111',
        '22222222-2222-2222-2222-222222222222', 'P7-FIXTURE-0001', 'Phase-7 fixture appliance')
ON CONFLICT (id) DO NOTHING;

-- Several gates resolve "an operator" by taking the first row rather than by id, so one is enough and its id
-- is not load-bearing.
INSERT INTO public.operators(id, email)
VALUES ('55555555-5555-5555-5555-555555555555', 'phase7-fixture@example.invalid')
ON CONFLICT (id) DO NOTHING;

COMMIT;

SELECT 'P7_FIXTURE_OK tenants='   || (SELECT count(*) FROM public.tenants  WHERE id='11111111-1111-1111-1111-111111111111')
    || ' sites='                  || (SELECT count(*) FROM public.sites    WHERE id='22222222-2222-2222-2222-222222222222')
    || ' networks='               || (SELECT count(*) FROM public.guest_networks WHERE id='33333333-3333-3333-3333-333333333333')
    || ' appliances='             || (SELECT count(*) FROM public.appliances WHERE id='44444444-4444-4444-4444-444444444444')
    || ' operators='              || (SELECT count(*) FROM public.operators) AS fixture;
