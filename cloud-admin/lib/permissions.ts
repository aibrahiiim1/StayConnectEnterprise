"use client";

// The server enforces every permission; this map only hides controls it will reject.
// Legacy roles grant nothing, and customer users remain server-scoped to their customer.

import { useSession } from "./session";
import type { Whoami } from "./api";

export const PLATFORM_ADMIN_ROLES = ["platform_owner", "platform_admin"] as const;
export const PLATFORM_READ_ROLES = ["platform_owner", "platform_admin", "platform_support"] as const;
export const CUSTOMER_MANAGER_ROLES = ["tenant_admin", "tenant_owner"] as const;
export const CUSTOMER_READ_ROLES = ["tenant_admin", "tenant_owner", "tenant_auditor", "viewer"] as const;

/** Unknown roles receive a no-access label. */
export const ROLE_LABELS: Record<string, string> = {
  platform_owner: "Platform owner",
  platform_admin: "Platform admin",
  platform_support: "Support (read only)",
  tenant_owner: "Customer owner",
  tenant_admin: "Customer admin",
  tenant_auditor: "Auditor (read only)",
  viewer: "Viewer (read only)",
};
export const roleLabel = (r: string) => ROLE_LABELS[r] ?? `${r} (no access)`;

export const TEAM_ROLES = ["platform_admin", "platform_support"] as const;
export const CUSTOMER_USER_ROLES = ["tenant_admin", "tenant_auditor", "viewer"] as const;

export type Subject = {
  roles: readonly string[];
  /** The server's super-admin flag also grants platform admin access. */
  platformAdmin: boolean;
  platform: boolean;
  /** Empty for platform operators. */
  customerId: string;
  customerName: string;
  customerManager: boolean;
  customerReader: boolean;
};

const has = (roles: readonly string[], set: readonly string[]) => roles.some((r) => set.includes(r));

export function subjectFrom(me: Pick<Whoami, "roles" | "is_super_admin" | "customer_id" | "customer_name">): Subject {
  const roles = me.roles ?? [];
  const platformAdmin = !!me.is_super_admin || has(roles, PLATFORM_ADMIN_ROLES);
  return {
    roles,
    platformAdmin,
    platform: platformAdmin || has(roles, PLATFORM_READ_ROLES),
    customerId: me.customer_id ?? "",
    customerName: me.customer_name ?? "",
    customerManager: has(roles, CUSTOMER_MANAGER_ROLES),
    customerReader: has(roles, CUSTOMER_READ_ROLES),
  };
}

type Rule = (s: Subject) => boolean;

const platformAdmin: Rule = (s) => s.platformAdmin;
const platformRead: Rule = (s) => s.platform;
const anyReader: Rule = (s) => s.platform || s.customerReader;
/** Customer managers act only within their server-scoped customer. */
const customerManage: Rule = (s) => s.platformAdmin || s.customerManager;

export const CAPABILITIES = {
  /** The server scopes fleet views for customer users. */
  "fleet.read": anyReader,
  "customers.list": platformRead,
  "customers.create": platformAdmin,
  "customers.edit": platformAdmin,
  "customers.delete": platformAdmin,
  "sites.manage": customerManage,
  "users.read": anyReader,
  "users.manage": customerManage,
  "appliances.activate": platformAdmin,
  "appliances.manage": platformAdmin,
  "licenses.manage": platformAdmin,
  "system.read": platformRead,
  "securityAlerts.triage": platformAdmin,
  "team.manage": platformAdmin,
  "portalTls.manage": platformAdmin,
} as const satisfies Record<string, Rule>;

export type Capability = keyof typeof CAPABILITIES;
export type Can = Record<Capability, boolean>;

export function capabilitiesFor(s: Subject): Can {
  const out = {} as Can;
  for (const k of Object.keys(CAPABILITIES) as Capability[]) out[k] = CAPABILITIES[k](s);
  return out;
}

export function usePermissions(): { subject: Subject; can: Can } {
  const { subject, can } = useSession();
  return { subject, can };
}
