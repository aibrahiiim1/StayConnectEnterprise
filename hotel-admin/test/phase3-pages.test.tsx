import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
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
          posting_block_reason: "NOT_IN_HOUSE", posting_permission_source: "PMS_FEED",
          posting_blocks: [],
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
    expect(screen.getByText(/Not allowed/)).toBeTruthy();

    await userEvent.click(screen.getByRole("button", { name: "View" }));
    expect(await screen.findByText("Byron, Ada")).toBeTruthy();
    expect(screen.getByText("Main guest")).toBeTruthy();
    // No folios any more: the stay shows room charge permission in words, with who decided it.
    expect(screen.queryByText(/folio/i)).toBeNull();
    const section = screen.getByRole("region", { name: "Room charge" });
    expect(within(section).getByText("The guest is not in house")).toBeTruthy();
    expect(within(section).getByText(/Decided by: PMS guest data/)).toBeTruthy();
    expect(within(section).getByText(/never been blocked/i)).toBeTruthy();
  });

  it("reports a load failure instead of rendering an empty table as if all were well", async () => {
    get.mockRejectedValue(new Error("boom"));
    const { default: StaysPage } = await import("@/app/(app)/stays/page");
    render(<StaysPage />);
    expect(await screen.findByRole("alert")).toBeTruthy();
  });
});

