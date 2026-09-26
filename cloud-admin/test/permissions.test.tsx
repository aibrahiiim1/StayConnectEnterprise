import { describe, expect, it, vi, beforeEach } from "vitest";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { mockFetch, PLATFORM_ME } from "./helpers";

// A STABLE router, as Next's is: Onboarding's loaders depend on it, and a fresh object per render would make
// their effect re-run on every render.
const router = vi.hoisted(() => ({ replace: () => {}, push: () => {}, refresh: () => {} }));
vi.mock("next/navigation", () => ({
  usePathname: () => "/dashboard",
  useRouter: () => router,
  useSearchParams: () => new URLSearchParams(),
}));

import { CustomerProvider } from "@/lib/customer-context";
import { ToastProvider } from "@/components/ui/toast";
import type { Whoami } from "@/lib/api";
import {
  PERMISSIONS, ROLE_PERMISSIONS, capabilitiesFor, hasPermission, subjectFrom, type Capability,
} from "@/lib/permissions";
import { Nav } from "@/components/nav";
import { DeleteDialog } from "@/components/delete-dialog";
import SitesPage from "@/app/(app)/sites/page";
import LicensesPage from "@/app/(app)/licenses/page";
import OperatorsPage from "@/app/(app)/operators/page";
import OnboardingPage from "@/app/(app)/onboarding/page";
import SecurityPage from "@/app/(app)/security/page";

// ------------------------------------------------------------------------------------------------------------
// The signed-in operators. Tenant users are pinned to t-semantics (whoami default_tenant_id).
// ------------------------------------------------------------------------------------------------------------

const tenantUser = (role: string) => ({
  operator_id: `op-${role}`, email: `${role}@semantics.test`, is_super_admin: false,
  default_tenant_id: "t-semantics", roles: [role], expires_at: "2099-01-01T00:00:00Z",
});

const ROLES: Record<string, Whoami> = {
  platform_admin: PLATFORM_ME as Whoami,
  tenant_admin: tenantUser("tenant_admin") as Whoami,
  tenant_operator: tenantUser("tenant_operator") as Whoami,
  viewer: tenantUser("viewer") as Whoami,
  billing: tenantUser("billing") as Whoami,
  // A catalog platform role without is_super_admin and without a customer (auth/repo.go loadRoles).
  platform_support: {
    operator_id: "op-sup", email: "support@vendor.test", is_super_admin: false, roles: ["platform_support"],
    expires_at: "2099-01-01T00:00:00Z",
  } as Whoami,
};
const TENANT_ROLES = ["tenant_admin", "tenant_operator", "viewer", "billing"] as const;
const TENANTS = { data: [{ id: "t-semantics", slug: "semantics", name: "Semantics" }] };
const empty = { data: [], meta: { has_more: false } };

function renderAs(role: string, ui: React.ReactNode) {
  return render(
    <CustomerProvider me={ROLES[role]}>
      <ToastProvider>{ui}</ToastProvider>
    </CustomerProvider>,
  );
}

beforeEach(() => window.localStorage.clear());

// ------------------------------------------------------------------------------------------------------------
// The map mirrors ctrlapi. These read the Go source so a server change that the map misses fails here.
// ------------------------------------------------------------------------------------------------------------

const CP = resolve(__dirname, "../../control-plane/internal");
const go = (rel: string) => readFileSync(resolve(CP, rel), "utf8");

