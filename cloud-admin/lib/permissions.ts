"use client";

/*
  CENTRAL PERMISSIONS: WHAT CTRLAPI ACTUALLY ENFORCES, MIRRORED FOR THE UI.

  The UI uses this map only to decide which controls to show. ctrlapi still makes the decision on every request.
  It adds no roles and no rules of its own. A control the server would refuse is hidden. A control the server
  does not refuse on role grounds stays visible, even when the product handoff suggests a role "should not"
  have it. Those gaps are listed at the end of this comment as facts.

  WHERE THE SERVER'S RULES COME FROM (control-plane/)
  ---------------------------------------------------
  Roles. `operator_roles.role` is one of: platform_owner, platform_admin, platform_support, platform_billing,
    tenant_owner, tenant_admin, tenant_auditor, tenant_operator, viewer, site_admin, hotel_it, hotel_operator
    (migrations/0021_operator_roles_expand.up.sql, CHECK constraint). The pre-0021 legacy name `billing` is no
    longer in that CHECK, but the operators API still accepts it (internal/api/operators.go isValidRole).
  Platform admin. `is_super_admin` is true when the operator holds platform_owner or platform_admin
    (internal/auth/repo.go Repo.loadRoles). No other role makes it true.
  Customer. `default_tenant_id` is the first non-NULL tenant_id among the role rows (repo.go loadRoles). A
    non-super operator ALWAYS acts on that customer; any ?tenant_id= is ignored (internal/auth/middleware.go
    EffectiveTenantID). A super-admin may pass ?tenant_id= or omit it for "All customers".
  whoami. GET /v1/auth/whoami returns { operator_id, email, display_name, is_super_admin, default_tenant_id,
    roles, expires_at } read fresh from the database (internal/http/auth_handlers.go whoami / whoamiResp). The
    server enforces the roles captured in the SESSION at login (auth/session.go Session.Roles), so a role change
    reaches the server only on the next sign-in.
  Three enforcement mechanisms, and only three:
    P  RequirePermission(perm) — a permission from the role catalog (internal/auth/permissions.go rolePermissions,
       mirrored below as ROLE_PERMISSIONS). platform_owner holds "*".
    S  An IsSuperAdmin check inside a handler, or auth.RequireRole("platform_admin") (auth/middleware.go), which
       passes for IsSuperAdmin or the platform_admin role — the same set of people.
    T  Tenant scope only: RequireTenantOrPlatform / RequireTenant / tenantScopeForList / ensureTenantAccess.
       ANY authenticated operator with a customer passes, whatever their role; super-admins pass everywhere.
  Plus one ad-hoc role check: mayManageOperators (internal/api/operators.go) = IsSuperAdmin OR the role string
  "tenant_admin" in the operator's own customer. (Not the catalog permission tenant.operators.manage, so
  tenant_owner, which holds that permission, is still refused.)
  Password step-up (RequireReauth, internal/api/reauth.go) is separate from authorization and is unchanged here;
  lib/api.ts withStepUp handles it.

  ENDPOINT TABLE — every call cloud-admin makes (lib/api.ts, app/**, components/**, lib/**)
  ----------------------------------------------------------------------------------------
  Endpoint                                                    Who the server allows             Go rule
  GET  /v1/auth/whoami                                        any signed-in operator            http/router.go (RequireAuth only)
  POST /v1/auth/reauth                                        any signed-in operator            http/auth_handlers.go reauth
  GET  /v1/tenants                                            anyone; non-super gets own only   T  api/tenants.go listTenants
  POST /v1/tenants                                            super-admin                       S  api/tenants.go createTenant
  PATCH /v1/tenants/{id} (name)                               super-admin, or ANY role on own   S/T api/tenants.go patchTenant
  POST /v1/tenants/{id}/archive|restore                       super-admin                       S  api/tenants.go archiveTenant/restoreTenant
  DELETE /v1/tenants/{id}                                     super-admin (+ step-up)           S  api/tenants.go deleteTenantSafe
  GET  /v1/tenants/{id}/audit                                 super-admin, or ANY role on own   T  api/audit_log.go listAudit → ensureTenantAccess
  GET|POST|PATCH /v1/sites, /v1/sites/{id}, archive, restore  any operator with a customer      T  api/sites.go SitesRoutes (RequireTenantOrPlatform), no role check
  DELETE /v1/sites/{id}                                       same (+ step-up)                  T  api/sites.go deleteSite
  GET|POST|DELETE /v1/appliances, /{id}, /{id}/effective-config  any operator with a customer   T  api/appliances.go AppliancesRoutes, no role check
  GET  /v1/appliance-bootstrap-tokens                         platform.appliances.view + tenant P  api/enrollment.go TokenRoutes
  POST /v1/appliance-bootstrap-tokens                         platform.enrollment_tokens.create P  api/enrollment.go TokenRoutes
  DELETE /v1/appliance-bootstrap-tokens/{id}                  platform.enrollment_tokens.revoke P  api/enrollment.go TokenRoutes
  GET|POST|PATCH|DELETE /v1/operators…, set-password, roles   super-admin or tenant_admin       mayManageOperators (api/operators.go), incl. READ
  GET  /cloud/v1/licenses, /{id}                              super-admin; others own customer  T  api/licenses.go list/get
  GET  /cloud/v1/licenses/fleet-summary                       super-admin; others own customer  T  api/licenses.go fleetSummary
  POST /cloud/v1/licenses, /{id}/renew|suspend|resume|revoke  super-admin (+ step-up)           S  api/licenses.go Routes → RequireRole("platform_admin")
  POST /cloud/v1/offline-packages/{appliance}/generate        platform.certificates.issue       P  api/offline_api.go Routes
  GET  /cloud/v1/appliances-admin/pending                     platform.appliances.view          P  api/appliance_lifecycle.go LifecycleRoutes
  GET  /cloud/v1/appliances-admin/{id}/assignment|delete-impact  platform.appliances.view       P  same
  POST /cloud/v1/appliances-admin/{id}/activate               platform.appliances.assign        P  same
  POST /cloud/v1/appliances-admin/{id}/deactivate|force-reconcile|decommission, DELETE /{id}
                                                              platform.appliances.manage        P  same (+ step-up)
  GET  /cloud/v1/appliances-admin/security-alerts             platform.appliances.view          P  same
  PATCH /cloud/v1/appliances-admin/security-alerts/{id}       platform.appliances.view (!)      P  same — the READ permission gates the triage write
  POST /cloud/v1/offline-activation/requests                  platform.appliances.manage        P  api/offline_activation_api.go Routes
  POST /cloud/v1/offline-activation/{id}/package              platform.certificates.issue       P  same (+ step-up)
  POST /cloud/v1/certificates/{id}/issue                      platform.certificates.issue       P  api/certificates.go PlatformRoutes (+ step-up)
  GET  /cloud/v1/certificates                                 platform.appliances.view          P  api/certificates.go PlatformRoutes
  GET  /cloud/v1/assignment-keys                              platform.appliances.manage        P  api/assignment_keys.go Routes
  GET  /cloud/v1/backup-health                                platform.appliances.view          P  http/router.go
  Retired pages (/commercial, /subscription: /v1/plans…, /v1/tenants/{id}/subscription…) are not listed in the
  menu and are out of this map's scope.

  WHAT THAT MEANS PER ROLE (Central screens)
  ------------------------------------------
  platform_owner, platform_admin (is_super_admin): everything below.
  platform_support: platform.*.view — Onboarding (read only), Security alerts (read AND triage), Certificates,
    Backup health. No customer, so every tenant-scoped list (Sites, Appliances, Licenses, Audit) is refused.
  platform_billing: platform.licenses.view only; no customer, so the licenses list itself is refused (400).
  tenant_admin: own customer's Sites, Appliances, Licenses (read), Audit, Customers (rename own) and Operators.
  tenant_owner, tenant_auditor, tenant_operator, viewer, site_admin, hotel_it, hotel_operator, billing: the same
    as tenant_admin minus Operators. None of the catalog's tenant.* / site.* permissions gate any Central route.

  WHERE THE SERVER DOES NOT ENFORCE WHAT THE HANDOFF IMPLIES (facts, not changed here)
  -------------------------------------------------------------------------------------
  - "Viewer: read-only for one hotel group" — the server lets viewer (and every tenant role) create, edit,
    archive and delete Sites, create and delete Appliances, and rename its own Customer. Only the tenant-scope
    check applies. So the UI does not hide those controls.
  - Security-alert triage needs only platform.appliances.view, so platform_support can triage.
  - tenant_owner holds tenant.operators.manage in the catalog but mayManageOperators checks the role string
    "tenant_admin", so tenant_owner cannot use Operators.
  - The operators API accepts role "billing" (isValidRole) but the database CHECK since migration 0021 does not,
    so granting it fails at insert.
  - Dashboard usage (/v1/tenants/{id}/usage/*) and /cloud/v1/fleet are not mounted in ctrlapi (http/router.go);
    those calls 404 for every role.
*/

