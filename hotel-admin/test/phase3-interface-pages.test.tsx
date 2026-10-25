import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

const get = vi.fn();
const post = vi.fn();
const put = vi.fn();
const del = vi.fn();
vi.mock("@/lib/api", () => ({
  api: {
    get: (...a: any[]) => get(...a),
    post: (...a: any[]) => post(...a),
    put: (...a: any[]) => put(...a),
    del: (...a: any[]) => del(...a),
  },
  // ApiError is re-exported by the real module and pages do `e instanceof ApiError`; a bare mock would make that throw.
  ApiError: class ApiError extends Error {},
}));

beforeEach(() => {
  get.mockReset();
  post.mockReset();
  put.mockReset();
  del.mockReset();
});
afterEach(() => vi.resetModules());

const iface = {
  id: "i1",
  connector_kind: "protel-fias",
  display_label: "Main PMS",
  lifecycle_state: "ACTIVE",
  current_revision_id: "r1",
  current_revision_no: 1,
  revision_count: 2,
  published: true,
  secret_generation: 3,
  secret_rotated_at: new Date().toISOString(),
};

const health = {
  pms_interface_id: "i1",
  room_auth_ready: true,
  transport_status: "CONNECTED",
  continuity_status: "CONTINUOUS",
  sync_status: "IN_SYNC",
  materialization_ready: true,
  in_house_stays: 12,
  pending_events: 4,
  review_events: 1,
  oldest_pending_at: new Date(Date.now() - 3 * 3600_000).toISOString(),
};

const revisions = [
  { id: "r2", revision_no: 2, source_timezone: "Europe/Berlin", posting_target_model: "UNSET",
    normalization_version: 1, config: { host: "pms.local", password: "[redacted]" }, published: false },
  { id: "r1", revision_no: 1, source_timezone: "Europe/Berlin", posting_target_model: "UNSET",
    normalization_version: 1, config: { host: "pms.local" }, published: true },
];

function mockInterfacePage(overrides: Record<string, any> = {}) {
  get.mockImplementation((path: string) => {
    if (path === "/auth/whoami") return Promise.resolve({ roles: overrides.roles ?? ["site_admin"] });
    if (path === "/pms-interfaces") return Promise.resolve({ interfaces: [overrides.iface ?? iface] });
    if (path.endsWith("/health")) return Promise.resolve({ health: overrides.health ?? health });
    if (path.endsWith("/revisions")) return Promise.resolve({ revisions: overrides.revisions ?? revisions });
    if (path === "/pms-providers") {
      return overrides.providers
        ? Promise.resolve({ providers: overrides.providers })
        : Promise.reject(Object.assign(new Error("page not found"), { status: 404 }));
    }
    if (path === "/pms-routing") return Promise.resolve({ routes: [], unmapped_guest_networks: [] });
    return Promise.resolve({ interface: overrides.iface ?? iface, guest_networks: overrides.routes ?? [] });
  });
}

async function openSheet(tab?: string) {
  await screen.findByText("Main PMS");
  await userEvent.click(await screen.findByRole("button", { name: "Manage" }));
  if (tab) await userEvent.click(await screen.findByRole("tab", { name: tab }));
}

