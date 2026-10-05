import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

// USAGE EXPLORER — BY ACCESS SOURCE.
//
// What this asserts: the list is organised by what granted the access (client account, voucher, Hotel
// room/stay), the type filter and search reach edged as parameters, room and stay words appear ONLY on a stay
// source, a voucher is named by its card reference and never a code, and /usage?stay=<id> still opens the stay.

class ResizeObserverStub {
  observe() {}
  unobserve() {}
  disconnect() {}
}
(globalThis as any).ResizeObserver = (globalThis as any).ResizeObserver ?? ResizeObserverStub;

const get = vi.fn();
vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return { ...actual, api: { get: (...a: any[]) => get(...a) } };
});

const T = (down: number, up: number, sessions = 1, devices = 1) =>
  ({ bytes_down: down, bytes_up: up, bytes_total: down + up, sessions, devices });

const ACCOUNT_ID = "aaaaaaaa-1111-4111-8111-111111111111";
const VOUCHER_ID = "3fa85f64-5717-4562-b3fc-2c963f66afa6";
const STAY_ID = "cccccccc-3333-4333-8333-333333333333";

const ROWS = [
  { source_type: "account", source_id: ACCOUNT_ID, account_username: "alex.morgan", totals: T(900_000_000, 10_000_000),
    quota_bytes: 1_000_000_000, consumed_bytes: 910_000_000, last_activity: "2026-09-20T10:00:00Z" },
  { source_type: "voucher", source_id: VOUCHER_ID, totals: T(500_000_000, 5_000_000), end_reason: "DATA" },
  { source_type: "stay", source_id: STAY_ID, room: "4202", pms_interface: "Main PMS", reservation: "BK-88213",
    stay_status: "IN_HOUSE", arrival: "2026-09-18T12:00:00Z", totals: T(100_000_000, 1_000_000) },
];

const DEVICES = [{ mac: "02:00:00:aa:bb:cc", bytes_down: 900_000_000, bytes_up: 10_000_000, bytes_total: 910_000_000, sessions: 1 }];
const SESSIONS = [{ session_id: "s1", mac: "02:00:00:aa:bb:cc", state: "CLOSED", started: "2026-09-20T10:00:00Z",
  ended: "2026-09-20T12:00:00Z", end_reason: "DATA", bytes_down: 900_000_000, bytes_up: 10_000_000, bytes_total: 910_000_000 }];

function detailFor(path: string) {
  if (path.startsWith("/usage/sources/account/")) {
    return { source: ROWS[0], service_plan: "Standard", access_status: "ACTIVE", devices: DEVICES, sessions: SESSIONS };
  }
  if (path.startsWith("/usage/sources/voucher/")) {
    return { source: ROWS[1], service_plan: "One day", access_status: "TERMINATED", devices: DEVICES, sessions: SESSIONS };
  }
  if (path.startsWith("/usage/sources/stay/")) {
    return { source: { ...ROWS[2], departure: "2026-09-25T10:00:00Z" }, service_plan: "Free Internet", devices: DEVICES, sessions: SESSIONS };
  }
  return null;
}

function wire() {
  get.mockImplementation((raw?: unknown) => {
    const path = typeof raw === "string" ? raw : "";
    if (path.startsWith("/usage/sources?")) {
      const type = new URLSearchParams(path.split("?")[1]).get("type");
      return Promise.resolve({ data: ROWS.filter((r) => !type || r.source_type === type), meta: { has_more: false } });
    }
    const d = detailFor(path);
    if (d) return Promise.resolve(d);
    return Promise.resolve({ data: [], meta: { has_more: false } });
  });
}

async function renderPage() {
  const { default: Page } = await import("@/app/(app)/usage/page");
  render(<Page />);
  return userEvent.setup();
}

const calls = () => get.mock.calls.map((c) => String(c[0]));

beforeEach(() => { get.mockReset(); window.history.replaceState(null, "", "/usage"); });
afterEach(() => vi.resetModules());

