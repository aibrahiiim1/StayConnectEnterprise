-- SCRATCH platform fixture — minimal stand-ins for EXISTING live platform tables in `public`.
-- This is NOT an IAM migration; it represents the pre-existing platform so cross-schema FKs resolve.
-- The IAM migrations (MG-0..MG-9) add NOTHING to public except the MG-0 guest_networks anchor.
BEGIN;
CREATE TABLE IF NOT EXISTS public.tenants (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid());
CREATE TABLE IF NOT EXISTS public.sites (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES public.tenants(id),
  UNIQUE (tenant_id, id));
CREATE TABLE IF NOT EXISTS public.guest_networks (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL,
  site_id uuid NOT NULL);
-- The Phase-3 guest path resolves a device's NETWORK from its source address, against these columns. A stub
-- carrying only ids cannot exercise that at all — a test against it would prove the resolver runs, not that a
-- real guest on a real subnet reaches the right PMS interface. Added idempotently so the fixture stays safe to
-- re-run and matches the shape migration 0002 creates on a real appliance.
ALTER TABLE public.guest_networks
  ADD COLUMN IF NOT EXISTS name text,
  ADD COLUMN IF NOT EXISTS enabled boolean NOT NULL DEFAULT true,
  ADD COLUMN IF NOT EXISTS parent_interface text,
  ADD COLUMN IF NOT EXISTS bridge_name text,
  ADD COLUMN IF NOT EXISTS vlan_id int,
  ADD COLUMN IF NOT EXISTS gateway_cidr inet,
  ADD COLUMN IF NOT EXISTS gateway_ip inet,
  ADD COLUMN IF NOT EXISTS subnet_cidr cidr;
-- seed one tenant/site/guest_network for tests (deterministic uuids)
INSERT INTO public.tenants(id) VALUES ('11111111-1111-1111-1111-111111111111') ON CONFLICT DO NOTHING;
INSERT INTO public.sites(id, tenant_id) VALUES
  ('22222222-2222-2222-2222-222222222222','11111111-1111-1111-1111-111111111111') ON CONFLICT DO NOTHING;
INSERT INTO public.guest_networks(id, tenant_id, site_id, name, parent_interface, bridge_name,
                                  gateway_cidr, gateway_ip, subnet_cidr, enabled) VALUES
  ('33333333-3333-3333-3333-333333333333','11111111-1111-1111-1111-111111111111','22222222-2222-2222-2222-222222222222',
   'fixture-guests','ens192','br-fixture','10.90.0.1/22','10.90.0.1','10.90.0.0/22',true)
  ON CONFLICT DO NOTHING;
-- Phase 6 anchors its per-appliance product settings and its audit actor to the REAL platform records, so a
-- settings row cannot name an appliance that does not exist and an audit row cannot name an operator the
-- server never authenticated. Those FKs need the platform tables to exist here too. Same shape as migration
-- 0001/0005 create on a real appliance, reduced to the columns the FKs and the gate actually use.
CREATE TABLE IF NOT EXISTS public.appliances (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL,
  site_id uuid NOT NULL,
  serial text UNIQUE,
  name text);
CREATE TABLE IF NOT EXISTS public.operators (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid,
  email text,
  name text);
-- The edged API integration suites seed operators the way the real appliance schema shapes them. The stub
-- carried only id/tenant/email/name, so every one of those suites failed at fixture time on a missing column
-- and could not run here at all -- which is not the same thing as passing. These columns are additive and
-- match the appliance's own operators table.
ALTER TABLE public.operators ADD COLUMN IF NOT EXISTS display_name text;
ALTER TABLE public.operators ADD COLUMN IF NOT EXISTS password_hash text;
ALTER TABLE public.operators ADD COLUMN IF NOT EXISTS status text DEFAULT 'active';
ALTER TABLE public.operators ADD COLUMN IF NOT EXISTS site_id uuid;
ALTER TABLE public.operators ADD COLUMN IF NOT EXISTS created_at timestamptz DEFAULT now();
INSERT INTO public.appliances(id, tenant_id, site_id, serial, name) VALUES
  ('44444444-4444-4444-4444-444444444444','11111111-1111-1111-1111-111111111111',
   '22222222-2222-2222-2222-222222222222','APP-FIXTURE-0001','fixture-appliance')
  ON CONFLICT DO NOTHING;
INSERT INTO public.operators(id, tenant_id, email, name) VALUES
  ('55555555-5555-5555-5555-555555555555','11111111-1111-1111-1111-111111111111',
   'fixture-operator@example.invalid','Fixture Operator')
  ON CONFLICT DO NOTHING;
COMMIT;
