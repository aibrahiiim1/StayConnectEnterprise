import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import { mockFetch, PLATFORM_ME, TENANT_ME } from "./helpers";

vi.mock("next/navigation", () => ({
  usePathname: () => "/licenses",
  useRouter: () => ({ replace: vi.fn(), push: vi.fn(), refresh: vi.fn() }),
  useSearchParams: () => new URLSearchParams(),
}));

import { Nav, NAV_SECTIONS, activeNavHref } from "@/components/nav";
import { CustomerSelector } from "@/components/customer-selector";
import { CustomerProvider } from "@/lib/customer-context";
import type { Whoami } from "@/lib/api";

beforeEach(() => {
  window.localStorage.clear();
  mockFetch([{ match: "/api/v1/tenants", body: { data: [{ id: "t-semantics", slug: "semantics", name: "Semantics" }] } }]);
});

describe("Central navigation", () => {
  it("has exactly the four groups, in order", () => {
    expect(NAV_SECTIONS.map((s) => s.title)).toEqual(["Overview", "Infrastructure", "Commercial", "Administration"]);
  });

  it("has no Fleet item and does not list the retired pages", () => {
    const items = NAV_SECTIONS.flatMap((s) => s.items);
    expect(items.map((i) => i.label)).not.toContain("Fleet");
    expect(items.map((i) => i.href)).not.toContain("/fleet");
    expect(items.map((i) => i.href)).not.toContain("/commercial");
    expect(items.map((i) => i.href)).not.toContain("/subscription");
    expect(items.map((i) => i.label)).toEqual([
      "Dashboard", "Sites", "Onboarding", "Appliances", "Customers", "Licenses",
      "Operators", "Security alerts", "Certificates", "Assignment keys", "Backup health", "Audit log",
    ]);
  });

  it("resolves the active item by path boundary", () => {
    expect(activeNavHref("/licenses")).toBe("/licenses");
    expect(activeNavHref("/fleet")).toBeNull();
  });

  it("renders the groups, marks the current page and shows the Central product line", async () => {
    render(
      <CustomerProvider me={PLATFORM_ME as Whoami}>
        <Nav email="admin@example.test" onLogout={() => {}} />
      </CustomerProvider>,
    );
    for (const g of ["Overview", "Infrastructure", "Commercial", "Administration"]) {
      expect(screen.getByText(g)).toBeInTheDocument();
    }
    expect(screen.getByText("Central")).toBeInTheDocument();
    expect(screen.getByLabelText("OneGate")).toBeInTheDocument();
    expect(screen.getByText("OneGate by Semantics")).toBeInTheDocument();
    expect(screen.queryByText("Fleet")).not.toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Licenses" })).toHaveAttribute("aria-current", "page");
    expect(screen.queryByText(/StayConnect/i)).not.toBeInTheDocument();
    await waitFor(() => expect(screen.getByRole("option", { name: "Semantics" })).toBeInTheDocument());
  });
});

describe("Customer context selector", () => {
  it("offers All customers and each customer to a platform admin", async () => {
    render(
      <CustomerProvider me={PLATFORM_ME as Whoami}>
        <CustomerSelector />
      </CustomerProvider>,
    );
    const select = screen.getByLabelText("Customer context");
    await waitFor(() => expect(within(select).getByRole("option", { name: "Semantics" })).toBeInTheDocument());
    expect(within(select).getByRole("option", { name: "All customers" })).toBeInTheDocument();
  });

  it("shows a customer user a fixed label and no selector", async () => {
    render(
      <CustomerProvider me={TENANT_ME as Whoami}>
        <CustomerSelector />
      </CustomerProvider>,
    );
    await waitFor(() => expect(screen.getByText("Your customer")).toBeInTheDocument());
    expect(screen.queryByRole("combobox")).not.toBeInTheDocument();
  });
});
