import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

// Mock the edged API client. ApiError is a real class so the page's `e instanceof ApiError` guard works.
vi.mock("@/lib/api", () => {
  class ApiError extends Error {
    status: number;
    body: unknown;
    constructor(status: number, body?: unknown) {
      super(typeof body === "object" && body && "message" in body ? String((body as { message: unknown }).message)
        : typeof body === "object" && body && "error" in body ? String((body as { error: unknown }).error) : `HTTP ${status}`);
      this.status = status;
      this.body = body;
    }
  }
  return {
    ApiError,
    api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), del: vi.fn() },
  };
});

import { api, ApiError } from "@/lib/api";
import InternetPackagesPage from "@/app/(app)/internet-packages/page";

const g = api.get as unknown as ReturnType<typeof vi.fn>;
const p = api.post as unknown as ReturnType<typeof vi.fn>;

function list<T>(data: T[]) { return { data, meta: { has_more: false } }; }

const EMPTY_SUMMARY = {
  in_range: 0, started_in_range: 0, status_counts: { active: 0, ended: 0, other: 0 },
  data_bytes: 0, active_now: 0, undated: 0, by_package: [], by_source: [], active_by_package: [],
};
function activity(data: unknown[] = [], summary: Partial<typeof EMPTY_SUMMARY> = {}, total = data.length) {
  return {
    data, meta: { total, limit: 25, offset: 0, has_more: total > data.length },
    summary: { ...EMPTY_SUMMARY, ...summary }, range: { from: "2026-09-17T00:00:00Z", to: "2026-09-24T00:00:00Z" },
  };
}

const PKG = {
  package_id: "pk1", code: "FREEWIFI", name: "Free WiFi", active: true,
  current_revision_id: "r1", revision_count: 2,
  service_plan_id: "p1", service_plan_revision_id: "rev-gold", service_plan_code: "GOLD",
  down_kbps: 10000, up_kbps: 5000, data_quota_bytes: 100000000, max_concurrent_devices: 4,
  speed_allocation: "PER_DEVICE", price_minor: 0,
};
const PLAN = { plan_id: "p1", code: "GOLD", name: "Gold", enabled: true, current_revision_id: "rev-gold", revision_count: 1, down_kbps: 10000 };

/** Route the mocked GETs by path; anything not named answers an empty list. */
function routes(map: Record<string, unknown>) {
  g.mockImplementation((path: string) => {
    for (const [prefix, value] of Object.entries(map)) {
      if (path === prefix || (prefix.endsWith("*") && path.startsWith(prefix.slice(0, -1)))) {
        return value instanceof Error ? Promise.reject(value) : Promise.resolve(value);
      }
    }
    if (path.startsWith("/commercial-packages/activity")) return Promise.resolve(activity());
    // A role that may change packages, unless a test routes whoami itself.
    if (path === "/auth/whoami") return Promise.resolve({ roles: ["site_admin"] });
    return Promise.resolve(list([]));
  });
}

// Radix Tooltip (inside the support ids of a record sheet) measures with ResizeObserver, which jsdom lacks.
if (!("ResizeObserver" in globalThis)) {
  (globalThis as Record<string, unknown>).ResizeObserver = class { observe() {} unobserve() {} disconnect() {} };
}

beforeEach(() => { vi.clearAllMocks(); });

