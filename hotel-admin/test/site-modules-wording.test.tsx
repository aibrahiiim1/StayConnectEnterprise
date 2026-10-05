import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, cleanup, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { fakeAudit } from "./fake-paged";

// A SITE DOES NOT LOOK LIKE A HOTEL UNLESS HOSPITALITY IS AVAILABLE, does not look like it takes cards unless
// Card payment is, and does not offer an optional sign-in method unless its identity module is (Product Owner
// rule). What these assert is mostly ABSENCE -- the room filter, the PMS reassurance, the payment-provider
// bullet -- on a site without the module, and presence with it, because a page that always hides them would
// pass the first half just as happily.
//
// History stays reachable: a filter over records follows moduleHasHistory (manageable), and a row of a kind
// that exists is never left without its filter.

class ResizeObserverStub { observe() {} unobserve() {} disconnect() {} }
(globalThis as any).ResizeObserver = (globalThis as any).ResizeObserver ?? ResizeObserverStub;

const get = vi.fn();
vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return { ...actual, api: { get: (...a: any[]) => get(...a), post: vi.fn(), put: vi.fn(), del: vi.fn(), patch: vi.fn() } };
});
vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn(), replace: vi.fn() }),
  useSearchParams: () => new URLSearchParams(""),
  usePathname: () => "/",
}));

// File-scoped, as in nav.test.tsx: the holder chooses, the real moduleLicensed / moduleHasHistory decide.
const ALL = ["hospitality", "email_otp", "sms_otp", "whatsapp_otp", "social_login", "card_payment", "room_charge"];
const MODS: { licensed: string[]; history: string[] } = { licensed: [], history: [] };
vi.mock("@/lib/capabilities", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/capabilities")>();
  return {
    ...actual,
    useCapabilities: () => ({
      surfaces: [],
      modules: Object.fromEntries(ALL.map((m) => {
        const on = MODS.licensed.includes(m);
        return [m, { deployed: on, licensed: on, enabled: on, ready: on, effective: on,
          manageable: on || MODS.history.includes(m) }];
      })),
    }),
  };
});

const pathOf = (raw: unknown) => (typeof raw === "string" ? raw : "");
function routes(map: Record<string, unknown>) {
  get.mockImplementation((raw: unknown) => {
    const path = pathOf(raw);
    if (path === "/auth/whoami") return Promise.resolve({ roles: ["site_admin"], operator_id: "me" });
    for (const [prefix, body] of Object.entries(map)) {
      if (path === prefix || path.startsWith(prefix + "?") || path.startsWith(prefix + "/")) {
        return Promise.resolve(typeof body === "function" ? body(path) : body);
      }
    }
    return Promise.resolve({ data: [], meta: { has_more: false } });
  });
}

const NONE = () => { MODS.licensed = []; MODS.history = []; };
const EVERYTHING = () => { MODS.licensed = [...ALL]; MODS.history = []; };

beforeEach(() => { get.mockReset(); NONE(); });
afterEach(() => { cleanup(); vi.resetModules(); });

const options = (label: RegExp) =>
  Array.from((screen.getByLabelText(label) as HTMLSelectElement).options).map((o) => o.textContent ?? "");

// ------------------------------------------------------------------------------------------------ sessions
const VOUCHER_SESSION = {
  id: "s1", tenant_id: "t", site_id: "s", appliance_id: "a", guest_id: "g", ip: "10.0.0.5", mac: "02:00:00:00:00:05",
  state: "active", started_at: new Date().toISOString(), bytes_down: 1, bytes_up: 1, subject_kind: "voucher",
};

