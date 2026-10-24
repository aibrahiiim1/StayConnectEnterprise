// Repinning a package must preserve its commercial terms and avoid publishing another plan revision.

import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor, within } from "@testing-library/react";

vi.mock("@/lib/api", () => {
  class ApiError extends Error {
    status: number;
    code: string;
    constructor(status: number, body?: unknown) {
      super(typeof body === "object" && body && "error" in body ? String((body as { error: unknown }).error) : `HTTP ${status}`);
      this.status = status;
      this.code = typeof body === "object" && body && "error" in body ? String((body as { error: unknown }).error) : "http_error";
    }
  }
  return { ApiError, api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), del: vi.fn() } };
});

import { api } from "@/lib/api";
import ServicePlansPage from "@/app/(app)/service-plans/page";

const g = api.get as unknown as ReturnType<typeof vi.fn>;
const p = api.post as unknown as ReturnType<typeof vi.fn>;
function list<T>(data: T[]) { return { data, meta: { has_more: false } }; }

const PLANS = [{
  plan_id: "plan-free", code: "FREE", name: "Free Internet", enabled: true,
  current_revision_id: "rev4", revision_count: 4,
  down_kbps: 10000, up_kbps: 5000, max_concurrent_devices: 1, used_by_active_packages: 2,
}];

const ONEDAY = {
  plan_id: "plan-oneday", code: "OneDay", name: "OneDay", enabled: true,
  current_revision_id: "rev-od-1", revision_count: 1,
  down_kbps: 10000000, up_kbps: 10000000, max_concurrent_devices: 5,
  time_quota_seconds: 86400, data_quota_bytes: null,
  idle_timeout_seconds: null, max_continuous_session_seconds: null,
  used_by_active_packages: 1,
};
const PACKAGES = [
  {
    package_id: "pkg-freee", code: "FREEE", name: "Freee", active: true,
    service_plan_id: "plan-free", service_plan_revision_id: "rev3",
  },
  {
    package_id: "pkg-current", code: "CURRENT", name: "Already current", active: true,
    service_plan_id: "plan-free", service_plan_revision_id: "rev4",
  },
];

const FREEE_CURRENT = {
  code: "FREEE",
  price_minor: 2500, currency: "USD", currency_exponent: 2,
  settlement_methods: ["PREPAID", "ONLINE_PAYMENT"], room_charge_mappings: null,
  package_type: "GENERAL", renewable: false,
  display: { name: "Freee" },
  duration_policy: { end_mode: "VALIDITY_WINDOW", duration_seconds: 86400 },
  eligibility_rules: [{ type: "AUTH_METHOD", value: { methods: ["pms"] } }],
  grant_tiers: [{ order: 10, value: { down_kbps: 2000 } }],
  visible_from: "2026-01-01T00:00:00Z",
  visible_until: null,
};

function mockLoad(packages = PACKAGES, plans: unknown[] = PLANS) {
  g.mockImplementation((path: string) => {
    if (path === "/auth/whoami") return Promise.resolve({ roles: ["site_admin"] });
    if (path === "/commercial-packages/plans") return Promise.resolve(list(plans));
    if (path === "/commercial-packages") return Promise.resolve(list(packages));
    if (path === "/commercial-packages/pkg-freee/current") return Promise.resolve(FREEE_CURRENT);
    return Promise.resolve(list([]));
  });
}

beforeEach(() => { vi.clearAllMocks(); });

