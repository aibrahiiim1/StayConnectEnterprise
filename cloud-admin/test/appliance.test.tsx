import { describe, expect, it, vi, beforeEach } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { mockFetch, PLATFORM_ME, SUPPORT_ME, renderAs } from "./helpers";
import { nav } from "./navigation";
import { CUSTOMER, SITE, appliance } from "./fixtures";

vi.mock("next/navigation", async () => (await import("./navigation")).navigationModule);

import AppliancePage from "@/app/(app)/appliances/[id]/page";

beforeEach(() => nav.reset("/appliances/a1"));

const detail = (a: ReturnType<typeof appliance>) => ({ match: "/api/cloud/v1/appliances/a1", method: "GET", body: a });
const lists = [
  { match: "/api/cloud/v1/customers?status=active", body: { items: [CUSTOMER] } },
  { match: "/api/cloud/v1/customers/c1/sites", body: { items: [SITE] } },
];
/** A §6 SU write: the first attempt is refused for step-up, the retry succeeds. */
const stepUp = (method: string, match: string) => ({
  method,
  match,
  body: (n: number) => (n === 0 ? { status: 403, body: { error: "reauth_required" } } : { body: { ok: true } }),
});
const reauth = { method: "POST", match: "/api/v1/auth/reauth", body: { ok: true } };

async function confirmPassword(user: ReturnType<typeof userEvent.setup>) {
  const dialog = await screen.findByRole("dialog", { name: "Confirm your password" });
  await user.type(within(dialog).getByLabelText(/^Password/), "hunter22");
  await user.click(within(dialog).getByRole("button", { name: "Confirm" }));
}