describe("InternetPackagesPage — packages", () => {
  it("renders the approved disabled state when the backend returns 503", async () => {
    g.mockRejectedValue(new ApiError(503, { error: "phase2_disabled" }));
    render(<InternetPackagesPage />);
    expect(await screen.findByText(/not switched on/i)).toBeInTheDocument();
    expect(screen.queryByRole("tab", { name: /guest activity/i })).toBeNull();
  });

  it("shows what each package GIVES, and Add asks for a service plan by name", async () => {
    routes({ "/commercial-packages": list([PKG]), "/commercial-packages/plans": list([PLAN]) });
    render(<InternetPackagesPage />);
    expect(await screen.findByText("Free WiFi")).toBeInTheDocument();
    expect(screen.getByText(/10 Mbps down/i)).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: /add package/i }));
    // The plan is chosen BY NAME. Its revision id never appears.
    const sel = (await screen.findByLabelText("service-plan")) as HTMLSelectElement;
    expect(Array.from(sel.options).map((o) => o.value)).toContain("p1");
    expect(Array.from(sel.options).map((o) => o.value)).not.toContain("rev-gold");
    expect(screen.queryByLabelText("down-mbps")).toBeNull();
    expect(document.body.innerHTML).not.toContain("rev-gold");
  });

  // THE PRICE BUG: "500 USD" was printed for a 5.00 USD package, because the list showed minor units raw.
  it("formats every price with the exponent it was published with", async () => {
    routes({
      "/commercial-packages": list([
        { ...PKG, package_id: "a", code: "USD5", name: "Five dollars", price_minor: 500, currency: "USD", currency_exponent: 2 },
        { ...PKG, package_id: "b", code: "JPY500", name: "Yen", price_minor: 500, currency: "JPY", currency_exponent: 0 },
        { ...PKG, package_id: "c", code: "BHD", name: "Dinar", price_minor: 1500, currency: "BHD", currency_exponent: 3 },
        { ...PKG, package_id: "d", code: "OLD", name: "No exponent", price_minor: 250, currency: "USD" },
      ]),
    });
    render(<InternetPackagesPage />);
    expect(await screen.findByText("5.00 USD")).toBeInTheDocument();
    expect(screen.queryByText("500 USD")).toBeNull();
    expect(screen.getByText("500 JPY")).toBeInTheDocument();
    expect(screen.getByText("1.500 BHD")).toBeInTheDocument();
    // An exponent the record does not carry is assumed out loud, never silently.
    expect(screen.getByText("2.50 USD (assumed 2 decimals)")).toBeInTheDocument();
  });

  it("opens the package record with its saved versions as a timeline", async () => {
    routes({
      "/commercial-packages": list([{ ...PKG, name: null, code: "FREEWIFI", price_minor: 500, currency: "USD", currency_exponent: 2 }]),
      "/commercial-packages/pk1/revisions": list([
        { revision_id: "r2", revision_no: 2, is_current: true, package_type: "GENERAL", price_minor: 500, currency: "USD", currency_exponent: 2 },
        { revision_id: "r1", revision_no: 1, is_current: false, package_type: "GENERAL", price_minor: 0, currency: "USD", currency_exponent: 2 },
      ]),
    });
    render(<InternetPackagesPage />);
    fireEvent.click(await screen.findByRole("button", { name: "FREEWIFI" }));
    const sheet = await screen.findByRole("dialog");
    expect(await within(sheet).findByText(/in force/i)).toBeInTheDocument();
    expect(within(sheet).getByText(/Version 1/)).toBeInTheDocument();
    // History prices are formatted too (they printed "500 USD" before).
    expect(within(sheet).getAllByText("5.00 USD").length).toBeGreaterThan(0);
    expect(within(sheet).queryByText("500 USD")).toBeNull();
  });

  it("shows how many guests are on each package now", async () => {
    routes({
      "/commercial-packages": list([PKG]),
      "/commercial-packages/activity*": activity([], { active_now: 3, active_by_package: [{ package_id: "pk1", grants: 3 }] }),
    });
    render(<InternetPackagesPage />);
    await screen.findByText("Free WiFi");
    const row = screen.getByText("Free WiFi").closest("tr")!;
    await waitFor(() => expect(within(row).getByText("3")).toBeInTheDocument());
  });

  // THE STEP-UP IS A DIALOG, NOT TWO window.prompt() CALLS IN A ROW. The backend contract is unchanged: the API
  // is called with active:false plus a reason and a password, and not at all until both are supplied.
  it("deactivation requires reason + password step-up before calling the API", async () => {
    routes({ "/commercial-packages": list([{ package_id: "pk1", code: "FREEWIFI", active: true, current_revision_id: "r1", revision_count: 1 }]) });
    p.mockResolvedValue({});
    render(<InternetPackagesPage />);
    await screen.findByText("FREEWIFI");
    fireEvent.click(screen.getByRole("button", { name: /^disable$/i }));

    expect(p).not.toHaveBeenCalled();
    fireEvent.change(await screen.findByLabelText(/why are you disabling it/i), { target: { value: "bad package" } });
    fireEvent.change(screen.getByLabelText(/confirm your password/i), { target: { value: "secretpw" } });
    fireEvent.click(screen.getByRole("button", { name: /stop offering it/i }));

    await waitFor(() => expect(p).toHaveBeenCalled());
    expect(p).toHaveBeenCalledWith("/commercial-packages/pk1/active", { active: false, reason: "bad package", password: "secretpw" });
  });

  it("aborts deactivation (no API call) if the operator cancels the step-up", async () => {
    routes({ "/commercial-packages": list([{ package_id: "pk1", code: "FREEWIFI", active: true, current_revision_id: "r1", revision_count: 1 }]) });
    p.mockResolvedValue({});
    render(<InternetPackagesPage />);
    await screen.findByText("FREEWIFI");
    fireEvent.click(screen.getByRole("button", { name: /^disable$/i }));
    fireEvent.click(await screen.findByRole("button", { name: /^cancel$/i }));
    await new Promise((r) => setTimeout(r, 10));
    expect(p).not.toHaveBeenCalled();
  });

  it("cannot confirm the disable until both the reason and the password are given", async () => {
    routes({ "/commercial-packages": list([{ package_id: "pk1", code: "FREEWIFI", active: true, current_revision_id: "r1", revision_count: 1 }]) });
    render(<InternetPackagesPage />);
    await screen.findByText("FREEWIFI");
    fireEvent.click(screen.getByRole("button", { name: /^disable$/i }));

    const confirm = await screen.findByRole("button", { name: /stop offering it/i });
    expect(confirm).toBeDisabled();
    fireEvent.change(screen.getByLabelText(/why are you disabling it/i), { target: { value: "superseded" } });
    expect(confirm).toBeDisabled();
    fireEvent.change(screen.getByLabelText(/confirm your password/i), { target: { value: "pw" } });
    expect(confirm).toBeEnabled();
  });

  it("a failed save shows an error and does not falsely report success", async () => {
    routes({ "/commercial-packages": list([]), "/commercial-packages/plans": list([PLAN]) });
    p.mockRejectedValue(new ApiError(400, { error: "invalid_grant_tier" }));
    render(<InternetPackagesPage />);
    fireEvent.click(await screen.findByRole("button", { name: /add package/i }));
    fireEvent.change(await screen.findByLabelText("code"), { target: { value: "X" } });
    fireEvent.change(screen.getByLabelText("service-plan"), { target: { value: "p1" } });
    fireEvent.click(screen.getByRole("button", { name: /^add package$/i }));
    expect(await screen.findByText(/invalid_grant_tier/i)).toBeInTheDocument();
    expect(screen.getByLabelText("code")).toBeInTheDocument();
  });

  // ADD NEVER BECOMES A SILENT REVISION of a package that already owns the code.
  it("Add sends create_only, and a taken code is shown as the server's refusal inside the form", async () => {
    routes({ "/commercial-packages": list([PKG]), "/commercial-packages/plans": list([PLAN]) });
    p.mockRejectedValue(new ApiError(409, {
      error: "code_exists", message: 'A package with the code "FREEWIFI" already exists. Open it and choose Edit to change it.',
    }));
    render(<InternetPackagesPage />);
    await screen.findByText("Free WiFi");
    fireEvent.click(screen.getByRole("button", { name: /add package/i }));
    fireEvent.change(await screen.findByLabelText("code"), { target: { value: "FREEWIFI" } });
    fireEvent.change(screen.getByLabelText("service-plan"), { target: { value: "p1" } });
    fireEvent.click(screen.getByRole("button", { name: /^add package$/i }));

    await waitFor(() => expect(p).toHaveBeenCalledTimes(1));
    expect(p.mock.calls[0][1].create_only).toBe(true);
    const dialog = screen.getByRole("dialog");
    expect(await within(dialog).findByText(/already exists/i)).toBeInTheDocument();
    expect(within(dialog).getByLabelText("code")).toBeInTheDocument();
  });

  it("Edit publishes a new version without create_only", async () => {
    routes({
      "/commercial-packages": list([PKG]), "/commercial-packages/plans": list([PLAN]),
      "/commercial-packages/pk1/current": {
        package_id: "pk1", code: "FREEWIFI", revision_id: "r1", revision_no: 1, service_plan_revision_id: "rev-gold",
        display: { name: "Free WiFi" }, duration_policy: { end_mode: "MANUAL_END" },
        eligibility_rules: [], grant_tiers: [{ order: 10, value: {} }],
      },
    });
    p.mockResolvedValue({});
    render(<InternetPackagesPage />);
    await screen.findByText("Free WiFi");
    fireEvent.click(screen.getByRole("button", { name: /^edit$/i }));
    fireEvent.click(await screen.findByRole("button", { name: /save changes/i }));
    await waitFor(() => expect(p).toHaveBeenCalledTimes(1));
    expect(p.mock.calls[0][0]).toBe("/commercial-packages");
    expect(p.mock.calls[0][1]).not.toHaveProperty("create_only");
  });

  // DELETE… REFUSES WITH THE SERVER'S REASONS when anything uses the package, and offers Disable.
  it("Delete… of a used package explains, with real counts, and offers Disable instead — it never deletes", async () => {
    routes({
      "/commercial-packages": list([PKG]),
      "/commercial-packages/pk1/deletability": {
        deletable: false,
        reasons: [
          { code: "ENTITLEMENTS", message: "2 internet grants given to guests record this package.", count: 2 },
          { code: "VOUCHERS", message: "3 vouchers were issued for this package.", count: 3 },
        ],
      },
    });
    render(<InternetPackagesPage />);
    fireEvent.click(await screen.findByRole("button", { name: "Free WiFi" }));
    fireEvent.click(await screen.findByRole("button", { name: /delete…/i }));

    expect(await screen.findByText("2 internet grants given to guests record this package.")).toBeInTheDocument();
    expect(screen.getByText("3 vouchers were issued for this package.")).toBeInTheDocument();
    expect(screen.getByText(/why it can't be deleted/i)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^delete package$/i })).toBeNull();
    expect(g).toHaveBeenCalledWith("/commercial-packages/pk1/deletability");

    fireEvent.click(screen.getByRole("button", { name: /disable instead/i }));
    expect(await screen.findByLabelText(/why are you disabling it/i)).toBeInTheDocument();
    expect(p).not.toHaveBeenCalled();
    expect((api as unknown as { del: ReturnType<typeof vi.fn> }).del).not.toHaveBeenCalled();
  });

  // AN UNUSED PACKAGE IS DELETED for real, with a reason and the operator's password.
  it("Delete… of an unused package asks for a reason and password, then deletes", async () => {
    const d = (api as unknown as { del: ReturnType<typeof vi.fn> }).del;
    d.mockResolvedValue({ deleted: true });
    routes({
      "/commercial-packages": list([PKG]),
      "/commercial-packages/pk1/deletability": { deletable: true, reasons: [] },
    });
    render(<InternetPackagesPage />);
    fireEvent.click(await screen.findByRole("button", { name: "Free WiFi" }));
    fireEvent.click(await screen.findByRole("button", { name: /delete…/i }));
    expect(await screen.findByText(/cannot be undone/i)).toBeInTheDocument();
    const go = screen.getByRole("button", { name: /^delete package$/i });
    expect(go).toBeDisabled();
    fireEvent.change(screen.getByLabelText(/why are you deleting it/i), { target: { value: "Created by mistake" } });
    fireEvent.change(screen.getByLabelText(/confirm your password/i), { target: { value: "pw" } });
    fireEvent.click(go);
    await waitFor(() => expect(d).toHaveBeenCalledWith("/commercial-packages/pk1", { reason: "Created by mistake", password: "pw" }));
  });

  // SOMETHING STARTED USING IT while the dialog was open: the server's 409 becomes the refusal.
  it("a 409 from the delete switches the dialog to the server's reasons", async () => {
    const d = (api as unknown as { del: ReturnType<typeof vi.fn> }).del;
    d.mockRejectedValue(new ApiError(409, { error: "in_use", deletability: {
      deletable: false, reasons: [{ code: "PURCHASES", message: "1 purchase is on record for this package.", count: 1 }] } }));
    routes({
      "/commercial-packages": list([PKG]),
      "/commercial-packages/pk1/deletability": { deletable: true, reasons: [] },
    });
    render(<InternetPackagesPage />);
    fireEvent.click(await screen.findByRole("button", { name: "Free WiFi" }));
    fireEvent.click(await screen.findByRole("button", { name: /delete…/i }));
    fireEvent.change(await screen.findByLabelText(/why are you deleting it/i), { target: { value: "Not needed" } });
    fireEvent.change(screen.getByLabelText(/confirm your password/i), { target: { value: "pw" } });
    fireEvent.click(screen.getByRole("button", { name: /^delete package$/i }));
    expect(await screen.findByText("1 purchase is on record for this package.")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^delete package$/i })).toBeNull();
  });
});