describe("ServicePlansPage — stale package pin", () => {
  it("flags the plan whose packages are behind, and offers the action", async () => {
    mockLoad();
    render(<ServicePlansPage />);
    expect(await screen.findByText("Free Internet")).toBeInTheDocument();
    expect(screen.getByTestId("stale-count-FREE").textContent).toContain("1 still on older settings");
    expect(screen.getByTestId("apply-current-FREE")).toBeInTheDocument();
  });

  it("offers ONLY the stale package — one already on the current revision is not listed", async () => {
    mockLoad();
    render(<ServicePlansPage />);
    fireEvent.click(await screen.findByTestId("apply-current-FREE"));

    expect(screen.getByTestId("repin-title").textContent).toBe("Apply current settings to packages");
    expect(screen.getByLabelText("repin-FREEE")).toBeInTheDocument();
    expect(screen.queryByLabelText("repin-CURRENT")).toBeNull();
  });

  it("shows no action at all when every package is already on the current settings", async () => {
    mockLoad([PACKAGES[1]]);
    render(<ServicePlansPage />);
    expect(await screen.findByText("Free Internet")).toBeInTheDocument();
    expect(screen.queryByTestId("apply-current-FREE")).toBeNull();
    expect(screen.queryByTestId("stale-count-FREE")).toBeNull();
  });

  it("changes NOTHING until the operator picks a package and applies", async () => {
    mockLoad();
    render(<ServicePlansPage />);
    fireEvent.click(await screen.findByTestId("apply-current-FREE"));
    expect(p).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: /not now/i }));
    expect(p).not.toHaveBeenCalled();
    expect(screen.getByRole("status").textContent).toContain("Nothing was changed");
  });

  it("publishes a PACKAGE revision only, pinned to the plan's EXISTING current revision", async () => {
    mockLoad();
    p.mockResolvedValue({});
    render(<ServicePlansPage />);
    fireEvent.click(await screen.findByTestId("apply-current-FREE"));
    fireEvent.click(screen.getByLabelText("repin-FREEE"));
    fireEvent.change(screen.getByLabelText("Confirm your password"), { target: { value: "pw-op" } });
    fireEvent.click(screen.getByRole("button", { name: /apply to selected packages/i }));

    await waitFor(() => expect(p).toHaveBeenCalledTimes(1));
    const [path, body] = p.mock.calls[0];
    expect(path).toBe("/commercial-packages");
    expect(p.mock.calls.some((c: unknown[]) => String(c[0]).includes("/plans"))).toBe(false);
    expect(body.service_plan_revision_id).toBe("rev4");
  });

  it("preserves the package's rules, tiers, duration, sale window and display exactly", async () => {
    mockLoad();
    p.mockResolvedValue({});
    render(<ServicePlansPage />);
    fireEvent.click(await screen.findByTestId("apply-current-FREE"));
    fireEvent.click(screen.getByLabelText("repin-FREEE"));
    fireEvent.change(screen.getByLabelText("Confirm your password"), { target: { value: "pw-op" } });
    fireEvent.click(screen.getByRole("button", { name: /apply to selected packages/i }));

    await waitFor(() => expect(p).toHaveBeenCalledTimes(1));
    const body = p.mock.calls[0][1];
    expect(body.code).toBe("FREEE");
    expect(body.display).toEqual({ name: "Freee" });
    expect(body.duration_policy).toEqual({ end_mode: "VALIDITY_WINDOW", duration_seconds: 86400 });
    expect(body.eligibility_rules).toEqual([{ type: "AUTH_METHOD", value: { methods: ["pms"] } }]);
    expect(body.grant_tiers).toEqual([{ order: 10, grant: { down_kbps: 2000 } }]);
    expect(body.visible_from).toBe("2026-01-01T00:00:00Z");
    // Use the package's current revision, not an abbreviated list row.
    expect(g).toHaveBeenCalledWith("/commercial-packages/pkg-freee/current");
    // Carry price and purchase methods so repinning cannot turn a priced package free.
    expect(body.price_minor).toBe(2500);
    expect(body.currency).toBe("USD");
    expect(body.currency_exponent).toBe(2);
    expect(body.acquisition_methods).toEqual(["PREPAID", "ONLINE_PAYMENT"]);
    expect(body.room_charge_mappings).toEqual([]);
    expect(body.package_type).toBe("GENERAL");
    expect(body.renewable).toBe(false);
    expect(body.password).toBe("pw-op");
  });

  it("does not post a package whose stored price cannot be read in full, and names it", async () => {
    mockLoad();
    g.mockImplementation((path: string) => {
      if (path === "/auth/whoami") return Promise.resolve({ roles: ["site_admin"] });
      if (path === "/commercial-packages/plans") return Promise.resolve(list(PLANS));
      if (path === "/commercial-packages") return Promise.resolve(list(PACKAGES));
      // Do not guess missing commercial fields for a priced package.
      if (path === "/commercial-packages/pkg-freee/current") {
        return Promise.resolve({ ...FREEE_CURRENT, currency: "", settlement_methods: null, currency_exponent: undefined });
      }
      return Promise.resolve(list([]));
    });
    render(<ServicePlansPage />);
    fireEvent.click(await screen.findByTestId("apply-current-FREE"));
    fireEvent.click(screen.getByLabelText("repin-FREEE"));
    fireEvent.change(screen.getByLabelText("Confirm your password"), { target: { value: "pw-op" } });
    fireEvent.click(screen.getByRole("button", { name: /apply to selected packages/i }));

    await waitFor(() => expect(screen.getByRole("status").textContent).toContain("Not moved: Freee"));
    expect(screen.getByRole("status").textContent).toContain("currency");
    expect(screen.getByRole("status").textContent).toContain("acquisition_methods");
    expect(p).not.toHaveBeenCalled();
  });

  it("will not apply without the password, and says so when the server refuses it", async () => {
    mockLoad();
    const { ApiError } = await import("@/lib/api");
    p.mockRejectedValue(new (ApiError as unknown as new (s: number, b: unknown) => Error)(401, { error: "reauth_required" }));
    render(<ServicePlansPage />);
    fireEvent.click(await screen.findByTestId("apply-current-FREE"));
    fireEvent.click(screen.getByLabelText("repin-FREEE"));
    expect(screen.getByRole("button", { name: /apply to selected packages/i })).toBeDisabled();
    fireEvent.change(screen.getByLabelText("Confirm your password"), { target: { value: "wrong" } });
    fireEvent.click(screen.getByRole("button", { name: /apply to selected packages/i }));
    expect(await screen.findByText("That password is not correct. Nothing was changed.")).toBeInTheDocument();
    expect(screen.getByTestId("repin-title")).toBeInTheDocument();
  });

  it("tells the operator that guests already connected are unaffected", async () => {
    mockLoad();
    p.mockResolvedValue({});
    render(<ServicePlansPage />);
    fireEvent.click(await screen.findByTestId("apply-current-FREE"));
    fireEvent.click(screen.getByLabelText("repin-FREEE"));
    fireEvent.change(screen.getByLabelText("Confirm your password"), { target: { value: "pw-op" } });
    fireEvent.click(screen.getByRole("button", { name: /apply to selected packages/i }));

    await waitFor(() => expect(screen.getByRole("status").textContent).toContain("1 package updated"));
    const msg = screen.getByRole("status").textContent ?? "";
    expect(msg).toContain("Existing client access is unchanged");
    expect(msg.toLowerCase()).not.toContain("revision");
  });

  it("republishes a stale package even when other packages of the same plan are current", async () => {
    mockLoad();
    p.mockResolvedValue({});
    render(<ServicePlansPage />);
    fireEvent.click(await screen.findByTestId("apply-current-FREE"));
    fireEvent.click(screen.getByLabelText("repin-FREEE"));
    fireEvent.change(screen.getByLabelText("Confirm your password"), { target: { value: "pw-op" } });
    fireEvent.click(screen.getByRole("button", { name: /apply to selected packages/i }));

    await waitFor(() => expect(p).toHaveBeenCalledTimes(1));
    expect(g).not.toHaveBeenCalledWith("/commercial-packages/pkg-current/current");
  });

  it("never asks the operator for a revision id", async () => {
    mockLoad();
    const { container } = render(<ServicePlansPage />);
    await screen.findByText("Free Internet");
    fireEvent.click(screen.getByTestId("apply-current-FREE"));
    expect(container.innerHTML).not.toContain("rev3");
    expect(container.innerHTML).not.toContain("rev4");
    expect(screen.queryByLabelText(/revision id/i)).toBeNull();
  });
});