describe("Active sessions", () => {
  it("offers no room or email/social filter, and no room search, on a site without those modules", async () => {
    routes({ "/sessions": { data: [VOUCHER_SESSION], meta: { has_more: false } } });
    const { default: Page } = await import("@/app/(app)/sessions/page");
    render(<Page />);
    await screen.findAllByText(/Voucher/);
    const opts = options(/Filter by how the client signed in/);
    expect(opts.some((o) => o.startsWith("Room"))).toBe(false);
    expect(opts.some((o) => o.startsWith("Email / social"))).toBe(false);
    expect(screen.getByPlaceholderText("Name, username, IP or MAC…")).toBeTruthy();
  });

  it("offers them where hospitality has history and an identity module is licensed", async () => {
    MODS.history = ["hospitality"]; MODS.licensed = ["social_login"];
    routes({ "/sessions": { data: [VOUCHER_SESSION], meta: { has_more: false } } });
    const { default: Page } = await import("@/app/(app)/sessions/page");
    render(<Page />);
    await screen.findAllByText(/Voucher/);
    const opts = options(/Filter by how the client signed in/);
    expect(opts).toContain("Room (0)");
    expect(opts).toContain("Email / social (0)");
    expect(screen.getByPlaceholderText("Room, name, username, IP or MAC…")).toBeTruthy();
  });

  it("keeps the room filter when a room session is in the list, whatever the licence says", async () => {
    routes({ "/sessions": { data: [{ ...VOUCHER_SESSION, subject_kind: "room", room: "318" }], meta: { has_more: false }, total: 1,
      summary: { devices_online: 1, clients_online: 1, rooms_online: 1, bytes_total: 2, kinds: { room: 1 } } } });
    const { default: Page } = await import("@/app/(app)/sessions/page");
    render(<Page />);
    await screen.findByText("Room 318");
    expect(options(/Filter by how the client signed in/)).toContain("Room (1)");
  });
});

// ------------------------------------------------------------------------------------------ usage explorer
describe("Usage explorer", () => {
  it("has no Hotel room/stay type and no room search without hospitality history", async () => {
    routes({ "/usage/sources": { data: [] } });
    const { default: Page } = await import("@/app/(app)/usage/page");
    render(<Page />);
    const chips = await screen.findByRole("radiogroup", { name: "Access source type" });
    expect(within(chips).queryByRole("radio", { name: /Hotel room\/stay/ })).toBeNull();
    expect(screen.getByLabelText("Username or card reference")).toBeTruthy();
    expect(screen.queryByPlaceholderText(/room/i)).toBeNull();
  });

  it("keeps Hotel room/stay while hospitality has history, licensed or not", async () => {
    MODS.history = ["hospitality"];
    routes({ "/usage/sources": { data: [] } });
    const { default: Page } = await import("@/app/(app)/usage/page");
    render(<Page />);
    const chips = await screen.findByRole("radiogroup", { name: "Access source type" });
    expect(within(chips).getByRole("radio", { name: /Hotel room\/stay/ })).toBeTruthy();
    expect(screen.getByLabelText("Username, card reference, room number or reservation")).toBeTruthy();
  });
});

// -------------------------------------------------------------------------------------------- activity log
const HOTEL_ENTRY = fakeAudit([{ ts: "2026-09-20T10:00:00Z", actor_type: "system", action: "pms_interface.created" }]);

describe("Activity", () => {
  it("has no Hotel category without hospitality history", async () => {
    routes({ "/audit": fakeAudit([{ ts: "2026-09-20T10:00:00Z", actor_type: "system", action: "health.recheck" }]) });
    const { default: Page } = await import("@/app/(app)/audit/page");
    render(<Page />);
    await screen.findByText("1 entry");
    const chips = screen.getByRole("radiogroup", { name: "Show" });
    expect(within(chips).queryByRole("radio", { name: /^Hotel/ })).toBeNull();
    expect(within(chips).getByRole("radio", { name: /^Networks/ })).toBeTruthy();
  });

  it("offers Hotel where hospitality has history, and wherever a Hotel entry is on screen", async () => {
    MODS.history = ["hospitality"];
    routes({ "/audit": fakeAudit([]) });
    const { default: Page } = await import("@/app/(app)/audit/page");
    const first = render(<Page />);
    expect(within(await screen.findByRole("radiogroup", { name: "Show" })).getByRole("radio", { name: /^Hotel/ })).toBeTruthy();
    first.unmount();

    NONE();
    routes({ "/audit": HOTEL_ENTRY });
    render(<Page />);
    await screen.findByText("1 entry");
    expect(within(screen.getByRole("radiogroup", { name: "Show" })).getByRole("radio", { name: /^Hotel/ })).toBeTruthy();
  });
});

