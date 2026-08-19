-- MG-0 — the contract-defined tenant/site-scoped guest-network anchor.
--
-- iam_v2.guest_network_pms_map (tenant_id, site_id, guest_network_id) carries a composite FOREIGN KEY to
-- public.guest_networks (tenant_id, site_id, id). PostgreSQL requires a unique index or constraint on the
-- referenced columns, and migration 0002 gives guest_networks only PRIMARY KEY (id). This index is that
-- target, and without it the entire IAM-v2 base schema cannot be created.
--
-- The accepted Phase-1A MG-0 built it with CREATE UNIQUE INDEX CONCURRENTLY, because on a live appliance it
-- is added to a table already in service. A factory-clean install has an empty, unattached table, so the same
-- index is built inside the normal migration transaction. The OBJECT is what matters and it is identical:
-- unique, btree, (tenant_id, site_id, id), named guest_networks_tsi_anchor.
--
-- Deliberately NOT an ALTER TABLE ... ADD CONSTRAINT ... UNIQUE. That would also satisfy the foreign key, but
-- it would create a different catalog object from the accepted baseline and the semantic comparison against
-- the reference appliance would -- correctly -- refuse it.
CREATE UNIQUE INDEX IF NOT EXISTS guest_networks_tsi_anchor
    ON public.guest_networks (tenant_id, site_id, id);

DO $$
BEGIN
  -- An invalid index is worse than a missing one: it exists, so IF NOT EXISTS skips the rebuild, and the
  -- foreign key it is supposed to anchor cannot use it.
  IF NOT EXISTS (
    SELECT 1 FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid
     WHERE c.relname = 'guest_networks_tsi_anchor' AND i.indisvalid AND i.indisready
  ) THEN
    RAISE EXCEPTION 'MG-0: guest_networks_tsi_anchor is missing or not valid';
  END IF;
END $$;