// Plan edits publish full revisions, so prefill every field and preserve the original unit.

describe("ServicePlansPage — editing an existing plan", () => {
  // Radix portals the dialog to document.body, outside the render container.
  function fieldsOf(_container: HTMLElement) {
    const v = (name: string) =>
      (document.querySelector(`[name="${name}"]`) as HTMLInputElement | HTMLSelectElement | null)?.value;
    return v;
  }

  it("pre-fills every value the plan already has, including the time allowance and its unit", async () => {
    mockLoad([], [ONEDAY]);
    const { container } = render(<ServicePlansPage />);
    fireEvent.click(await screen.findByRole("button", { name: /^edit\b/i }));

    const v = fieldsOf(container);
    expect(v("code")).toBe("OneDay");
    expect(v("down_mbps")).toBe("10000");
    expect(v("up_mbps")).toBe("10000");
    expect(v("max_concurrent_devices")).toBe("5");
    expect(v("time_quota")).toBe("1");
    expect(v("time_quota_unit")).toBe("days");
  });

  it("leaves genuinely unset fields empty rather than inventing a zero", async () => {
    mockLoad([], [ONEDAY]);
    const { container } = render(<ServicePlansPage />);
    fireEvent.click(await screen.findByRole("button", { name: /^edit\b/i }));

    const v = fieldsOf(container);
    for (const unset of ["data_quota_gb", "idle_timeout", "max_session"]) expect(v(unset)).toBe("");
  });

  it("republishes the plan unchanged when the operator edits one field and saves", async () => {
    mockLoad([], [ONEDAY]);
    p.mockResolvedValue({ current_revision_id: "rev-od-2" });
    const { container } = render(<ServicePlansPage />);
    fireEvent.click(await screen.findByRole("button", { name: /^edit\b/i }));
    fireEvent.change(document.querySelector('[name="max_concurrent_devices"]')!, { target: { value: "6" } });
    fireEvent.change(document.querySelector('[name="password"]')!, { target: { value: "pw-op" } });
    fireEvent.click(screen.getByRole("button", { name: /save changes/i }));

    await waitFor(() => expect(p).toHaveBeenCalledTimes(1));
    const body = p.mock.calls[0][1];
    expect(body.max_concurrent_devices).toBe(6);
    expect(body.time_quota_seconds).toBe(86400);
    expect(body.down_kbps).toBe(10000000);
    expect(body.up_kbps).toBe(10000000);
  });

  it("keeps an online-time plan's time accounting when it is edited (audit O-20)", async () => {
    mockLoad([], [{ ...ONEDAY, time_accounting_mode: "AGGREGATE_ONLINE_TIME" }]);
    p.mockResolvedValue({ current_revision_id: "rev-od-2" });
    render(<ServicePlansPage />);
    fireEvent.click(await screen.findByRole("button", { name: /^edit\b/i }));
    const mode = document.getElementById("plan-time-mode") as HTMLSelectElement;
    expect(mode.disabled).toBe(true);
    expect(mode.value).toBe("AGGREGATE_ONLINE_TIME");
    fireEvent.change(document.querySelector('[name="password"]')!, { target: { value: "pw-op" } });
    fireEvent.click(screen.getByRole("button", { name: /save changes/i }));
    await waitFor(() => expect(p).toHaveBeenCalledTimes(1));
    expect(p.mock.calls[0][1].time_accounting_mode).toBe("AGGREGATE_ONLINE_TIME");
  });

  it("says a refused password in words when an existing plan is saved", async () => {
    mockLoad([], [ONEDAY]);
    const { ApiError } = await import("@/lib/api");
    p.mockRejectedValue(new (ApiError as unknown as new (s: number, b: unknown) => Error)(401, { error: "reauth_required" }));
    render(<ServicePlansPage />);
    fireEvent.click(await screen.findByRole("button", { name: /^edit\b/i }));
    fireEvent.change(document.querySelector('[name="password"]')!, { target: { value: "wrong" } });
    fireEvent.click(screen.getByRole("button", { name: /save changes/i }));
    expect(await screen.findByText("That password is not correct. The plan was not saved.")).toBeInTheDocument();
  });

  it("stops an over-limit speed at the field, with the ceiling stated on screen", async () => {
    mockLoad([], [ONEDAY]);
    const { container } = render(<ServicePlansPage />);
    fireEvent.click(await screen.findByRole("button", { name: /^edit\b/i }));

    const down = document.querySelector('[name="down_mbps"]') as HTMLInputElement;
    expect(down.max).toBe("10000");
    fireEvent.change(down, { target: { value: "10001" } });
    expect(down.checkValidity()).toBe(false);
    expect(screen.getAllByText(/up to 10000 mbps/i).length).toBeGreaterThan(0);
  });
});

