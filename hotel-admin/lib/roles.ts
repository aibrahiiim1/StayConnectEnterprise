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

// THIS TABLE HAD DRIFTED FROM THE SERVER'S, AND THE DRIFT HID SCREENS.
//
// edged's rolePerms grants hotel_it_manager WRITE on "pms-interfaces" and READ on "pms-routing" and
// "pms-source-conflicts". None of those three keys appeared here, so `canRead` returned false for them and the
// sidebar hid PMS connection, Network routing and Duplicate sources from the role that owns the PMS integration.
// The screens worked; they were unreachable. The same omission hid "financial-ops" from everyone but site_admin
// and "commercial-packages" from site_viewer.
//
// The keys below now mirror data-plane/cmd/edged/auth.go. When that file changes, this one has to change with it:
// a permission this table is missing is a screen nobody can find, and a permission it invents is a button that
// 403s on click.
const MATRIX: Matrix = {
  hotel_it_manager: {
    // Guest sign-in attempts. Two keys, mirroring edged exactly: the list, and the credential comparison.
    // "View_Guest_SignIn_Attempts" and "View_Guest_SignIn_Credentials" are the same two permissions under
    // the names the Product Owner uses.
    "guest-signin-attempts": "read", "guest-signin-credentials": "read",
    // Guest sign-in protection: the property's thresholds are a configuration decision, which is this
    // role's territory, and whoever sets the threshold may waive one instance of it.
    "guest-signin-protection": "write", "guest-signin-restrictions": "write",
    // Phase 3 (DARK): the IT manager owns the PMS integration — publishing the
    // checkout-grace policy and clearing alerts are manager actions; stays,
    // events and resolutions are read-only evidence.
    "pms-stays": "read", "pms-events": "read", "pms-resolutions": "read",
    "checkout-grace": "write", "operational-alerts": "write",
    // The interface itself is this role's job; routing and conflicts are read-only for everyone but site_admin.
    "pms-interfaces": "write", "pms-routing": "read", "pms-source-conflicts": "read",
    // Phase 5 (DARK): rotating or ending a post-stay credential. This mirrors the edged matrix exactly --
    // it decides whether the BUTTON is offered, never whether the action is allowed.
    "post-stay-profiles": "write",
    "stay-transfers": "write",
    // Phase 6 (DARK): the per-appliance Guest Device Self-Service setting follows auth-methods -- which
    // capabilities the property offers its guests is a configuration decision, not a desk action.
    "guest-device-self-service": "write",
    "guest-accounts": "write",
    sessions: "write",
    "auth-methods": "write", "walled-garden": "write",
    "portal-branding": "write", "notification-providers": "write",
    "social-providers": "write", "stripe-accounts": "write",
    network: "write",
    "financial-review": "read", "financial-ops": "read",
    operators: "read", audit: "read",
    reports: "read", backups: "read", license: "read", diagnostics: "write",
  },
  front_office_operator: {
    // The reception desk: the role this screen was asked for, and the one that needs the comparison.
    "guest-signin-attempts": "read", "guest-signin-credentials": "read",
    // The desk RELEASES and does not re-tune: the restricted guest is standing there now, but making
    // "five" into "twenty" for the whole property must not be the quickest way to help one person.
    "guest-signin-restrictions": "write", "guest-signin-protection": "read",
    // Phase 6 (DARK): the desk answers "why can't I remove my old phone" and changes no capability.
    "guest-device-self-service": "read",
    "pms-stays": "read", "pms-events": "read", "operational-alerts": "write", "checkout-grace": "read",
    "pms-interfaces": "read", "pms-routing": "read", "pms-source-conflicts": "read",
    "post-stay-profiles": "write", "stay-transfers": "write",
    "financial-review": "read", "financial-ops": "read",
    "guest-accounts": "write", sessions: "write",
    "auth-methods": "read", "walled-garden": "read", reports: "read", audit: "read", license: "read", backups: "read", diagnostics: "read",
  },
  guest_relations_operator: {
    "guest-signin-attempts": "read", "guest-signin-credentials": "read",
    "guest-signin-restrictions": "write", "guest-signin-protection": "read",
    "guest-device-self-service": "read",
    "pms-stays": "read", "pms-events": "read", "operational-alerts": "write", "checkout-grace": "read",
    "pms-interfaces": "read", "pms-routing": "read", "pms-source-conflicts": "read",
    "post-stay-profiles": "write", "stay-transfers": "write",
    "guest-accounts": "write", sessions: "write",
    "auth-methods": "read", reports: "read",
    audit: "read", license: "read", backups: "read", "walled-garden": "read", diagnostics: "read",
  },
  voucher_operator: {
    "guest-accounts": "write", sessions: "read", reports: "read",
    license: "read", diagnostics: "read",
  },
  payments_operator: {
    "guest-device-self-service": "read",
    "stripe-accounts": "read",
    // Contract section 15 gives the charge decision to this role; edged additionally requires password
    // re-authentication at the route.
    "financial-review": "write", "financial-ops": "write",
    sessions: "read", reports: "read", audit: "read", license: "read", diagnostics: "read",
  },
  site_viewer: {
    // The list only. A read-only observer has no reason to hold thirty days of what guests typed, so the
    // credentials key is absent here rather than merely unused — and edged enforces that, not this file.
    "guest-signin-attempts": "read",
    // A viewer sees what the policy is and which devices are waiting it out, and acts on neither.
    "guest-signin-protection": "read", "guest-signin-restrictions": "read",
    "guest-device-self-service": "read",
    "pms-stays": "read", "pms-events": "read", "pms-resolutions": "read", "checkout-grace": "read", "operational-alerts": "read",
    "pms-interfaces": "read", "pms-routing": "read", "pms-source-conflicts": "read",
    "commercial-packages": "read",
    "post-stay-profiles": "read", "stay-transfers": "read",
    "financial-review": "read", "financial-ops": "read",
    "guest-accounts": "read", sessions: "read", "auth-methods": "read",
    "walled-garden": "read", "portal-branding": "read", "notification-providers": "read", "social-providers": "read",
    "stripe-accounts": "read", audit: "read", reports: "read",
    backups: "read", license: "read", network: "read", diagnostics: "read",
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
