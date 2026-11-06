import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, cleanup, fireEvent } from "@testing-library/react";
import { ApiError } from "@/lib/api";

class ResizeObserverStub { observe() {} unobserve() {} disconnect() {} }
(globalThis as any).ResizeObserver = (globalThis as any).ResizeObserver ?? ResizeObserverStub;

const get = vi.fn();
vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return { ...actual, api: { get: (...a: any[]) => get(...a), post: vi.fn(), put: vi.fn(), del: vi.fn() } };
});
vi.mock("@/lib/pms-scope", () => ({ usePmsConnections: () => ({ connections: [], multi: false, labelOf: () => "" }) }));

const SUMMARY = {
  cases: 1, recorded_rows: 3, rooms_multi_occupancy: 0, stays_past_departure: 0, stays_past_departure_absent_from_roster: 0,
  by_state: [{ state: "ROOM_SHARED", cases: 1, recorded_rows: 3 }],
};
const CASE = {
  pms_interface_id: "p1", case_key: "k1", room: "", reservation: "", repeat_count: 3, generations: 2,
  first_seen_at: "2026-11-01T08:00:00Z", last_seen_at: "2026-11-02T08:00:00Z", latest_event_id: "e1",
  candidate_stays: 2, roster_present: false, resolution_state: "ROOM_SHARED",
};

let failing: Map<string, number>;
const answers: Record<string, unknown> = {
  "/pms-reconciliation/summary": SUMMARY,
  "/pms-reconciliation": { cases: [CASE] },
  "/pms-reconciliation/rooms": { rooms: [] },
  "/pms-reconciliation/past-departure": { stays: [] },
};

beforeEach(() => {
  get.mockReset();
  failing = new Map();
  get.mockImplementation((path: string) => {
    const base = path.split("?")[0];
    const status = failing.get(base);
    if (status) return Promise.reject(new ApiError(status, { error: status === 404 ? "not_found" : "internal", message: `HTTP ${status}` }));
    if (path.includes("?state=")) return Promise.resolve({ cases: [] });
    return Promise.resolve(answers[base] ?? {});
  });
});
afterEach(() => { cleanup(); vi.resetModules(); });

async function renderPage() {
  const Page = (await import("@/app/(app)/pms-reconciliation/page")).default;
  render(<Page />);
}

describe("Unresolved departures", () => {
  it("never says everything is clear when the departures cannot be read", async () => {
    failing.set("/pms-reconciliation", 500);
    await renderPage();
    expect(await screen.findByText("The unresolved departures could not be loaded")).toBeTruthy();
    expect(screen.queryByText("No unresolved departures")).toBeNull();
    expect(screen.queryByText(/showing the last answer/)).toBeNull();
    expect(screen.getByText("3")).toBeTruthy();
  });

  it("says PMS administration is off instead of showing a 404", async () => {
    for (const p of Object.keys(answers)) failing.set(p, 404);
    await renderPage();
    expect(await screen.findByText("PMS administration is not turned on for this appliance")).toBeTruthy();
    expect(screen.queryByText(/HTTP 404/)).toBeNull();
  });

  it("says no departure is in the chosen state, not that every departure matched", async () => {
    await renderPage();
    fireEvent.click(await screen.findByRole("radio", { name: /Room has more than one stay/ }));
    expect(await screen.findByText("No departure is in this state")).toBeTruthy();
    expect(screen.queryByText(/Every departure the PMS has sent was matched/)).toBeNull();
  });

  it("shows missing values in words, with no dashes", async () => {
    await renderPage();
    expect(await screen.findByText("No room")).toBeTruthy();
    expect(screen.getAllByText("Not recorded").length).toBeGreaterThan(0);
    expect(screen.queryByText("—")).toBeNull();
  });

  it("does not count the other tabs before they are read", async () => {
    failing.set("/pms-reconciliation/rooms", 500);
    await renderPage();
    await screen.findByText("No room");
    expect(screen.getByRole("tab", { name: "Rooms with several stays" })).toBeTruthy();
    expect(screen.getByRole("tab", { name: "Past their departure date (0)" })).toBeTruthy();
  });
});
