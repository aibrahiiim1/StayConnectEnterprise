import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

// The Phase-3 pages talk to edged through lib/api; the client is mocked so the components can be exercised
// in jsdom without a backend. What is asserted here is the behaviour an operator depends on: the review queue
// shows WHY an event was refused, the alert queue can be acted on, and the grace form publishes the COMPLETE
// policy rather than a patch.
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

describe("Stays page", () => {
  it("shows a stay with its occupant count and checkout boundary, and opens details", async () => {
    get.mockImplementation((path: string) => {
      if (path.startsWith("/pms-stays/")) {
        return Promise.resolve({
          id: "s1", pms_interface_id: "i1", external_reservation_id: "R900", room: "900",
          status: "CHECKED_OUT", lifecycle_version: 1, posting_allowed: false, occupants: 2,
          effective_checkout_at: new Date().toISOString(),
          occupant_list: [{ display_name: "Byron, Ada", is_primary: true }, { display_name: "Babbage, Chas", is_primary: false }],
          folios: [{ external_folio_id: "F900", folio_kind: "GUEST", status: "OPEN", is_default_posting_target: true }],
        });
      }
      return Promise.resolve({
        data: [{
          id: "s1", pms_interface_id: "i1", external_reservation_id: "R900", room: "900",
          status: "CHECKED_OUT", lifecycle_version: 1, posting_allowed: false, occupants: 2,
          effective_checkout_at: new Date().toISOString(),
        }],
        meta: { has_more: false },
      });
    });
    const { default: StaysPage } = await import("@/app/(app)/stays/page");
    render(<StaysPage />);
    expect(await screen.findByText("R900")).toBeTruthy();
    // sharing a stay is ordinary: the occupant count is shown plainly
    expect(screen.getByText("2")).toBeTruthy();
    expect(screen.getByText("closed")).toBeTruthy();

    await userEvent.click(screen.getByRole("button", { name: "Details" }));
    expect(await screen.findByText("Byron, Ada")).toBeTruthy();
    expect(screen.getByText("primary")).toBeTruthy();
    expect(screen.getByText(/F900/)).toBeTruthy();
  });

  it("reports a load failure instead of rendering an empty table as if all were well", async () => {
    get.mockRejectedValue(new Error("boom"));
    const { default: StaysPage } = await import("@/app/(app)/stays/page");
    render(<StaysPage />);
    expect(await screen.findByRole("alert")).toBeTruthy();
  });
});

describe("Stay events page", () => {
  it("defaults to the review queue and shows the bounded reason an event was refused", async () => {
    get.mockResolvedValue({
      data: [{
        id: "e1", pms_interface_id: "i1", external_event_identity: "FC-2", event_type: "GI",
        processing_status: "MANUAL_REVIEW", review_code: "FOLIO_CLAIMED_BY_OTHER_STAY",
        received_at: new Date().toISOString(),
      }],
      meta: { has_more: false },
    });
    const { default: StayEventsPage } = await import("@/app/(app)/stay-events/page");
    render(<StayEventsPage />);
    await waitFor(() => expect(get).toHaveBeenCalled());
    // the default view is what needs attention
    expect(get.mock.calls[0][0]).toContain("processing_status=MANUAL_REVIEW");
    expect(await screen.findByText("FOLIO_CLAIMED_BY_OTHER_STAY")).toBeTruthy();
  });
});