describe("permission map mirrors ctrlapi", () => {
  it("ROLE_PERMISSIONS equals auth/permissions.go rolePermissions, role for role", () => {
    const src = go("auth/permissions.go");
    const body = src.slice(src.indexOf("var rolePermissions"), src.indexOf("// HasPermission"));
    const parsed: Record<string, string[]> = {};
    const re = /"([a-z_]+)":\s*\{([^}]*)\}/g;
    let m: RegExpExecArray | null;
    while ((m = re.exec(body))) parsed[m[1]] = [...m[2].matchAll(/"([^"]+)"/g)].map((x) => x[1]);
    expect(Object.keys(parsed).sort()).toEqual(Object.keys(ROLE_PERMISSIONS).sort());
    for (const [role, perms] of Object.entries(parsed)) {
      expect([...ROLE_PERMISSIONS[role]].sort(), role).toEqual([...perms].sort());
    }
  });

  it("covers every role the database allows", () => {
    const sql = readFileSync(resolve(CP, "../migrations/0021_operator_roles_expand.up.sql"), "utf8");
    const roles = [...sql.slice(sql.indexOf("role IN")).matchAll(/'([a-z_]+)'/g)].map((x) => x[1]);
    expect(roles.sort()).toEqual(Object.keys(ROLE_PERMISSIONS).sort());
  });

  it("every catalog-gated capability cites a Go file that applies exactly that permission", () => {
    for (const [cap, rule] of Object.entries(PERMISSIONS)) {
      const file = rule.server.split(" ")[0];
      const src = go(file);
      if (rule.mechanism === "P") {
        expect(src, `${cap} → ${file}`).toContain(`RequirePermission("${(rule as { permission: string }).permission}")`);
      } else if (rule.mechanism === "S") {
        expect(/IsSuperAdmin|RequireRole\("platform_admin"\)/.test(src), `${cap} → ${file}`).toBe(true);
      } else if (rule.mechanism === "O") {
        expect(src).toContain("mayManageOperators");
        expect(src).toContain(`hasAnyRole(s.Roles, "tenant_admin")`);
      }
    }
  });

  it("Sites and Appliances really have no role check server-side (so the UI hides nothing by role)", () => {
    for (const f of ["api/sites.go", "api/appliances.go"]) {
      const src = go(f);
      expect(src).toContain("RequireTenantOrPlatform");
      expect(src).not.toMatch(/RequirePermission|RequireRole|IsSuperAdmin|hasAnyRole/);
    }
  });

  it("key entries", () => {
    expect(PERMISSIONS["licenses.change"].server).toContain("RequireRole(\"platform_admin\")");
    expect(go("api/licenses.go")).toContain(`auth.RequireRole("platform_admin")`);
    expect(PERMISSIONS["securityAlerts.triage"].permission).toBe("platform.appliances.view");
    expect(go("api/appliance_lifecycle.go")).toContain(
      `RequirePermission("platform.appliances.view")).Patch("/security-alerts/{id}"`);
    expect(hasPermission(subjectFrom(ROLES.platform_admin), "platform.appliances.assign")).toBe(true);
    expect(hasPermission({ isSuperAdmin: false, roles: ["platform_owner"], defaultTenantId: "" }, "anything")).toBe(true);
    expect(hasPermission(subjectFrom(ROLES.tenant_admin), "platform.appliances.view")).toBe(false);
  });

  it("capabilities per role", () => {
    const expected: Record<string, Capability[]> = {
      platform_admin: Object.keys(PERMISSIONS) as Capability[],
      tenant_admin: [
        "customers.read", "customers.rename", "sites.read", "sites.write", "appliances.read", "appliances.write",
        "licenses.read", "operators.read", "operators.write", "audit.read",
      ],
      tenant_operator: [
        "customers.read", "customers.rename", "sites.read", "sites.write", "appliances.read", "appliances.write",
        "licenses.read", "audit.read",
      ],
      platform_support: [
        "customers.read", "onboarding.read", "securityAlerts.read", "securityAlerts.triage", "certificates.read",
        "backupHealth.read",
      ],
    };
    expected.viewer = expected.tenant_operator;
    expected.billing = expected.tenant_operator;
    for (const [role, caps] of Object.entries(expected)) {
      const can = capabilitiesFor(subjectFrom(ROLES[role]));
      const granted = (Object.keys(can) as Capability[]).filter((k) => can[k]);
      expect(granted.sort(), role).toEqual([...caps].sort());
    }
  });
});

// ------------------------------------------------------------------------------------------------------------
// Pages show exactly the controls the map allows.
// ------------------------------------------------------------------------------------------------------------

describe("Navigation per role", () => {
  it("lists only the pages the role can read", async () => {
    mockFetch([{ match: "/api/v1/tenants", body: TENANTS }]);
    const labels = (role: string) => {
      const { unmount } = renderAs(role, <Nav email="x" onLogout={() => {}} />);
      const nav = screen.getByRole("navigation", { name: "Main" });
      const out = within(nav).getAllByRole("link").map((a) => a.textContent);
      unmount();
      return out;
    };
    expect(labels("platform_admin")).toHaveLength(12);
    expect(labels("tenant_admin")).toEqual(["Dashboard", "Sites", "Appliances", "Customers", "Licenses", "Operators", "Audit log"]);
    for (const r of ["tenant_operator", "viewer", "billing"]) {
      expect(labels(r)).toEqual(["Dashboard", "Sites", "Appliances", "Customers", "Licenses", "Audit log"]);
    }
    expect(labels("platform_support")).toEqual(["Dashboard", "Onboarding", "Customers", "Security alerts", "Certificates", "Backup health"]);
  });
});

