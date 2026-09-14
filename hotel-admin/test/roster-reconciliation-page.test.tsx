import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";

// ROSTER RECONCILIATION — that the screen offers no repair, and says the right thing about the exception.
//
// This page shipped with two action buttons and a count that contradicted the dashboard. An operator opened
// it and saw "Close 0 stays", "Answer the snapshot cases", and "1 recorded departure are still waiting for
// an answer" beside a dashboard demanding decisions about 424 messages. Every one of those numbers was
// either already answered or not answerable here.
//
// So what is asserted is the absence: reconciliation runs itself, the snapshot artifacts were answered once
// and cannot recur, and the ONLY control left is the settings the hotel may legitimately change. A test that
// checked the happy path would not have caught any of it.

const get = vi.fn();
const put = vi.fn();
vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return { ...actual, api: { get: (...a: any[]) => get(...a), put: (...a: any[]) => put(...a) } };
});

const STATE = {
  blockers: [
    {
      blocker: "DEPARTURE_FOR_UNKNOWN_STAY",
      since: "2026-08-23T11:46:15Z",
      detail:
        "HISTORICAL EXCEPTION, not a fault and not something that will clear on its own. 1 departure(s) " +
        "from before this appliance had a complete picture of the property name a reservation it never " +
        "received an arrival for. Guests are unaffected.",
      guests_affected: false,
    },
  ],
  connection_settings: {
    backoff_min_ms: 500,
    backoff_max_ms: 30000,
    stable_reset_seconds: 60,
    link_down_alert_seconds: 900,
    blocked_after_refusals: 3,
    config_version: 0,
    is_default: true,
  },
  settings: {
    roster_trust_min: 50,
    inventory_tolerance: 2,
    inventory_lookback: 10,
    max_close_per_run: 500,
    config_version: 0,
    is_default: true,
  },
  generation: 261,
  preview: {
    outcome: "COMPLETED",
    roster_size: 477,
    mirror_in_house: 477,
    absent_from_roster: 0,
    stays_closed: 0,
    rooms_enumerated: 587,
    rooms_expected: 587,
    protected_by_newer_events: 0,
  },
  undisposed_cases: 0,
  pms_interface_id: "iface-1",
};

beforeEach(() => {
  get.mockReset();
  put.mockReset();
  get.mockImplementation((path: string) =>
    path.endsWith("/runs") ? Promise.resolve({ runs: [] }) : Promise.resolve(STATE),
  );
});

async function renderPage() {
  const Page = (await import("@/app/(app)/roster-reconciliation/page")).default;
  render(<Page />);
  await waitFor(() => expect(get).toHaveBeenCalled());
}

describe("roster reconciliation offers no repair", () => {
  it("has no control that closes stays and none that answers snapshot cases", async () => {
    await renderPage();
    await waitFor(() => expect(screen.getByText(/what the next automatic run will do/i)).toBeTruthy());

    // The two repair buttons that used to be here. Matched loosely on purpose: any button whose label
    // offers to close stays or answer cases is the thing being forbidden, whatever the wording.
    const buttons = screen.queryAllByRole("button").map((b) => b.textContent ?? "");
    expect(buttons.some((t) => /close\s+\d*\s*stay/i.test(t))).toBe(false);
    expect(buttons.some((t) => /answer the snapshot/i.test(t))).toBe(false);

    // And the one control that must remain, because a hotel may legitimately change it.
    expect(buttons.some((t) => /save recovery settings/i.test(t))).toBe(true);
  });

  it("states that reconciliation happens on its own", async () => {
    await renderPage();
    await waitFor(() =>
      expect(screen.getByText(/happens on its own when the PMS publishes a complete roster/i)).toBeTruthy(),
    );
  });

  it("reports zero outstanding snapshot artifacts rather than counting the external exception", async () => {
    await renderPage();
    await waitFor(() => expect(screen.getByText(/none outstanding/i)).toBeTruthy());
    // The historical exception is a BLOCKER, never a snapshot artifact waiting for a bulk answer.
    expect(screen.queryByText(/still to answer/i)).toBeNull();
  });

  it("shows the historical exception as an exception, and says guests are unaffected", async () => {
    await renderPage();
    await waitFor(() => expect(screen.getByText(/departure for unknown stay/i)).toBeTruthy());
    expect(screen.getByText(/guests not affected/i)).toBeTruthy();
    expect(screen.getByText(/HISTORICAL EXCEPTION/i)).toBeTruthy();
  });
});