describe("Appliance page — the primary action follows the activation state", () => {
  it("waiting: Activate, and no license yet", async () => {
    mockFetch([detail(appliance("waiting"))]);
    renderAs(PLATFORM_ME, <AppliancePage params={{ id: "a1" }} />);
    expect(await screen.findByRole("heading", { name: "Waiting for activation" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Activate" })).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "License" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Delete record/ })).toBeInTheDocument();
    expect(screen.getByText("Not assigned yet")).toBeInTheDocument();
  });

  it("activating: explains the wait and offers the offline package", async () => {
    mockFetch([detail(appliance("activating"))]);
    renderAs(PLATFORM_ME, <AppliancePage params={{ id: "a1" }} />);
    expect(await screen.findByRole("heading", { name: "Activating" })).toBeInTheDocument();
    expect(screen.getByText(/checks every 5 seconds/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Activation package/ })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Activate" })).not.toBeInTheDocument();
  });

  it("activated: the license and its operations, plus Move and Retire", async () => {
    mockFetch([detail(appliance("activated"))]);
    renderAs(PLATFORM_ME, <AppliancePage params={{ id: "a1" }} />);
    expect(await screen.findByRole("heading", { name: "License" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Renew or change" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Suspend/ })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Revoke/ })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Offline license file/ })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Move/ })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Retire appliance/ })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Activate" })).not.toBeInTheDocument();
    // Protocol details stay out of the primary view.
    const advanced = screen.getByText("Advanced").closest("details")!;
    expect(advanced).not.toHaveAttribute("open");
    expect(within(advanced).getByText("Certificate fingerprint")).toBeInTheDocument();
  });

  it("retired: only Delete record", async () => {
    mockFetch([detail(appliance("retired"))]);
    renderAs(PLATFORM_ME, <AppliancePage params={{ id: "a1" }} />);
    expect(await screen.findByRole("heading", { name: "Retired" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Delete record/ })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Renew or change" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Retire appliance/ })).not.toBeInTheDocument();
  });

  it("support staff see the same page without any write control", async () => {
    mockFetch([detail(appliance("activated"))]);
    renderAs(SUPPORT_ME, <AppliancePage params={{ id: "a1" }} />);
    expect(await screen.findByRole("heading", { name: "License" })).toBeInTheDocument();
    for (const name of [/Renew or change/, /Suspend/, /Revoke/, /Move/, /Retire/, /Delete/]) {
      expect(screen.queryByRole("button", { name })).not.toBeInTheDocument();
    }
  });
});

describe("Activate", () => {
  it("sends the §6 body with the default terms, after the password step-up", async () => {
    const user = userEvent.setup();
    const { calls } = mockFetch([
      detail(appliance("waiting")),
      ...lists,
      stepUp("POST", "/api/cloud/v1/appliances/a1/activate"),
      reauth,
    ]);
    renderAs(PLATFORM_ME, <AppliancePage params={{ id: "a1" }} />);
    await user.click(await screen.findByRole("button", { name: "Activate" }));
    const dialog = await screen.findByRole("dialog", { name: "Activate OG-0001" });
    const customer = within(dialog).getByLabelText(/^Customer/);
    await waitFor(() => expect(within(customer).getByRole("option", { name: "Semantics Hotels" })).toBeInTheDocument());
    await user.selectOptions(customer, "c1");
    const site = within(dialog).getByLabelText(/^Site/);
    await waitFor(() => expect(within(site).getByRole("option", { name: "Demo Resort" })).toBeInTheDocument());
    await user.selectOptions(site, "s1");
    expect(within(dialog).getByLabelText(/Guests online at once/)).toHaveValue("500");
    expect(within(dialog).getByLabelText(/Days from today/)).toHaveValue("365");
    expect(within(dialog).getByLabelText(/Grace period/)).toHaveValue("30");
    await user.click(within(dialog).getByRole("button", { name: "Activate" }));
    await confirmPassword(user);

    await waitFor(() => expect(calls.filter((c) => c.url.endsWith("/activate"))).toHaveLength(2));
    const sent = calls.filter((c) => c.url === "/api/cloud/v1/appliances/a1/activate");
    expect(sent[1].body).toEqual({
      customer_id: "c1",
      site_id: "s1",
      license: { max_concurrent_online_guests: 500, valid_days: 365, grace_period_days: 30 },
    });
    expect(calls.some((c) => c.url === "/api/v1/auth/reauth")).toBe(true);
  });

  it("an appliance that still holds a customer's data: that customer is preselected and cannot be changed", async () => {
    const user = userEvent.setup();
    const { calls } = mockFetch([
      detail(appliance("waiting", { holds_customer_id: "c1", holds_customer_name: "Semantics Hotels" })),
      ...lists,
      { method: "POST", match: "/api/cloud/v1/appliances/a1/activate", body: { ok: true } },
    ]);
    renderAs(PLATFORM_ME, <AppliancePage params={{ id: "a1" }} />);
    expect(await screen.findByText(/can be activated only for\s+that customer/)).toBeInTheDocument();
    await user.click(await screen.findByRole("button", { name: "Activate" }));
    const dialog = await screen.findByRole("dialog", { name: "Activate OG-0001" });
    expect(within(dialog).getByText("This appliance still holds a customer's data")).toBeInTheDocument();
    expect(within(dialog).getByText(/factory-reset it first/)).toBeInTheDocument();
    const customer = within(dialog).getByLabelText(/^Customer/);
    expect(customer).toBeDisabled();
    expect(customer).toHaveValue("c1");
    expect(within(customer).queryByRole("option", { name: "New customer…" })).not.toBeInTheDocument();
    const site = within(dialog).getByLabelText(/^Site/);
    await waitFor(() => expect(within(site).getByRole("option", { name: "Demo Resort" })).toBeInTheDocument());
    await user.selectOptions(site, "s1");
    await user.click(within(dialog).getByRole("button", { name: "Activate" }));

    await waitFor(() => expect(calls.some((c) => c.url.endsWith("/activate"))).toBe(true));
    const body = calls.find((c) => c.url.endsWith("/activate"))!.body as Record<string, any>;
    expect(body.customer_id).toBe("c1");
    expect(body.site_id).toBe("s1");
    expect(body.new_customer).toBeUndefined();
  });

  it("an appliance holding a customer Central no longer has cannot be activated: factory-reset first", async () => {
    const user = userEvent.setup();
    const { calls } = mockFetch([
      detail(appliance("waiting", { holds_customer_id: "c-gone", holds_customer_name: null })),
      ...lists,
      { method: "POST", match: "/api/cloud/v1/appliances/a1/activate", body: { ok: true } },
    ]);
    renderAs(PLATFORM_ME, <AppliancePage params={{ id: "a1" }} />);
    await user.click(await screen.findByRole("button", { name: "Activate" }));
    const dialog = await screen.findByRole("dialog", { name: "Activate OG-0001" });
    expect(within(dialog).getByText(/no longer in Central, so it cannot be activated for anyone/)).toBeInTheDocument();
    await user.click(within(dialog).getByRole("button", { name: "Activate" }));
    expect(await within(dialog).findByText(/still holds another customer's data\. Factory-reset it/)).toBeInTheDocument();
    expect(calls.some((c) => c.url.endsWith("/activate"))).toBe(false);
  });

  it("creates the customer and site in the same step, with an end date", async () => {
    const user = userEvent.setup();
    const { calls } = mockFetch([
      detail(appliance("waiting")),
      ...lists,
      { method: "POST", match: "/api/cloud/v1/appliances/a1/activate", body: { ok: true } },
    ]);
    renderAs(PLATFORM_ME, <AppliancePage params={{ id: "a1" }} />);
    await user.click(await screen.findByRole("button", { name: "Activate" }));
    const dialog = await screen.findByRole("dialog", { name: "Activate OG-0001" });
    const customer = within(dialog).getByLabelText(/^Customer/);
    await waitFor(() => expect(customer).toBeEnabled());
    await user.selectOptions(customer, "__new");
    await user.type(within(dialog).getByLabelText(/New customer's name/), "Red Sea Hotels");
    await user.type(within(dialog).getByLabelText(/New site's name/), "Marsa Resort");
    await user.type(within(dialog).getByLabelText(/^Country/), "eg");
    await user.clear(within(dialog).getByLabelText(/Guests online at once/));
    await user.type(within(dialog).getByLabelText(/Guests online at once/), "250");
    await user.click(within(dialog).getByRole("radio", { name: "Until a date" }));
    await user.type(within(dialog).getByLabelText(/Ends on/), "2030-12-31");
    await user.click(within(dialog).getByRole("button", { name: "Activate" }));

    await waitFor(() => expect(calls.some((c) => c.url.endsWith("/activate"))).toBe(true));
    const body = calls.find((c) => c.url.endsWith("/activate"))!.body as Record<string, any>;
    expect(body.new_customer).toEqual({ name: "Red Sea Hotels" });
    expect(body.new_site).toMatchObject({ name: "Marsa Resort", country: "EG" });
    expect(typeof body.new_site.timezone).toBe("string");
    expect(body.customer_id).toBeUndefined();
    expect(body.license).toEqual({ max_concurrent_online_guests: 250, valid_until: "2030-12-31T23:59:59Z", grace_period_days: 30 });
  });
});

describe("License operations", () => {
  it("Suspend posts the reason to the §6 path, with step-up", async () => {
    const user = userEvent.setup();
    const { calls } = mockFetch([
      detail(appliance("activated")),
      stepUp("POST", "/api/cloud/v1/licenses/l1/suspend"),
      reauth,
    ]);
    renderAs(PLATFORM_ME, <AppliancePage params={{ id: "a1" }} />);
    await user.click(await screen.findByRole("button", { name: /Suspend/ }));
    const dialog = await screen.findByRole("dialog", { name: "Suspend license" });
    await user.type(within(dialog).getByLabelText(/Reason/), "unpaid invoice");
    await user.click(within(dialog).getByRole("button", { name: "Suspend license" }));
    await confirmPassword(user);
    await waitFor(() => expect(calls.filter((c) => c.url === "/api/cloud/v1/licenses/l1/suspend")).toHaveLength(2));
    expect(calls.filter((c) => c.url === "/api/cloud/v1/licenses/l1/suspend")[1].body).toEqual({ reason: "unpaid invoice" });
  });

  it("Revoke needs the typed serial", async () => {
    const user = userEvent.setup();
    const { calls } = mockFetch([
      detail(appliance("activated")),
      { method: "POST", match: "/api/cloud/v1/licenses/l1/revoke", body: { ok: true } },
    ]);
    renderAs(PLATFORM_ME, <AppliancePage params={{ id: "a1" }} />);
    await user.click(await screen.findByRole("button", { name: /Revoke/ }));
    const dialog = await screen.findByRole("dialog", { name: "Revoke license" });
    await user.type(within(dialog).getByLabelText(/Reason/), "contract ended");
    const confirm = within(dialog).getByRole("button", { name: "Revoke license" });
    expect(confirm).toBeDisabled();
    await user.type(within(dialog).getByLabelText(/Type the serial/), "OG-0001");
    expect(confirm).toBeEnabled();
    await user.click(confirm);
    await waitFor(() => expect(calls.some((c) => c.url === "/api/cloud/v1/licenses/l1/revoke")).toBe(true));
    expect(calls.find((c) => c.url === "/api/cloud/v1/licenses/l1/revoke")!.body).toEqual({ reason: "contract ended" });
  });

  it("Renew or change posts new terms and a reason to /appliances/{id}/license", async () => {
    const user = userEvent.setup();
    const { calls } = mockFetch([
      detail(appliance("activated")),
      { method: "POST", match: "/api/cloud/v1/appliances/a1/license", body: { ok: true } },
    ]);
    renderAs(PLATFORM_ME, <AppliancePage params={{ id: "a1" }} />);
    await user.click(await screen.findByRole("button", { name: "Renew or change" }));
    const dialog = await screen.findByRole("dialog", { name: "Renew or change license" });
    await user.type(within(dialog).getByLabelText(/^Reason/), "annual renewal");
    await user.click(within(dialog).getByRole("button", { name: "Save new license" }));
    await waitFor(() => expect(calls.some((c) => c.url === "/api/cloud/v1/appliances/a1/license")).toBe(true));
    expect(calls.find((c) => c.url === "/api/cloud/v1/appliances/a1/license")!.body).toEqual({
      max_concurrent_online_guests: 500, valid_days: 365, grace_period_days: 30, reason: "annual renewal",
    });
  });

  it("Retire explains the two phases and sends the typed serial and the emergency choice", async () => {
    const user = userEvent.setup();
    const { calls } = mockFetch([
      detail(appliance("activated")),
      { method: "POST", match: "/api/cloud/v1/appliances/a1/retire", body: { ok: true } },
    ]);
    renderAs(PLATFORM_ME, <AppliancePage params={{ id: "a1" }} />);
    await user.click(await screen.findByRole("button", { name: /Retire appliance/ }));
    const dialog = await screen.findByRole("dialog", { name: "Retire OG-0001" });
    expect(within(dialog).getByText(/shows Retiring until then/)).toBeInTheDocument();
    expect(within(dialog).getByText(/Either way it ends as Retired/)).toBeInTheDocument();
    await user.type(within(dialog).getByLabelText(/Type the serial/), "OG-0001");
    await user.type(within(dialog).getByLabelText(/Reason/), "hotel closed");
    await user.click(within(dialog).getByRole("button", { name: "Retire appliance" }));
    await waitFor(() => expect(calls.some((c) => c.url.endsWith("/retire"))).toBe(true));
    expect(calls.find((c) => c.url.endsWith("/retire"))!.body).toEqual({ reason: "hotel closed", emergency: false, confirm_serial: "OG-0001" });
  });
});

describe("Move — same customer only", () => {
  const SITE_2 = { ...SITE, id: "s2", code: "north", name: "North Beach", appliances: 0 };
  const SITE_ARCHIVED = { ...SITE, id: "s3", code: "old", name: "Old Wing", status: "archived", appliances: 0 };
  const sites = { match: "/api/cloud/v1/customers/c1/sites", body: { items: [SITE, SITE_2, SITE_ARCHIVED] } };

  async function openMove(user: ReturnType<typeof userEvent.setup>) {
    await user.click(await screen.findByRole("button", { name: /Move/ }));
    return screen.findByRole("dialog", { name: "Move OG-0001" });
  }

  it("offers only the customer's other active sites and posts the §6 body", async () => {
    const user = userEvent.setup();
    const { calls } = mockFetch([
      detail(appliance("activated")),
      sites,
      stepUp("POST", "/api/cloud/v1/appliances/a1/move"),
      reauth,
    ]);
    renderAs(PLATFORM_ME, <AppliancePage params={{ id: "a1" }} />);
    const dialog = await openMove(user);

    // No customer picker, and no customer list is even fetched.
    expect(within(dialog).queryByLabelText(/^Customer/)).not.toBeInTheDocument();
    expect(calls.some((c) => c.url.startsWith("/api/cloud/v1/customers?"))).toBe(false);
    expect(within(dialog).getByText("Semantics Hotels")).toBeInTheDocument();

    const site = within(dialog).getByLabelText(/^New site/);
    await waitFor(() => expect(within(site).getByRole("option", { name: "North Beach" })).toBeInTheDocument());
    const offered = within(site).getAllByRole("option").map((o) => o.textContent);
    expect(offered).toEqual(["Choose a site…", "North Beach"]); // not the current site, not the archived one

    expect(within(dialog).getByText(/re-issued for the new site with the same terms/)).toBeInTheDocument();
    expect(within(dialog).getByText(/retire it, factory-reset it and activate it for that customer/)).toBeInTheDocument();

    await user.selectOptions(site, "s2");
    await user.type(within(dialog).getByLabelText(/^Reason/), "moved to the north building");
    await user.click(within(dialog).getByRole("button", { name: "Move appliance" }));
    await confirmPassword(user);

    await waitFor(() => expect(calls.filter((c) => c.url === "/api/cloud/v1/appliances/a1/move")).toHaveLength(2));
    expect(calls.filter((c) => c.url === "/api/cloud/v1/appliances/a1/move")[1].body).toEqual({
      customer_id: "c1", site_id: "s2", reason: "moved to the north building",
    });
  });

  it.each([
    [409, "conflict", "an appliance cannot move to another customer; retire it and activate it for the new customer"],
    [503, "licensing_unavailable", "the licence could not be re-issued, so the move was refused and nothing changed"],
  ])("shows the server's own message on a %i", async (status, code, message) => {
    const user = userEvent.setup();
    mockFetch([
      detail(appliance("activated")),
      sites,
      { method: "POST", match: "/api/cloud/v1/appliances/a1/move", status, body: { error: code, message } },
    ]);
    renderAs(PLATFORM_ME, <AppliancePage params={{ id: "a1" }} />);
    const dialog = await openMove(user);
    const site = within(dialog).getByLabelText(/^New site/);
    await waitFor(() => expect(within(site).getByRole("option", { name: "North Beach" })).toBeInTheDocument());
    await user.selectOptions(site, "s2");
    await user.type(within(dialog).getByLabelText(/^Reason/), "reorganisation");
    await user.click(within(dialog).getByRole("button", { name: "Move appliance" }));
    expect(await within(dialog).findByText(message)).toBeInTheDocument();
    // Still open, so the operator reads why.
    expect(screen.getByRole("dialog", { name: "Move OG-0001" })).toBeInTheDocument();
  });

  it("with no other active site, says so and cannot submit", async () => {
    const user = userEvent.setup();
    mockFetch([detail(appliance("activated")), { match: "/api/cloud/v1/customers/c1/sites", body: { items: [SITE] } }]);
    renderAs(PLATFORM_ME, <AppliancePage params={{ id: "a1" }} />);
    const dialog = await openMove(user);
    expect(await within(dialog).findByText(/has no other active site/)).toBeInTheDocument();
    expect(within(dialog).getByRole("link", { name: /Add one on its Sites tab/ })).toHaveAttribute("href", "/customers/c1?tab=sites");
    expect(within(dialog).getByRole("button", { name: "Move appliance" })).toBeDisabled();
  });
});

describe("Retirement and replacement", () => {
  const future = new Date(Date.now() + 3_600_000).toISOString();

  it("retiring: waiting for the appliance, credentials still valid, with the emergency way out", async () => {
    mockFetch([detail(appliance("retiring", { retirement: { state: "terminal_pending_ack", deadline: future } }))]);
    renderAs(PLATFORM_ME, <AppliancePage params={{ id: "a1" }} />);
    expect(await screen.findByText(/waiting for the appliance to confirm \(by/)).toBeInTheDocument();
    expect(screen.getByText(/stay valid until it does/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "retire it now without waiting" })).toBeInTheDocument();
    expect(screen.queryByText("Retirement not confirmed")).not.toBeInTheDocument();
  });

  it("not confirmed: says so, and Retire now sends an emergency retirement", async () => {
    const user = userEvent.setup();
    const { calls } = mockFetch([
      detail(appliance("retiring", { retirement: { state: "terminal_delivery_failed", deadline: future } })),
      { method: "POST", match: "/api/cloud/v1/appliances/a1/retire", body: { ok: true } },
    ]);
    renderAs(PLATFORM_ME, <AppliancePage params={{ id: "a1" }} />);
    expect(await screen.findByText("Retirement not confirmed")).toBeInTheDocument();
    expect(screen.getByText(/did not confirm its retirement, so its credentials are still valid/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "retire it now without waiting" }));
    const dialog = await screen.findByRole("dialog", { name: "Retire OG-0001 now" });
    // Already retiring: not waiting is the only choice left, so there is no switch to turn it off.
    expect(within(dialog).queryByRole("switch")).not.toBeInTheDocument();
    expect(within(dialog).getByText(/revoked now, without waiting/)).toBeInTheDocument();
    await user.type(within(dialog).getByLabelText(/Type the serial/), "OG-0001");
    await user.type(within(dialog).getByLabelText(/Reason/), "box is dead");
    await user.click(within(dialog).getByRole("button", { name: "Retire now" }));
    await waitFor(() => expect(calls.some((c) => c.url.endsWith("/retire"))).toBe(true));
    expect(calls.find((c) => c.url.endsWith("/retire"))!.body).toEqual({ reason: "box is dead", emergency: true, confirm_serial: "OG-0001" });
  });

  it("marked for replacement: keeps working until the new one is activated, then retires once it confirms", async () => {
    mockFetch([detail(appliance("activated", { replacement: { pending: true, deadline: future } }))]);
    renderAs(PLATFORM_ME, <AppliancePage params={{ id: "a1" }} />);
    expect(await screen.findByText("Marked for replacement")).toBeInTheDocument();
    expect(screen.getByText(/It keeps working until a new appliance is activated at the same site/)).toBeInTheDocument();
    expect(document.body.textContent).toMatch(/starts retiring: it shows Retiring until it confirms, then Retired/);
    expect(document.body.textContent).not.toMatch(/instantly|immediately/i);
  });

  it("Mark for replacement describes the acknowledged retirement, not an instant cut-off", async () => {
    const user = userEvent.setup();
    mockFetch([detail(appliance("activated"))]);
    renderAs(PLATFORM_ME, <AppliancePage params={{ id: "a1" }} />);
    await screen.findByRole("heading", { name: "License" });
    await user.click(screen.getByText("Advanced"));
    await user.click(screen.getByRole("button", { name: /Mark for replacement/ }));
    const dialog = await screen.findByRole("dialog", { name: "Mark for replacement" });
    expect(within(dialog).getByText(/keeps working until the new appliance is activated at the same site/)).toBeInTheDocument();
    expect(within(dialog).getByText(/retired once it confirms/)).toBeInTheDocument();
  });

  it("links a replaced appliance to its successor", async () => {
    mockFetch([detail(appliance("retired", { replacement: { pending: false, replaced_by: "a2" } }))]);
    renderAs(PLATFORM_ME, <AppliancePage params={{ id: "a1" }} />);
    expect(await screen.findByRole("link", { name: "its new appliance" })).toHaveAttribute("href", "/appliances/a2");
  });
});