if (!("ResizeObserver" in globalThis)) {
  (globalThis as Record<string, unknown>).ResizeObserver = class { observe() {} unobserve() {} disconnect() {} };
}

describe("ServicePlansPage — add, record and delete", () => {
  it("Add sends create_only so a taken code is refused rather than revised; Edit does not", async () => {
    mockLoad([], [ONEDAY]);
    p.mockResolvedValue({ current_revision_id: "new" });
    render(<ServicePlansPage />);
    await screen.findByText("OneDay", { selector: "span" });

    fireEvent.click(screen.getByRole("button", { name: /add plan/i }));
    fireEvent.change(document.querySelector('[name="code"]')!, { target: { value: "NEWPLAN" } });
    fireEvent.click(screen.getByRole("button", { name: /^add plan$/i }));
    await waitFor(() => expect(p).toHaveBeenCalledTimes(1));
    expect(p.mock.calls[0][1].create_only).toBe(true);

    fireEvent.click(await screen.findByRole("button", { name: /^edit\b/i }));
    fireEvent.change(document.querySelector('[name="password"]')!, { target: { value: "pw-op" } });
    fireEvent.click(screen.getByRole("button", { name: /save changes/i }));
    await waitFor(() => expect(p).toHaveBeenCalledTimes(2));
    expect(p.mock.calls[1][1]).not.toHaveProperty("create_only");
    expect(p.mock.calls[1][1].password).toBe("pw-op");
    expect(p.mock.calls[0][1]).not.toHaveProperty("password");
  });

  // ADMIN_APPROVAL has no approval workflow and must not be offered.
  it("offers only the device-limit policies the appliance enforces", async () => {
    mockLoad([], [ONEDAY]);
    render(<ServicePlansPage />);
    await screen.findByText("OneDay", { selector: "span" });
    fireEvent.click(screen.getByRole("button", { name: /add plan/i }));
    const select = document.querySelector('[name="device_limit_policy"]') as HTMLSelectElement;
    expect(Array.from(select.options).map((o) => o.value)).toEqual(["REJECT_NEW_DEVICE", "DISCONNECT_OLDEST"]);
    expect(screen.getByText(/two devices online for an hour use two/i)).toBeInTheDocument();
  });

  it("shows the server's code_exists refusal inside the form", async () => {
    mockLoad([], [ONEDAY]);
    const { ApiError } = await import("@/lib/api");
    p.mockRejectedValue(new (ApiError as unknown as new (s: number, b: unknown) => Error)(409, { error: "code_exists" }));
    render(<ServicePlansPage />);
    await screen.findByText("OneDay", { selector: "span" });
    fireEvent.click(screen.getByRole("button", { name: /add plan/i }));
    fireEvent.change(document.querySelector('[name="code"]')!, { target: { value: "OneDay" } });
    fireEvent.click(screen.getByRole("button", { name: /^add plan$/i }));
    const dialog = await screen.findByRole("dialog");
    expect(await within(dialog).findByText(/code_exists/)).toBeInTheDocument();
  });

  it("presents no enable/disable state for a plan", async () => {
    mockLoad([], [{ ...ONEDAY, enabled: false }]);
    render(<ServicePlansPage />);
    await screen.findByText("OneDay", { selector: "span" });
    expect(screen.queryByText(/^disabled$/i)).toBeNull();
    expect(screen.queryByRole("button", { name: /^(disable|enable)$/i })).toBeNull();
    fireEvent.click(screen.getByText("OneDay", { selector: "span" }));
    const sheet = await screen.findByRole("dialog");
    expect(within(sheet).getByText(/does not change what clients receive/i)).toBeInTheDocument();
  });

  it("the plan record lists the packages using it and its saved versions", async () => {
    g.mockImplementation((path: string) => {
      if (path === "/auth/whoami") return Promise.resolve({ roles: ["site_admin"] });
      if (path === "/commercial-packages/plans") return Promise.resolve(list(PLANS));
      if (path === "/commercial-packages") return Promise.resolve(list(PACKAGES));
      if (path === "/commercial-packages/plans/plan-free/revisions") return Promise.resolve(list([
        { revision_id: "rev4", revision_no: 4, is_current: true, label: "Free Internet" },
        { revision_id: "rev3", revision_no: 3, is_current: false, label: "Free Internet" },
      ]));
      return Promise.resolve(list([]));
    });
    render(<ServicePlansPage />);
    fireEvent.click(await screen.findByText("Free Internet", { selector: "span" }));
    const sheet = await screen.findByRole("dialog");
    expect(within(sheet).getByText("Freee")).toBeInTheDocument();
    expect(within(sheet).getByText("Older settings")).toBeInTheDocument();
    expect(await within(sheet).findByText(/Version 4/)).toBeInTheDocument();
    expect(within(sheet).getByText(/in force/)).toBeInTheDocument();
  });

  it("Delete… of a used plan shows what is attached and why, offers no delete, and sends nothing", async () => {
    g.mockImplementation((path: string) => {
      if (path === "/auth/whoami") return Promise.resolve({ roles: ["site_admin"] });
      if (path === "/commercial-packages/plans") return Promise.resolve(list(PLANS));
      if (path === "/commercial-packages") return Promise.resolve(list(PACKAGES));
      if (path === "/commercial-packages/plans/plan-free/deletability") return Promise.resolve({
        deletable: false,
        reasons: [
          { code: "PACKAGE_REVISIONS", message: "2 package versions are built on this plan, including earlier saved versions.", count: 2 },
        ],
      });
      return Promise.resolve(list([]));
    });
    render(<ServicePlansPage />);
    fireEvent.click(await screen.findByText("Free Internet", { selector: "span" }));
    fireEvent.click(await screen.findByRole("button", { name: /delete…/i }));
    expect(await screen.findByText(/2 package versions are built on this plan/)).toBeInTheDocument();
    expect(screen.getByText(/why it can't be deleted/i)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^delete service plan$/i })).toBeNull();
    expect(p).not.toHaveBeenCalled();
  });

  it("Delete… of an unused plan deletes it with a reason and password", async () => {
    const { api } = await import("@/lib/api");
    const d = (api as unknown as { del: ReturnType<typeof vi.fn> }).del;
    d.mockResolvedValue({ deleted: true });
    g.mockImplementation((path: string) => {
      if (path === "/auth/whoami") return Promise.resolve({ roles: ["site_admin"] });
      if (path === "/commercial-packages/plans") return Promise.resolve(list(PLANS));
      if (path === "/commercial-packages") return Promise.resolve(list(PACKAGES));
      if (path === "/commercial-packages/plans/plan-free/deletability") return Promise.resolve({ deletable: true, reasons: [] });
      return Promise.resolve(list([]));
    });
    render(<ServicePlansPage />);
    fireEvent.click(await screen.findByText("Free Internet", { selector: "span" }));
    fireEvent.click(await screen.findByRole("button", { name: /delete…/i }));
    fireEvent.change(await screen.findByLabelText(/why are you deleting it/i), { target: { value: "Test plan" } });
    fireEvent.change(screen.getByLabelText(/confirm your password/i), { target: { value: "pw" } });
    fireEvent.click(screen.getByRole("button", { name: /^delete service plan$/i }));
    await waitFor(() => expect(d).toHaveBeenCalledWith("/commercial-packages/plans/plan-free", { reason: "Test plan", password: "pw" }));
  });
});
