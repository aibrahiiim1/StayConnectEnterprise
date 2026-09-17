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
// ApiError is part of the module's RUNTIME surface, not just its types: ErrorBanner does
// `err instanceof ApiError` to decide whether to show a trace id. A mock that omits it makes that check
// throw, React unmounts the tree, and the page renders as an empty div — which is what happened here, and
// it looked like the error state was missing rather than the mock being incomplete.
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

describe("Stays page", () => {
  it("shows a stay with its guest, dates and sharing count, and opens details", async () => {
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
    expect(screen.getByText(/1 sharing/)).toBeTruthy();
    expect(screen.getByText(/Closed/)).toBeTruthy();

    await userEvent.click(screen.getByRole("button", { name: "View" }));
    expect(await screen.findByText("Byron, Ada")).toBeTruthy();
    expect(screen.getByText("Main guest")).toBeTruthy();
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
  // IT NO LONGER DEFAULTS TO THE REVIEW QUEUE, and that is the fix rather than a regression.
  //
  // The default filter was MANUAL_REVIEW, so the normal and healthy state of this screen was an empty table
  // reading "Nothing to review" -- which an operator checking whether the PMS feed is alive reads as "the feed
  // is dead". It now opens on everything the feed has sent, and the review queue is called out above the table
  // whenever it is non-empty. What this test pins is that the bounded review code is still surfaced.
  it("opens on the whole feed and still surfaces the bounded reason an event was refused", async () => {
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
    // No status filter on the first load: the screen shows what the PMS has actually sent.
    expect(get.mock.calls[0][0]).not.toContain("processing_status=");
    // The review code is rendered de-underscored and lower-cased, as every other bounded code in the UI now is.
    expect(await screen.findByText(/folio claimed by other stay/i)).toBeTruthy();
    // And the queue is announced rather than being the only thing visible.
    expect(screen.getByText(/need a decision/i)).toBeTruthy();
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

// THE CHECKOUT GRACE TESTS THAT LIVED HERE DESCRIBED A WORKFLOW THAT NO LONGER EXISTS.
//
// They asserted that the operator SELECTS a package revision, that publishing without one is refused, and
// that the publish control stays disabled until a package is chosen. That workflow was a dead end: the
// checkout validator only ever accepts a package derived from the policy, and the operator publisher refuses
// to create the reserved system package those tests assumed somebody had prepared. On a site with none --
// PRE-LIVE among them -- the page offered nothing to select and told the operator to visit a catalog that
// could not help.
//
// The operator now authors the POLICY and the system derives the package. The equivalent claims, and the
// whole journey from an unconfigured site to a published second version, are performed in
// test/checkout-grace-self-service.test.tsx: what is actually SENT on publish, the read-only role, and the
//409-reloads-rather-than-overwrites contract.


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

  // The grace 409 contract moved with the workflow it belongs to: it is asserted against the real publish
  // journey (author -> review -> confirm) in test/checkout-grace-self-service.test.tsx. The version that was
  // here drove a package selector that no longer exists.

});
