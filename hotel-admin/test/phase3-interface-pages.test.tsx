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
vi.mock("@/lib/api", () => ({
  api: {
    get: (...a: any[]) => get(...a),
    post: (...a: any[]) => post(...a),
    put: (...a: any[]) => put(...a),
  },
}));

beforeEach(() => {
  get.mockReset();
  post.mockReset();
  put.mockReset();
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
  transport_status: "CONNECTED",
  continuity_status: "CONTINUOUS",
  sync_status: "IN_SYNC",
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
    await userEvent.click(screen.getByRole("button", { name: "Open" }));

    // revision 1 is published even though revision 2 exists and is newer
    const published = await screen.findByText("published");
    const row = published.closest("tr")!;
    expect(within(row).getByText(/#1/)).toBeTruthy();
  });

  it("states plainly when an interface has nothing published", async () => {
    mockInterfacePage({
      iface: { ...iface, published: false, current_revision_id: undefined, current_revision_no: null },
    });
    const Page = (await import("@/app/(app)/pms-interfaces/page")).default;
    render(<Page />);
    // an empty cell would read as "not loaded yet"; an interface with nothing published resolves nothing
    expect(await screen.findByText("nothing published")).toBeTruthy();
  });

  it("shows the four health dimensions separately and the age of the backlog", async () => {
    mockInterfacePage();
    const Page = (await import("@/app/(app)/pms-interfaces/page")).default;
    render(<Page />);
    await screen.findByText("Main PMS");
    await userEvent.click(screen.getByRole("button", { name: "Open" }));

    await screen.findByText("Health");
    // separate, because they fail separately and each has a different response
    expect(screen.getByText("Transport")).toBeTruthy();
    expect(screen.getByText("Continuity")).toBeTruthy();
    expect(screen.getByText("Synchronization")).toBeTruthy();
    expect(screen.getByText(/12 stays in house/)).toBeTruthy();
    // the age of the oldest waiting event is what separates a busy morning from a stuck processor
    expect(screen.getByText(/oldest waiting since/)).toBeTruthy();
  });

  it("publishes with the revision the operator believed was live, a reason and a password", async () => {
    mockInterfacePage();
    post.mockResolvedValue({ current_revision_id: "r2", revision_no: 2 });
    const Page = (await import("@/app/(app)/pms-interfaces/page")).default;
    render(<Page />);
    await screen.findByText("Main PMS");
    await userEvent.click(screen.getByRole("button", { name: "Open" }));

    await screen.findByText("Revisions");
    await userEvent.click(screen.getByRole("button", { name: "Publish" }));
    await userEvent.type(screen.getByLabelText(/Reason/), "CONFIG_UPDATE");
    await userEvent.type(screen.getByLabelText(/Confirm your password/), "pw");
    await userEvent.click(screen.getByRole("button", { name: "Publish revision" }));

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
    await userEvent.click(screen.getByRole("button", { name: "Open" }));
    await screen.findByText("Revisions");
    await userEvent.click(screen.getByRole("button", { name: "Publish" }));
    await userEvent.type(screen.getByLabelText(/Reason/), "CONFIG_UPDATE");
    await userEvent.type(screen.getByLabelText(/Confirm your password/), "pw");
    await userEvent.click(screen.getByRole("button", { name: "Publish revision" }));

    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toMatch(/another operator published/);
  });

  it("never renders a credential and never asks the server for one", async () => {
    mockInterfacePage();
    post.mockResolvedValue({ generation_no: 4 });
    const Page = (await import("@/app/(app)/pms-interfaces/page")).default;
    const { container } = render(<Page />);
    await screen.findByText("Main PMS");
    await userEvent.click(screen.getByRole("button", { name: "Open" }));

    await screen.findByRole("heading", { name: "Credential" });
    // it names the generation, which is what "did my rotation take effect?" needs — and nothing else
    expect(screen.getByText(/Currently using generation 3/)).toBeTruthy();

    await userEvent.click(screen.getByRole("button", { name: "Replace credential" }));
    const field = screen.getByLabelText(/New credential/) as HTMLInputElement;
    // a masked TEXT field would still put the value in the DOM; this must be a password input
    expect(field.type).toBe("password");
    await userEvent.type(field, "s3cr3t-value");
    await userEvent.type(screen.getByLabelText(/Reason/), "ROTATION");
    await userEvent.type(screen.getByLabelText(/Confirm your password/), "pw");
    await userEvent.click(screen.getByRole("button", { name: "Replace credential" }));

    await waitFor(() => expect(post).toHaveBeenCalled());
    const [path, body] = post.mock.calls[0];
    expect(path).toBe("/pms-interfaces/i1/secret");
    expect(body.reason_code).toBe("ROTATION");
    // the confirmation names the generation, never the value
    expect(await screen.findByRole("status")).toBeTruthy();
    expect(screen.getByRole("status").textContent).toMatch(/generation 4/);
    expect(container.innerHTML).not.toContain("s3cr3t-value");
    // and nothing on the page ever GETs a credential
    for (const [p] of get.mock.calls) expect(String(p)).not.toMatch(/secret/);
  });

  it("says when no guest network routes to the interface", async () => {
    mockInterfacePage({ routes: [] });
    const Page = (await import("@/app/(app)/pms-interfaces/page")).default;
    render(<Page />);
    await screen.findByText("Main PMS");
    await userEvent.click(screen.getByRole("button", { name: "Open" }));
    // configured but unreachable looks identical to healthy everywhere else on the page
    expect(await screen.findByText(/No guest network routes to this interface/)).toBeTruthy();
  });
});

