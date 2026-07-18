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
import CommercialPackagesPage from "@/app/(app)/commercial-packages/page";

const g = api.get as unknown as ReturnType<typeof vi.fn>;
const p = api.post as unknown as ReturnType<typeof vi.fn>;

function list<T>(data: T[]) { return { data, meta: { has_more: false } }; }

beforeEach(() => { vi.clearAllMocks(); });

describe("CommercialPackagesPage", () => {
  it("renders the approved disabled state when the backend returns 503", async () => {
    g.mockRejectedValue(new ApiError(503, { error: "phase2_disabled" }));
    render(<CommercialPackagesPage />);
    expect(await screen.findByText(/not enabled/i)).toBeInTheDocument();
    // tabs are not shown in the disabled state
    expect(screen.queryByText("Service plans")).toBeNull();
  });

  it("lists packages and populates the plan selector (no raw UUID entry)", async () => {
    g.mockImplementation((path: string) => {
      if (path === "/commercial-packages") return Promise.resolve(list([{ package_id: "pk1", code: "FREEWIFI", active: true, current_revision_id: "r1", revision_count: 2 }]));
      if (path === "/commercial-packages/plans") return Promise.resolve(list([{ plan_id: "p1", code: "GOLD", enabled: true, current_revision_id: "rev-gold", revision_count: 1 }]));
      return Promise.resolve(list([]));
    });
    render(<CommercialPackagesPage />);
    expect(await screen.findByText("FREEWIFI")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /publish package/i }));
    const select = (await screen.findByLabelText("service-plan")) as HTMLSelectElement;
    expect(Array.from(select.options).map((o) => o.value)).toContain("rev-gold");
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
    render(<CommercialPackagesPage />);
    await screen.findByText("FREEWIFI");
    fireEvent.click(screen.getByText(/2 ▾/));
    expect(await screen.findByText(/current · immutable/i)).toBeInTheDocument();
    expect(screen.getByText(/#1/)).toBeInTheDocument();
  });

  it("deactivation requires reason + password step-up before calling the API", async () => {
    g.mockImplementation((path: string) => {
      if (path === "/commercial-packages") return Promise.resolve(list([{ package_id: "pk1", code: "FREEWIFI", active: true, current_revision_id: "r1", revision_count: 1 }]));
      return Promise.resolve(list([]));
    });
    p.mockResolvedValue({});
    const promptSpy = vi.spyOn(window, "prompt").mockReturnValueOnce("bad package").mockReturnValueOnce("secretpw");
    render(<CommercialPackagesPage />);
    await screen.findByText("FREEWIFI");
    fireEvent.click(screen.getByRole("button", { name: /deactivate/i }));
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
    render(<CommercialPackagesPage />);
    await screen.findByText("FREEWIFI");
    fireEvent.click(screen.getByRole("button", { name: /deactivate/i }));
    await new Promise((r) => setTimeout(r, 10));
    expect(p).not.toHaveBeenCalled();
  });

  it("inspection tab renders sanitized quotes/purchases with NO guest PII", async () => {
    g.mockImplementation((path: string) => {
      if (path === "/commercial-packages/quotes") return Promise.resolve(list([{ id: "q1", package_revision_id: "r1", price_minor: 0, currency: "USD", expires_at: "2026-08-01T00:00:00Z", consumed_at: null }]));
      if (path === "/commercial-packages/purchases") return Promise.resolve(list([{ id: "pu1", package_revision_id: "r1", state: "GRANTED", amount_minor: 0, currency: "USD" }]));
      return Promise.resolve(list([]));
    });
    render(<CommercialPackagesPage />);
    fireEvent.click(await screen.findByRole("button", { name: /inspection/i }));
    expect(await screen.findByText("q1")).toBeInTheDocument();
    expect(screen.getByText("GRANTED")).toBeInTheDocument();
    const html = document.body.innerHTML.toLowerCase();
    for (const pii of ["auth_context", "auth-context", "device_id", "guest_network", "mac", "subject", "voucher_id", "guest_account", "password"]) {
      expect(html).not.toContain(pii);
    }
  });

  it("grace tab: a rejected grace config surfaces the validation error", async () => {
    g.mockImplementation((path: string) =>
      path === "/commercial-packages/grace"
        ? Promise.resolve({ grace_package_revision_id: "", config: {} })
        : Promise.resolve(list([])));
    (api.put as unknown as ReturnType<typeof vi.fn>).mockRejectedValue(new ApiError(400, { error: "grace_package_wrong_type" }));
    render(<CommercialPackagesPage />);
    fireEvent.click(await screen.findByRole("button", { name: /checkout grace/i }));
    fireEvent.change(await screen.findByPlaceholderText(/uuid of a CHECKOUT_GRACE/i), { target: { value: "some-rev" } });
    fireEvent.click(screen.getByRole("button", { name: /save grace config/i }));
    // the specific backend validation reason is surfaced (exact match avoids the descriptive helper text)
    expect(await screen.findByText("grace_package_wrong_type")).toBeInTheDocument();
  });

  it("a failed publish shows an error and does not falsely report success", async () => {
    g.mockImplementation((path: string) => {
      if (path === "/commercial-packages") return Promise.resolve(list([]));
      if (path === "/commercial-packages/plans") return Promise.resolve(list([{ plan_id: "p1", code: "GOLD", enabled: true, current_revision_id: "rev-gold", revision_count: 1 }]));
      return Promise.resolve(list([]));
    });
    p.mockRejectedValue(new ApiError(400, { error: "invalid_grant_tier" }));
    render(<CommercialPackagesPage />);
    fireEvent.click(await screen.findByRole("button", { name: /publish package/i }));
    fireEvent.change(await screen.findByLabelText("code"), { target: { value: "X" } });
    fireEvent.change(screen.getByLabelText("service-plan"), { target: { value: "rev-gold" } });
    fireEvent.click(screen.getByRole("button", { name: /^publish$/i }));
    // error surfaces; the publish form stays open (no false "success"/navigation)
    expect(await screen.findByText(/invalid_grant_tier/i)).toBeInTheDocument();
    expect(screen.getByLabelText("service-plan")).toBeInTheDocument();
  });
});
