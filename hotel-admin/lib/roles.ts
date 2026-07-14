// Site-role → resource permission matrix, mirroring edged's rolePerms
// (data-plane/cmd/edged/auth.go) and docs/ROLE_AND_SCOPE_MATRIX.md §3.
// The UI uses this only to hide nav items / write controls the operator's
// role cannot use — edged enforces the real gate on every request.

export type Perm = "none" | "read" | "write";

export const SITE_ROLES = [
  "site_admin",
  "hotel_it_manager",
  "front_office_operator",
  "guest_relations_operator",
  "voucher_operator",
  "payments_operator",
  "site_viewer",
] as const;

export type SiteRole = (typeof SITE_ROLES)[number];

export const ROLE_LABELS: Record<SiteRole, string> = {
  site_admin: "Site admin",
  hotel_it_manager: "Hotel IT manager",
  front_office_operator: "Front office operator",
  guest_relations_operator: "Guest relations operator",
  voucher_operator: "Voucher operator",
  payments_operator: "Payments operator",
  site_viewer: "Site viewer",
};

// Resource keys match edged's mountResource names.
type Matrix = Record<string, Record<string, Perm>>;

const MATRIX: Matrix = {
  hotel_it_manager: {
    "guest-access-plans": "write", "voucher-batches": "write",
    vouchers: "write", sessions: "write", "pms-providers": "write",
    "auth-methods": "write", "walled-garden": "write",
    "portal-branding": "write", "notification-providers": "write",
    "social-providers": "write", "stripe-accounts": "write",
    network: "write",
    payments: "read", operators: "read", audit: "read",
    reports: "read", backups: "read", license: "read", health: "write",
  },
  front_office_operator: {
    "voucher-batches": "write", vouchers: "write", sessions: "write",
    "guest-access-plans": "read", "pms-providers": "read",
    "auth-methods": "read", "walled-garden": "read", payments: "read",
    reports: "read", audit: "read", license: "read", backups: "read", health: "read",
  },
  guest_relations_operator: {
    "voucher-batches": "write", vouchers: "write", sessions: "write",
    "guest-access-plans": "read", "pms-providers": "read",
    "auth-methods": "read", payments: "read", reports: "read",
    audit: "read", license: "read", backups: "read", "walled-garden": "read", health: "read",
  },
  voucher_operator: {
    "voucher-batches": "write", vouchers: "write",
    "guest-access-plans": "read", sessions: "read", reports: "read",
    license: "read", health: "read",
  },
  payments_operator: {
    payments: "write", "stripe-accounts": "read",
    sessions: "read", reports: "read", audit: "read", license: "read", health: "read",
  },
  site_viewer: {
    "guest-access-plans": "read", "voucher-batches": "read", vouchers: "read",
    sessions: "read", "pms-providers": "read", "auth-methods": "read",
    "walled-garden": "read", "portal-branding": "read", payments: "read",
    "notification-providers": "read", "social-providers": "read",
    "stripe-accounts": "read", audit: "read", reports: "read",
    backups: "read", license: "read", network: "read", health: "read",
  },
};

const rank: Record<Perm, number> = { none: 0, read: 1, write: 2 };

function has(roles: string[], resource: string, want: Perm): boolean {
  for (let role of roles) {
    // site_admin (and the legacy tenant_admin mapping) can do everything;
    // legacy tenant_operator maps to hotel_it_manager.
    if (role === "site_admin" || role === "tenant_admin") return true;
    if (role === "tenant_operator") role = "hotel_it_manager";
    const p = MATRIX[role]?.[resource];
    if (p && rank[p] >= rank[want]) return true;
  }
  return false;
}

export function canRead(resource: string, roles: string[]): boolean {
  return has(roles, resource, "read");
}

export function canWrite(resource: string, roles: string[]): boolean {
  return has(roles, resource, "write");
}