describe("PMS interfaces page", () => {
  it("shows the published revision as live, and a newer one only as a draft", async () => {
    mockInterfacePage();
    const Page = (await import("@/app/(app)/pms-interfaces/page")).default;
    render(<Page />);
    await openSheet("Configuration");

    await screen.findByRole("heading", { name: "Live configuration" });
    const sheet = screen.getByRole("dialog");
    expect(await within(sheet).findByText("Version 1")).toBeTruthy();
    expect(within(sheet).getAllByText("In use")).toHaveLength(1);
    expect(within(sheet).getByText("Draft")).toBeTruthy();
    expect(within(sheet).getByRole("button", { name: "Put version 2 live" })).toBeTruthy();
  });

  it("states plainly when an interface has nothing published", async () => {
    mockInterfacePage({
      iface: { ...iface, published: false, current_revision_id: undefined, current_revision_no: null },
      health: { ...health, room_auth_ready: false, room_auth_reason: "NO_PUBLISHED_REVISION" },
    });
    const Page = (await import("@/app/(app)/pms-interfaces/page")).default;
    render(<Page />);
    expect(await screen.findByText(/No configuration has been put live/i)).toBeTruthy();
    expect(screen.getByText("None published")).toBeTruthy();
  });

  it("shows the health dimensions separately and the age of the backlog", async () => {
    mockInterfacePage();
    const Page = (await import("@/app/(app)/pms-interfaces/page")).default;
    render(<Page />);
    await openSheet();

    await screen.findByText("The checks room sign-in depends on");
    expect(screen.getAllByText("Connection").length).toBeGreaterThan(0);
    expect(screen.getByText("Live updates")).toBeTruthy();
    expect(screen.getAllByText("Guest list").length).toBeGreaterThan(0);
    expect(screen.getAllByText(/12/).length).toBeGreaterThan(0);
    expect(screen.getByText(/^Oldest waiting message arrived/)).toBeTruthy();
  });

  it("publishes with the revision the operator believed was live, a reason and a password", async () => {
    mockInterfacePage();
    post.mockResolvedValue({ current_revision_id: "r2", revision_no: 2 });
    const Page = (await import("@/app/(app)/pms-interfaces/page")).default;
    render(<Page />);
    await openSheet("History");

    await screen.findByText("Configuration history");
    await userEvent.click(screen.getByRole("button", { name: /^Version 2/ }));
    await userEvent.click(await screen.findByRole("button", { name: "Put version 2 live" }));
    const dialog = await screen.findByRole("dialog", { name: /Put version 2 live/ });
    await userEvent.selectOptions(within(dialog).getByLabelText(/Reason/), "ENDPOINT_CHANGE");
    await userEvent.type(within(dialog).getByLabelText(/Confirm your password/), "pw");
    await userEvent.click(within(dialog).getByRole("button", { name: /^Put live$/ }));

    await waitFor(() => expect(post).toHaveBeenCalled());
    const [path, body] = post.mock.calls[0];
    expect(path).toBe("/pms-interfaces/i1/publish");
    // expected_revision_id is required: without it the server cannot tell a deliberate change from one that reverts a
    // concurrent publish.
    expect(body).toEqual({ revision_id: "r2", expected_revision_id: "r1", reason_code: "ENDPOINT_CHANGE", password: "pw" });
  });

  it("puts an older version back in use as a rollback", async () => {
    mockInterfacePage({
      iface: { ...iface, current_revision_id: "r2", current_revision_no: 2 },
      revisions: [
        { ...revisions[0], published: true },
        { ...revisions[1], published: false },
      ],
    });
    post.mockResolvedValue({ current_revision_id: "r1", revision_no: 1 });
    const Page = (await import("@/app/(app)/pms-interfaces/page")).default;
    render(<Page />);
    await openSheet("History");

    await userEvent.click(await screen.findByRole("button", { name: /^Version 1/ }));
    await userEvent.click(await screen.findByRole("button", { name: /Put this version back in use/ }));
    const dialog = await screen.findByRole("dialog", { name: /Put version 1 live/ });
    await userEvent.type(within(dialog).getByLabelText(/Confirm your password/), "pw");
    await userEvent.click(within(dialog).getByRole("button", { name: /^Put live$/ }));

    await waitFor(() => expect(post).toHaveBeenCalled());
    expect(post.mock.calls[0][1]).toEqual({
      revision_id: "r1", expected_revision_id: "r2", reason_code: "CONFIG_ROLLBACK", password: "pw",
    });
  });

  it("surfaces a refused publication instead of appearing to succeed", async () => {
    mockInterfacePage();
    post.mockRejectedValue(Object.assign(
      new Error("another operator published a different revision while this form was open"),
      { code: "revision_conflict", status: 409 },
    ));
    const Page = (await import("@/app/(app)/pms-interfaces/page")).default;
    render(<Page />);
    await openSheet("History");
    await userEvent.click(await screen.findByRole("button", { name: /^Version 2/ }));
    await userEvent.click(await screen.findByRole("button", { name: "Put version 2 live" }));
    const dialog = await screen.findByRole("dialog", { name: /Put version 2 live/ });
    await userEvent.type(within(dialog).getByLabelText(/Confirm your password/), "pw");
    await userEvent.click(within(dialog).getByRole("button", { name: /^Put live$/ }));

    const alert = await within(dialog).findByRole("alert");
    expect(alert.textContent).toMatch(/another operator published a different revision/i);
    expect(alert.textContent).toMatch(/reload/i);
  });

  // The Protel FIAS link has no transport credential (credential_mode=NONE), so no Credentials tab or "never set"
  // warning is shown.
  it("presents no credential surface for a connector that needs none", async () => {
    mockInterfacePage();
    const Page = (await import("@/app/(app)/pms-interfaces/page")).default;
    const { container } = render(<Page />);
    await openSheet();

    expect(screen.queryByRole("tab", { name: /credential/i })).toBeNull();
    expect(screen.queryByRole("button", { name: /replace credential/i })).toBeNull();
    expect(container.textContent).not.toMatch(/never set/i);
    for (const [p] of get.mock.calls) expect(String(p)).not.toMatch(/secret/);
  });

  it("offers only Protel when the provider catalogue is unavailable", async () => {
    mockInterfacePage();
    const Page = (await import("@/app/(app)/pms-interfaces/page")).default;
    render(<Page />);
    await screen.findByText("Main PMS");
    await userEvent.click(await screen.findByRole("button", { name: /add connection/i }));

    const dialog = await screen.findByRole("dialog", { name: /Add a PMS connection/ });
    const radios = within(dialog).getAllByRole("radio");
    expect(radios.map((r) => (r as HTMLInputElement).value)).toEqual(["protel-fias"]);
    expect(within(dialog).getByText(/Only Protel is offered/)).toBeTruthy();
  });

  it("offers exactly the catalogue's providers and never one it does not list", async () => {
    mockInterfacePage({
      providers: [
        { kind: "protel-fias", label: "Protel (FIAS)", transport: "SOCKET", verification: "LIVE_PROVIDER_VERIFIED" },
        { kind: "apaleo", label: "apaleo", transport: "REST_POLL", verification: "AUTOMATED_CONTRACT_VERIFIED" },
      ],
    });
    const Page = (await import("@/app/(app)/pms-interfaces/page")).default;
    render(<Page />);
    await screen.findByText("Main PMS");
    await userEvent.click(await screen.findByRole("button", { name: /add connection/i }));

    const dialog = await screen.findByRole("dialog", { name: /Add a PMS connection/ });
    await waitFor(() =>
      expect(within(dialog).getAllByRole("radio").map((r) => (r as HTMLInputElement).value))
        .toEqual(["protel-fias", "apaleo"]));
    for (const unlisted of ["opera-fias", "fidelio-fias", "mews", "stub"]) {
      expect(dialog.textContent).not.toContain(unlisted);
    }
  });

  it("does not ask the operator for implementation-controlled revision settings", async () => {
    mockInterfacePage();
    const Page = (await import("@/app/(app)/pms-interfaces/page")).default;
    render(<Page />);
    await openSheet("Configuration");
    await userEvent.click(await screen.findByRole("button", { name: "Edit configuration" }));

    expect(await screen.findByLabelText(/PMS time zone/)).toBeTruthy(); // still the operator's to set
    for (const gone of [/folio identity/i, /credential mode/i, /normalization version/i, /resync supported/i]) {
      expect(screen.queryByLabelText(gone)).toBeNull();
    }
  });

  it("says when no guest network routes to the interface", async () => {
    mockInterfacePage({ routes: [] });
    const Page = (await import("@/app/(app)/pms-interfaces/page")).default;
    render(<Page />);
    await openSheet("Client networks");
    expect(await screen.findByText(/No Wi-Fi network points at this connection/)).toBeTruthy();
  });
});

