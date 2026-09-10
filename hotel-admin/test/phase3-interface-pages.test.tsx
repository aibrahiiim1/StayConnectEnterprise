import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

// THE PMS INTERFACE ADMIN PAGES, in jsdom with a mocked edged.
//
// The API contract itself is proven against a real PostgreSQL in edged's integration tests. What is asserted
// here is what the OPERATOR sees and can do — the parts a backend test cannot reach:
//
//   the PUBLISHED revision is the one the interface points at, even when a newer one exists;
//   publishing sends the revision the operator BELIEVED was live, so a concurrent change is refused;
//   the credential form never renders a value and never asks the server for one;
//   an interface nobody routes to, and a network mapped to nobody, are both stated rather than left blank.

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
  // The ApiError class is re-exported by the real module and is used by the pages' error handling. A bare object
  // mock with no ApiError makes `e instanceof ApiError` throw rather than return false.
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
  // deliberately newest-first, with the OLDER one published
  { id: "r2", revision_no: 2, source_timezone: "Europe/Berlin", folio_identity_strategy: "UNIQUE_PER_STAY",
    normalization_version: 1, config: { host: "pms.local", password: "[redacted]" }, published: false },
  { id: "r1", revision_no: 1, source_timezone: "Europe/Berlin", folio_identity_strategy: "UNIQUE_PER_STAY",
    normalization_version: 1, config: { host: "pms.local" }, published: true },
];

function mockInterfacePage(overrides: Record<string, any> = {}) {
  get.mockImplementation((path: string) => {
    if (path === "/auth/whoami") return Promise.resolve({ roles: overrides.roles ?? ["site_admin"] });
    if (path === "/pms-interfaces") return Promise.resolve({ interfaces: [overrides.iface ?? iface] });
    if (path.endsWith("/health")) return Promise.resolve({ health: overrides.health ?? health });
    if (path.endsWith("/revisions")) return Promise.resolve({ revisions: overrides.revisions ?? revisions });
    return Promise.resolve({ interface: iface, guest_networks: overrides.routes ?? [] });
  });
}