const SITE = { id: "s1", tenant_id: "t-semantics", code: "demo", name: "Semantics Demo", timezone: "UTC", status: "active", created_at: "2026-01-01T00:00:00Z", updated_at: "" };

describe("Sites per role", () => {
  it.each(TENANT_ROLES)("%s: New site, Edit, Archive and Delete (the server refuses no role here)", async (role) => {
    mockFetch([{ match: "/api/v1/sites?tenant_id=t-semantics&status=all", body: { data: [SITE], meta: { has_more: false } } }]);
    renderAs(role, <SitesPage />);
    await screen.findByRole("table");
    expect(screen.getByRole("button", { name: /New site/ })).toBeEnabled();
    expect(screen.getByRole("button", { name: "Edit Semantics Demo" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Archive/ })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Delete Semantics Demo" })).toBeInTheDocument();
    expect(screen.queryByText("Your role can view this but not change it.")).not.toBeInTheDocument();
  });

  it("platform admin in All customers: creation disabled with the reason", async () => {
    mockFetch([
      { match: "/api/v1/tenants", body: TENANTS },
      { match: "/api/v1/sites?tenant_id=&status=all", body: { data: [SITE], meta: { has_more: false } } },
    ]);
    renderAs("platform_admin", <SitesPage />);
    await screen.findByRole("table");
    expect(screen.getByRole("button", { name: /New site/ })).toBeDisabled();
    expect(screen.getByText(/Select a customer in the sidebar to create a site/)).toBeInTheDocument();
  });

  it("platform admin with a customer: creates under the selected customer", async () => {
    window.localStorage.setItem("sc.customerContext", "t-semantics");
    const user = userEvent.setup();
    const { calls } = mockFetch([
      { match: "/api/v1/tenants", body: TENANTS },
      { match: "/api/v1/sites?tenant_id=t-semantics&status=all", body: empty },
      { method: "POST", match: "/api/v1/sites?tenant_id=t-semantics", body: SITE },
    ]);
    renderAs("platform_admin", <SitesPage />);
    const btn = await screen.findByRole("button", { name: /New site/ });
    await waitFor(() => expect(btn).toBeEnabled());
    await user.click(btn);
    expect(screen.getByLabelText("Owning customer")).toHaveValue("Semantics");
    await user.type(screen.getByLabelText(/^Code/), "demo");
    await user.type(screen.getByLabelText(/^Name/), "Semantics Demo");
    await user.click(screen.getByRole("button", { name: "Create site" }));
    await waitFor(() => expect(calls.some((c) => c.method === "POST")).toBe(true));
    expect(calls.find((c) => c.method === "POST")!.url).toBe("/api/v1/sites?tenant_id=t-semantics");
  });

  it("platform_support (no customer): restricted, no controls", () => {
    mockFetch([]);
    renderAs("platform_support", <SitesPage />);
    expect(screen.getByText("Not available to your role")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /New site/ })).not.toBeInTheDocument();
  });
});

const LIC = {
  id: "l1", tenant_id: "t-semantics", site_id: "s1", commercial_plan_code: "", status: "active",
  issued_at: "2026-01-01T00:00:00Z", valid_until: "2099-01-01T00:00:00Z", offline_grace_days: 0,
  appliance_ids: ["a1"], key_id: "k", created_at: "", grace_period_days: 30, license_version: 1,
  max_concurrent_online_guests: 500,
};
const licenseRoutes = (tid: string) => [
  { match: "/api/v1/tenants", body: TENANTS },
  { match: `/api/cloud/v1/licenses?tenant_id=${tid}`, body: { data: [LIC, { ...LIC, id: "l2", status: "suspended" }], meta: { has_more: false } } },
  { match: `/api/v1/sites?tenant_id=${tid}`, body: { data: [SITE], meta: { has_more: false } } },
  { match: `/api/v1/appliances?tenant_id=${tid}`, body: { data: [{ id: "a1", site_id: "s1", serial: "SN-1", name: "gw" }], meta: { has_more: false } } },
];

