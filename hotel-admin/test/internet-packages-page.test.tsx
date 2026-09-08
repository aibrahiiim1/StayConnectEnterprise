import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor, within } from "@testing-library/react";

// Mock the edged API client. ApiError is a real class so the page's `e instanceof ApiError` guard works.
vi.mock("@/lib/api", () => {
  class ApiError extends Error {
    status: number;
    constructor(status: number, body?: unknown) {
      super(typeof body === "object" && body && "error" in body ? String((body as { error: unknown }).error) : `HTTP ${status}`);
      this.status = status;
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

beforeEach(() => { vi.clearAllMocks(); });

describe("InternetPackagesPage", () => {
  it("renders the approved disabled state when the backend returns 503", async () => {
    g.mockRejectedValue(new ApiError(503, { error: "phase2_disabled" }));
    render(<InternetPackagesPage />);
    expect(await screen.findByText(/not switched on/i)).toBeInTheDocument();
    // tabs are not shown in the disabled state
    expect(screen.queryByText("Service plans")).toBeNull();
  });

  it("shows what each package GIVES, and Add asks for a service plan by name", async () => {
    // The old list was a code, a status and a revision count, so this screen could not answer "what speed is
    // this?". It now answers it directly, and Add asks for the speed rather than for a plan revision.
    g.mockImplementation((path: string) => {
      if (path === "/commercial-packages") return Promise.resolve(list([{
        package_id: "pk1", code: "FREEWIFI", name: "Free WiFi", active: true,
        current_revision_id: "r1", revision_count: 2,
        service_plan_id: "p1", service_plan_revision_id: "rev-gold", service_plan_code: "GOLD",
        down_kbps: 10000, up_kbps: 5000, data_quota_bytes: 100000000, max_concurrent_devices: 4,
        speed_allocation: "PER_DEVICE", price_minor: 0,
      }]));
      if (path === "/commercial-packages/plans") return Promise.resolve(list([{ plan_id: "p1", code: "GOLD", name: "Gold", enabled: true, current_revision_id: "rev-gold", revision_count: 1, down_kbps: 10000 }]));
      return Promise.resolve(list([]));
    });
    render(<InternetPackagesPage />);
    expect(await screen.findByText("Free WiFi")).toBeInTheDocument();
    expect(screen.getByText(/10 Mbps down/i)).toBeInTheDocument();
    // The eligibility summary is deliberately absent: edged cannot read the rules table it writes, and
    // reading it here is what made this list 500 in PRE-LIVE.

    fireEvent.click(screen.getByRole("button", { name: /add package/i }));
    // The plan is chosen BY NAME. Its technical settings are the plan's, shown read-only, and its revision
    // id never appears.
    const sel = (await screen.findByLabelText("service-plan")) as HTMLSelectElement;
    expect(Array.from(sel.options).map((o) => o.value)).toContain("p1");
    expect(Array.from(sel.options).map((o) => o.value)).not.toContain("rev-gold");
    expect(screen.queryByLabelText("down-mbps")).toBeNull();
    expect(document.body.innerHTML).not.toContain("rev-gold");
  });

  it("renders package revision history with current/immutable status", async () => {
    g.mockImplementation((path: string) => {
      if (path === "/commercial-packages") return Promise.resolve(list([{ package_id: "pk1", code: "FREEWIFI", active: true, current_revision_id: "r2", revision_count: 2 }]));
      if (path === "/commercial-packages/plans") return Promise.resolve(list([]));
      if (path === "/commercial-packages/pk1/revisions") return Promise.resolve(list([
        { revision_id: "r2", revision_no: 2, is_current: true, package_type: "GENERAL", price_minor: 0, currency: "USD" },
        { revision_id: "r1", revision_no: 1, is_current: false, package_type: "GENERAL", price_minor: 0, currency: "USD" },
      ]));
      return Promise.resolve(list([]));
    });
    render(<InternetPackagesPage />);
    await screen.findByText("FREEWIFI");
    fireEvent.click(screen.getByText(/^History$/));
    expect(await screen.findByText(/in force/i)).toBeInTheDocument();
    expect(screen.getByText(/#1/)).toBeInTheDocument();
  });

  it("deactivation requires reason + password step-up before calling the API", async () => {
    g.mockImplementation((path: string) => {
      if (path === "/commercial-packages") return Promise.resolve(list([{ package_id: "pk1", code: "FREEWIFI", active: true, current_revision_id: "r1", revision_count: 1 }]));
      return Promise.resolve(list([]));
    });
    p.mockResolvedValue({});
    const promptSpy = vi.spyOn(window, "prompt").mockReturnValueOnce("bad package").mockReturnValueOnce("secretpw");
    render(<InternetPackagesPage />);
    await screen.findByText("FREEWIFI");
    fireEvent.click(screen.getByRole("button", { name: /^disable$/i }));
    await waitFor(() => expect(p).toHaveBeenCalled());
    expect(promptSpy).toHaveBeenCalledTimes(2); // reason then password
    expect(p).toHaveBeenCalledWith("/commercial-packages/pk1/active", { active: false, reason: "bad package", password: "secretpw" });
  });

  it("aborts deactivation (no API call) if the operator cancels the step-up", async () => {
    g.mockImplementation((path: string) => path === "/commercial-packages"
      ? Promise.resolve(list([{ package_id: "pk1", code: "FREEWIFI", active: true, current_revision_id: "r1", revision_count: 1 }]))
      : Promise.resolve(list([])));
    p.mockResolvedValue({});
    vi.spyOn(window, "prompt").mockReturnValue(null); // cancel
    render(<InternetPackagesPage />);
    await screen.findByText("FREEWIFI");
    fireEvent.click(screen.getByRole("button", { name: /^disable$/i }));
    await new Promise((r) => setTimeout(r, 10));
    expect(p).not.toHaveBeenCalled();
  });

  it("inspection tab renders sanitized quotes/purchases with NO guest PII", async () => {
    g.mockImplementation((path: string) => {
      if (path === "/commercial-packages/quotes") return Promise.resolve(list([{ id: "q1", package_revision_id: "r1", price_minor: 0, currency: "USD", expires_at: "2026-08-01T00:00:00Z", consumed_at: null }]));
      if (path === "/commercial-packages/purchases") return Promise.resolve(list([{ id: "pu1", package_revision_id: "r1", state: "GRANTED", amount_minor: 0, currency: "USD" }]));
      return Promise.resolve(list([]));
    });
    render(<InternetPackagesPage />);
    fireEvent.click(await screen.findByRole("button", { name: /guest activity/i }));
    expect(await screen.findByText("q1")).toBeInTheDocument();
    expect(screen.getByText("GRANTED")).toBeInTheDocument();
    const html = document.body.innerHTML.toLowerCase();
    for (const pii of ["auth_context", "auth-context", "device_id", "guest_network", "mac", "subject", "voucher_id", "guest_account", "password"]) {
      expect(html).not.toContain(pii);
    }
  });

  // The grace tab was removed from this page: checkout grace is its own screen (/checkout-grace),
  // covered by the checkout-grace component tests. Asserting it here would pin a duplicate surface.

  it("a failed save shows an error and does not falsely report success", async () => {
    g.mockImplementation((path: string) => {
      if (path === "/commercial-packages") return Promise.resolve(list([]));
      if (path === "/commercial-packages/plans") return Promise.resolve(list([{ plan_id: "p1", code: "GOLD", enabled: true, current_revision_id: "rev-gold", revision_count: 1 }]));
      return Promise.resolve(list([]));
    });
    p.mockRejectedValue(new ApiError(400, { error: "invalid_grant_tier" }));
    render(<InternetPackagesPage />);
    fireEvent.click(await screen.findByRole("button", { name: /add package/i }));
    fireEvent.change(await screen.findByLabelText("code"), { target: { value: "X" } });
    fireEvent.change(screen.getByLabelText("service-plan"), { target: { value: "p1" } });
    fireEvent.click(screen.getByRole("button", { name: /^add package$/i, hidden: false }));
    expect(await screen.findByText(/invalid_grant_tier/i)).toBeInTheDocument();
    // the form stays open rather than reporting a success that did not happen
    expect(screen.getByLabelText("code")).toBeInTheDocument();
  });
});
