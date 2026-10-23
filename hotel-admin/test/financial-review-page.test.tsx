import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen } from "@testing-library/react";

class ResizeObserverStub { observe() {} unobserve() {} disconnect() {} }
(globalThis as any).ResizeObserver = (globalThis as any).ResizeObserver ?? ResizeObserverStub;

vi.mock("@/lib/api", async (orig) => {
  const actual = await (orig() as Promise<Record<string, unknown>>);
  return { ...actual, api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), del: vi.fn() } };
});
vi.mock("@/components/phase4/manual-review-view", () => ({ ManualReviewView: () => <div data-testid="review-queue" /> }));

import { api, ApiError } from "@/lib/api";
import FinancialReviewPage from "@/app/(app)/financial-review/page";

const g = api.get as unknown as ReturnType<typeof vi.fn>;

beforeEach(() => vi.clearAllMocks());

describe("Financial review page", () => {
  it("shows only the card cases when the review queue is not mounted", async () => {
    g.mockImplementation(async (path: string) => {
      if (path === "/auth/whoami") return { roles: ["payments_operator"] };
      if (path === "/financial-review/queue") throw new ApiError(404, { error: "not_found" });
      return { cases: [], refund_terminations: [] };
    });
    render(<FinancialReviewPage />);
    expect(await screen.findByTestId("review-not-mounted")).toBeTruthy();
    expect(screen.queryByTestId("review-queue")).toBeNull();
  });

  it("shows the review queue when it is mounted", async () => {
    g.mockImplementation(async (path: string) => {
      if (path === "/auth/whoami") return { roles: ["payments_operator"] };
      if (path === "/financial-review/queue") return { queue: [] };
      return { cases: [], refund_terminations: [] };
    });
    render(<FinancialReviewPage />);
    expect(await screen.findByTestId("review-queue")).toBeTruthy();
    expect(screen.queryByTestId("review-not-mounted")).toBeNull();
  });
});