// ---------------------------------------------------------------------------------------------------------

const ROW_STAY = {
  purchase_id: "pu1", entitlement_id: "en1", package_id: "pk1", package_code: "FREEWIFI",
  package_name: "Free Internet Package", package_revision_id: "r1", revision_no: 3,
  price_minor: 500, currency: "USD", currency_exponent: 2,
  source: "GUEST_SELECTION", source_label: "Chosen on the portal", purchase_state: "GRANTED", status: "ACTIVE",
  sign_in_kind: "STAY", stay_id: "st1", room: "4202", pms_interface: "Protel", reservation: "BK-1",
  had_offer: true, offer_taken_at: "2026-09-19T09:59:00Z", offer_expires_at: "2026-09-19T10:10:00Z",
  started_at: "2026-09-19T10:00:00Z", occurred_at: "2026-09-19T10:00:00Z",
  service_plan: "Free Internet", quota_bytes: 1073741824, consumed_data_bytes: 5000000,
  sessions: 4, devices: 2, bytes_down: 300000000, bytes_up: 20000000,
  first_session_at: "2026-09-19T10:01:00Z", last_session_at: "2026-09-19T12:00:00Z", online_now: true,
  usage_href: "/usage?stay=st1",
};
const ROW_VOUCHER = {
  purchase_id: "pu2", entitlement_id: "en2", package_id: "pk2", package_code: "PREMIUM", package_name: "Premium",
  package_revision_id: "r9", revision_no: 1, price_minor: 0, currency: "USD", currency_exponent: 2,
  source: "VOUCHER_REDEMPTION", source_label: "Voucher", purchase_state: "GRANTED", status: "TERMINATED",
  end_reason: "DATA", sign_in_kind: "VOUCHER", had_offer: false,
  started_at: "2026-09-18T08:00:00Z", ended_at: "2026-09-18T20:00:00Z", occurred_at: "2026-09-18T08:00:00Z",
  sessions: 1, devices: 1, bytes_down: 1000, bytes_up: 1000, online_now: false,
};
const SUMMARY = {
  in_range: 2, started_in_range: 2, status_counts: { active: 1, ended: 1, other: 0 },
  data_bytes: 320002000, active_now: 7, undated: 1,
  by_package: [
    { package_id: "pk1", code: "FREEWIFI", name: "Free Internet Package", is_system: false, grants: 1 },
    { package_id: "pk2", code: "PREMIUM", name: "Premium", is_system: false, grants: 1 },
  ],
  by_source: [{ source: "GUEST_SELECTION", label: "Chosen on the portal", grants: 1 }, { source: "VOUCHER_REDEMPTION", label: "Voucher", grants: 1 }],
  active_by_package: [],
};

