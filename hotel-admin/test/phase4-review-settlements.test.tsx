// Phase 4 (DARK) — Manual Review and the settlement browser.
//
// The assertions concentrate on the two ways these screens could mislead an operator: offering an action the
// backend will refuse, and implying a capability that does not exist. Both are worse than a broken screen,
// because both look like they worked.

import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import "@testing-library/jest-dom/vitest";
import { ManualReviewView } from "@/components/phase4/manual-review-view";
import { SettlementsView } from "@/components/phase4/settlements-view";

const get = vi.fn();
const post = vi.fn();
vi.mock("@/lib/api", async (orig) => {
  const actual = await (orig() as Promise<any>);
  return { ...actual, api: { get: (...a: any[]) => get(...a), post: (...a: any[]) => post(...a) } };
});

function route(map: Record<string, any>) {
  get.mockImplementation(async (path: string) => {
    if (!(path in map)) throw new Error(`unexpected GET ${path}`);
    return map[path];
  });
}

beforeEach(() => {
  get.mockReset();
  post.mockReset();
  post.mockResolvedValue({});
});
afterEach(() => vi.restoreAllMocks());

const ACTIONS = {
  actions: [
    { action: "CONFIRM_POSTED", terminal: true, needs_evidence: true, accepts_amount: false,
      summary: "the folio already shows it" },
    { action: "CONFIRM_NOT_POSTED_RETRY", terminal: true, needs_evidence: true, accepts_amount: false,
      summary: "nothing was posted; authorise one retry" },
    { action: "CREATE_REVERSAL", terminal: true, needs_evidence: true, accepts_amount: true,
      summary: "record a reversal ledger row" },
    { action: "ESCALATE", terminal: false, needs_evidence: false, accepts_amount: false,
      summary: "someone else must look" },
  ],
};

const QUEUE = {
  queue: [{
    posting_id: "p1", pms_interface_id: "i1", execution_state: "UNKNOWN", amount_minor: 1500,
    currency: "USD", currency_exponent: 2, latest_attempt_no: 1, latest_p_number: "42",
    latest_pa_as_status: null, outbox_state: "HELD_RECOVERY", review_version: 0,
    terminal_review_action: null, awaiting_manual_review: true, created_at: "2026-08-12T10:00:00Z",
  }],
};

const DETAIL = {
  posting: QUEUE.queue[0],
  pinned_evidence: {
    settlement_id: "s1", purchase_id: "pu1", stay_id: "st1", folio_id: "f1",
    connector_kind: "protel-fias", folio_identity_strategy: "UNIQUE_PER_STAY",
    interface_lifecycle_state: "ACTIVE", settlement_status: "REQUIRED", purchase_state: "AWAITING_SETTLEMENT",
  },
  attempts: [{ attempt_no: 1, p_number: "42", rn: "101", g_number: "7", outcome: "UNKNOWN",
    pa_as_status: null, sent_at: "2026-08-12T10:01:00Z", response_at: null }],
  review: { history: [], version: 0, terminal_action: null, escalation_count: 0,
    retry_authorized_attempt_no: null, retry_authorization_consumed: false },
  diagnostics: { attempt_count: 1, unknown_attempt_count: 1, has_unknown_history: true,
    interface_freshness_block: null },
  available_actions: ["CONFIRM_POSTED", "ESCALATE"],
  evidence_contract: { source_types: ["PMS_SCREEN", "PROVIDER_DASHBOARD"] },
  limitations: ["Programmatic PMS reversal is capability=false in v1."],
};