import { useMemo } from "react";
import type { Whoami } from "./api";
import { useCustomer } from "./customer-context";

/** Mirror of control-plane/internal/auth/permissions.go `rolePermissions`. Keep the two in step. */
export const ROLE_PERMISSIONS: Readonly<Record<string, readonly string[]>> = {
  platform_owner: ["*"],
  platform_admin: [
    "platform.tenants.view", "platform.tenants.manage",
    "platform.plans.view", "platform.plans.manage",
    "platform.subscriptions.view", "platform.subscriptions.manage",
    "platform.licenses.view", "platform.licenses.issue", "platform.licenses.revoke",
    "platform.appliances.view", "platform.appliances.manage",
    "platform.appliances.claim", "platform.appliances.assign",
    "platform.appliances.reassign", "platform.appliances.revoke",
    "platform.enrollment_tokens.create", "platform.enrollment_tokens.revoke",
    "platform.certificates.issue", "platform.certificates.revoke",
    "platform.commands.issue",
    "platform.fleet.view", "platform.updates.manage",
    "platform.operators.manage", "platform.audit.view",
  ],
  platform_support: [
    "platform.tenants.view", "platform.plans.view", "platform.subscriptions.view",
    "platform.licenses.view", "platform.appliances.view", "platform.fleet.view",
    "platform.audit.view",
  ],
  platform_billing: [
    "platform.tenants.view", "platform.plans.view", "platform.plans.manage",
    "platform.subscriptions.view", "platform.subscriptions.manage",
    "platform.licenses.view", "platform.audit.view",
  ],
  tenant_owner: [
    "tenant.sites.view", "tenant.appliances.view", "tenant.reports.view",
    "tenant.subscription.view", "tenant.operators.manage", "tenant.audit.view",
  ],
  tenant_admin: [
    "tenant.sites.view", "tenant.appliances.view", "tenant.appliances.support_request",
    "tenant.reports.view", "tenant.subscription.view", "tenant.operators.manage", "tenant.audit.view",
  ],
  tenant_auditor: [
    "tenant.sites.view", "tenant.appliances.view", "tenant.reports.view",
    "tenant.subscription.view", "tenant.audit.view",
  ],
  site_admin: [
    "tenant.sites.view", "tenant.appliances.view",
    "site.reports.view", "site.vouchers.manage", "site.pms.manage",
    "site.portal.manage", "site.payments.manage", "site.network.manage", "site.audit.view",
  ],
  hotel_it: ["site.reports.view", "site.network.manage", "site.pms.manage", "site.portal.manage", "site.audit.view"],
  hotel_operator: ["site.reports.view", "site.vouchers.manage", "site.audit.view"],
  viewer: [
    "tenant.sites.view", "tenant.appliances.view", "tenant.reports.view",
    "tenant.subscription.view", "tenant.audit.view",
  ],
  tenant_operator: [
    "tenant.sites.view", "tenant.appliances.view", "tenant.reports.view",
    "tenant.subscription.view", "site.reports.view",
  ],
};

