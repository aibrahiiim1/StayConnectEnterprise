import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";

class ResizeObserverStub { observe() {} unobserve() {} disconnect() {} }
(globalThis as any).ResizeObserver = (globalThis as any).ResizeObserver ?? ResizeObserverStub;

vi.mock("@/lib/api", async (orig) => {
  const actual = await (orig() as Promise<Record<string, unknown>>);
  return { ...actual, api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), del: vi.fn() } };
});

import { api } from "@/lib/api";
import { CardCasesCard } from "@/components/phase4/card-cases";

const g = api.get as unknown as ReturnType<typeof vi.fn>;
const post = api.post as unknown as ReturnType<typeof vi.fn>;
const open = { id: "11111111-2222-3333-4444-555555555555", kind: "CHARGEBACK", settlement_id: "s1", charge_transaction_id: "c1",
  provider_ref: "ledger", opened_at: "2026-10-12T08:00:00Z", amount_minor: 100, currency: "USD", entitlement_status: "ACTIVE" };
const decided = { ...open, id: "aaaaaaaa-2222-3333-4444-555555555555", kind: "DISPUTE", provider_ref: "dp_1",
  resolution: "KEEP_ACCESS", resolved_at: "2026-10-12T09:00:00Z", resolved_by: "payments:alice", resolution_reason: "won" };
const refund = { entitlement_id: "e1e1e1e1-0000-0000-0000-000000000000", settlement_id: "s2", captured_minor: 100, refunded_minor: 100, currency: "USD", terminated_at: "2026-10-12T07:00:00Z" };

beforeEach(() => { vi.clearAllMocks(); g.mockResolvedValue({ cases: [open, decided], refund_terminations: [refund] }); });

describe("Card disputes and chargebacks", () => {
  it("ends access only after an explicit decision with a reason and the password", async () => {
    post.mockResolvedValue({ resolution: "END_ACCESS" });
    render(<CardCasesCard canAct />);
    fireEvent.click(await screen.findByRole("button", { name: "End access" }));
    fireEvent.change(screen.getByLabelText("Reason"), { target: { value: "chargeback lost" } });
    fireEvent.change(screen.getByLabelText("Confirm your password"), { target: { value: "pw" } });
    fireEvent.click(screen.getAllByRole("button", { name: "End access" }).pop()!);
    await waitFor(() => expect(post).toHaveBeenCalledWith(`/financial-review/card-cases/${open.id}/resolve`,
      { resolution: "END_ACCESS", reason: "chargeback lost", password: "pw" }));
  });

  it("shows decided cases and refund terminations, and no actions to a role that may not act", async () => {
    render(<CardCasesCard canAct={false} />);
    expect(await screen.findByText("Access kept")).toBeTruthy();
    expect(screen.getByText(/ended by a full provider refund/i)).toBeTruthy();
    expect(screen.queryByRole("button", { name: "End access" })).toBeNull();
    expect(screen.getByText("Waiting for a decision")).toBeTruthy();
  });
});
