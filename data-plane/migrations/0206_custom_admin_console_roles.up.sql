-- 0206 -- CUSTOM ADMIN CONSOLE ROLES (Product Owner, 2026-11-04).
-- edge-migrate: platform
--
-- A site administrator defines roles of their own: a name, a description and, per Admin Console feature, No access, View or
-- Manage. edged enforces them with the seven built-in roles, which are unchanged and stay compiled (cmd/edged/auth.go).
-- What a custom role may hold, and who may define or assign one, is decided in edged (custom_roles.go): no custom role can grant
-- more than a built-in role other than Site admin grants, and only Site admin manages roles and assigns them.
--
-- permissions is {"<feature>": "read" | "write"}; a feature left out is No access. A role in use cannot be deleted by edged; the
-- cascade below is for the cross-tenant purge (scd), which removes a previous customer's operators and roles together.
-- The name and description describe a job, not a person; they are not personal data.
-- Additive and reversible: the .down.sql drops both tables.

CREATE TABLE IF NOT EXISTS public.custom_roles (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id   uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
  name        text NOT NULL CHECK (length(btrim(name)) BETWEEN 2 AND 60),
  description text NOT NULL DEFAULT '' CHECK (length(description) <= 300),
  permissions jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(permissions) = 'object'),
  created_by  uuid REFERENCES public.operators(id) ON DELETE SET NULL,
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_by  uuid REFERENCES public.operators(id) ON DELETE SET NULL,
  updated_at  timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS custom_roles_name_uniq ON public.custom_roles (tenant_id, lower(btrim(name)));
COMMENT ON TABLE public.custom_roles IS
  'Admin Console roles a site administrator defines (0206): per feature No access (absent), View (read) or Manage (write). Enforced by edged beside the built-in roles.';

CREATE TABLE IF NOT EXISTS public.operator_custom_roles (
  operator_id uuid NOT NULL REFERENCES public.operators(id) ON DELETE CASCADE,
  role_id     uuid NOT NULL REFERENCES public.custom_roles(id) ON DELETE CASCADE,
  assigned_by uuid REFERENCES public.operators(id) ON DELETE SET NULL,
  assigned_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (operator_id, role_id)
);
CREATE INDEX IF NOT EXISTS operator_custom_roles_role ON public.operator_custom_roles (role_id);
COMMENT ON TABLE public.operator_custom_roles IS 'Which staff accounts hold which custom role (0206).';

-- Made again by deploy/gatep/gatep-grants.sql, which a Gate-P reconcile applies after revoking.
GRANT SELECT, INSERT, UPDATE, DELETE ON public.custom_roles, public.operator_custom_roles TO svc_edged;
GRANT SELECT, DELETE ON public.custom_roles TO svc_scd;
