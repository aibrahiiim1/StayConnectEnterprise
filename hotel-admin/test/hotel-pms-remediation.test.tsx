import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

// jsdom has no ResizeObserver; the dialog's scroll area asks for one.
(globalThis as any).ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} };

const get = vi.fn();
const post = vi.fn();
const put = vi.fn();
vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return {
    ...actual,
    api: {
      get: (...a: any[]) => get(...a),
      post: (...a: any[]) => post(...a),
      put: (...a: any[]) => put(...a),
    },
  };
});

beforeEach(() => {
  get.mockReset();
  post.mockReset();
  put.mockReset();
});
afterEach(() => vi.resetModules());

const REVIEW_EVENT = {
  id: "11111111-1111-4111-8111-111111111111", pms_interface_id: "i1", external_event_identity: "X-1",
  event_type: "GC", processing_status: "MANUAL_REVIEW", review_code: "GC_UNKNOWN_STAY",
  received_at: new Date().toISOString(), decided: false, message_reservation: "R55",
};

function eventsBackend(roles: string[]) {
  get.mockImplementation((path: string) => {
    if (path === "/auth/whoami") return Promise.resolve({ roles });
    if (path === "/pms-event-decisions/settings") {
      return Promise.resolve({ enabled: false, expire_after_days: 30, config_version: 0, is_default: true,
        bounds: { min_days: 1, max_days: 365 } });
    }
    if (path.includes("/candidates")) return Promise.resolve({ data: [] });
    return Promise.resolve({
      data: [REVIEW_EVENT], meta: { has_more: false }, page: 1, page_size: 50, total: 1,
      summary: { applied: 5, pending: 0, manual_review: 13717, undecided: 1, rejected: 0, unmatched: 1 },
    });
  });
}

describe("PMS activity decisions (H-15)", () => {
  it("counts only undecided messages and lets the IT manager acknowledge one under the password", async () => {
    eventsBackend(["hotel_it_manager"]);
    post.mockResolvedValue({ decision: "ACKNOWLEDGE" });
    const { default: Page } = await import("@/app/(app)/stay-events/page");
    render(<Page />);
    expect(await screen.findByText("1 message need a decision")).toBeTruthy();
    expect(screen.queryByText("13,717")).toBeNull();
    expect(await screen.findByText(/automatic expiry of waiting messages/i)).toBeTruthy();

    await userEvent.click((await screen.findAllByRole("button", { name: /^Details/ }))[0]);
    await userEvent.click(await screen.findByRole("button", { name: "Acknowledge" }));
    const dialog = await screen.findByRole("dialog", { name: /acknowledge this message/i });
    await userEvent.type(within(dialog).getByLabelText(/^Reason/), "vendor test message");
    await userEvent.type(within(dialog).getByLabelText(/confirm your password/i), "pw");
    await userEvent.click(within(dialog).getByRole("button", { name: "Acknowledge" }));
    await waitFor(() => expect(post).toHaveBeenCalledWith(
      `/pms-event-decisions/${REVIEW_EVENT.id}/acknowledge`, { reason: "vendor test message", password: "pw" }));
  });

  it("asks for the undecided list when 'Needs a decision' is chosen", async () => {
    eventsBackend(["hotel_it_manager"]);
    const { default: Page } = await import("@/app/(app)/stay-events/page");
    render(<Page />);
    await userEvent.click(await screen.findByRole("button", { name: "Show only those" }));
    await waitFor(() => expect(get.mock.calls.some(([p]) => String(p).includes("undecided=true"))).toBe(true));
  });

  it("offers no decision to a desk role", async () => {
    eventsBackend(["front_office_operator"]);
    const { default: Page } = await import("@/app/(app)/stay-events/page");
    render(<Page />);
    await userEvent.click((await screen.findAllByRole("button", { name: /^Details/ }))[0]);
    await screen.findByRole("dialog");
    expect(screen.queryByRole("button", { name: "Acknowledge" })).toBeNull();
    expect(screen.queryByText(/automatic expiry of waiting messages/i)).toBeNull();
  });
});

describe("Stays (H-14, H-7)", () => {
  it("is at the limit when the devices holding a place reach it, even with nobody online", async () => {
    get.mockImplementation((path: string) => {
      if (path === "/auth/whoami") return Promise.resolve({ roles: ["site_viewer"] });
      return Promise.resolve({
        data: [{
          id: "s1", pms_interface_id: "i1", external_reservation_id: "", room: "412", status: "IN_HOUSE",
          lifecycle_version: 1, posting_allowed: true, occupants: 1, identity_hidden: true,
          access_status: "ACTIVE", access_package_code: "DAY", access_max_devices: 2,
          access_active_devices: 0, access_authorized_devices: 2,
        }],
        meta: { has_more: false }, page: 1, page_size: 50,
        summary: { total: 1, with_internet: 1, devices_online: 0, arriving: 0, vip: 0 },
      });
    });
    const { default: StaysPage } = await import("@/app/(app)/stays/page");
    render(<StaysPage />);
    expect(await screen.findByText(/at the limit/)).toBeTruthy();
    expect(screen.getByText("Not shown to your role")).toBeTruthy();
  });
});