describe("usage explorer by access source", () => {
  it("lists client accounts, vouchers and stays, each named by what it can honestly be named by", async () => {
    wire();
    await renderPage();

    expect(screen.getByRole("tab", { name: /by access source/i })).toBeTruthy();
    expect(screen.getByRole("tab", { name: /by device/i })).toBeTruthy();
    expect(screen.queryByRole("tab", { name: /by room or stay/i })).toBeNull();

    expect(await screen.findByText("alex.morgan")).toBeTruthy();
    expect(screen.getByText("Card 3fa85f64…")).toBeTruthy();
    expect(screen.getByText("Room 4202")).toBeTruthy();
    // A room always travels with its PMS connection.
    expect(screen.getByText(/Hotel room\/stay · Main PMS/)).toBeTruthy();
    expect(calls()[0]).toBe("/usage/sources?type=&q=&page=1&page_size=50");
  });

  it("sends the type filter and the search text to edged", async () => {
    wire();
    const user = await renderPage();
    await screen.findByText("alex.morgan");

    await user.click(screen.getByRole("radio", { name: "Voucher" }));
    await waitFor(() => expect(calls()).toContain("/usage/sources?type=voucher&q=&page=1&page_size=50"));
    await waitFor(() => expect(screen.queryByText("alex.morgan")).toBeNull());

    const box = screen.getByLabelText("Voucher card reference");
    await user.type(box, "3fa8");
    await user.click(screen.getByRole("button", { name: "Search" }));
    await waitFor(() => expect(calls()).toContain("/usage/sources?type=voucher&q=3fa8&page=1&page_size=50"));
  });

  it("pages access sources on the server, and a new type starts again at page 1", async () => {
    get.mockImplementation((raw?: unknown) => {
      const path = typeof raw === "string" ? raw : "";
      if (path.startsWith("/usage/sources?")) {
        const page = Number(new URLSearchParams(path.split("?")[1]).get("page"));
        return Promise.resolve({ data: page === 1 ? ROWS : [ROWS[0]], meta: { has_more: page === 1 }, page, page_size: 50 });
      }
      return Promise.resolve({ data: [], meta: { has_more: false } });
    });
    const user = await renderPage();
    await screen.findByText("alex.morgan");
    await user.click(screen.getByRole("button", { name: /Next/ }));
    await waitFor(() => expect(calls()).toContain("/usage/sources?type=&q=&page=2&page_size=50"));
    expect(await screen.findByText(`Showing 51–51`)).toBeTruthy();

    await user.click(screen.getByRole("radio", { name: "Voucher" }));
    await waitFor(() => expect(calls()[calls().length - 1]).toBe("/usage/sources?type=voucher&q=&page=1&page_size=50"));
  });

  it("shows a client account without any room or stay words", async () => {
    wire();
    const user = await renderPage();
    await user.click(await screen.findByRole("button", { name: "Details for alex.morgan" }));

    expect(await screen.findByText("Devices used under this access")).toBeTruthy();
    expect(calls()).toContain(`/usage/sources/account/${ACCOUNT_ID}`);
    expect(screen.getByText("Active")).toBeTruthy();
    expect(screen.getByText("Service plan: Standard")).toBeTruthy();
    const text = document.body.textContent ?? "";
    for (const hotelWord of ["Room ", "reservation", "Reservation", "stay", "PMS"]) {
      expect(text.includes(hotelWord), `account detail mentions "${hotelWord}"`).toBe(false);
    }
  });

  it("names a voucher by its card reference and never shows a code", async () => {
    wire();
    const user = await renderPage();
    await user.click(await screen.findByRole("button", { name: "Details for Card 3fa85f64…" }));

    expect(await screen.findByText("Card reference")).toBeTruthy();
    expect(calls()).toContain(`/usage/sources/voucher/${VOUCHER_ID}`);
    // "Ended" is both the access status badge and the sessions column heading.
    expect(screen.getAllByText("Ended").length).toBeGreaterThanOrEqual(2);
    expect(document.body.textContent ?? "").not.toMatch(/\bcode\b/i);
  });

  it("keeps the stay detail: room, PMS connection, reservation, arrival and departure", async () => {
    wire();
    const user = await renderPage();
    await user.click(await screen.findByRole("button", { name: "Details for Room 4202" }));

    expect(await screen.findByText("Devices used during this stay")).toBeTruthy();
    const header = screen.getByText(/Main PMS · reservation BK-88213/);
    expect(header.textContent).toMatch(/→/);
    expect(screen.getByText("In house")).toBeTruthy();
    expect(screen.getByText("Service plan: Free Internet")).toBeTruthy();
    // The samples drill-down is still one click from each session.
    expect(screen.getByRole("button", { name: "Show evidence" })).toBeTruthy();
  });

  it("still opens a stay from the /usage?stay=<id> deep link", async () => {
    wire();
    window.history.replaceState(null, "", `/usage?stay=${STAY_ID}`);
    await renderPage();
    expect(await screen.findByText("Devices used during this stay")).toBeTruthy();
    expect(calls()).toContain(`/usage/sources/stay/${STAY_ID}`);
  });
});