async function openActivity() {
  render(<InternetPackagesPage />);
  await userEvent.click(await screen.findByRole("tab", { name: /guest activity/i }));
}

describe("InternetPackagesPage — guest activity", () => {
  it("shows every grant, however it was given, as words — and leaks no guest identity", async () => {
    routes({ "/commercial-packages/activity*": activity([ROW_STAY, ROW_VOUCHER], SUMMARY) });
    await openActivity();

    expect(await screen.findByText("Room 4202")).toBeInTheDocument();
    // A voucher grant (no quote at all) is listed — the old view could not show it.
    expect(screen.getByText("A voucher guest")).toBeInTheDocument();
    expect(screen.getAllByText("Voucher").length).toBeGreaterThan(0);
    expect(screen.getAllByText("In use").length).toBeGreaterThan(1); // the status chip and the row badge
    // Summary: guests on a package now, data used, top package, breakdown by source.
    expect(screen.getByText("7")).toBeInTheDocument();
    expect(screen.getAllByText("Chosen on the portal").length).toBeGreaterThan(0);
    expect(screen.getByText(/cannot be placed in a period/i)).toBeInTheDocument();
    // No row claims an offer; ids are not on the surface.
    expect(screen.queryByText(/was offered/i)).toBeNull();
    expect(screen.queryByText("pu1")).toBeNull();

    const html = document.body.innerHTML.toLowerCase();
    for (const pii of ["auth_context", "auth-context", "device_id", "guest_network", "voucher_id", "guest_account", "password", "subject"]) {
      expect(html).not.toContain(pii);
    }
  });

  it("opens the full record with a timeline of recorded moments and a link to THIS stay's usage", async () => {
    routes({ "/commercial-packages/activity*": activity([ROW_STAY, ROW_VOUCHER], SUMMARY) });
    await openActivity();
    await userEvent.click(await screen.findByRole("button", { name: "Free Internet Package" }));
    const sheet = await screen.findByRole("dialog");

    expect(within(sheet).getByText("Offer taken on the portal")).toBeInTheDocument();
    expect(within(sheet).getByText("Access started")).toBeInTheDocument();
    expect(within(sheet).getByText("First device connected")).toBeInTheDocument();
    expect(within(sheet).getByText("5.00 USD")).toBeInTheDocument();
    const link = within(sheet).getByRole("link", { name: /open this stay/i });
    expect(link.getAttribute("href")).toBe("/usage?stay=st1");
    // Support ids live here, not on the list.
    expect(sheet.innerHTML).toContain("pu1");
  });

  it("never shows an offer for a grant that had none, and never invents an offer time", async () => {
    routes({ "/commercial-packages/activity*": activity([ROW_VOUCHER], SUMMARY) });
    await openActivity();
    await userEvent.click(await screen.findByRole("button", { name: "Premium" }));
    const sheet = await screen.findByRole("dialog");
    expect(within(sheet).getByText("Given without a portal offer")).toBeInTheDocument();
    expect(within(sheet).queryByText(/offer taken/i)).toBeNull();
    expect(within(sheet).getAllByText("Data allowance used up").length).toBeGreaterThan(0); // timeline + "why it ended"
    // No stay, so no usage link pretending to be one.
    expect(within(sheet).queryByRole("link", { name: /open this stay/i })).toBeNull();
  });

  it("pages on the server and resets to the first page when a filter changes", async () => {
    const rows = Array.from({ length: 25 }, (_, i) => ({ ...ROW_VOUCHER, purchase_id: `pv${i}` }));
    routes({ "/commercial-packages/activity*": activity(rows, SUMMARY, 60) });
    await openActivity();
    expect(await screen.findByText(/Showing 1–25 of 60/)).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: /next/i }));
    await waitFor(() => expect(g.mock.calls.some((c) => String(c[0]).includes("offset=25"))).toBe(true));

    fireEvent.change(screen.getByLabelText("How it was given"), { target: { value: "VOUCHER_REDEMPTION" } });
    await waitFor(() => {
      const last = String(g.mock.calls.filter((c) => String(c[0]).startsWith("/commercial-packages/activity")).at(-1)?.[0]);
      expect(last).toContain("source=VOUCHER_REDEMPTION");
      expect(last).toContain("offset=0");
    });
  });

  it("asks the server for the period and status the operator chose", async () => {
    routes({ "/commercial-packages/activity*": activity([ROW_STAY], SUMMARY) });
    await openActivity();
    await screen.findByText("Room 4202");
    await userEvent.click(screen.getByRole("radio", { name: "30 days" }));
    await userEvent.click(screen.getByRole("radio", { name: /^ended/i }));
    await waitFor(() => {
      const last = String(g.mock.calls.filter((c) => String(c[0]).startsWith("/commercial-packages/activity")).at(-1)?.[0]);
      expect(last).toContain("range=30d");
      expect(last).toContain("status=ended");
    });
  });
});

describe("a package with no published configuration (found live on PRE-LIVE)", () => {
  // The appliance holds an active package with no current revision. The page asked the server for its
  // configuration on every load, got a correct 404, and listed the package as "Active" -- which it cannot be,
  // since there is nothing to offer.
  it("is shown as not configured, and its configuration is never requested", async () => {
    routes({ "/commercial-packages": list([{ package_id: "pk9", code: "SCAFFOLD", name: "Scaffold package", active: true, current_revision_id: "", revision_count: 0 }]) });
    render(<InternetPackagesPage />);
    const cell = (await screen.findAllByText(/Scaffold package|SCAFFOLD/))[0];
    const row = cell.closest("tr")!;
    expect(within(row).getByText(/not configured/i)).toBeInTheDocument();
    expect(within(row).queryByText(/^active$/i)).toBeNull();
    await waitFor(() => expect(g).toHaveBeenCalled());
    const paths = g.mock.calls.map((c) => String(c[0]));
    expect(paths.some((p) => p.endsWith("/current"))).toBe(false);
  });
});
