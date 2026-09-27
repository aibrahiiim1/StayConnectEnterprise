import { describe, expect, it, vi, beforeEach } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { mockFetch, PLATFORM_ME, TENANT_ME, renderAs } from "./helpers";
import { nav } from "./navigation";
import { CUSTOMER, LICENSES, OVERVIEW, SITE, appliance } from "./fixtures";

vi.mock("next/navigation", async () => (await import("./navigation")).navigationModule);

import LicensesPage from "@/app/(app)/licenses/page";
import AppliancesPage from "@/app/(app)/appliances/page";
import CustomersPage from "@/app/(app)/customers/page";
import CustomerPage from "@/app/(app)/customers/[id]/page";

beforeEach(() => nav.reset());

describe("Licenses", () => {
  it("filters by state through the address, and asks the server for that state", async () => {
    const user = userEvent.setup();
    nav.reset("/licenses", "state=expired");
    const { calls } = mockFetch([
      { match: /\/api\/cloud\/v1\/licenses/, body: { items: [LICENSES[1]] } },
      { match: "/api/cloud/v1/overview", body: OVERVIEW },
    ]);
    renderAs(PLATFORM_ME, <LicensesPage />);

    const table = await screen.findByRole("table", { name: "Licenses" });
    expect(within(table).getByRole("link", { name: "OG-0003" })).toHaveAttribute("href", "/appliances/a2");
    expect(within(table).getByText("Expired")).toBeInTheDocument();
    expect(calls.some((c) => c.url === "/api/cloud/v1/licenses?state=expired")).toBe(true);

    const chips = screen.getByRole("radiogroup", { name: "License state" });
    expect(within(chips).getByRole("radio", { name: /Expired/ })).toHaveAttribute("aria-checked", "true");
    await user.click(within(chips).getByRole("radio", { name: /Suspended/ }));
    expect(nav.router.replace).toHaveBeenCalledWith("/licenses?state=suspended", { scroll: false });
    await user.click(within(chips).getByRole("radio", { name: /^All/ }));
    expect(nav.router.replace).toHaveBeenLastCalledWith("/licenses", { scroll: false });
  });
});

describe("Appliances", () => {
  it("lists waiting appliances first with an Activate button", async () => {
    nav.reset("/appliances");
    mockFetch([
      { match: "/api/cloud/v1/appliances", body: { items: [appliance("activated"), { ...appliance("waiting"), id: "a9", serial: "OG-0009" }] } },
      { match: "/api/cloud/v1/overview", body: OVERVIEW },
      { match: "/api/cloud/v1/customers?status=all", body: { items: [CUSTOMER] } },
    ]);
    renderAs(PLATFORM_ME, <AppliancesPage />);
    const table = await screen.findByRole("table", { name: "Appliances" });
    const rows = within(table).getAllByRole("row").slice(1);
    expect(within(rows[0]).getByRole("link", { name: "OG-0009" })).toBeInTheDocument();
    expect(within(rows[0]).getByRole("button", { name: /Activate/ })).toBeInTheDocument();
    expect(within(rows[1]).queryByRole("button", { name: /Activate/ })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Import activation request/ })).toBeInTheDocument();
  });

  it("passes the address filters to the server", async () => {
    nav.reset("/appliances", "activation=waiting&customer_id=c1");
    const { calls } = mockFetch([
      { match: /\/api\/cloud\/v1\/appliances\?/, body: { items: [] } },
      { match: "/api/cloud/v1/overview", body: OVERVIEW },
      { match: "/api/cloud/v1/customers?status=all", body: { items: [CUSTOMER] } },
      { match: "/api/cloud/v1/customers/c1/sites", body: { items: [SITE] } },
    ]);
    renderAs(PLATFORM_ME, <AppliancesPage />);
    expect(await screen.findByText("No appliances match")).toBeInTheDocument();
    expect(calls.some((c) => c.url === "/api/cloud/v1/appliances?customer_id=c1&activation=waiting")).toBe(true);
  });

  it("offers a customer's user no activation controls and no customer filter", async () => {
    nav.reset("/appliances");
    mockFetch([
      { match: "/api/cloud/v1/appliances", body: { items: [appliance("waiting")] } },
      { match: "/api/cloud/v1/overview", body: OVERVIEW },
    ]);
    renderAs(TENANT_ME, <AppliancesPage />);
    await screen.findByRole("table", { name: "Appliances" });
    expect(screen.queryByRole("button", { name: /Activate/ })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Import activation request/ })).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Customer")).not.toBeInTheDocument();
  });
});

