-- Enforce the ownership hierarchy at the DATABASE layer (not only in Go handlers):
-- an Appliance's (site_id, tenant_id) pair must reference a Site that is actually
-- owned by that same Customer (tenant). This makes an "appliance under a site owned
-- by a different customer" state impossible even if a future code path forgets the
-- application-level check.
--
-- Mechanism: a composite FK (site_id, tenant_id) -> sites(id, tenant_id). Postgres
-- composite FKs use MATCH SIMPLE by default, so a row where EITHER column is NULL is
-- exempt — exactly what we want for a factory-clean "Pending Activation" appliance,
-- whose tenant_id/site_id are both NULL until an operator activates it (migration
-- 0036 made those columns nullable for that reason). Once BOTH are set
-- (activation/assignment), the pair is validated against real site ownership.
--
-- Drop-then-add throughout keeps this migration safe to re-run (deploy applies all
-- *.up.sql in order). The FK is dropped BEFORE its referenced unique key.

ALTER TABLE appliances DROP CONSTRAINT IF EXISTS appliances_site_tenant_fk;

ALTER TABLE sites DROP CONSTRAINT IF EXISTS sites_id_tenant_key;
ALTER TABLE sites ADD CONSTRAINT sites_id_tenant_key UNIQUE (id, tenant_id);

ALTER TABLE appliances
    ADD CONSTRAINT appliances_site_tenant_fk
    FOREIGN KEY (site_id, tenant_id) REFERENCES sites (id, tenant_id)
    ON DELETE RESTRICT;
