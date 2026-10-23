// Site-role to permission matrix, used only to hide nav items / write controls; edged enforces the real gate.
// GENERATED from edged's rolePerms (auth.go) into role-permissions.generated.json; a test fails on drift.
// To change it, change auth.go and run: UPDATE_ROLE_PERMISSIONS=1 go test ./cmd/edged -run
// TestConsoleRolePermissionsAreGeneratedFromTheServerTable

import generated from "./role-permissions.generated.json";

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
  hotel_it_manager: "Site IT manager",
  front_office_operator: "Client services operator",
  guest_relations_operator: "Client relations operator",
  voucher_operator: "Voucher operator",
  payments_operator: "Payments operator",
  site_viewer: "Site viewer",
};

type Matrix = Record<string, Record<string, Perm>>;

const MATRIX: Matrix = generated.roles as Matrix;

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