describe("PMS interfaces page", () => {
  it("shows the published revision, not the newest one", async () => {
    mockInterfacePage();
    const Page = (await import("@/app/(app)/pms-interfaces/page")).default;
    render(<Page />);
    await screen.findByText("Main PMS");
    await userEvent.click(screen.getByRole("button", { name: "Manage" }));

    // revision 1 is live even though revision 2 exists and is newer
    const published = await screen.findByText("Live");
    const row = published.closest("tr")!;
    expect(within(row).getByText(/#1/)).toBeTruthy();
  });

  it("states plainly when an interface has nothing published", async () => {
    mockInterfacePage({
      iface: { ...iface, published: false, current_revision_id: undefined, current_revision_no: null },
      // The server would never report an interface with nothing published as ready; saying so in the fixture is
      // what makes this a test of the screen rather than of an impossible state.
      health: { ...health, room_auth_ready: false, room_auth_reason: "NO_PUBLISHED_REVISION" },
    });
    const Page = (await import("@/app/(app)/pms-interfaces/page")).default;
    render(<Page />);
    // An interface with nothing published resolves nothing. It is stated as the CONSEQUENCE now -- "No" under
    // "Can guests sign in?" with the reason beneath -- rather than as a "not configured" badge, because the
    // operator's question is whether guests can get online, not whether a record exists.
    expect(await screen.findByText("No")).toBeTruthy();
    expect(screen.getByText(/No configuration has been put live/i)).toBeTruthy();
  });

  it("shows the four health dimensions separately and the age of the backlog", async () => {
    mockInterfacePage();
    const Page = (await import("@/app/(app)/pms-interfaces/page")).default;
    render(<Page />);
    await screen.findByText("Main PMS");
    await userEvent.click(screen.getByRole("button", { name: "Manage" }));

    await screen.findByText("Connection status");
    // separate, because they fail separately and each has a different response
    expect(screen.getAllByText("Connection").length).toBeGreaterThan(0);
    expect(screen.getByText("Live updates")).toBeTruthy();
    expect(screen.getAllByText("Guest list").length).toBeGreaterThan(0);
    expect(screen.getAllByText(/12/).length).toBeGreaterThan(0);
    // the age of the oldest waiting event is what separates a busy morning from a stuck processor
    expect(screen.getByText(/^Oldest /)).toBeTruthy();
  });

  it("publishes with the revision the operator believed was live, a reason and a password", async () => {
    mockInterfacePage();
    post.mockResolvedValue({ current_revision_id: "r2", revision_no: 2 });
    const Page = (await import("@/app/(app)/pms-interfaces/page")).default;
    render(<Page />);
    await screen.findByText("Main PMS");
    await userEvent.click(screen.getByRole("button", { name: "Manage" }));

    await screen.findByText("Configuration history");
    await userEvent.click(screen.getByRole("button", { name: "Put live" }));
    await userEvent.type(await screen.findByLabelText(/Reason/), "CONFIG_UPDATE");
    await userEvent.type(screen.getByLabelText(/Confirm your password/), "pw");
    // Scoped to the dialog rather than picked by index: the row button that OPENED it has the same name, it is
    // inert while the dialog is open, and which of the two comes first in the document is an implementation
    // detail of where Radix portals its content.
    await userEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: /^Put live$/ }));

    await waitFor(() => expect(post).toHaveBeenCalled());
    const [path, body] = post.mock.calls[0];
    expect(path).toBe("/pms-interfaces/i1/publish");
    expect(body.revision_id).toBe("r2");
    // THE OPTIMISTIC CHECK: without this the server cannot tell a deliberate change from one that would
    // silently revert whoever published while this form was open.
    expect(body.expected_revision_id).toBe("r1");
    expect(body.reason_code).toBe("CONFIG_UPDATE");
    expect(body.password).toBe("pw");
  });

  it("surfaces a refused publication instead of appearing to succeed", async () => {
    mockInterfacePage();
    post.mockRejectedValue(new Error("another operator published a different revision"));
    const Page = (await import("@/app/(app)/pms-interfaces/page")).default;
    render(<Page />);
    await screen.findByText("Main PMS");
    await userEvent.click(screen.getByRole("button", { name: "Manage" }));
    await screen.findByText("Configuration history");
    await userEvent.click(screen.getByRole("button", { name: "Put live" }));
    await userEvent.type(await screen.findByLabelText(/Reason/), "CONFIG_UPDATE");
    await userEvent.type(screen.getByLabelText(/Confirm your password/), "pw");
    await userEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: /^Put live$/ }));

    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toMatch(/another operator published/);
  });

  // THE SUPPORTED CONNECTOR HAS NO CREDENTIAL, so the page must not present one.
  //
  // This replaces a test that asserted the Credential card behaved SECURELY — masked input, never echoed,
  // never fetched. That was the right test while a credential could exist. The Protel FIAS link carries no
  // transport authentication (credential_mode=NONE), so the card's "never set" badge described a missing
  // secret that is not supposed to exist, and read as a fault on a correctly configured interface.
  //
  // The component and its endpoint are deliberately still in the tree for a connector that authenticates;
  // what is asserted here is that nothing REACHES the operator and nothing is requested.
  it("presents no credential surface for a connector that needs none", async () => {
    mockInterfacePage();
    const Page = (await import("@/app/(app)/pms-interfaces/page")).default;
    const { container } = render(<Page />);
    await screen.findByText("Main PMS");
    await userEvent.click(screen.getByRole("button", { name: "Manage" }));

    expect(screen.queryByRole("heading", { name: "Credential" })).toBeNull();
    expect(screen.queryByRole("button", { name: /replace credential/i })).toBeNull();
    // the misleading warning specifically: an interface that needs no secret is not "never set"
    expect(container.textContent).not.toMatch(/never set/i);
    // and nothing on the page ever asks the server for one
    for (const [p] of get.mock.calls) expect(String(p)).not.toMatch(/secret/);
  });

  // Only the connector the canonical runtime supports is offered. pmsd refuses any other kind at validation,
  // so offering one here would let an operator author and publish configuration that can never connect.
  it("offers only the supported connector when creating an interface", async () => {
    mockInterfacePage();
    const Page = (await import("@/app/(app)/pms-interfaces/page")).default;
    const { container } = render(<Page />);
    await screen.findByText("Main PMS");
    await userEvent.click(screen.getByRole("button", { name: /add connection/i }));

    for (const unsupported of ["opera-fias", "fidelio-fias", "mews", "apaleo", "stub"]) {
      expect(container.textContent).not.toContain(unsupported);
    }
    // Two matches is correct: the existing interface's row and the create form's fixed connector. Asserting
    // "at least one" keeps the test about WHICH connectors are offered rather than about page layout.
    expect((await screen.findAllByText("Protel (FIAS)")).length).toBeGreaterThan(0);
  });

  // A Protel revision form must not ask for values the implementation controls. Each of these was a way to
  // save a revision that could never work, and folio identity was a financial assertion on a dropdown.
  it("does not ask the operator for implementation-controlled revision settings", async () => {
    mockInterfacePage();
    const Page = (await import("@/app/(app)/pms-interfaces/page")).default;
    render(<Page />);
    await screen.findByText("Main PMS");
    await userEvent.click(screen.getByRole("button", { name: "Settings" }));

    expect(await screen.findByLabelText(/PMS time zone/)).toBeTruthy(); // still the operator's to set
    for (const gone of [/folio identity/i, /credential mode/i, /normalization version/i, /resync supported/i]) {
      expect(screen.queryByLabelText(gone)).toBeNull();
    }
  });

  it("says when no guest network routes to the interface", async () => {
    mockInterfacePage({ routes: [] });
    const Page = (await import("@/app/(app)/pms-interfaces/page")).default;
    render(<Page />);
    await screen.findByText("Main PMS");
    await userEvent.click(screen.getByRole("button", { name: "Manage" }));
    // configured but unreachable looks identical to healthy everywhere else on the page
    expect(await screen.findByText(/No Wi-Fi network points at this connection/)).toBeTruthy();
  });
});

