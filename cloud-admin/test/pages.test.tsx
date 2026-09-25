import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { mockFetch, PLATFORM_ME } from "./helpers";

vi.mock("next/navigation", () => ({
  usePathname: () => "/sites",
  useRouter: () => ({ replace: vi.fn(), push: vi.fn(), refresh: vi.fn() }),
  useSearchParams: () => new URLSearchParams(),
}));

import { CustomerProvider } from "@/lib/customer-context";
import { ToastProvider } from "@/components/ui/toast";
import type { Whoami } from "@/lib/api";
import SitesPage from "@/app/(app)/sites/page";
import OperatorsPage from "@/app/(app)/operators/page";
import LicensesPage from "@/app/(app)/licenses/page";
import LoginPage from "@/app/login/page";

const TENANTS = { data: [{ id: "t-acme", slug: "acme", name: "Acme Hotels" }] };

function withContext(ui: React.ReactNode) {
  return (
    <CustomerProvider me={PLATFORM_ME as Whoami}>
      <ToastProvider>{ui}</ToastProvider>
    </CustomerProvider>
  );
}

beforeEach(() => window.localStorage.clear());

describe("Sites page", () => {
  it("in All customers mode shows a Customer column, status words and designed Edit dialog", async () => {
    const user = userEvent.setup();
    const { calls } = mockFetch([
      { match: "/api/v1/tenants", body: TENANTS },
      {
        match: "/api/v1/sites?tenant_id=&status=all",
        body: { data: [
          { id: "s1", tenant_id: "t-acme", code: "coral", name: "Coral Sea", timezone: "Africa/Cairo", country: "EG", status: "active", created_at: "2026-01-01T00:00:00Z", updated_at: "" },
          { id: "s2", tenant_id: "t-acme", code: "old", name: "Old Resort", timezone: "UTC", status: "archived", created_at: "2026-01-01T00:00:00Z", updated_at: "" },
        ], meta: { has_more: false } },
      },
      { method: "PATCH", match: /\/api\/v1\/sites\/s1\?tenant_id=t-acme/, body: {} },
    ]);
    render(withContext(<SitesPage />));

    expect(screen.getByRole("heading", { level: 1, name: "Sites" })).toBeInTheDocument();
    expect(screen.getByText("Infrastructure")).toBeInTheDocument();
    const table = await screen.findByRole("table");
    expect(within(table).getByRole("columnheader", { name: "Customer" })).toBeInTheDocument();
    expect(within(table).getAllByText("Acme Hotels").length).toBe(2);
    expect(within(table).getByText("Active")).toBeInTheDocument();
    expect(within(table).getByText("Archived")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Edit Coral Sea" }));
    const name = await screen.findByLabelText("Name");
    await user.clear(name);
    await user.type(name, "Coral Sea Resort");
    await user.click(screen.getByRole("button", { name: "Save changes" }));
    await waitFor(() => expect(calls.some((c) => c.method === "PATCH")).toBe(true));
    const patch = calls.find((c) => c.method === "PATCH")!;
    expect(patch.url).toBe("/api/v1/sites/s1?tenant_id=t-acme");
    expect(patch.body).toEqual({ name: "Coral Sea Resort", timezone: "Africa/Cairo", country: "EG" });
  });
});

describe("Operators page", () => {
  it("in All customers mode asks for a customer instead of listing staff", async () => {
    const { calls } = mockFetch([{ match: "/api/v1/tenants", body: TENANTS }]);
    render(withContext(<OperatorsPage />));
    expect(await screen.findByText("Select a customer")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /New operator/ })).toBeDisabled();
    expect(calls.some((c) => c.url.startsWith("/api/v1/operators"))).toBe(false);
  });
});

describe("Licenses page", () => {
  it("shows every license state as a word", async () => {
    const day = 86400000;
    const now = Date.now();
    const lic = (id: string, over: Record<string, unknown>) => ({
      id, tenant_id: "t-acme", site_id: "s1", commercial_plan_code: "", status: "active",
      issued_at: new Date(now - 10 * day).toISOString(), valid_until: new Date(now + 100 * day).toISOString(),
      offline_grace_days: 0, appliance_ids: ["a1"], key_id: "k", created_at: "", grace_period_days: 30,
      license_version: 1, max_concurrent_online_guests: 500, ...over,
    });
    mockFetch([
      { match: "/api/v1/tenants", body: TENANTS },
      { match: /\/api\/cloud\/v1\/licenses\?/, body: { data: [
        lic("l1", {}),
        lic("l2", { valid_until: new Date(now - 5 * day).toISOString() }),
        lic("l3", { valid_until: new Date(now - 60 * day).toISOString() }),
        lic("l4", { status: "suspended" }),
        lic("l5", { status: "revoked" }),
        lic("l6", { status: "superseded" }),
        lic("l7", { appliance_ids: [] }),
      ], meta: { has_more: false } } },
      { match: /\/api\/v1\/sites\?/, body: { data: [{ id: "s1", code: "coral", name: "Coral Sea" }], meta: { has_more: false } } },
      { match: /\/api\/v1\/appliances\?/, body: { data: [{ id: "a1", site_id: "s1", serial: "SN-1", name: "gw" }], meta: { has_more: false } } },
      { match: /\/api\/cloud\/v1\/fleet\?/, body: { data: [], meta: { has_more: false } } },
    ]);
    render(withContext(<LicensesPage />));
    const table = await screen.findByRole("table");
    for (const word of ["Active", "Grace", "Expired", "Suspended", "Revoked", "Superseded", "Awaiting appliance binding"]) {
      expect(within(table).getByText(word)).toBeInTheDocument();
    }
    // Creation is disabled in All customers mode.
    expect(screen.getByRole("button", { name: /Issue license/ })).toBeDisabled();
  });
});

describe("Login page", () => {
  it("is Velonet Central with email, password and a collapsed single sign-on", async () => {
    const user = userEvent.setup();
    mockFetch([{ match: /\/api\/v1\/auth\/sso\/providers\?tenant=acme/, body: { data: [{ name: "okta", display_name: "Okta", kind: "oidc" }] } }]);
    render(<LoginPage />);
    expect(screen.getByRole("heading", { level: 1, name: "Velonet Central" })).toBeInTheDocument();
    expect(screen.getByLabelText("Email")).toBeInTheDocument();
    expect(screen.getByLabelText("Password")).toHaveAttribute("type", "password");
    expect(screen.queryByLabelText("Organisation slug")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /Use single sign-on instead/ }));
    await user.type(screen.getByLabelText("Organisation slug"), "acme");
    const link = await screen.findByRole("link", { name: "Sign in with Okta" });
    expect(link.getAttribute("href")).toContain("/api/v1/auth/sso/start?tenant=acme&provider=okta");
  });
});
