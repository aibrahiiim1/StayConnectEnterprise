-- REVERSES 0049_site_type_and_licence_modules. The site types and the licence module projection are dropped;
-- the signed licence envelopes keep their modules, but a preserved-terms re-issue after this down would no
-- longer know them.

BEGIN;

ALTER TABLE licenses DROP COLUMN IF EXISTS modules;
ALTER TABLE sites DROP CONSTRAINT IF EXISTS sites_site_type_shape;
ALTER TABLE sites DROP COLUMN IF EXISTS site_type;

COMMIT;
