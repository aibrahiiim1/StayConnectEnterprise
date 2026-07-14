ALTER TABLE appliances DROP CONSTRAINT IF EXISTS appliances_site_tenant_fk;
ALTER TABLE sites DROP CONSTRAINT IF EXISTS sites_id_tenant_key;