describe("Licenses per role", () => {
  it.each(TENANT_ROLES)("%s: reads, changes nothing, sees the read-only notice", async (role) => {
    mockFetch(licenseRoutes("t-semantics"));
    renderAs(role, <LicensesPage />);
    await screen.findByRole("table");
    expect(screen.getByText("Your role can view this but not change it.")).toBeInTheDocument();
    for (const name of [/Issue license/, /Renew/, /Suspend/, /Resume/, /Revoke/, /Download for offline/]) {
      expect(screen.queryByRole("button", { name })).not.toBeInTheDocument();
    }
  });

  it("platform admin: issue, renew, suspend, resume (confirmed), revoke, download", async () => {
    window.localStorage.setItem("sc.customerContext", "t-semantics");
    const user = userEvent.setup();
    mockFetch(licenseRoutes("t-semantics"));
    renderAs("platform_admin", <LicensesPage />);
    await screen.findByRole("table");
    await waitFor(() => expect(screen.getByRole("button", { name: /Issue license/ })).toBeEnabled());
    expect(screen.queryByText("Your role can view this but not change it.")).not.toBeInTheDocument();
    expect(screen.getAllByRole("button", { name: /Renew/ }).length).toBe(2);
    expect(screen.getByRole("button", { name: /Suspend/ })).toBeInTheDocument();
    expect(screen.getAllByRole("button", { name: /Revoke/ }).length).toBe(2);
    expect(screen.getAllByRole("button", { name: /Download for offline/ }).length).toBeGreaterThan(0);
    // Resume is license-changing, so it asks first, like Suspend and Revoke.
    await user.click(screen.getByRole("button", { name: /Resume/ }));
    expect(await screen.findByRole("heading", { name: "Resume this license?" })).toBeInTheDocument();
  });
});

const OPS = { data: [{ id: "op-x", email: "desk@semantics.test", status: "active", roles: [{ id: "r1", role: "viewer" }], created_at: "", updated_at: "" }], meta: { has_more: false } };

describe("Operators per role", () => {
  it("tenant_admin: full management of its own customer", async () => {
    mockFetch([{ match: "/api/v1/operators?tenant_id=t-semantics", body: OPS }]);
    renderAs("tenant_admin", <OperatorsPage />);
    await screen.findByRole("table");
    expect(screen.getByRole("button", { name: /New operator/ })).toBeEnabled();
    expect(screen.getByRole("button", { name: /Reset password/ })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Disable/ })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Remove role Viewer/ })).toBeInTheDocument();
  });

  it.each(["tenant_operator", "viewer", "billing"])("%s: the server refuses even the list — restricted, no controls", async (role) => {
    mockFetch([{ match: "/api/v1/operators?tenant_id=t-semantics", status: 403, body: { error: "forbidden", message: "operators management requires tenant_admin / platform_admin" } }]);
    renderAs(role, <OperatorsPage />);
    expect(await screen.findByText("Not available to your role")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /New operator/ })).not.toBeInTheDocument();
    expect(screen.queryByRole("table")).not.toBeInTheDocument();
    expect(screen.queryByText(/operators management requires/)).not.toBeInTheDocument();
  });
});

const onboardingRoutes = [
  { match: "/api/cloud/v1/appliances-admin/pending", body: { data: [{ id: "p1", serial: "SN-NEW", wan_mac: "aa:bb" }] } },
  { match: "/api/v1/tenants", body: TENANTS },
  { match: "/api/v1/appliances?tenant_id=t-semantics", body: { data: [{ id: "a1", serial: "SN-1", lifecycle_state: "activated" }] } },
];

describe("Onboarding per role", () => {
  it("platform admin: selects and activates, imports, deactivates, downloads, deletes", async () => {
    const user = userEvent.setup();
    mockFetch(onboardingRoutes);
    renderAs("platform_admin", <OnboardingPage />);
    await user.click(await screen.findByText("SN-NEW"));
    expect(screen.getByRole("button", { name: "Activate" })).toBeInTheDocument();
    expect(screen.getByText("Offline activation")).toBeInTheDocument();
    await screen.findByText("SN-1");
    expect(screen.getByRole("button", { name: /Deactivate/ })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Activation package/ })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Delete/ })).toBeInTheDocument();
    expect(screen.queryByText("Your role can view this but not change it.")).not.toBeInTheDocument();
  });

  it("platform_support (view only): lists, read-only notice, nothing to activate or change", async () => {
    const user = userEvent.setup();
    mockFetch(onboardingRoutes);
    renderAs("platform_support", <OnboardingPage />);
    await user.click(await screen.findByText("SN-NEW"));
    expect(screen.getByText("Your role can view this but not change it.")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Activate" })).not.toBeInTheDocument();
    expect(screen.queryByText("Offline activation")).not.toBeInTheDocument();
    for (const name of [/Deactivate/, /Activation package/, /Delete/]) {
      expect(screen.queryByRole("button", { name })).not.toBeInTheDocument();
    }
  });

  it.each(TENANT_ROLES)("%s: restricted", async (role) => {
    mockFetch([]);
    renderAs(role, <OnboardingPage />);
    expect(screen.getByText("Not available to your role")).toBeInTheDocument();
    expect(screen.queryByText("Pending activation")).not.toBeInTheDocument();
  });
});

