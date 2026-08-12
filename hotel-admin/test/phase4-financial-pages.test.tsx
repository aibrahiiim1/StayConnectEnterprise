// Phase 4 (DARK) — financial operator surface.
//
// These tests are about the two things that would actually hurt if they were wrong:
//
//   1. the screens must not display anything they should not have, and must not invent affordances the
//      backend does not have (a "retry" button on a recovery screen would be a way to double-charge);
//   2. an operator must be able to understand and complete a reconciliation without guessing.
//
// The accessibility assertions are part of that second point rather than a separate concern: on the day
// this screen matters, someone is reading it under pressure, possibly with a screen reader, and every
// control needs a name.

import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import "@testing-library/jest-dom/vitest";
import { FinancialHealthView } from "@/components/phase4/financial-health-view";
import { FinancialRecoveryView } from "@/components/phase4/financial-recovery-view";

const HEALTHY = {
  outbox_queued: 0, outbox_in_flight: 0, outbox_held_recovery: 0, outbox_oldest_age_seconds: 0,
  postings_unknown: 0, review_queue_open: 0, review_oldest_age_seconds: 0,
  payments_created: 0, payments_pending: 0, payments_unknown: 0, payments_oldest_age_seconds: 0,
  settlements_required: 2, settlements_in_progress: 0, settlements_manual_review: 0, settlements_failed: 0,
  recovery_active: false, recovery_epoch: 1, recovery_holds_open: 0,
  payment_account_configured: true, provider_egress_enabled: false,
  status: "OK" as const, reasons: [] as string[],
};

// The client is mocked rather than fetch, matching the other page tests: these are assertions about what
// the SCREEN does with an answer, not about transport.
const get = vi.fn();
const post = vi.fn();
vi.mock("@/lib/api", async (orig) => {
  const actual = await (orig() as Promise<any>);
  return {
    ...actual,
    api: { get: (...a: any[]) => get(...a), post: (...a: any[]) => post(...a) },
  };
});

// route(map) answers each path from a table, and rejects anything not in it -- so a screen that starts
// calling something new fails loudly instead of silently rendering an empty state.
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
afterEach(() => {
  vi.restoreAllMocks();
});

describe("financial health", () => {
  it("leads with the status and explains every condition in words", async () => {
    route({
      "/financial-ops/health": {
        health: {
          ...HEALTHY,
          payments_unknown: 2,
          settlements_manual_review: 1,
          status: "ATTENTION_REQUIRED",
          reasons: ["UNKNOWN_OUTCOMES_AWAITING_REVIEW", "SETTLEMENTS_AWAITING_REVIEW"],
        },
      },
    });
    render(<FinancialHealthView />);
    expect(await screen.findByText("ATTENTION REQUIRED")).toBeInTheDocument();
    // the fixed backend code is never shown raw; the operator gets a sentence
    expect(screen.queryByText("UNKNOWN_OUTCOMES_AWAITING_REVIEW")).not.toBeInTheDocument();
    expect(screen.getByText(/nobody knows yet whether the money moved/i)).toBeInTheDocument();
    expect(screen.getByText(/waiting on a manual review decision/i)).toBeInTheDocument();
  });

  it("says plainly that provider egress is off and never implies a provider is live", async () => {
    route({ "/financial-ops/health": { health: HEALTHY } });
    render(<FinancialHealthView />);
    expect(await screen.findByText("Disabled (DARK)")).toBeInTheDocument();
    expect(screen.getByText(/No payment provider has been integrated or verified/i)).toBeInTheDocument();
    // no real provider is ever named on this screen
    const body = document.body.textContent ?? "";
    for (const name of ["Stripe", "Adyen", "Checkout.com", "PayPal", "Braintree"]) {
      expect(body).not.toContain(name);
    }
  });

  it("has an accessible name on every control", async () => {
    route({ "/financial-ops/health": { health: HEALTHY } });
    render(<FinancialHealthView />);
    await screen.findByText("OK");
    for (const el of screen.getAllByRole("button")) {
      expect(el).toHaveAccessibleName();
    }
  });

  it("reports a load failure to assistive technology rather than showing an empty screen", async () => {
    get.mockRejectedValue(new Error("edged unavailable"));
    render(<FinancialHealthView />);
    expect(await screen.findByRole("alert")).toBeInTheDocument();
  });
});

const HELD_STATUS = {
  recovery: { Epoch: 2, Reason: "RESTORE_DETECTED", Active: true, HeldTotal: 1, HeldOpen: 1,
    EnteredAt: "2026-08-12T10:00:00Z", ReleasedAt: "" },
};
const HELD_HOLDS = {
  holds: [{ hold_id: "h1", work_kind: "PAYMENT_TRANSACTION", work_id: "w1", held_status: "PENDING",
    amount_minor: 1000, currency: "USD", held_at: "2026-08-12T10:00:00Z" }],
};