describe("Guest network routing page", () => {
  const net = (id: string, name: string, mode: string, ifaces: { pms_interface_id: string; label: string }[] = [], over: Record<string, unknown> = {}) => ({
    network_id: id, name, enabled: true, archived: false, is_default: false, mode, fingerprint: `fp-${id}`,
    interfaces: ifaces.map((i) => ({ ...i, lifecycle_state: "ACTIVE", published: true })), ...over,
  });
  const assessment = (networks: Record<string, unknown>[]) => ({
    generated_at: "2026-10-26T08:00:00Z", overall: "READY", networks, interfaces: [], issues: [],
  });
  const routingMock = (over: Record<string, any> = {}) =>
    get.mockImplementation((path: string) => {
      if (path === "/auth/whoami") return Promise.resolve({ roles: over.roles ?? ["site_admin"] });
      if (path === "/pms-interfaces") return Promise.resolve({ interfaces: over.interfaces ?? [{ ...iface }] });
      if (path === "/pms-routing/readiness") return Promise.resolve(over.assessment ?? assessment([]));
      if (path === "/pms-routing") return Promise.resolve({ routes: [], policies: over.policies ?? [], unmapped_guest_networks: [] });
      return Promise.resolve({});
    });

  it("shows each network's room sign-in choice and what it checks", async () => {
    routingMock({
      policies: [
        net("gn1", "Guest VLAN 10", "DEDICATED", [{ pms_interface_id: "i1", label: "Main PMS" }]),
        net("gn2", "Resort", "SHARED_STRICT", [{ pms_interface_id: "i1", label: "Main PMS" }, { pms_interface_id: "i2", label: "Spa PMS" }]),
        net("gn3", "Vouchers", "NONE"),
        net("gn4", "Conference VLAN 20", ""),
      ],
      assessment: assessment([
        { network_id: "gn1", state: "READY", summary: "Room sign-in checks Main PMS and it can answer.", interfaces: [], reasons: [] },
        { network_id: "gn3", state: "OFF", summary: "Room sign-in is not offered on this network, by choice. Guests use the other sign-in methods.", interfaces: [], reasons: [] },
        { network_id: "gn4", state: "UNDECIDED", summary: "No one has decided how room sign-in works on this network, so it is not offered.", interfaces: [], reasons: [] },
      ]),
    });
    const Page = (await import("@/app/(app)/pms-routing/page")).default;
    render(<Page />);

    expect(await screen.findByText("Guest VLAN 10")).toBeTruthy();
    expect(screen.getByText("One dedicated PMS")).toBeTruthy();
    expect(screen.getByText("Main PMS, Spa PMS")).toBeTruthy();
    expect(screen.getByText("Shared strict set")).toBeTruthy();
    // a deliberately PMS-free network is "Not offered", not a fault; an undecided one needs a decision
    expect(screen.getByText("Not offered")).toBeTruthy();
    expect(screen.getByText("Needs a decision")).toBeTruthy();
    expect(screen.getByRole("button", { name: /decide/i })).toBeTruthy();
  });

  // Three explicit choices; the change is previewed, and is applied only against the preview that was shown.
  it("lets a site admin choose a dedicated PMS, previews it, and applies it against that preview", async () => {
    routingMock({ policies: [net("gn2", "Conference VLAN 20", "")] });
    post.mockResolvedValue({
      network: { ...net("gn2", "Conference VLAN 20", ""), fingerprint: "fp-gn2" },
      proposed: { mode: "DEDICATED", interfaces: [{ pms_interface_id: "i1", label: "Main PMS", lifecycle_state: "ACTIVE", published: true, in_house_stays: 12, room_auth_ready: true, health_known: true, also_used_by: [], live_sessions_on_this_network: 0 }] },
      added: [], removed: [], blockers: [], warnings: [{ code: "INTERFACE_ALSO_USED", text: "Main PMS is also used by Rooms." }],
      impact: { active_sessions: 0, live_room_entitlements: 0, room_numbers_in_more_than_one_selected_pms: 0, sign_in_methods_switched_off: [], network_serves_clients: true },
      guarantees: ["No PMS traffic is started or stopped, and nothing is posted to any PMS."],
    });
    put.mockResolvedValue({});
    const Page = (await import("@/app/(app)/pms-routing/page")).default;
    render(<Page />);

    await userEvent.click(await screen.findByRole("button", { name: /decide/i }));
    await userEvent.click(await screen.findByRole("radio", { name: /one dedicated pms/i }));
    await userEvent.click(await screen.findByRole("radio", { name: /main pms/i }));
    await userEvent.click(screen.getByRole("button", { name: /preview the change/i }));

    await screen.findByTestId("routing-preview");
    expect(post).toHaveBeenCalledWith("/pms-routing/gn2/preview", { mode: "DEDICATED", pms_interface_ids: ["i1"] });
    expect(screen.getByText(/also used by Rooms/)).toBeTruthy();
    expect(screen.getByText(/nothing is posted to any PMS/)).toBeTruthy();
    expect(put).not.toHaveBeenCalled();

    await userEvent.click(screen.getByRole("button", { name: /confirm and apply/i }));
    await waitFor(() => expect(put).toHaveBeenCalled());
    expect(put.mock.calls[0][0]).toBe("/pms-routing/gn2");
    expect(put.mock.calls[0][1]).toEqual({ mode: "DEDICATED", pms_interface_ids: ["i1"], based_on: "fp-gn2" });
  });

  it("does not let a shared set exceed three, or be applied with a blocker", async () => {
    const four = ["a", "b", "c", "d"].map((x) => ({ ...iface, id: `i-${x}`, display_label: `PMS ${x}` }));
    routingMock({ interfaces: four, policies: [net("gn2", "Resort", "")] });
    post.mockResolvedValue({
      network: net("gn2", "Resort", ""), proposed: { mode: "SHARED_STRICT", interfaces: [] }, added: [], removed: [],
      blockers: [{ code: "DUPLICATE_SOURCE", text: "PMS a and PMS b may be the same property's PMS." }], warnings: [],
      impact: { active_sessions: 0, live_room_entitlements: 0, room_numbers_in_more_than_one_selected_pms: 0, sign_in_methods_switched_off: [], network_serves_clients: true },
      guarantees: [],
    });
    const Page = (await import("@/app/(app)/pms-routing/page")).default;
    render(<Page />);

    await userEvent.click(await screen.findByRole("button", { name: /decide/i }));
    await userEvent.click(await screen.findByRole("radio", { name: /shared strict set/i }));
    for (const x of ["a", "b", "c"]) await userEvent.click(await screen.findByRole("checkbox", { name: new RegExp(`PMS ${x}`) }));
    expect((screen.getByRole("checkbox", { name: /PMS d/ }) as HTMLInputElement).disabled).toBe(true);

    await userEvent.click(screen.getByRole("button", { name: /preview the change/i }));
    await screen.findByText("This cannot be applied");
    expect((screen.getByRole("button", { name: /confirm and apply/i }) as HTMLButtonElement).disabled).toBe(true);
    expect(put).not.toHaveBeenCalled();
  });

  // A shared set whose member has since gone out of service is repaired by removing it: it must stay uncheckable while an unselected one cannot be added.
  it("lets an administrator remove a selected interface that has gone out of service, but not add one that is", async () => {
    const gone = { ...iface, id: "i1", display_label: "Old PMS", lifecycle_state: "AUTH_DISABLED" };
    const live = { ...iface, id: "i2", display_label: "Main PMS" };
    const off = { ...iface, id: "i3", display_label: "Spa PMS", lifecycle_state: "AUTH_DISABLED" };
    routingMock({
      interfaces: [gone, live, off],
      policies: [net("gn1", "Resort", "SHARED_STRICT", [{ pms_interface_id: "i1", label: "Old PMS" }, { pms_interface_id: "i2", label: "Main PMS" }])],
    });
    const Page = (await import("@/app/(app)/pms-routing/page")).default;
    render(<Page />);
    await userEvent.click(await screen.findByRole("button", { name: /^change/i }));
    const old = await screen.findByRole("checkbox", { name: /Old PMS/ });
    expect((old as HTMLInputElement).checked).toBe(true);
    expect((old as HTMLInputElement).disabled).toBe(false);
    expect((screen.getByRole("checkbox", { name: /Spa PMS/ }) as HTMLInputElement).disabled).toBe(true);
    await userEvent.click(old);
    expect((old as HTMLInputElement).checked).toBe(false);
  });

  it("offers no way to change the routing to a role that may only read it", async () => {
    routingMock({
      roles: ["front_office_operator"],
      policies: [net("gn1", "Guest VLAN 10", "DEDICATED", [{ pms_interface_id: "i1", label: "Main PMS" }])],
    });
    const Page = (await import("@/app/(app)/pms-routing/page")).default;
    render(<Page />);
    await screen.findByText("Guest VLAN 10");

    expect(screen.queryByRole("button", { name: /change/i })).toBeNull();
    expect(screen.queryByRole("button", { name: /decide/i })).toBeNull();
    expect(put).not.toHaveBeenCalled();
    expect(del).not.toHaveBeenCalled();
  });
});