const ALERTS = { data: [{ id: "al1", appliance_id: "a1", serial: "SN-1", kind: "hardware_reused", detail: null, source_ip: "1.2.3.4", resolved: false, status: "open", at: "2026-01-01T00:00:00Z" }] };

describe("Security alerts per role", () => {
  it.each(["platform_admin", "platform_support"])("%s: reads and triages (the server gates both with the view permission)", async (role) => {
    mockFetch([{ match: "/api/cloud/v1/appliances-admin/security-alerts", body: ALERTS }]);
    renderAs(role, <SecurityPage />);
    await screen.findByRole("table");
    for (const name of ["Investigate", "Acknowledge", "Resolve", "False positive"]) {
      expect(screen.getByRole("button", { name })).toBeInTheDocument();
    }
  });

  it.each(TENANT_ROLES)("%s: restricted, no triage", async (role) => {
    mockFetch([{ match: "/api/cloud/v1/appliances-admin/security-alerts", status: 403, body: { error: "forbidden" } }]);
    renderAs(role, <SecurityPage />);
    expect(await screen.findByText("Not available to your role")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Investigate" })).not.toBeInTheDocument();
  });
});

// ------------------------------------------------------------------------------------------------------------
// One destructive pattern, each endpoint's own contract.
// ------------------------------------------------------------------------------------------------------------

describe("Appliance delete — same dialog, each endpoint's contract", () => {
  const base = {
    open: true, onClose: () => {}, onDeleted: () => {}, title: "Delete appliance SN-1", what: "Appliance",
    expected: "SN-1", confirmHint: "Type the appliance serial",
  };

  it("Appliances page (DELETE /v1/appliances/{id}): typed serial, no reason, no body", async () => {
    const user = userEvent.setup();
    const { calls } = mockFetch([{ method: "DELETE", match: /\/api\/v1\/appliances\/a1/, status: 204, body: {} }]);
    render(
      <DeleteDialog {...base} deleteUrl="/v1/appliances/a1?tenant_id=t-semantics" takesReason={false} stepUp={false}
        consequences={["The appliance record is removed from this customer.", "It cannot be undone."]} />,
    );
    expect(screen.getByText("It cannot be undone.")).toBeInTheDocument();
    expect(screen.queryByLabelText(/Reason/)).not.toBeInTheDocument();
    const btn = screen.getByRole("button", { name: "Delete appliance" });
    expect(btn).toBeDisabled();
    await user.type(screen.getByLabelText(/Type the appliance serial/), "SN-1");
    await user.click(btn);
    await waitFor(() => expect(calls.length).toBe(1));
    expect(calls[0]).toEqual({ method: "DELETE", url: "/api/v1/appliances/a1?tenant_id=t-semantics", body: undefined });
  });

  it("Onboarding (DELETE /cloud/v1/appliances-admin/{id}): typed serial plus reason, sent as the body", async () => {
    const user = userEvent.setup();
    const { calls } = mockFetch([{ method: "DELETE", match: /appliances-admin\/a1/, status: 204, body: {} }]);
    render(<DeleteDialog {...base} deleteUrl="/cloud/v1/appliances-admin/a1" consequences={["It cannot be undone."]} />);
    expect(screen.getByText("It cannot be undone.")).toBeInTheDocument();
    await user.type(screen.getByLabelText(/Type the appliance serial/), "SN-1");
    const btn = screen.getByRole("button", { name: "Delete appliance" });
    expect(btn).toBeDisabled();
    await user.type(screen.getByLabelText(/Reason/), "returned");
    await user.click(btn);
    await waitFor(() => expect(calls.length).toBe(1));
    expect(calls[0].body).toEqual({ confirm: "SN-1", reason: "returned" });
  });
});