describe("Operational alerts page", () => {
  it("acknowledges an alert and reloads the queue", async () => {
    get.mockResolvedValue({
      data: [{
        audit_id: "a1", stay_id: "s1", lifecycle_version: 1, alert_code: "EMERGENCY_GRACE_USED",
        trigger: "EMERGENCY_GRACE", reason_code: "POLICY_MISMATCH",
        boundary_at: new Date().toISOString(), boundary_clock_suspect: false, created_at: new Date().toISOString(),
        state: "OPEN", seq: 1,
      }],
      meta: { has_more: false },
    });
    post.mockResolvedValue({ id: "x", action: "ACK" });
    const { OperationalAlertsView: AlertsPage } = await import("@/components/phase3/operational-alerts-view");
    render(<AlertsPage />);
    await userEvent.click(await screen.findByRole("button", { name: /Acknowledge alert/ }));
    await waitFor(() => expect(post).toHaveBeenCalled());
    const [path, body] = post.mock.calls[0];
    expect(path).toBe("/operational-alerts/a1/acknowledge");
    // the action carries the state this operator was looking at, so a concurrent change is a clean conflict
    expect(body.expected_state).toBe("OPEN");
    expect(get).toHaveBeenCalledTimes(2); // the queue is re-read after acting
  });

  it("surfaces a refused action rather than pretending it succeeded", async () => {
    get.mockResolvedValue({
      data: [{
        audit_id: "a1", stay_id: "s1", lifecycle_version: 1, alert_code: "EMERGENCY_GRACE_USED",
        trigger: "EMERGENCY_GRACE", boundary_at: new Date().toISOString(),
        boundary_clock_suspect: false, created_at: new Date().toISOString(), state: "OPEN", seq: 1,
      }],
      meta: { has_more: false },
    });
    post.mockRejectedValue(new Error("illegal transition"));
    const { OperationalAlertsView: AlertsPage } = await import("@/components/phase3/operational-alerts-view");
    render(<AlertsPage />);
    await userEvent.click(await screen.findByRole("button", { name: /Resolve alert/ }));
    expect(await screen.findByRole("alert")).toBeTruthy();
  });

  it("hides the actions from a role that may only read", async () => {
    get.mockResolvedValue({
      data: [{
        audit_id: "a1", stay_id: "s1", lifecycle_version: 1, alert_code: "EMERGENCY_GRACE_USED",
        trigger: "EMERGENCY_GRACE", boundary_at: new Date().toISOString(),
        boundary_clock_suspect: false, created_at: new Date().toISOString(), state: "OPEN", seq: 1,
      }],
      meta: { has_more: false },
    });
    const { OperationalAlertsView: AlertsPage } = await import("@/components/phase3/operational-alerts-view");
    render(<AlertsPage canAct={false} />);
    await screen.findByText(/emergency grace used/);
    expect(screen.queryByRole("button", { name: /Acknowledge alert/ })).toBeNull();
  });
});

describe("Checkout grace page", () => {
  const pkg = {
    package_revision_id: "rev-1", package_code: "site-grace-pkg", revision_no: 1,
    service_plan_revision_id: "plan-1", service_plan_code: "site-grace-plan",
    down_kbps: 4000, up_kbps: 1500, data_quota_bytes: 524288000, device_limit: 2,
    device_limit_policy: "REJECT_NEW_DEVICE", grace_duration_seconds: 3600,
    settlement_mode: "NOT_REQUIRED", is_current: true, is_active: true, selected: true,
    service_plan_revision_no: 1, time_accounting_mode: "VALIDITY_WINDOW",
    end_mode: "GRACE_AFTER_CHECKOUT", policy_version: "CHECKOUT_GRACE_V1",
  };
  const state = (v: number) => ({ published: v > 0, config_version: v, supported_device_policies: ["REJECT_NEW_DEVICE"] });

  function mockGrace(v: number, packages = [pkg]) {
    get.mockImplementation((path: string) => {
      if (path === "/checkout-grace/packages") return Promise.resolve({ data: packages, meta: { has_more: false } });
      return Promise.resolve(state(v));
    });
  }

  it("publishes the SELECTED package's own pinned values, with the version, reason and step-up", async () => {
    mockGrace(7);
    put.mockResolvedValue({ config_version: 8 });
    const { CheckoutGraceForm: GracePage } = await import("@/components/phase3/checkout-grace-form");
    render(<GracePage />);
    // the pinned attributes are displayed, not typed
    expect(await screen.findByText("4000 kbps")).toBeTruthy();
    expect(screen.getByText("1 h")).toBeTruthy();
    await userEvent.type(screen.getByLabelText("Confirm your password"), "pw");
    await userEvent.click(screen.getByRole("button", { name: /Publish policy/ }));

    await waitFor(() => expect(put).toHaveBeenCalled());
    const [path, body] = put.mock.calls[0];
    expect(path).toBe("/checkout-grace");
    // every scalar came from the package, so the policy cannot contradict what the package delivers
    expect(body.grace_package_revision_id).toBe("rev-1");
    expect(body.grace_down_kbps).toBe(4000);
    expect(body.grace_duration_seconds).toBe(3600);
    expect(body.expected_config_version).toBe(7);
    expect(body.reason_code).toBeTruthy();
    expect(body.password).toBe("pw");
    expect(await screen.findByRole("status")).toBeTruthy();
  });

  it("cannot publish without a package, and says why", async () => {
    mockGrace(0, []);
    const { CheckoutGraceForm: GracePage } = await import("@/components/phase3/checkout-grace-form");
    render(<GracePage />);
    // no package to choose: publishing is not offered at all, and the reason is explained
    expect(await screen.findByRole("alert")).toBeTruthy();
    expect(screen.queryByRole("button", { name: /Publish policy/ })).toBeNull();
    expect(put).not.toHaveBeenCalled();
  });

  it("does not offer publishing until a package is chosen", async () => {
    mockGrace(0, [{ ...pkg, selected: false }]);
    const { CheckoutGraceForm: GracePage } = await import("@/components/phase3/checkout-grace-form");
    render(<GracePage />);
    const btn = (await screen.findByRole("button", { name: /Publish policy/ })) as HTMLButtonElement;
    expect(btn.disabled).toBe(true);
    await userEvent.selectOptions(screen.getByLabelText("Grace package"), "rev-1");
    expect((screen.getByRole("button", { name: /Publish policy/ }) as HTMLButtonElement).disabled).toBe(false);
  });

  it("disables publishing for a role that may only read the policy", async () => {
    mockGrace(7);
    const { CheckoutGraceForm: GracePage } = await import("@/components/phase3/checkout-grace-form");
    render(<GracePage canWrite={false} />);
    const btn = (await screen.findByRole("button", { name: /Publish policy/ })) as HTMLButtonElement;
    expect(btn.disabled).toBe(true);
  });
});

