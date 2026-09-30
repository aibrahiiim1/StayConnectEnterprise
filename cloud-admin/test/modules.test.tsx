import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { mockFetch, PLATFORM_ME, renderAs } from "./helpers";
import { nav } from "./navigation";
import { CUSTOMER, SITE, appliance } from "./fixtures";

vi.mock("next/navigation", async () => (await import("./navigation")).navigationModule);

import { presetFor, resetModuleCatalogCache, toggleModule, type ModuleCatalog } from "@/lib/modules";
import { ModuleFields } from "@/components/license-terms";
import { SiteTypeSelect } from "@/components/placement-fields";
import { SITE_TYPES } from "@/lib/site-types";
import AppliancePage from "@/app/(app)/appliances/[id]/page";
import CustomerPage from "@/app/(app)/customers/[id]/page";

/** The registry exactly as GET /cloud/v1/modules serves it (license/modules.go). */
const CATALOG: ModuleCatalog = {
  modules: [
    { id: "hospitality", label: "Hotel (PMS, Room sign-in, stays)", requires: [] },
    { id: "paid_access", label: "Paid access (priced Internet Packages)", requires: [] },
    { id: "card_payment", label: "Card payment", requires: ["paid_access"] },
    { id: "room_charge", label: "Room charge (PMS posting)", requires: ["hospitality", "paid_access"] },
    { id: "sms_otp", label: "SMS one-time code", requires: [] },
    { id: "email_otp", label: "Email one-time code", requires: [] },
    { id: "social_login", label: "Social sign-in", requires: [] },
    { id: "white_label", label: "White label", requires: [] },
    { id: "ha", label: "High availability", requires: [] },
  ],
  site_types: SITE_TYPES.map((t) => ({ id: t.value, preset: t.value === "HOTEL" ? ["hospitality"] : [] })),
};
const catalogRoute = { match: "/api/cloud/v1/modules", body: CATALOG };

beforeEach(() => {
  resetModuleCatalogCache();
  nav.reset("/appliances/a1");
});

describe("module dependency rules", () => {
  it("ticking a module ticks what it requires", () => {
    expect(toggleModule(CATALOG, [], "card_payment", true)).toEqual(["paid_access", "card_payment"]);
    expect(toggleModule(CATALOG, ["sms_otp"], "room_charge", true)).toEqual(["hospitality", "paid_access", "room_charge", "sms_otp"]);
  });
  it("unticking a module unticks everything that requires it", () => {
    const all = ["hospitality", "paid_access", "card_payment", "room_charge", "sms_otp"];
    expect(toggleModule(CATALOG, all, "paid_access", false)).toEqual(["hospitality", "sms_otp"]);
    expect(toggleModule(CATALOG, all, "hospitality", false)).toEqual(["paid_access", "card_payment", "sms_otp"]);
    expect(toggleModule(CATALOG, all, "card_payment", false)).toEqual(["hospitality", "paid_access", "room_charge", "sms_otp"]);
  });
  it("a module the registry does not list is kept, never silently dropped", () => {
    expect(toggleModule(CATALOG, ["future_vertical"], "ha", true)).toEqual(["ha", "future_vertical"]);
  });
  it("site-type presets are suggestions from Central", () => {
    expect(presetFor(CATALOG, "HOTEL")).toEqual(["hospitality"]);
    expect(presetFor(CATALOG, "CAFE")).toEqual([]);
    expect(presetFor(CATALOG, "UNKNOWN")).toEqual([]);
  });
});

function Harness({ initial = [] as string[], siteType }: { initial?: string[]; siteType?: string }) {
  const [v, setV] = useState<string[]>(initial);
  return (
    <>
      <ModuleFields value={v} onChange={setV} siteType={siteType} />
      <output data-testid="selected">{v.join(",")}</output>
    </>
  );
}

describe("ModuleFields", () => {
  it("renders the registry as checkboxes and keeps dependencies in step", async () => {
    const user = userEvent.setup();
    mockFetch([catalogRoute]);
    render(<Harness />);
    const card = await screen.findByRole("checkbox", { name: /Card payment/ });
    expect(screen.getByText("Needs Paid access (priced Internet Packages)")).toBeInTheDocument();
    await user.click(card);
    expect(screen.getByTestId("selected")).toHaveTextContent("paid_access,card_payment");
    expect(screen.getByRole("checkbox", { name: /^Paid access/ })).toBeChecked();
    await user.click(screen.getByRole("checkbox", { name: /^Paid access/ }));
    expect(screen.getByTestId("selected")).toHaveTextContent(/^$/);
    expect(card).not.toBeChecked();
  });

  it("offers the site type's suggestion", async () => {
    const user = userEvent.setup();
    mockFetch([catalogRoute]);
    render(<Harness siteType="HOTEL" />);
    await user.click(await screen.findByRole("button", { name: "Use the Hotel suggestion" }));
    expect(screen.getByTestId("selected")).toHaveTextContent("hospitality");
    expect(screen.queryByRole("button", { name: /suggestion/ })).not.toBeInTheDocument();
  });
});

