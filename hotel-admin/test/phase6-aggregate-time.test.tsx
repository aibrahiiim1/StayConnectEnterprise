// Phase 6 (DARK) — the online-time budget view.
//
// The risk on this screen is not a crash, it is a confident wrong number read out to a guest. So the tests
// are about truthfulness: both clocks present, an ended budget not shown as if it still had time, internal
// cause codes never displayed raw, and no guest identity anywhere.

import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { render, screen } from "@testing-library/react";
import "@testing-library/jest-dom/vitest";
import { AggregateTimeView } from "@/components/phase6/aggregate-time-view";

const get = vi.fn();
vi.mock("@/lib/api", async (orig) => {
  const actual = await (orig() as Promise<any>);
  return { ...actual, api: { get: (...a: any[]) => get(...a) } };
});

function rows(data: any[]) {
  get.mockImplementation(async (path: string) => {
    if (path !== "/sessions/aggregate-time") throw new Error(`unexpected GET ${path}`);
    return { data };
  });
}

beforeEach(() => get.mockReset());
afterEach(() => vi.restoreAllMocks());

const live = {
  entitlement_id: "e1", status: "ACTIVE", budget_seconds: 7200, consumed_seconds: 5400,
  remaining_seconds: 1800, hard_expiry: "2026-08-20T12:00:00Z", live_devices: 2,
};

describe("online-time budgets", () => {
  it("shows the time left AND the end date, because either one can end the access", async () => {
    rows([live]);
    render(<AggregateTimeView />);
    expect(await screen.findByTestId("remaining")).toHaveTextContent("30 min");
    expect(screen.getByTestId("expiry")).not.toHaveTextContent("No end date");
    expect(screen.getByText(/counts down only while a device is actually connected/i)).toBeInTheDocument();
    expect(screen.getByText(/any time left at that point is lost/i)).toBeInTheDocument();
  });

  it("does not show an ended budget as if it still had time", async () => {
    rows([{ ...live, status: "TERMINATED", remaining_seconds: 0,
            terminal_cause: "AGGREGATE_ONLINE_TIME_EXHAUSTED" }]);
    render(<AggregateTimeView />);
    expect(await screen.findByTestId("remaining")).toHaveTextContent("—");
    expect(screen.getByText(/Used all of its online time/i)).toBeInTheDocument();
  });

  it("translates every cause code instead of showing it raw", async () => {
    rows([
      { ...live, entitlement_id: "a", status: "TERMINATED", terminal_cause: "AGGREGATE_OUTER_WINDOW_EXPIRED" },
      { ...live, entitlement_id: "b", status: "TERMINATED", terminal_cause: "VALIDITY_WINDOW_ELAPSED" },
    ]);
    render(<AggregateTimeView />);
    expect(await screen.findByText(/Reached its end date with time still unused/i)).toBeInTheDocument();
    expect(screen.getByText(/Its validity period ended/i)).toBeInTheDocument();
    const body = document.body.textContent ?? "";
    for (const code of ["AGGREGATE_OUTER_WINDOW_EXPIRED", "VALIDITY_WINDOW_ELAPSED", "AGGREGATE_ONLINE_TIME"]) {
      expect(body).not.toContain(code);
    }
  });

  it("says plainly when no package uses a budget, rather than showing an empty table", async () => {
    rows([]);
    render(<AggregateTimeView />);
    expect(await screen.findByTestId("empty")).toHaveTextContent(/No package on this property uses/i);
  });

  it("exposes no guest identity", async () => {
    rows([{ ...live }]);
    render(<AggregateTimeView />);
    await screen.findByTestId("remaining");
    const body = (document.body.textContent ?? "").toLowerCase();
    for (const leak of ["room", "stay", "guest name", "mac", "pms"]) {
      expect(body).not.toContain(leak);
    }
  });
});