describe("Guest network routing page", () => {
  it("names the networks that are mapped to nothing", async () => {
    get.mockResolvedValue({
      routes: [{ guest_network_id: "gn1", guest_network_name: "Guest VLAN 10", pms_interface_id: "i1",
        pms_interface_label: "Main PMS", is_default: true, routing_mode: "MAPPED" }],
      unmapped_guest_networks: [{ guest_network_id: "gn2", guest_network_name: "Conference VLAN 20" }],
    });
    const Page = (await import("@/app/(app)/pms-routing/page")).default;
    render(<Page />);

    expect(await screen.findByText("Guest VLAN 10")).toBeTruthy();
    expect(screen.getByText("Main PMS")).toBeTruthy();
    // the point of the page: an absence is invisible in a list of what exists
    expect(await screen.findByText("Conference VLAN 20")).toBeTruthy();
    expect(screen.getByText(/resolved against no\s+PMS interface/)).toBeTruthy();
  });

  it("does not offer any way to change the mapping", async () => {
    get.mockResolvedValue({ routes: [], unmapped_guest_networks: [] });
    const Page = (await import("@/app/(app)/pms-routing/page")).default;
    render(<Page />);
    await screen.findByText(/Every guest network is mapped/);
    // routing follows the network topology; editing it here would be editing it without that context
    expect(screen.queryAllByRole("button")).toHaveLength(0);
    expect(post).not.toHaveBeenCalled();
    expect(put).not.toHaveBeenCalled();
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

    expect(await screen.findByText("1 of 3 verified")).toBeTruthy();
    // a hundred NO_MATCH rows is a different problem from a hundred INDETERMINATE ones, and the difference
    // is invisible while scrolling
    expect(screen.getByText(/ambiguous discriminator required · 2/)).toBeTruthy();
    // And the DATA carries no guest identity — a resolution list that named rooms or reservations would be a
    // way to enumerate who is staying at the property. The check is scoped to the table because the page's
    // own explanatory copy legitimately uses the word "room" to say that no room is named.
    const table = container.querySelector("table")!.innerHTML.toLowerCase();
    for (const forbidden of ["room", "reservation", "guest_name", "stay_id", "folio"]) {
      expect(table).not.toContain(forbidden);
    }
  });
});