// -------------------------------------------------------------------------------------------- allowed sites
describe("Allowed sites", () => {
  async function tips() {
    routes({ "/walled-garden": { data: [] } });
    const { default: Page } = await import("@/app/(app)/walled-garden/page");
    render(<Page />);
    await userEvent.click(screen.getByRole("button", { name: "Tips: Allowed sites" }));
    return screen.findByRole("dialog");
  }

  it("mentions no payment or identity provider without Card payment or Social sign-in", async () => {
    const sheet = await tips();
    expect(within(sheet).getByText(/a captive-portal check/)).toBeTruthy();
    expect(within(sheet).queryByText(/payment provider/)).toBeNull();
    expect(within(sheet).queryByText(/identity provider/)).toBeNull();
  });

  it("mentions each where its module is licensed", async () => {
    EVERYTHING();
    const sheet = await tips();
    expect(within(sheet).getByText(/a payment provider/)).toBeTruthy();
    expect(within(sheet).getByText(/an identity provider, for social login/)).toBeTruthy();
  });
});

// ------------------------------------------------------------------------------------------- client accounts
describe("Client accounts", () => {
  async function tips() {
    routes({ "/guest-accounts": { data: [], meta: { has_more: false } } });
    const { default: Page } = await import("@/app/(app)/guest-accounts/page");
    render(<Page />);
    await userEvent.click(screen.getByRole("button", { name: "Tips: Client accounts" }));
    return screen.findByRole("dialog");
  }

  it("is an alternative to a voucher, not a room number, without hospitality", async () => {
    const sheet = await tips();
    expect(within(sheet).getByText(/instead of a voucher\./)).toBeTruthy();
    expect(sheet.textContent).not.toMatch(/room/i);
  });

  it("mentions the room number where hospitality is licensed", async () => {
    MODS.licensed = ["hospitality"];
    const sheet = await tips();
    expect(within(sheet).getByText(/instead of a room number or a voucher\./)).toBeTruthy();
  });
});

// ------------------------------------------------------------------------------------------------ diagnostics
describe("Diagnostics", () => {
  const PG = {
    service: "postgres", state: "healthy", process_state: "running", health_ok: true, health_detail: "ok",
    consecutive_failures: 0, restart_count: 0, restarts_in_window: 0, restart_window_secs: 300, backoff_level: 0,
    backoff_ms: 0, next_retry_at: null, first_failure_at: null, last_failure_at: null, last_failure_reason: "",
    last_exit_code: null, last_exit_signal: "", last_healthy_at: new Date().toISOString(), last_recovery_at: null,
    time_since_healthy_s: 0, degraded_dependency: "", critical: true, updated_at: new Date().toISOString(),
  };
  async function restartDialog() {
    routes({ "/diagnostics/services": { overall: "healthy", counts: { healthy: 1 }, services: [PG], generated_at: new Date().toISOString() } });
    const { default: Page } = await import("@/app/(app)/health/page");
    render(<Page />);
    await userEvent.click(await screen.findByRole("button", { name: "Restart postgres" }));
    return screen.findByRole("dialog");
  }

  it("does not name a PMS connection as a casualty without hospitality", async () => {
    const dialog = await restartDialog();
    expect(within(dialog).getByText(/client sign-in and this admin both depend on the database/)).toBeTruthy();
    expect(dialog.textContent).not.toMatch(/PMS/);
  });

  it("names it where hospitality is licensed", async () => {
    MODS.licensed = ["hospitality"];
    const dialog = await restartDialog();
    expect(within(dialog).getByText(/this admin and the PMS connection all depend on the database/)).toBeTruthy();
  });
});