describe("Source conflicts page", () => {
  it("names both interfaces by label, not by id", async () => {
    get.mockResolvedValue({
      conflicts: [{
        id: "c1", interface_a: "i1", interface_a_label: "Main PMS",
        interface_b: "i2", interface_b_label: "Spa PMS", severity: "HIGH", resolution: "UNRESOLVED",
      }],
    });
    const Page = (await import("@/app/(app)/pms-source-conflicts/page")).default;
    render(<Page />);

    expect(await screen.findByText("Main PMS")).toBeTruthy();
    expect(screen.getByText("Spa PMS")).toBeTruthy();
    expect(screen.getByText("High")).toBeTruthy();
    expect(screen.queryByText("i1")).toBeNull();
    expect(screen.queryByText("i2")).toBeNull();
  });

  it("says so when there are none", async () => {
    get.mockResolvedValue({ conflicts: [] });
    const Page = (await import("@/app/(app)/pms-source-conflicts/page")).default;
    render(<Page />);
    expect(await screen.findByText("No source conflicts")).toBeTruthy();
  });
});

describe("Resolution evidence page", () => {
  it("summarises the outcomes and names no guest", async () => {
    get.mockResolvedValue({
      data: [
        { id: "a1", guest_network_id: "gn1", outcome_code: "VERIFIED", resolved: true, resolved_at: new Date().toISOString() },
        { id: "a2", guest_network_id: "gn1", outcome_code: "AMBIGUOUS_DISCRIMINATOR_REQUIRED", resolved: false, resolved_at: new Date().toISOString() },
        { id: "a3", guest_network_id: "gn1", outcome_code: "AMBIGUOUS_DISCRIMINATOR_REQUIRED", resolved: false, resolved_at: new Date().toISOString() },
      ],
      meta: { has_more: false }, page: 1, page_size: 50, total: 3,
      summary: {
        window: 200, total: 3, verified: 1, newest_resolved_at: new Date().toISOString(),
        outcomes: [{ outcome_code: "AMBIGUOUS_DISCRIMINATOR_REQUIRED", count: 2 }, { outcome_code: "VERIFIED", count: 1 }],
        networks: [{ guest_network_id: "gn1", total: 3, verified: 1 }],
      },
    });
    const Page = (await import("@/app/(app)/pms-resolutions/page")).default;
    const { container } = render(<Page />);

    expect(await screen.findByText("Checks recorded")).toBeTruthy();
    expect(screen.getAllByText("Let online").length).toBeGreaterThan(0);
    expect(screen.getAllByText("2").length).toBeGreaterThan(0);
    expect(screen.getAllByText(/ambiguous discriminator required/i).length).toBeGreaterThan(0);
    // The data carries no guest identity: naming rooms or reservations would let someone enumerate who is staying.
    const table = container.querySelector("table")!.innerHTML.toLowerCase();
    for (const forbidden of ["room", "reservation", "guest_name", "stay_id", "folio"]) {
      expect(table).not.toContain(forbidden);
    }
  });
});