describe("Guest network routing page", () => {
  const routingMock = (over: Record<string, any> = {}) =>
    get.mockImplementation((path: string) => {
      if (path === "/auth/whoami") return Promise.resolve({ roles: over.roles ?? ["site_admin"] });
      if (path === "/pms-interfaces") {
        return Promise.resolve({ interfaces: over.interfaces ?? [{ ...iface }] });
      }
      return Promise.resolve({
        routes: over.routes ?? [],
        unmapped_guest_networks: over.unmapped ?? [],
      });
    });

  it("names the networks that are mapped to nothing", async () => {
    routingMock({
      routes: [{ guest_network_id: "gn1", guest_network_name: "Guest VLAN 10", pms_interface_id: "i1",
        pms_interface_label: "Main PMS", is_default: true, routing_mode: "MAPPED" }],
      unmapped: [{ guest_network_id: "gn2", guest_network_name: "Conference VLAN 20" }],
    });
    const Page = (await import("@/app/(app)/pms-routing/page")).default;
    render(<Page />);

    expect(await screen.findByText("Guest VLAN 10")).toBeTruthy();
    expect(screen.getByText("Main PMS")).toBeTruthy();
    // the point of the page: an absence is invisible in a list of what exists
    expect(await screen.findByText("Conference VLAN 20")).toBeTruthy();
    expect(screen.getByText(/will not be recognised/i)).toBeTruthy();
  });

  // THE PAGE CAN NOW SET THE MAPPING, AND THE PREVIOUS DECISION IS REVERSED ON PURPOSE.
  //
  // It used to be read-only, on the reasoning that which PMS a VLAN resolves against follows the network
  // topology and should therefore be changed "where the networks are configured". The reasoning is sound; the
  // problem is that it was not true. The guest-network API has no PMS field, so the conclusion in practice was
  // that the mapping could be set NOWHERE in the product -- it existed only as a row written by integration-test
  // fixtures, which is how a deployment reached "PMS connected, stays ingested, nothing authenticates" with no
  // product action available to fix it. edged has carried PUT/DELETE on this resource since; this is the screen
  // finally using them.
  //
  // WRITE is site_admin only, matching edged's rolePerms, and the test below pins that the controls are HIDDEN
  // for a reader rather than offered and refused.
  it("lets a site admin point a network at a PMS", async () => {
    routingMock({ unmapped: [{ guest_network_id: "gn2", guest_network_name: "Conference VLAN 20" }] });
    put.mockResolvedValue({});
    const Page = (await import("@/app/(app)/pms-routing/page")).default;
    render(<Page />);

    await userEvent.click(await screen.findByRole("button", { name: /point at a pms/i }));
    await userEvent.click(await screen.findByRole("button", { name: /save mapping/i }));

    await waitFor(() => expect(put).toHaveBeenCalled());
    expect(put.mock.calls[0][0]).toBe("/pms-routing/gn2");
    expect(put.mock.calls[0][1]).toEqual({ pms_interface_id: "i1", routing_mode: "MAPPED" });
  });

  it("offers no way to change the mapping to a role that may only read it", async () => {
    routingMock({
      roles: ["front_office_operator"],
      routes: [{ guest_network_id: "gn1", guest_network_name: "Guest VLAN 10", pms_interface_id: "i1",
        pms_interface_label: "Main PMS", is_default: false, routing_mode: "MAPPED" }],
      unmapped: [{ guest_network_id: "gn2", guest_network_name: "Conference VLAN 20" }],
    });
    const Page = (await import("@/app/(app)/pms-routing/page")).default;
    render(<Page />);
    await screen.findByText("Guest VLAN 10");

    expect(screen.queryByRole("button", { name: /change/i })).toBeNull();
    expect(screen.queryByRole("button", { name: /remove/i })).toBeNull();
    expect(screen.queryByRole("button", { name: /point at a pms/i })).toBeNull();
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
    expect(screen.getByText("high")).toBeTruthy();
    // a conflict rendered as two UUIDs is a conflict nobody resolves
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
      meta: { has_more: false },
    });
    const Page = (await import("@/app/(app)/pms-resolutions/page")).default;
    const { container } = render(<Page />);

    // The split is stated as a figure plus a proportion now, rather than as one "1 of 3 verified" string.
    expect(await screen.findByText("Checks recorded")).toBeTruthy();
    expect(screen.getAllByText("Let online").length).toBeGreaterThan(0);
    // a hundred NO_MATCH rows is a different problem from a hundred INDETERMINATE ones, and the difference
    // is invisible while scrolling, so the outcome breakdown carries its own counts
    expect(screen.getAllByText("2").length).toBeGreaterThan(0);
    expect(screen.getAllByText(/ambiguous discriminator required/i).length).toBeGreaterThan(0);
    // And the DATA carries no guest identity — a resolution list that named rooms or reservations would be a
    // way to enumerate who is staying at the property. The check is scoped to the table because the page's
    // own explanatory copy legitimately uses the word "room" to say that no room is named.
    const table = container.querySelector("table")!.innerHTML.toLowerCase();
    for (const forbidden of ["room", "reservation", "guest_name", "stay_id", "folio"]) {
      expect(table).not.toContain(forbidden);
    }
  });
});
