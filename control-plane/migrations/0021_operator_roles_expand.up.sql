ALTER TABLE operator_roles DROP CONSTRAINT IF EXISTS operator_roles_role_check;
ALTER TABLE operator_roles ADD CONSTRAINT operator_roles_role_check CHECK (
  role IN ('platform_owner','platform_admin','platform_support','platform_billing',
           'tenant_owner','tenant_admin','tenant_auditor','tenant_operator','viewer',
           'site_admin','hotel_it','hotel_operator'));