describe("financial recovery", () => {
  it("says nothing was replayed and offers no way to replay anything", async () => {
    route({
      "/financial-ops/recovery": HELD_STATUS,
      "/financial-ops/recovery/holds": HELD_HOLDS,
    });
    render(<FinancialRecoveryView />);
    expect(await screen.findByText("FINANCIAL RECOVERY")).toBeInTheDocument();
    expect(screen.getByText(/Nothing has been replayed and nothing will be/i)).toBeInTheDocument();
    // the affordances that must NOT exist
    for (const forbidden of [/retry/i, /resend/i, /re-send/i, /replay/i, /resume now/i, /force/i]) {
      expect(screen.queryByRole("button", { name: forbidden })).not.toBeInTheDocument();
    }
  });

  it("tells the operator that guest access is unaffected", async () => {
    route({
      "/financial-ops/recovery": HELD_STATUS,
      "/financial-ops/recovery/holds": HELD_HOLDS,
    });
    render(<FinancialRecoveryView />);
    expect(await screen.findByText(/Guest internet access is unaffected/i)).toBeInTheDocument();
  });

  it("refuses to submit a decision the operator has not actually made", async () => {
    route({
      "/financial-ops/recovery": HELD_STATUS,
      "/financial-ops/recovery/holds": HELD_HOLDS,
    });
    render(<FinancialRecoveryView />);
    const btn = await screen.findByRole("button", { name: /record/i });
    await userEvent.click(btn);
    expect(await screen.findByRole("alert")).toHaveTextContent(/choose what you established/i);
    expect(post).not.toHaveBeenCalled();
  });

  it("sends the chosen conclusion, the evidence and the password, and never an actor", async () => {
    let sent: any = null;
    let sentPath = "";
    route({
      "/financial-ops/recovery": HELD_STATUS,
      "/financial-ops/recovery/holds": HELD_HOLDS,
    });
    post.mockImplementation(async (path: string, body: any) => {
      sentPath = path;
      sent = body;
      return { resolved: true };
    });
    render(<FinancialRecoveryView />);
    await screen.findByText("FINANCIAL RECOVERY");

    await userEvent.type(screen.getByLabelText(/your password/i), "hunter2");
    await userEvent.selectOptions(
      screen.getByLabelText(/conclusion for this payment/i),
      "CONFIRMED_NOT_COMPLETED",
    );
    await userEvent.type(
      screen.getByLabelText(/evidence for this decision/i),
      "provider dashboard shows no charge",
    );
    await userEvent.click(screen.getByRole("button", { name: /record/i }));

    await waitFor(() => expect(sent).not.toBeNull());
    expect(sentPath).toBe("/financial-ops/recovery/holds/h1/resolve");
    expect(sent).toEqual({
      resolution: "CONFIRMED_NOT_COMPLETED",
      note: "provider dashboard shows no charge",
      password: "hunter2",
    });
    // the author comes from the session, never the request
    expect(Object.keys(sent)).not.toContain("actor");
    expect(Object.keys(sent)).not.toContain("operator_id");
  });

  it("offers release only once nothing is left to reconcile", async () => {
    route({
      "/financial-ops/recovery": HELD_STATUS,
      "/financial-ops/recovery/holds": HELD_HOLDS,
    });
    const { unmount } = render(<FinancialRecoveryView />);
    await screen.findByText("FINANCIAL RECOVERY");
    expect(screen.queryByRole("button", { name: /release financial recovery/i })).not.toBeInTheDocument();
    unmount();

    route({
      "/financial-ops/recovery": { recovery: { ...HELD_STATUS.recovery, HeldOpen: 0 } },
      "/financial-ops/recovery/holds": { holds: [] },
    });
    render(<FinancialRecoveryView />);
    expect(await screen.findByRole("button", { name: /release financial recovery/i })).toBeInTheDocument();
  });

  it("is read-only for an operator without the financial-review permission", async () => {
    route({
      "/financial-ops/recovery": HELD_STATUS,
      "/financial-ops/recovery/holds": HELD_HOLDS,
    });
    render(<FinancialRecoveryView canAct={false} />);
    const btn = await screen.findByRole("button", { name: /record/i });
    expect(btn).toBeDisabled();
  });

  it("labels every control, including the ones inside the table", async () => {
    route({
      "/financial-ops/recovery": HELD_STATUS,
      "/financial-ops/recovery/holds": HELD_HOLDS,
    });
    render(<FinancialRecoveryView />);
    await screen.findByText("FINANCIAL RECOVERY");
    for (const el of [
      ...screen.getAllByRole("button"),
      ...screen.getAllByRole("combobox"),
      ...screen.getAllByRole("textbox"),
    ]) {
      expect(el).toHaveAccessibleName();
    }
    // the password field is not exposed as a textbox role, so it is checked by its label
    expect(screen.getByLabelText(/your password/i)).toHaveAttribute("type", "password");
  });

  it("shows a plain non-recovery state rather than an empty page", async () => {
    route({
      "/financial-ops/recovery": {
        recovery: { Epoch: 1, Reason: "INITIAL", Active: false, HeldTotal: 0, HeldOpen: 0,
          EnteredAt: "", ReleasedAt: "" },
      },
      "/financial-ops/recovery/holds": { holds: [] },
    });
    render(<FinancialRecoveryView />);
    expect(await screen.findByText("NOT IN RECOVERY")).toBeInTheDocument();
  });
});
