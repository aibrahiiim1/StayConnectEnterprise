-- Explicit operator scope bindings: a role row may bind to a specific site
-- (site_admin/hotel_it/hotel_operator) or be tenant-wide (site_id NULL).
ALTER TABLE operator_roles ADD COLUMN IF NOT EXISTS site_id UUID;
CREATE INDEX IF NOT EXISTS operator_roles_site_idx ON operator_roles(site_id);