/** The signed-in operator, as the server sees them. Built from whoami. */
export type Subject = {
  isSuperAdmin: boolean;
  roles: readonly string[];
  /** The customer a non-super operator is pinned to ("" when none). */
  defaultTenantId: string;
};

export function subjectFrom(me: Pick<Whoami, "is_super_admin" | "roles" | "default_tenant_id">): Subject {
  return {
    isSuperAdmin: !!me.is_super_admin,
    roles: me.roles ?? [],
    defaultTenantId: me.default_tenant_id ?? "",
  };
}

/** auth.Session.HasPermission. */
export function hasPermission(s: Subject, perm: string): boolean {
  return s.roles.some((r) => (ROLE_PERMISSIONS[r] ?? []).some((p) => p === "*" || p === perm));
}

/** Mechanism T: any operator with a customer; super-admins everywhere. */
const tenantScoped = (s: Subject) => s.isSuperAdmin || s.defaultTenantId !== "";
/** Mechanism S. */
const superAdmin = (s: Subject) => s.isSuperAdmin;
/** Mechanism P. */
const perm = (p: string) => (s: Subject) => hasPermission(s, p);
/** mayManageOperators (api/operators.go). */
const operatorsManager = (s: Subject) => s.isSuperAdmin || (s.defaultTenantId !== "" && s.roles.includes("tenant_admin"));

