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
  const cfg = {
    grace_package_revision_id: null, grace_duration_seconds: 3600, grace_down_kbps: 4000,
    grace_up_kbps: 1500, grace_data_quota_bytes: 524288000, grace_device_limit: 2,
    grace_device_limit_policy: "REJECT_NEW_DEVICE", eligibility_window_seconds: 86400, config_version: 7,
  };

  it("publishes the COMPLETE policy with the version it read, a reason and a step-up", async () => {
    get.mockResolvedValue({ published: true, config_version: 7, supported_device_policies: ["REJECT_NEW_DEVICE"], policy: { ...cfg } });
    put.mockResolvedValue({ config_version: 8 });
    const { CheckoutGraceForm: GracePage } = await import("@/components/phase3/checkout-grace-form");
    render(<GracePage />);
    const duration = await screen.findByDisplayValue("3600");
    await userEvent.clear(duration);
    await userEvent.type(duration, "1800");
    await userEvent.click(screen.getByRole("button", { name: /Publish policy/ }));

    await waitFor(() => expect(put).toHaveBeenCalled());
    const [path, body] = put.mock.calls[0];
    expect(path).toBe("/checkout-grace");
    // every field travels together — the database publishes one coherent version
    for (const k of Object.keys(cfg)) expect(body).toHaveProperty(k);
    expect(body.grace_duration_seconds).toBe(1800);
    // and the publication is governed: the version read, a bounded reason, and a password confirmation
    expect(body.expected_config_version).toBe(7);
    expect(body.reason_code).toBeTruthy();
    expect(body).toHaveProperty("password");
    expect(await screen.findByRole("status")).toBeTruthy();
  });

  it("treats a site with no published policy as a starting point, not an error", async () => {
    get.mockResolvedValue({ published: false, config_version: 0, supported_device_policies: ["REJECT_NEW_DEVICE"] });
    const { CheckoutGraceForm: GracePage } = await import("@/components/phase3/checkout-grace-form");
    render(<GracePage />);
    expect(await screen.findByDisplayValue("3600")).toBeTruthy();
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("disables publishing for a role that may only read the policy", async () => {
    get.mockResolvedValue({ published: true, config_version: 7, supported_device_policies: ["REJECT_NEW_DEVICE"], policy: { ...cfg } });
    const { CheckoutGraceForm: GracePage } = await import("@/components/phase3/checkout-grace-form");
    render(<GracePage canWrite={false} />);
    const btn = await screen.findByRole("button", { name: /Publish policy/ });
    expect((btn as HTMLButtonElement).disabled).toBe(true);
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
    get.mockResolvedValue({
      published: true, config_version: 7, supported_device_policies: ["REJECT_NEW_DEVICE"],
      policy: {
        grace_package_revision_id: null, grace_duration_seconds: 3600, grace_down_kbps: 4000,
        grace_up_kbps: 1500, grace_data_quota_bytes: 524288000, grace_device_limit: 2,
        grace_device_limit_policy: "REJECT_NEW_DEVICE", eligibility_window_seconds: 86400, config_version: 7,
      },
    });
    put.mockRejectedValue(Object.assign(new Error("version conflict"), { status: 409 }));
    const { CheckoutGraceForm: GracePage } = await import("@/components/phase3/checkout-grace-form");
    render(<GracePage />);
    await screen.findByDisplayValue("3600");
    await userEvent.click(screen.getByRole("button", { name: /Publish policy/ }));
    expect(await screen.findByRole("alert")).toBeTruthy();
    await waitFor(() => expect(get).toHaveBeenCalledTimes(2));
  });

  it("only the device-limit policies the backend supports are offered", async () => {
    get.mockResolvedValue({
      published: false, config_version: 0,
      supported_device_policies: ["REJECT_NEW_DEVICE"],
    });
    const { CheckoutGraceForm: GracePage } = await import("@/components/phase3/checkout-grace-form");
    render(<GracePage />);
    const select = (await screen.findByLabelText("Device limit policy")) as HTMLSelectElement;
    expect(Array.from(select.options).map((o) => o.value)).toEqual(["REJECT_NEW_DEVICE"]);
  });
});