describe("Concurrency contracts in the UI", () => {
  it("an alert changed by someone else reloads the queue instead of overwriting it", async () => {
    get.mockResolvedValue({
      data: [{
        audit_id: "a1", stay_id: "s1", lifecycle_version: 1, alert_code: "EMERGENCY_GRACE_USED",
        trigger: "EMERGENCY_GRACE", boundary_at: new Date().toISOString(),
        boundary_clock_suspect: false, created_at: new Date().toISOString(), state: "OPEN", seq: 1,
      }],
      meta: { has_more: false },
    });
    post.mockRejectedValue(Object.assign(new Error("state conflict"), { status: 409 }));
    const { OperationalAlertsView: AlertsPage } = await import("@/components/phase3/operational-alerts-view");
    render(<AlertsPage />);
    await userEvent.click(await screen.findByRole("button", { name: /Acknowledge alert/ }));
    // the operator is told what happened, and the queue is re-read rather than retried blindly
    expect(await screen.findByRole("status")).toBeTruthy();
    await waitFor(() => expect(get).toHaveBeenCalledTimes(2));
  });

  it("a policy published by someone else reloads instead of overwriting", async () => {
    get.mockImplementation((path: string) => {
      if (path === "/checkout-grace/packages") {
        return Promise.resolve({
          data: [{
            package_revision_id: "rev-1", package_code: "site-grace-pkg", revision_no: 1,
            service_plan_revision_id: "plan-1", service_plan_code: "site-grace-plan",
            down_kbps: 4000, up_kbps: 1500, data_quota_bytes: 524288000, device_limit: 2,
            device_limit_policy: "REJECT_NEW_DEVICE", grace_duration_seconds: 3600,
            settlement_mode: "NOT_REQUIRED", is_current: true, is_active: true, selected: true,
            service_plan_revision_no: 1, time_accounting_mode: "VALIDITY_WINDOW",
            end_mode: "GRACE_AFTER_CHECKOUT", policy_version: "CHECKOUT_GRACE_V1",
          }],
          meta: { has_more: false },
        });
      }
      return Promise.resolve({ published: true, config_version: 7, supported_device_policies: ["REJECT_NEW_DEVICE"] });
    });
    put.mockRejectedValue(Object.assign(new Error("version conflict"), { status: 409 }));
    const { CheckoutGraceForm: GracePage } = await import("@/components/phase3/checkout-grace-form");
    render(<GracePage />);
    await screen.findByText("4000 kbps");
    await userEvent.click(screen.getByRole("button", { name: /Publish policy/ }));
    expect(await screen.findByRole("alert")).toBeTruthy();
    // the page RELOADS the current policy instead of overwriting it (state + packages are re-read)
    await waitFor(() => expect(get.mock.calls.length).toBeGreaterThanOrEqual(4));
  });

});