describe("Stays page — room charge on a stay", () => {
  // Phase-0 Amendment A1: only ADMIN_BLOCK is an operator's. PMS_NO_POST / PMS_DATA_SUSPECT clear from fresh
  // Protel data and POSTING_UNRESOLVED from the charge's manual review, so the page never offers to lift them.
  const STAY = {
    id: "s7", pms_interface_id: "i1", external_reservation_id: "G5000", room: "205",
    status: "IN_HOUSE", lifecycle_version: 3, posting_allowed: false, occupants: 1,
  };
  const ROOM_CHARGE_CAPS = {
    surfaces: ["pms-stays", "pms-financial-onboarding"],
    modules: { room_charge: { deployed: true, licensed: true, enabled: true, ready: false, effective: false, manageable: true } },
  };

  function routes(detail: Record<string, unknown>, roles: string[], caps: unknown = ROOM_CHARGE_CAPS) {
    get.mockImplementation((path: string) => {
      if (path === "/auth/whoami") return Promise.resolve({ roles });
      if (path === "/capabilities") return Promise.resolve(caps);
      if (path.startsWith("/pms-stays/")) return Promise.resolve({ ...STAY, occupant_list: [], ...detail });
      return Promise.resolve({ data: [{ ...STAY, ...detail }], meta: { has_more: false } });
    });
  }

  async function openStay() {
    const { default: StaysPage } = await import("@/app/(app)/stays/page");
    render(<StaysPage />);
    await userEvent.click(await screen.findByRole("button", { name: "View" }));
    return await screen.findByRole("region", { name: "Room charge" });
  }

  it("never offers to lift a PMS or unresolved-charge block, and says who clears it", async () => {
    routes({
      posting_block_reason: "POSTING_UNRESOLVED", posting_permission_source: "POSTING_LEDGER",
      posting_blocks: [
        { reason: "POSTING_UNRESOLVED", source: "POSTING_LEDGER", created_at: "2026-09-29T09:00:00Z" },
        { reason: "PMS_NO_POST", source: "PMS_ANSWER", pa_as_status: "NP", created_at: "2026-09-29T08:00:00Z" },
        { reason: "PMS_DATA_SUSPECT", source: "PMS_ANSWER", pa_as_status: "NG", created_at: "2026-09-28T08:00:00Z",
          cleared_at: "2026-09-28T09:00:00Z", cleared_by_source: "PMS_FEED" },
      ],
    }, ["site_admin"]);
    const section = await openStay();
    expect(within(section).getAllByText(/waiting for manual review/i).length).toBeGreaterThan(0);
    expect(within(section).getByText(/no-post restriction/i)).toBeTruthy();
    expect(within(section).getByText(/clears only when the charge is decided in Manual review/i)).toBeTruthy();
    expect(within(section).getByText(/clears only when fresh Protel data allows posting again/i)).toBeTruthy();
    expect(within(section).getByText("Earlier blocks")).toBeTruthy();
    // The site admin may SET an administrative block, but nothing offers to remove these.
    expect(within(section).getByRole("button", { name: /block room charge/i })).toBeTruthy();
    expect(within(section).queryByRole("button", { name: /remove|lift|clear|unblock/i })).toBeNull();
  });

  it("sets an administrative block with a reason and the password", async () => {
    routes({ posting_allowed: true, posting_permission_source: "PMS_FEED", posting_blocks: [] }, ["site_admin"]);
    post.mockResolvedValue({ result: "SET" });
    const section = await openStay();
    await userEvent.click(within(section).getByRole("button", { name: /block room charge/i }));
    await userEvent.type(await screen.findByLabelText(/^Reason/), "Guest settles in cash");
    await userEvent.type(screen.getByLabelText(/^Confirm your password/), "pw");
    const dialogs = screen.getAllByRole("dialog");
    await userEvent.click(within(dialogs.at(-1)!).getByRole("button", { name: "Block room charge" }));
    await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
    expect(post).toHaveBeenCalledWith("/pms-financial-onboarding/stays/s7/admin-block", {
      action: "SET", reason: "Guest settles in cash", password: "pw",
    });
  });

  it("removes an active administrative block with action CLEAR", async () => {
    routes({
      posting_block_reason: "ADMIN_BLOCK", posting_permission_source: "OPERATOR",
      posting_blocks: [{ reason: "ADMIN_BLOCK", source: "OPERATOR", note: "Guest settles in cash", created_at: "2026-09-29T09:00:00Z" }],
    }, ["site_admin"]);
    post.mockResolvedValue({ result: "CLEARED" });
    const section = await openStay();
    expect(within(section).getAllByText("Blocked by an administrator").length).toBeGreaterThan(0);
    expect(within(section).queryByRole("button", { name: /^block room charge/i })).toBeNull();
    await userEvent.click(within(section).getByRole("button", { name: /remove administrative block/i }));
    await userEvent.type(await screen.findByLabelText(/^Reason/), "Cleared with Front Office");
    await userEvent.type(screen.getByLabelText(/^Confirm your password/), "pw");
    const dialogs = screen.getAllByRole("dialog");
    await userEvent.click(within(dialogs.at(-1)!).getByRole("button", { name: "Remove administrative block" }));
    await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
    expect(post.mock.calls[0]).toEqual([
      "/pms-financial-onboarding/stays/s7/admin-block",
      { action: "CLEAR", reason: "Cleared with Front Office", password: "pw" },
    ]);
  });

  it("says when a room charge is already in progress for the stay", async () => {
    routes({ posting_allowed: true, posting_permission_source: "PMS_FEED", posting_blocks: [], room_charge_open: true }, ["site_admin"]);
    const section = await openStay();
    expect(within(section).getByTestId("room-charge-open").textContent).toMatch(/pending, being sent or under review/i);
  });

  it("will not submit an administrative block reason shorter than the backend accepts", async () => {
    routes({ posting_allowed: true, posting_permission_source: "PMS_FEED", posting_blocks: [] }, ["site_admin"]);
    const section = await openStay();
    await userEvent.click(within(section).getByRole("button", { name: /block room charge/i }));
    await userEvent.type(await screen.findByLabelText(/^Reason/), "abc");
    await userEvent.type(screen.getByLabelText(/^Confirm your password/), "pw");
    const dialogs = screen.getAllByRole("dialog");
    expect(within(dialogs.at(-1)!).getByRole("button", { name: "Block room charge" })).toBeDisabled();
    expect(post).not.toHaveBeenCalled();
  });

  it("offers no block action to a role that is not site_admin", async () => {
    routes({ posting_allowed: true, posting_blocks: [] }, ["front_desk"]);
    const section = await openStay();
    expect(within(section).queryByRole("button")).toBeNull();
  });

  it("offers no block action where room charge is not licensed", async () => {
    routes({ posting_allowed: true, posting_blocks: [] }, ["site_admin"], { surfaces: ["pms-stays"], modules: {} });
    const section = await openStay();
    expect(within(section).getByText(/charge goes to this guest.s reservation/i)).toBeTruthy();
    expect(within(section).queryByRole("button")).toBeNull();
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
      meta: { has_more: false }, page: 1, page_size: 50, total: 1,
      // edged counts every message for the tiles, whatever the filter.
      summary: { applied: 0, pending: 0, manual_review: 1, rejected: 0, unmatched: 1 },
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