describe("SiteTypeSelect", () => {
  it("lists every site type with its label and keeps a value it does not know", () => {
    const { rerender } = render(<SiteTypeSelect value="UNSPECIFIED" onChange={() => {}} />);
    const labels = screen.getAllByRole("option").map((o) => o.textContent);
    expect(labels).toEqual([
      "Hotel", "Cafe", "Office", "Clinic", "Campus", "Venue", "Compound", "Beach / Beach club", "Other", "Not specified",
    ]);
    rerender(<SiteTypeSelect value="MARINA" onChange={() => {}} />);
    expect(screen.getByRole("combobox")).toHaveValue("MARINA");
  });
});

describe("licence modules in the appliance dialogs", () => {
  it("Renew or change pre-fills the current licence's modules and sends them", async () => {
    const user = userEvent.setup();
    const a = appliance("activated");
    a.license = { ...a.license, modules: ["hospitality", "paid_access"] };
    const { calls } = mockFetch([
      { match: "/api/cloud/v1/appliances/a1", method: "GET", body: a },
      catalogRoute,
      { method: "POST", match: "/api/cloud/v1/appliances/a1/license", body: { ok: true } },
    ]);
    renderAs(PLATFORM_ME, <AppliancePage params={{ id: "a1" }} />);
    await user.click(await screen.findByRole("button", { name: "Renew or change" }));
    const dialog = await screen.findByRole("dialog", { name: "Renew or change license" });
    expect(await within(dialog).findByRole("checkbox", { name: /^Hotel/ })).toBeChecked();
    expect(within(dialog).getByRole("checkbox", { name: /^Paid access/ })).toBeChecked();
    await user.click(within(dialog).getByRole("checkbox", { name: /Card payment/ }));
    await user.type(within(dialog).getByLabelText(/Reason/), "add card payment");
    await user.click(within(dialog).getByRole("button", { name: "Save new license" }));
    await waitFor(() => expect(calls.some((c) => c.url === "/api/cloud/v1/appliances/a1/license")).toBe(true));
    const body = calls.find((c) => c.url === "/api/cloud/v1/appliances/a1/license")!.body as Record<string, unknown>;
    expect(body.modules).toEqual(["hospitality", "paid_access", "card_payment"]);
  });

  it("Activate suggests the modules for a hotel site", async () => {
    const user = userEvent.setup();
    const { calls } = mockFetch([
      { match: "/api/cloud/v1/appliances/a1", method: "GET", body: appliance("waiting") },
      { match: "/api/cloud/v1/customers?status=active", body: { items: [CUSTOMER] } },
      { match: "/api/cloud/v1/customers/c1/sites", body: { items: [{ ...SITE, site_type: "HOTEL" }] } },
      catalogRoute,
      { method: "POST", match: "/api/cloud/v1/appliances/a1/activate", body: { ok: true } },
    ]);
    renderAs(PLATFORM_ME, <AppliancePage params={{ id: "a1" }} />);
    await user.click(await screen.findByRole("button", { name: "Activate" }));
    const dialog = await screen.findByRole("dialog", { name: "Activate OG-0001" });
    const customer = within(dialog).getByLabelText(/^Customer/);
    await waitFor(() => expect(within(customer).getByRole("option", { name: "Semantics Hotels" })).toBeInTheDocument());
    await user.selectOptions(customer, "c1");
    const site = within(dialog).getByLabelText(/^Site$|^Site\b/);
    await waitFor(() => expect(within(site).getByRole("option", { name: "Demo Resort" })).toBeInTheDocument());
    await user.selectOptions(site, "s1");
    await waitFor(() => expect(within(dialog).getByRole("checkbox", { name: /^Hotel/ })).toBeChecked());
    await user.click(within(dialog).getByRole("button", { name: "Activate" }));
    await waitFor(() => expect(calls.some((c) => c.url.endsWith("/activate"))).toBe(true));
    const body = calls.find((c) => c.url.endsWith("/activate"))!.body as { license: { modules: string[] } };
    expect(body.license.modules).toEqual(["hospitality"]);
  });
});

describe("site type on the customer's Sites tab", () => {
  it("shows the type and sends it when a site is created", async () => {
    const user = userEvent.setup();
    nav.reset("/customers/c1", "tab=sites");
    const { calls } = mockFetch([
      { match: "/api/cloud/v1/customers/c1", method: "GET", body: CUSTOMER },
      { match: "/api/cloud/v1/customers/c1/sites", method: "GET", body: { items: [{ ...SITE, site_type: "BEACH_CLUB" }] } },
      { match: "/api/cloud/v1/customers/c1/sites", method: "POST", body: { ...SITE, id: "s9" } },
    ]);
    renderAs(PLATFORM_ME, <CustomerPage params={{ id: "c1" }} />);
    expect(await screen.findByText("Beach / Beach club")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /New site/ }));
    const dialog = await screen.findByRole("dialog", { name: "New site" });
    await user.type(within(dialog).getByLabelText(/^Name/), "Harbour Cafe");
    await user.selectOptions(within(dialog).getByLabelText(/^Site type/), "CAFE");
    await user.click(within(dialog).getByRole("button", { name: "Create site" }));
    await waitFor(() => expect(calls.some((c) => c.method === "POST")).toBe(true));
    expect(calls.find((c) => c.method === "POST")!.body).toMatchObject({ name: "Harbour Cafe", site_type: "CAFE" });
  });
});
