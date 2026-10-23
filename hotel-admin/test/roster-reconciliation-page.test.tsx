import { describe, it, expect, vi, beforeEach } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";

// Reconciliation runs automatically; this screen must not expose state-changing repair actions.

const get = vi.fn();
const put = vi.fn();
const post = vi.fn();
vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return {
    ...actual,
    api: {
      get: (...a: any[]) => get(...a), put: (...a: any[]) => put(...a), post: (...a: any[]) => post(...a),
    },
  };
});
let roles: string[] = [];

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
  last_run_mode: "APPLY",
  last_run_at: "2026-10-15T08:00:00Z",
  last_run: {
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
  post.mockReset();
  roles = [];
  get.mockImplementation((path: string) =>
    path === "/auth/whoami" ? Promise.resolve({ roles })
      : path.endsWith("/runs") ? Promise.resolve({ runs: [] }) : Promise.resolve(STATE),
  );
});

async function renderPage() {
  const Page = (await import("@/app/(app)/roster-reconciliation/page")).default;
  render(<Page />);
  await waitFor(() => expect(get).toHaveBeenCalled());
}

describe("roster reconciliation offers no state-changing action", () => {
  it("has no control that closes stays and none that answers snapshot cases", async () => {
    await renderPage();
    await waitFor(() => expect(screen.getByText(/what the last run decided/i)).toBeTruthy());

    const buttons = screen.queryAllByRole("button").map((b) => b.textContent ?? "");
    expect(buttons.some((t) => /close\s+\d*\s*stay/i.test(t))).toBe(false);
    expect(buttons.some((t) => /answer the snapshot/i.test(t))).toBe(false);

    expect(buttons.some((t) => /save recovery settings/i.test(t))).toBe(false);
    expect(screen.getByText(/configured with the connection itself/i)).toBeTruthy();
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
    expect(screen.queryByText(/still to answer/i)).toBeNull();
  });

  it("shows the historical exception as an exception, and says guests are unaffected", async () => {
    await renderPage();
    await waitFor(() => expect(screen.getByText(/departure for unknown stay/i)).toBeTruthy());
    expect(screen.getByText(/guests not affected/i)).toBeTruthy();
    expect(screen.getAllByText(/HISTORICAL EXCEPTION/i).length).toBeGreaterThan(0);
  });

  // Historical exceptions require intervention; only operational blockers may clear themselves.
  it("never tells the operator the historical exception will clear itself", async () => {
    await renderPage();
    await waitFor(() => expect(screen.getAllByText(/HISTORICAL EXCEPTION/i).length).toBeGreaterThan(0));
    expect(screen.queryByText(/these clear themselves/i)).toBeNull();
    expect(screen.getByText(/historical exception — for information/i)).toBeTruthy();
  });

  it("says the page needs no manual action, and what it is for", async () => {
    await renderPage();
    await waitFor(() => expect(screen.getAllByText(/happens on its own/i).length).toBeGreaterThan(0));
    fireEvent.click(screen.getByRole("button", { name: "Tips: Roster reconciliation" }));
    expect(await screen.findByText(/no queue to work through/i)).toBeTruthy();
  });

  // Opening a read-only page must not create a reconciliation run row.
  it("opening the page runs nothing", async () => {
    await renderPage();
    await waitFor(() => expect(screen.getByText(/what the last run decided/i)).toBeTruthy());
    expect(post).not.toHaveBeenCalled();
    expect(put).not.toHaveBeenCalled();
    expect(screen.queryByRole("button", { name: /run dry run/i })).toBeNull();
  });

  it("the integration owner can run a dry run explicitly", async () => {
    roles = ["hotel_it_manager"];
    post.mockResolvedValue({ ...STATE.last_run, absent_from_roster: 3 });
    await renderPage();
    const btn = await screen.findByRole("button", { name: /run dry run/i });
    fireEvent.click(btn);
    await waitFor(() => expect(post).toHaveBeenCalledWith("/pms-roster-reconciliation/preview",
      expect.objectContaining({ pms_interface_id: "iface-1" })));
    expect(await screen.findByText(/what a run would do now/i)).toBeTruthy();
  });

  it("changing the trust settings sends the version, a reason and the password", async () => {
    roles = ["hotel_it_manager"];
    put.mockResolvedValue({ config_version: 1 });
    await renderPage();
    fireEvent.click(await screen.findByRole("button", { name: "Change" }));
    fireEvent.change(screen.getByLabelText(/smallest roster acted on/i), { target: { value: "40" } });
    fireEvent.change(screen.getByLabelText(/^reason/i), { target: { value: "smaller property" } });
    fireEvent.change(screen.getByLabelText(/confirm your password/i), { target: { value: "pw" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(put).toHaveBeenCalledWith("/pms-roster-reconciliation/settings", expect.objectContaining({
      expected_config_version: 0, roster_trust_min: 40, reason: "smaller property", password: "pw",
    })));
  });

  it("a read-only role sees the settings and cannot change them", async () => {
    roles = ["front_office_operator"];
    await renderPage();
    await waitFor(() => expect(screen.getByText(/when a roster is trusted/i)).toBeTruthy());
    expect(screen.queryByRole("button", { name: "Change" })).toBeNull();
  });

});