export type Rule = {
  /** The request(s) this capability stands for. */
  endpoints: readonly string[];
  /** Which Go file/function decides. */
  server: string;
  /** P = catalog permission, S = super-admin, T = tenant scope only, O = mayManageOperators. */
  mechanism: "P" | "S" | "T" | "O";
  /** The catalog permission, for mechanism P. */
  permission?: string;
  allow: (s: Subject) => boolean;
};

export const PERMISSIONS = {
  "customers.read": {
    endpoints: ["GET /v1/tenants"], server: "api/tenants.go listTenants", mechanism: "T",
    allow: () => true, // any signed-in operator; a non-super operator gets only their own customer (or none)
  },
  "customers.create": {
    endpoints: ["POST /v1/tenants"], server: "api/tenants.go createTenant", mechanism: "S", allow: superAdmin,
  },
  "customers.rename": {
    endpoints: ["PATCH /v1/tenants/{id}"], server: "api/tenants.go patchTenant", mechanism: "T",
    allow: tenantScoped, // own customer for non-super, any role
  },
  "customers.archive": {
    endpoints: ["POST /v1/tenants/{id}/archive", "POST /v1/tenants/{id}/restore"],
    server: "api/tenants.go archiveTenant, restoreTenant", mechanism: "S", allow: superAdmin,
  },
  "customers.delete": {
    endpoints: ["DELETE /v1/tenants/{id}"], server: "api/tenants.go deleteTenantSafe", mechanism: "S", allow: superAdmin,
  },
  "sites.read": {
    endpoints: ["GET /v1/sites"], server: "api/sites.go SitesRoutes (RequireTenantOrPlatform), listSites", mechanism: "T",
    allow: tenantScoped,
  },
  "sites.write": {
    endpoints: ["POST /v1/sites", "PATCH /v1/sites/{id}", "POST /v1/sites/{id}/archive", "POST /v1/sites/{id}/restore", "DELETE /v1/sites/{id}"],
    server: "api/sites.go createSite, patchSite, archiveSite, restoreSite, deleteSite (no role check)", mechanism: "T",
    allow: tenantScoped,
  },
  "appliances.read": {
    endpoints: ["GET /v1/appliances", "GET /v1/appliances/{id}/effective-config"],
    server: "api/appliances.go AppliancesRoutes (RequireTenantOrPlatform)", mechanism: "T", allow: tenantScoped,
  },
  "appliances.write": {
    endpoints: ["POST /v1/appliances", "DELETE /v1/appliances/{id}"],
    server: "api/appliances.go createAppliance, deleteAppliance (no role check)", mechanism: "T", allow: tenantScoped,
  },
  "enrollmentTokens.read": {
    endpoints: ["GET /v1/appliance-bootstrap-tokens"], server: "api/enrollment.go TokenRoutes", mechanism: "P",
    permission: "platform.appliances.view", allow: (s) => tenantScoped(s) && hasPermission(s, "platform.appliances.view"),
  },
  "enrollmentTokens.create": {
    endpoints: ["POST /v1/appliance-bootstrap-tokens"], server: "api/enrollment.go TokenRoutes", mechanism: "P",
    permission: "platform.enrollment_tokens.create",
    allow: (s) => tenantScoped(s) && hasPermission(s, "platform.enrollment_tokens.create"),
  },
  "enrollmentTokens.revoke": {
    endpoints: ["DELETE /v1/appliance-bootstrap-tokens/{id}"], server: "api/enrollment.go TokenRoutes", mechanism: "P",
    permission: "platform.enrollment_tokens.revoke",
    allow: (s) => tenantScoped(s) && hasPermission(s, "platform.enrollment_tokens.revoke"),
  },
  "licenses.read": {
    endpoints: ["GET /cloud/v1/licenses", "GET /cloud/v1/licenses/fleet-summary"],
    server: "api/licenses.go list, fleetSummary", mechanism: "T", allow: tenantScoped,
  },
  "licenses.change": {
    endpoints: ["POST /cloud/v1/licenses", "POST /cloud/v1/licenses/{id}/renew", "POST /cloud/v1/licenses/{id}/suspend",
      "POST /cloud/v1/licenses/{id}/resume", "POST /cloud/v1/licenses/{id}/revoke"],
    server: "api/licenses.go Routes → auth.RequireRole(\"platform_admin\")", mechanism: "S",
    allow: (s) => s.isSuperAdmin || s.roles.includes("platform_admin"),
  },
  "licenses.offlinePackage": {
    endpoints: ["POST /cloud/v1/offline-packages/{applianceID}/generate"], server: "api/offline_api.go Routes",
    mechanism: "P", permission: "platform.certificates.issue", allow: perm("platform.certificates.issue"),
  },
  "operators.read": {
    endpoints: ["GET /v1/operators"], server: "api/operators.go listOperators → mayManageOperators", mechanism: "O",
    allow: operatorsManager,
  },
  "operators.write": {
    endpoints: ["POST /v1/operators", "DELETE /v1/operators/{id}", "POST /v1/operators/{id}/set-password",
      "POST /v1/operators/{id}/roles", "DELETE /v1/operators/{id}/roles/{role}"],
    server: "api/operators.go createOperator, disableOperator, setOperatorPassword, addOperatorRole, removeOperatorRole → mayManageOperators",
    mechanism: "O", allow: operatorsManager,
  },
  "onboarding.read": {
    endpoints: ["GET /cloud/v1/appliances-admin/pending", "GET /cloud/v1/appliances-admin/{id}/assignment",
      "GET /cloud/v1/appliances-admin/{id}/delete-impact"],
    server: "api/appliance_lifecycle.go LifecycleRoutes", mechanism: "P", permission: "platform.appliances.view",
    allow: perm("platform.appliances.view"),
  },
  "onboarding.activate": {
    endpoints: ["POST /cloud/v1/appliances-admin/{id}/activate"], server: "api/appliance_lifecycle.go LifecycleRoutes",
    mechanism: "P", permission: "platform.appliances.assign", allow: perm("platform.appliances.assign"),
  },
  "onboarding.manage": {
    endpoints: ["POST /cloud/v1/appliances-admin/{id}/deactivate", "POST /cloud/v1/appliances-admin/{id}/force-reconcile",
      "POST /cloud/v1/appliances-admin/{id}/decommission", "DELETE /cloud/v1/appliances-admin/{id}"],
    server: "api/appliance_lifecycle.go LifecycleRoutes", mechanism: "P", permission: "platform.appliances.manage",
    allow: perm("platform.appliances.manage"),
  },
  "onboarding.importRequest": {
    endpoints: ["POST /cloud/v1/offline-activation/requests"], server: "api/offline_activation_api.go Routes",
    mechanism: "P", permission: "platform.appliances.manage", allow: perm("platform.appliances.manage"),
  },
  "onboarding.certificates": {
    endpoints: ["POST /cloud/v1/offline-activation/{id}/package", "POST /cloud/v1/certificates/{id}/issue"],
    server: "api/offline_activation_api.go Routes, api/certificates.go PlatformRoutes", mechanism: "P",
    permission: "platform.certificates.issue", allow: perm("platform.certificates.issue"),
  },
  "securityAlerts.read": {
    endpoints: ["GET /cloud/v1/appliances-admin/security-alerts"], server: "api/appliance_lifecycle.go LifecycleRoutes",
    mechanism: "P", permission: "platform.appliances.view", allow: perm("platform.appliances.view"),
  },
  "securityAlerts.triage": {
    endpoints: ["PATCH /cloud/v1/appliances-admin/security-alerts/{id}"],
    server: "api/appliance_lifecycle.go LifecycleRoutes (updateAlertStatus is gated by the VIEW permission)",
    mechanism: "P", permission: "platform.appliances.view", allow: perm("platform.appliances.view"),
  },
  "certificates.read": {
    endpoints: ["GET /cloud/v1/certificates"], server: "api/certificates.go PlatformRoutes", mechanism: "P",
    permission: "platform.appliances.view", allow: perm("platform.appliances.view"),
  },
  "assignmentKeys.read": {
    endpoints: ["GET /cloud/v1/assignment-keys"], server: "api/assignment_keys.go Routes", mechanism: "P",
    permission: "platform.appliances.manage", allow: perm("platform.appliances.manage"),
  },
  "backupHealth.read": {
    endpoints: ["GET /cloud/v1/backup-health"], server: "http/router.go", mechanism: "P",
    permission: "platform.appliances.view", allow: perm("platform.appliances.view"),
  },
  "audit.read": {
    endpoints: ["GET /v1/tenants/{id}/audit"], server: "api/audit_log.go listAudit → ensureTenantAccess", mechanism: "T",
    allow: tenantScoped,
  },
} as const satisfies Record<string, Rule>;

export type Capability = keyof typeof PERMISSIONS;
export type Can = Record<Capability, boolean>;

export function capabilitiesFor(s: Subject): Can {
  const out = {} as Can;
  for (const k of Object.keys(PERMISSIONS) as Capability[]) out[k] = PERMISSIONS[k].allow(s);
  return out;
}

/** Which capability a menu destination needs to be readable at all. */
export const PAGE_READ: Record<string, Capability | null> = {
  "/dashboard": null,
  "/sites": "sites.read",
  "/onboarding": "onboarding.read",
  "/appliances": "appliances.read",
  "/tenants": "customers.read",
  "/licenses": "licenses.read",
  "/operators": "operators.read",
  "/security": "securityAlerts.read",
  "/certificates": "certificates.read",
  "/assignment-keys": "assignmentKeys.read",
  "/backup-health": "backupHealth.read",
  "/audit": "audit.read",
};

/** What the signed-in operator may do, derived from whoami (via the Customer context). */
export function usePermissions(): { subject: Subject; can: Can } {
  const { me } = useCustomer();
  return useMemo(() => {
    const subject = subjectFrom(me);
    return { subject, can: capabilitiesFor(subject) };
  }, [me]);
}