describe("Customers", () => {
  it("lists customers, each opening its own page", async () => {
    nav.reset("/customers");
    mockFetch([{ match: "/api/cloud/v1/customers?status=active", body: { items: [CUSTOMER] } }]);
    renderAs(PLATFORM_ME, <CustomersPage />);
    const table = await screen.findByRole("table", { name: "Customers" });
    expect(within(table).getByRole("link", { name: "Semantics Hotels" })).toHaveAttribute("href", "/customers/c1");
    expect(screen.getByRole("button", { name: /New customer/ })).toBeInTheDocument();
  });

  it("sends a customer's user straight to their own customer", async () => {
    nav.reset("/customers");
    mockFetch([]);
    renderAs(TENANT_ME, <CustomersPage />);
    await waitFor(() => expect(nav.router.replace).toHaveBeenCalledWith("/customers/c-semantics"));
  });

  it("drills into a customer: tabs, and a new site posts the §6 body", async () => {
    const user = userEvent.setup();
    nav.reset("/customers/c1", "tab=sites");
    const { calls } = mockFetch([
      { match: "/api/cloud/v1/customers/c1", method: "GET", body: CUSTOMER },
      { match: "/api/cloud/v1/customers/c1/sites", method: "GET", body: { items: [SITE] } },
      { match: "/api/cloud/v1/customers/c1/sites", method: "POST", body: { ...SITE, id: "s2" } },
    ]);
    renderAs(PLATFORM_ME, <CustomerPage params={{ id: "c1" }} />);
    expect(await screen.findByRole("heading", { level: 1, name: "Semantics Hotels" })).toBeInTheDocument();
    const tabs = screen.getByRole("tablist", { name: "Customer sections" });
    expect(within(tabs).getAllByRole("tab").map((t) => t.textContent?.replace(/\d+$/, ""))).toEqual([
      "Summary", "Sites", "Appliances", "Licenses", "Users", "Activity",
    ]);
    expect(within(tabs).getByRole("tab", { name: /Sites/ })).toHaveAttribute("aria-selected", "true");

    const sites = await screen.findByRole("table", { name: "Sites" });
    expect(within(sites).getByText("Demo Resort", { selector: "div" })).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: /New site/ }));
    const dialog = await screen.findByRole("dialog", { name: "New site" });
    await user.type(within(dialog).getByLabelText(/^Name/), "Beach Hotel");
    await user.type(within(dialog).getByLabelText(/^Country/), "eg");
    await user.click(within(dialog).getByRole("button", { name: "Create site" }));
    await waitFor(() => expect(calls.some((c) => c.method === "POST")).toBe(true));
    const post = calls.find((c) => c.method === "POST")!;
    expect(post.url).toBe("/api/cloud/v1/customers/c1/sites");
    expect(post.body).toMatchObject({ name: "Beach Hotel", country: "EG" });
    expect(typeof (post.body as { timezone: string }).timezone).toBe("string");

    await user.click(within(tabs).getByRole("tab", { name: /Appliances/ }));
    expect(nav.router.replace).toHaveBeenLastCalledWith("/customers/c1?tab=appliances", { scroll: false });
  });

  it("hides customer-level controls from a customer's own admin, but lets them manage sites", async () => {
    nav.reset("/customers/c-semantics", "tab=sites");
    mockFetch([
      { match: "/api/cloud/v1/customers/c-semantics", method: "GET", body: { ...CUSTOMER, id: "c-semantics" } },
      { match: "/api/cloud/v1/customers/c-semantics/sites", method: "GET", body: { items: [SITE] } },
    ]);
    renderAs(TENANT_ME, <CustomerPage params={{ id: "c-semantics" }} />);
    await screen.findByRole("table", { name: "Sites" });
    expect(screen.queryByRole("button", { name: /Rename/ })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^Delete$/ })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: /New site/ })).toBeInTheDocument();
  });
});
