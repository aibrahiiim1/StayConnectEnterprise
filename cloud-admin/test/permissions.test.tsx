import { describe, expect, it } from "vitest";
import { CAPABILITIES, capabilitiesFor, subjectFrom, type Capability } from "@/lib/permissions";

/*
  docs/CENTRAL_CONTROL_PLANE.md §7, written out as the console must read it. This table is the test's own
  statement of the contract, independent of lib/permissions.ts: if either changes, they must be changed together.

    platform_owner, platform_admin   everything
    platform_support                 read everything
    tenant_admin, tenant_owner       read their customer; manage its sites and users
    tenant_auditor, viewer           read their customer
    legacy roles                     nothing
*/
const ALL = Object.keys(CAPABILITIES) as Capability[];
const PLATFORM_READ: Capability[] = ["fleet.read", "customers.list", "users.read", "system.read"];
const CUSTOMER_READ: Capability[] = ["fleet.read", "users.read"];
const CUSTOMER_MANAGE: Capability[] = [...CUSTOMER_READ, "sites.manage", "users.manage"];

const TABLE: Record<string, Capability[]> = {
  platform_owner: ALL,
  platform_admin: ALL,
  platform_support: PLATFORM_READ,
  tenant_admin: CUSTOMER_MANAGE,
  tenant_owner: CUSTOMER_MANAGE,
  tenant_auditor: CUSTOMER_READ,
  viewer: CUSTOMER_READ,
  platform_billing: [],
  billing: [],
  tenant_operator: [],
  site_admin: [],
  hotel_it: [],
  hotel_operator: [],
};

const CUSTOMER_ROLES = new Set(["tenant_admin", "tenant_owner", "tenant_auditor", "viewer", "tenant_operator", "site_admin", "hotel_it", "hotel_operator", "billing"]);

function granted(role: string): Capability[] {
  const s = subjectFrom({
    roles: [role],
    is_super_admin: role === "platform_owner" || role === "platform_admin",
    customer_id: CUSTOMER_ROLES.has(role) ? "c1" : null,
    customer_name: null,
  });
  const can = capabilitiesFor(s);
  return ALL.filter((k) => can[k]);
}

describe("capabilities per role (§7)", () => {
  for (const [role, expected] of Object.entries(TABLE)) {
    it(role, () => {
      expect(granted(role).sort()).toEqual([...expected].sort());
    });
  }

  it("license and activation writes are platform-admin only", () => {
    for (const role of Object.keys(TABLE)) {
      const g = granted(role);
      const admin = role === "platform_owner" || role === "platform_admin";
      for (const k of ["appliances.activate", "appliances.manage", "licenses.manage"] as Capability[]) {
        expect(g.includes(k), `${role} ${k}`).toBe(admin);
      }
    }
  });

  it("a super-admin flag from the server counts as platform admin even without the role name", () => {
    const can = capabilitiesFor(subjectFrom({ roles: [], is_super_admin: true, customer_id: null, customer_name: null }));
    expect(ALL.every((k) => can[k])).toBe(true);
  });

  it("several roles combine", () => {
    const can = capabilitiesFor(subjectFrom({ roles: ["viewer", "tenant_admin"], is_super_admin: false, customer_id: "c1", customer_name: "C" }));
    expect(can["sites.manage"]).toBe(true);
    expect(can["licenses.manage"]).toBe(false);
  });
});