describe("PMS routing (H-16)", () => {
  // A route saved by an older version as "every active PMS" always resolved against the one PMS it was mapped to. It is shown as exactly that, and any change is an
  // explicit, previewed choice of named interfaces: "every active PMS" is not something the screen can send.
  it("shows a legacy every-active-PMS row as the one PMS it resolved against, and saves only an explicit choice", async () => {
    get.mockImplementation((path: string) => {
      if (path === "/auth/whoami") return Promise.resolve({ roles: ["site_admin"] });
      if (path === "/pms-routing/readiness") return Promise.resolve({ generated_at: "x", overall: "READY", networks: [], interfaces: [], issues: [] });
      if (path === "/pms-routing") {
        return Promise.resolve({
          routes: [], unmapped_guest_networks: [],
          policies: [{ network_id: "g1", name: "Guest", enabled: true, archived: false, is_default: false, mode: "DEDICATED", fingerprint: "fp1",
            interfaces: [{ pms_interface_id: "i1", label: "Protel", lifecycle_state: "ACTIVE", published: true }] }],
        });
      }
      if (path.startsWith("/pms-interfaces")) {
        return Promise.resolve({ interfaces: [{ id: "i1", display_label: "Protel", connector_kind: "protel-fias",
          lifecycle_state: "ACTIVE", published: true, current_revision_id: "r1" }] });
      }
      return Promise.resolve({});
    });
    post.mockResolvedValue({
      proposal_id: "pr1", network: { network_id: "g1", name: "Guest", mode: "DEDICATED", interfaces: [] },
      proposed: { mode: "DEDICATED", interfaces: [] }, added: [], removed: [], blockers: [], warnings: [],
      impact: { active_sessions: 0, live_room_entitlements: 0, room_numbers_in_more_than_one_selected_pms: 0, sign_in_methods_switched_off: [], network_serves_clients: true },
      guarantees: [],
    });
    put.mockResolvedValue({});
    const { default: Page } = await import("@/app/(app)/pms-routing/page");
    render(<Page />);
    await userEvent.click(await screen.findByRole("button", { name: /^Change\b/ }));
    await userEvent.click(screen.getByRole("button", { name: /preview the change/i }));
    await userEvent.click(await screen.findByRole("button", { name: /confirm and apply/i }));
    await waitFor(() => expect(put).toHaveBeenCalledWith("/pms-routing/g1",
      { mode: "DEDICATED", pms_interface_ids: ["i1"], proposal_id: "pr1" }));
    expect(JSON.stringify(put.mock.calls)).not.toContain("ALL_ACTIVE");
  });
});

describe("Protel configuration follows the server (H-3)", () => {
  it("uses the catalogue's fields and carries max_auth_cache_age_seconds and the currency through an edit", async () => {
    const m = await import("@/lib/api/pms-connections");
    const server = {
      ...m.PROTEL_FALLBACK,
      fields: [
        ...m.PROTEL_FIELDS.filter((f) => f.key !== "max_auth_cache_age_seconds"),
        { key: "max_auth_cache_age_seconds", label: "Offline sign-in window", type: "int", min: 0, max: 604800 },
        { key: "source_timezone", label: "Property time zone", type: "string", required: true },
      ],
    } as any;
    const fields = m.fieldsFor(server);
    expect(fields.some((f) => f.key === "max_auth_cache_age_seconds")).toBe(true);
    expect(fields.some((f) => f.key === "source_timezone")).toBe(false);
    const { values, timezone } = m.valuesFromRevision(server, {
      source_timezone: "Europe/Berlin",
      financial_base_currency: "EUR", financial_base_currency_exponent: 2,
      config: { endpoint: "10.0.0.1:5010", dial_timeout_ms: 5000, read_timeout_ms: 15000, write_timeout_ms: 15000,
        heartbeat_interval_ms: 30000, heartbeat_timeout_ms: 90000, feed_freshness_ms: 120000,
        complete_sync_ms: 600000, max_auth_cache_age_seconds: 259200 },
    } as any);
    const body = m.providerRevisionBody(server, values, timezone);
    expect(body.max_auth_cache_age_seconds).toBe(259200);
    expect(body.financial_base_currency).toBe("EUR");
    expect(body.financial_base_currency_exponent).toBe(2);
    expect(body.source_timezone).toBe("Europe/Berlin");
  });
});

describe("Room-charge posting notice (D-E)", () => {
  it("is shown when room charge is effective and hidden otherwise", async () => {
    const caps = await import("@/lib/capabilities");
    const { RoomChargeLiveNotice } = await import("@/components/room-charge-live-notice");
    get.mockImplementation((path: string) =>
      path === "/capabilities"
        ? Promise.resolve({ surfaces: [], modules: { room_charge: { deployed: true, licensed: true, enabled: true, ready: true, effective: true, manageable: true } } })
        : Promise.resolve({}));
    caps.invalidateCapabilities();
    const { unmount } = render(<RoomChargeLiveNotice />);
    expect(await screen.findByText("Room-charge posting is live on this appliance")).toBeTruthy();
    unmount();

    vi.resetModules();
    const caps2 = await import("@/lib/capabilities");
    const { RoomChargeLiveNotice: Notice2 } = await import("@/components/room-charge-live-notice");
    get.mockImplementation((path: string) =>
      path === "/capabilities"
        ? Promise.resolve({ surfaces: [], modules: { room_charge: { deployed: true, licensed: true, enabled: true, ready: false, effective: false, manageable: true } } })
        : Promise.resolve({}));
    caps2.invalidateCapabilities();
    render(<Notice2 />);
    await waitFor(() => expect(get).toHaveBeenCalled());
    await new Promise((r) => setTimeout(r, 20));
    expect(screen.queryByText("Room-charge posting is live on this appliance")).toBeNull();
  });
});
