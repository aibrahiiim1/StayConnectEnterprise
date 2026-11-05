import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

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

// Search text travels in a header to keep sensitive terms out of URLs and logs.
const calls = () => get.mock.calls.map((c) => {
  const [path, query = ""] = String(c[0]).split("?");
  const params = new URLSearchParams(query);
  const hasPeriod = params.has("from") && params.has("to");
  params.delete("from"); params.delete("to");
  const header = (c[1] as Record<string, string> | undefined)?.["X-Usage-Search"];
  if (path === "/usage/sources") {
    const out = new URLSearchParams();
    out.set("type", params.get("type") ?? "");
    out.set("q", header ? decodeURIComponent(header) : "");
    out.set("page", params.get("page") ?? "");
    out.set("page_size", params.get("page_size") ?? "");
    return `${path}?${out.toString()}${hasPeriod ? "" : "#no-period"}`;
  }
  const rest = params.toString();
  return `${path}${rest ? `?${rest}` : ""}${hasPeriod ? "" : "#no-period"}`;
});

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
    expect(screen.getByRole("button", { name: "Show evidence" })).toBeTruthy();
  });

  it("reads the list and a source's detail over the SAME period, and the search never goes in the URL (C-8)", async () => {
    wire();
    const user = await renderPage();
    await user.type(await screen.findByRole("searchbox", { name: /card reference/i }), "alex");
    await user.click(screen.getByRole("button", { name: "Search" }));
    await user.click(await screen.findByRole("button", { name: "Details for alex.morgan" }));
    await screen.findByText("Devices used under this access");
    const raw = get.mock.calls.map((c) => String(c[0]));
    const period = (u: string) => {
      const q = new URLSearchParams(u.split("?")[1] ?? "");
      return `${q.get("from")}|${q.get("to")}`;
    };
    const list = raw.filter((u) => u.startsWith("/usage/sources?"));
    const detail = raw.find((u) => u.startsWith("/usage/sources/account/"))!;
    expect(list.length).toBeGreaterThan(0);
    for (const u of list) expect(u).not.toMatch(/[?&]q=/);
    const span = (u: string) => {
      const q = new URLSearchParams(u.split("?")[1] ?? "");
      return Math.round((Date.parse(q.get("to")!) - Date.parse(q.get("from")!)) / 86_400_000);
    };
    expect(span(detail)).toBe(30);
    expect(span(list[list.length - 1])).toBe(30);
    expect(period(detail)).not.toBe("null|null");
  });

  // Room 3311 on the test appliance: free, then unlimited, then after check-out; one device connected through all three.
  const CARRIED_STAY = "dddddddd-4444-4444-8444-444444444444";
  const UNLIMITED_STAY = "eeeeeeee-5555-4555-8555-555555555555";
  const CARRIED_ROWS = [
    { source_type: "stay", source_id: CARRIED_STAY, room: "3311", pms_interface: "Main PMS", stay_status: "CHECKED_OUT",
      totals: T(20_000_000_000, 2_180_000_000), quota_bytes: 2_000_000_000, consumed_bytes: 2_001_000_000, end_reason: "DATA",
      latest_package_name: "Checkout grace", latest_package_type: "CHECKOUT_GRACE", latest_package_status: "TERMINATED" },
    { source_type: "stay", source_id: UNLIMITED_STAY, room: "1201", pms_interface: "Main PMS", stay_status: "IN_HOUSE",
      totals: T(40_000_000_000, 5_330_000_000), consumed_bytes: 40_710_000_000,
      latest_package_name: "Full Stay", latest_package_type: "GENERAL", latest_package_status: "ACTIVE" },
  ];
  const PACKAGES = [
    { entitlement_id: "e1", name: "Free Internet", package_type: "GENERAL", status: "TERMINATED", activated_at: "2026-11-03T14:00:00Z",
      terminated_at: "2026-11-03T18:06:00Z", end_reason: "SUPERSEDED", quota_bytes: 9_000_000_000, used_bytes: 3_468_000_000 },
    { entitlement_id: "e2", name: "Full Stay", package_type: "GENERAL", status: "TERMINATED", activated_at: "2026-11-03T18:06:00Z",
      terminated_at: "2026-11-04T10:46:00Z", end_reason: "CONVERTED", quota_bytes: null, used_bytes: 16_710_000_000 },
    { entitlement_id: "e3", name: "Checkout grace", package_type: "CHECKOUT_GRACE", status: "TERMINATED", activated_at: "2026-11-04T10:46:00Z",
      terminated_at: "2026-11-04T14:07:00Z", end_reason: "DATA", quota_bytes: 2_000_000_000, used_bytes: 2_001_000_000 },
  ];
  const CARRIED_SESSIONS = [
    { session_id: "c1", mac: "02:00:00:aa:bb:01", state: "ended", started: "2026-11-03T14:11:00Z", ended: "2026-11-04T14:07:00Z",
      end_reason: "ENTITLEMENT_ENDED", bytes_down: 5_690_000_000, bytes_up: 260_000_000, bytes_total: 5_950_000_000,
      package_split_exact: true, packages: [
        { entitlement_id: "e1", package_name: "Free Internet", package_type: "GENERAL", from: "2026-11-03T14:11:00Z", until: "2026-11-03T18:06:00Z", bytes_total: 1_200_000_000 },
        { entitlement_id: "e2", package_name: "Full Stay", package_type: "GENERAL", from: "2026-11-03T18:06:00Z", until: "2026-11-04T10:46:00Z", bytes_total: 4_250_000_000 },
        { entitlement_id: "e3", package_name: "Checkout grace", package_type: "CHECKOUT_GRACE", from: "2026-11-04T10:46:00Z", until: "2026-11-04T14:07:00Z", bytes_total: 500_000_000 },
      ] },
    { session_id: "c2", mac: "02:00:00:aa:bb:02", state: "ended", started: "2026-11-01T09:00:00Z", ended: "2026-11-03T20:00:00Z",
      bytes_down: 3_000_000_000, bytes_up: 100_000_000, bytes_total: 3_100_000_000, package_split_exact: false, packages: [
        { entitlement_id: "e1", package_name: "Free Internet", package_type: "GENERAL", from: "2026-11-01T09:00:00Z", until: "2026-11-03T18:06:00Z" },
        { entitlement_id: "e2", package_name: "Full Stay", package_type: "GENERAL", from: "2026-11-03T18:06:00Z", until: "2026-11-03T20:00:00Z" },
      ] },
  ];
  function wireCarried() {
    get.mockImplementation((raw?: unknown) => {
      const path = typeof raw === "string" ? raw : "";
      if (path.startsWith("/usage/sources?")) return Promise.resolve({ data: CARRIED_ROWS, meta: { has_more: false } });
      if (path.startsWith(`/usage/sources/stay/${CARRIED_STAY}`)) {
        return Promise.resolve({ source: CARRIED_ROWS[0], packages: PACKAGES, devices: DEVICES, sessions: CARRIED_SESSIONS });
      }
      return Promise.resolve({ data: [], meta: { has_more: false } });
    });
  }

  it("sets a stay's total over every package apart from the latest package's own allowance and use", async () => {
    wireCarried();
    await renderPage();
    expect(await screen.findByText("Room 3311")).toBeTruthy();
    expect(screen.getByRole("columnheader", { name: "Total, all packages" })).toBeTruthy();
    expect(screen.getByRole("columnheader", { name: "Latest package" })).toBeTruthy();
    expect(screen.getByText("22.2 GB")).toBeTruthy();
    expect(screen.getByText("After check-out access")).toBeTruthy();
    // its own meter, capped at the allowance: the cut-off delay's extra megabyte is not shown as an overrun
    expect(screen.getByText("2.00 GB of 2.00 GB")).toBeTruthy();
    expect(screen.getByText("Ended · The data allowance was used up")).toBeTruthy();
    // an unlimited latest package has no meter at all, whatever an earlier package allowed
    expect(screen.getByText("Full Stay")).toBeTruthy();
    expect(screen.getAllByText("No limit").length).toBe(1);
    expect(screen.queryByText("Allowance used up")).toBeNull();
    expect(document.body.textContent ?? "").not.toMatch(/\d+% of /);
  });

  it("shows each package with its own use, dates and ending, and splits a carried session only when exact", async () => {
    wireCarried();
    const user = await renderPage();
    await user.click(await screen.findByRole("button", { name: "Details for Room 3311" }));
    const table = await screen.findByRole("table", { name: "Packages" });
    const rows = within(table).getAllByRole("row").slice(1);
    expect(rows.map((r) => within(r).getAllByRole("cell")[0].textContent)).toEqual([
      "Free InternetEnded", "Full StayEnded", "After check-out accessEnded",
    ]);
    expect(within(rows[0]).getByText("3.47 GB")).toBeTruthy();
    expect(within(rows[1]).getByText("16.7 GB")).toBeTruthy();
    expect(within(rows[1]).getByText("No limit")).toBeTruthy();
    expect(within(rows[1]).getByText("Moved to after check-out access")).toBeTruthy();
    expect(within(rows[2]).getByText("2.00 GB of 2.00 GB")).toBeTruthy();
    expect(within(rows[2]).getByText("The data allowance was used up")).toBeTruthy();

    expect(screen.getByText("Latest package: After check-out access")).toBeTruthy();
    expect(screen.getByText("Total, all packages")).toBeTruthy();

    const sessions = screen.getByRole("table", { name: "Sessions" });
    expect(within(sessions).getByText("Free Internet: 1.20 GB")).toBeTruthy();
    expect(within(sessions).getByText("Full Stay: 4.25 GB")).toBeTruthy();
    expect(within(sessions).getByText("After check-out access: 500 MB")).toBeTruthy();
    // without the samples to split it, the session names its packages and shows no invented figure
    expect(within(sessions).getByText(/no longer kept/)).toBeTruthy();
    expect(within(sessions).queryByText(/Free Internet: .*3\.10 GB|Full Stay: 1\./)).toBeNull();
  });

  it("still opens a stay from the /usage?stay=<id> deep link", async () => {
    wire();
    window.history.replaceState(null, "", `/usage?stay=${STAY_ID}`);
    await renderPage();
    expect(await screen.findByText("Devices used during this stay")).toBeTruthy();
    expect(calls()).toContain(`/usage/sources/stay/${STAY_ID}`);
  });
});
