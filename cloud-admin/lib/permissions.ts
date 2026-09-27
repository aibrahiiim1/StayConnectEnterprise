"use client";

/*
  WHAT EACH ROLE MAY DO IN CENTRAL — docs/CENTRAL_CONTROL_PLANE.md §7, as the console reads it.

  ctrlapi is the authority and decides every request. This map only decides which controls the console shows,
  so a role is never offered a button the server will refuse. It adds no rule of its own.

    platform_owner, platform_admin   everything
    platform_support                 read everything
    tenant_admin, tenant_owner       read their customer; manage its sites and users
    tenant_auditor, viewer           read their customer

  License and activation writes are platform-only. The legacy roles (platform_billing, billing, tenant_operator,
  site_admin, hotel_it, hotel_operator) grant nothing in Central.

  A customer-scoped user never sees the fleet: the server scopes /overview, /appliances and /licenses to their
  customer, and the console sends /customers straight to their own customer page. The System pages are
  platform-only.
*/

import { useSession } from "./session";
import type { Whoami } from "./api";

export const PLATFORM_ADMIN_ROLES = ["platform_owner", "platform_admin"] as const;
export const PLATFORM_READ_ROLES = ["platform_owner", "platform_admin", "platform_support"] as const;
export const CUSTOMER_MANAGER_ROLES = ["tenant_admin", "tenant_owner"] as const;
export const CUSTOMER_READ_ROLES = ["tenant_admin", "tenant_owner", "tenant_auditor", "viewer"] as const;

/** Every role §7 names, and the words the console shows for it. Legacy roles are shown, but grant nothing. */
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

/** The roles a Team member can be given, and the roles a customer's user can be given. */
export const TEAM_ROLES = ["platform_admin", "platform_support"] as const;
export const CUSTOMER_USER_ROLES = ["tenant_admin", "tenant_auditor", "viewer"] as const;

/** The signed-in operator, as the console needs to see them. */
export type Subject = {
  roles: readonly string[];
  /** Holds platform_owner / platform_admin (or the server says super-admin). */
  platformAdmin: boolean;
  /** Holds any platform role. */
  platform: boolean;
  /** The customer a customer-scoped user belongs to ("" for platform operators). */
  customerId: string;
  customerName: string;
  /** Holds tenant_admin / tenant_owner. */
  customerManager: boolean;
  /** Holds any role that reads a customer. */
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
/** Anyone who can read at least one customer. */
const anyReader: Rule = (s) => s.platform || s.customerReader;
/** Manage a customer's sites and users: platform admins anywhere, customer managers in their own customer. */
const customerManage: Rule = (s) => s.platformAdmin || s.customerManager;

export const CAPABILITIES = {
  /** Overview, Appliances, Licenses, a customer page (server-scoped for customer users). */
  "fleet.read": anyReader,
  /** The customer list across all customers. */
  "customers.list": platformRead,
  "customers.create": platformAdmin,
  "customers.edit": platformAdmin,       // rename, archive, restore
  "customers.delete": platformAdmin,
  "sites.manage": customerManage,        // create, edit, archive, restore, delete
  "users.read": anyReader,               // a customer's users
  "users.manage": customerManage,
  "appliances.activate": platformAdmin,  // activate, import an activation request
  "appliances.manage": platformAdmin,    // move, retire, replace, rebind, reissue, delete, offline packages
  "licenses.manage": platformAdmin,      // set, suspend, resume, revoke, offline license
  "system.read": platformRead,           // the System section
  "securityAlerts.triage": platformAdmin,
  "team.manage": platformAdmin,
} as const satisfies Record<string, Rule>;

export type Capability = keyof typeof CAPABILITIES;
export type Can = Record<Capability, boolean>;

export function capabilitiesFor(s: Subject): Can {
  const out = {} as Can;
  for (const k of Object.keys(CAPABILITIES) as Capability[]) out[k] = CAPABILITIES[k](s);
  return out;
}

/** What the signed-in operator may do. */
export function usePermissions(): { subject: Subject; can: Can } {
  const { subject, can } = useSession();
  return { subject, can };
}