// ---------------------------------------------------------------------------------- internet package activity
describe("Client activity on Internet packages", () => {
  const VOUCHER_ROW = {
    purchase_id: "pu2", package_id: "pk2", package_code: "PREMIUM", package_name: "Premium", package_revision_id: "r9",
    revision_no: 1, price_minor: 0, currency: "USD", currency_exponent: 2, source: "VOUCHER_REDEMPTION",
    source_label: "Voucher", purchase_state: "GRANTED", status: "ACTIVE", sign_in_kind: "VOUCHER", had_offer: false,
    started_at: "2026-09-18T08:00:00Z", occurred_at: "2026-09-18T08:00:00Z", sessions: 1, devices: 1,
    bytes_down: 1, bytes_up: 1, online_now: false,
  };
  const activity = (data: unknown[]) => ({
    data, meta: { total: data.length, limit: 25, offset: 0, has_more: false },
    summary: { in_range: 0, started_in_range: 0, status_counts: { active: 0, ended: 0, other: 0 }, data_bytes: 0,
      active_now: 0, undated: 0, by_package: [], by_source: [], active_by_package: [] },
    range: { from: "2026-09-17T00:00:00Z", to: "2026-09-24T00:00:00Z" },
  });
  async function renderTab(rows: unknown[] = [VOUCHER_ROW]) {
    routes({ "/commercial-packages/activity": activity(rows) });
    const { ActivityTab } = await import("@/app/(app)/internet-packages/activity-tab");
    render(<ActivityTab guard={() => false} setErr={() => {}} />);
    await screen.findByRole("button", { name: "Premium" });
  }

  it("lists no hospitality or identity way of giving access, and no room search, without those modules", async () => {
    await renderTab();
    const ways = options(/How it was given/);
    for (const w of ["Grace Period", "Emergency grace", "After-stay access", "Moved between PMS connections",
      "Email, phone or social sign-in"]) expect(ways).not.toContain(w);
    expect(ways).toContain("Voucher");
    expect(screen.queryByPlaceholderText("Room or reservation")).toBeNull();
  });

  it("lists them where hospitality has history and an identity module is licensed", async () => {
    MODS.history = ["hospitality"]; MODS.licensed = ["email_otp"];
    await renderTab();
    const ways = options(/How it was given/);
    for (const w of ["Grace Period", "Emergency grace", "After-stay access", "Moved between PMS connections",
      "Email, phone or social sign-in"]) expect(ways).toContain(w);
    expect(screen.getByPlaceholderText("Room or reservation")).toBeTruthy();
  });

  it("shows no Room or Reservation rows on a grant that has no room", async () => {
    await renderTab();
    await userEvent.click(screen.getByRole("button", { name: "Premium" }));
    const sheet = await screen.findByRole("dialog");
    expect(within(sheet).queryByText("Room")).toBeNull();
    expect(within(sheet).queryByText("Reservation")).toBeNull();
  });
});

// ------------------------------------------------------------------------------------------- portal preview
describe("the portal preview's sign-in methods", () => {
  const DOC = {
    voucher: { enabled: true }, guest_account: { enabled: false }, open: { enabled: true },
    pms: { enabled: true, mode: "room_lastname" }, email: { enabled: true }, sms: { enabled: true },
    whatsapp: { enabled: true }, social: { google: { enabled: true }, apple: { enabled: false } },
    phase5_poststay: true, internet_packages_available: false,
  };
  const capsWith = (on: string[]) => ({
    surfaces: [],
    modules: Object.fromEntries(ALL.map((m) => [m, { deployed: on.includes(m), licensed: on.includes(m), enabled: true, ready: true, effective: on.includes(m), manageable: on.includes(m) }])),
  });

  it("keeps only what is switched on AND licensed, in the shape the portal reads", async () => {
    const { previewMethods, optionalMethodsShown } = await import("@/app/(app)/portal-branding/preview");
    const bare = previewMethods(DOC, capsWith([]));
    expect(bare).toEqual({ voucher: { enabled: true }, open: { enabled: true }, internet_packages_available: false });
    expect(optionalMethodsShown(bare)).toEqual([]);

    const full = previewMethods(DOC, capsWith(ALL));
    expect(full.pms).toEqual({ enabled: true, mode: "room_lastname" });
    expect(full.phase5_poststay).toBe(true);
    expect(full.social).toEqual({ google: { enabled: true } });
    expect(optionalMethodsShown(full)).toEqual(["Room", "Email", "Phone", "WhatsApp", "Social"]);
  });

  it("offers nothing it could not read, and defaults package availability to available", async () => {
    const { previewMethods } = await import("@/app/(app)/portal-branding/preview");
    expect(previewMethods(null, capsWith(ALL))).toEqual({ internet_packages_available: true });
    expect(previewMethods({ voucher: { enabled: true } }, null)).toEqual({ voucher: { enabled: true }, internet_packages_available: true });
  });
});
