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
-- seed one tenant/site/guest_network for tests (deterministic uuids)
INSERT INTO public.tenants(id) VALUES ('11111111-1111-1111-1111-111111111111') ON CONFLICT DO NOTHING;
INSERT INTO public.sites(id, tenant_id) VALUES
  ('22222222-2222-2222-2222-222222222222','11111111-1111-1111-1111-111111111111') ON CONFLICT DO NOTHING;
INSERT INTO public.guest_networks(id, tenant_id, site_id) VALUES
  ('33333333-3333-3333-3333-333333333333','11111111-1111-1111-1111-111111111111','22222222-2222-2222-2222-222222222222')
  ON CONFLICT DO NOTHING;
COMMIT;