describe("manual review", () => {
  it("offers ONLY the actions the backend says are available", async () => {
    route({ "/financial-review/queue": QUEUE, "/financial-review/actions": ACTIONS,
      "/financial-review/postings/p1": DETAIL });
    render(<ManualReviewView />);
    await userEvent.click(await screen.findByRole("button", { name: /review/i }));

    const select = await screen.findByLabelText(/what did you establish/i);
    const offered = Array.from(select.querySelectorAll("option")).map((o) => o.getAttribute("value"));
    expect(offered).toContain("CONFIRM_POSTED");
    expect(offered).toContain("ESCALATE");
    // available_actions did not include these, so the screen must not offer them
    expect(offered).not.toContain("CREATE_REVERSAL");
    expect(offered).not.toContain("CONFIRM_NOT_POSTED_RETRY");
    // and there is no generic approve anywhere
    expect(screen.queryByRole("button", { name: /^approve$/i })).not.toBeInTheDocument();
  });

  it("shows the UNKNOWN evidence an operator has to decide on", async () => {
    route({ "/financial-review/queue": QUEUE, "/financial-review/actions": ACTIONS,
      "/financial-review/postings/p1": DETAIL });
    render(<ManualReviewView />);
    await userEvent.click(await screen.findByRole("button", { name: /review/i }));
    expect(await screen.findByText(/nobody knows whether the folio was charged/i)).toBeInTheDocument();
    expect(screen.getByText("protel-fias (ACTIVE)")).toBeInTheDocument();
    expect(screen.getByText(/programmatic pms reversal is capability=false/i)).toBeInTheDocument();
  });

  it("sends the decision with the version it was looking at, and never an actor", async () => {
    route({ "/financial-review/queue": QUEUE, "/financial-review/actions": ACTIONS,
      "/financial-review/postings/p1": DETAIL });
    let sent: any = null;
    post.mockImplementation(async (_p: string, body: any) => { sent = body; return {}; });
    render(<ManualReviewView />);
    await userEvent.click(await screen.findByRole("button", { name: /review/i }));
    await userEvent.selectOptions(await screen.findByLabelText(/what did you establish/i), "CONFIRM_POSTED");
    await userEvent.type(screen.getByLabelText(/^why$/i), "the folio shows the charge");
    await userEvent.selectOptions(screen.getByLabelText(/evidence source/i), "PMS_SCREEN");
    await userEvent.type(screen.getByLabelText(/reference to it/i), "folio screen 14:22");
    await userEvent.type(screen.getByLabelText(/your password/i), "hunter2");
    await userEvent.click(screen.getByRole("button", { name: /record decision/i }));

    await waitFor(() => expect(sent).not.toBeNull());
    expect(sent.action).toBe("CONFIRM_POSTED");
    expect(sent.expected_version).toBe(0);
    expect(sent.password).toBe("hunter2");
    expect(sent.evidence).toEqual({ source_type: "PMS_SCREEN", reference: "folio screen 14:22" });
    expect(Object.keys(sent)).not.toContain("actor");
  });

  it("is read-only without the financial-review permission", async () => {
    route({ "/financial-review/queue": QUEUE, "/financial-review/actions": ACTIONS,
      "/financial-review/postings/p1": DETAIL });
    render(<ManualReviewView canAct={false} />);
    await userEvent.click(await screen.findByRole("button", { name: /review/i }));
    await userEvent.selectOptions(await screen.findByLabelText(/what did you establish/i), "ESCALATE");
    expect(screen.getByRole("button", { name: /record decision/i })).toBeDisabled();
  });

  it("labels every control", async () => {
    route({ "/financial-review/queue": QUEUE, "/financial-review/actions": ACTIONS,
      "/financial-review/postings/p1": DETAIL });
    render(<ManualReviewView />);
    await userEvent.click(await screen.findByRole("button", { name: /review/i }));
    await screen.findByLabelText(/what did you establish/i);
    for (const el of [...screen.getAllByRole("button"), ...screen.getAllByRole("combobox"),
      ...screen.getAllByRole("textbox")]) {
      expect(el).toHaveAccessibleName();
    }
  });
});

const SETTLEMENTS = {
  settlements: [{
    settlement_id: "s1", purchase_id: "pu1", method: "ONLINE_PAYMENT", status: "SETTLED",
    purchase_state: "GRANTED", amount_minor: 1000, currency: "USD", currency_exponent: 2,
  }],
};
const SETTLEMENT_DETAIL = {
  settlement: SETTLEMENTS.settlements[0],
  payments: [{ payment_id: "x1", transaction_type: "CHARGE", status: "CAPTURED", provider: "test-double",
    amount_minor: 1000, currency: "USD", currency_exponent: 2, parent_transaction_id: null }],
  available_actions: [],
  note: "Refund and chargeback initiation are NOT available from this surface in Phase 4.",
};

describe("settlement browser", () => {
  it("shows the charge and offers no refund affordance", async () => {
    route({ "/financial-ops/settlements": SETTLEMENTS, "/financial-ops/settlements/s1": SETTLEMENT_DETAIL });
    render(<SettlementsView />);
    await userEvent.click(await screen.findByRole("button", { name: /open/i }));
    expect(await screen.findByText("CAPTURED")).toBeInTheDocument();
    for (const forbidden of [/refund/i, /chargeback/i, /reverse/i, /charge again/i]) {
      expect(screen.queryByRole("button", { name: forbidden })).not.toBeInTheDocument();
    }
    expect(screen.getByText(/not available from this surface/i)).toBeInTheDocument();
  });

  it("filters by status through the API rather than in the browser", async () => {
    route({
      "/financial-ops/settlements": SETTLEMENTS,
      "/financial-ops/settlements?status=MANUAL_REVIEW": { settlements: [] },
    });
    render(<SettlementsView />);
    // the status appears in the badge AND in the filter dropdown, so target the badge specifically
    await screen.findAllByText("SETTLED");
    await userEvent.selectOptions(screen.getByLabelText(/^status$/i), "MANUAL_REVIEW");
    expect(await screen.findByText(/no settlements match/i)).toBeInTheDocument();
  });

  it("never names a payment provider as live or supported", async () => {
    route({ "/financial-ops/settlements": SETTLEMENTS, "/financial-ops/settlements/s1": SETTLEMENT_DETAIL });
    render(<SettlementsView />);
    await userEvent.click(await screen.findByRole("button", { name: /open/i }));
    await screen.findByText("CAPTURED");
    const body = document.body.textContent ?? "";
    for (const name of ["Stripe", "Adyen", "Checkout.com", "PayPal", "Braintree"]) {
      expect(body).not.toContain(name);
    }
  });
});
