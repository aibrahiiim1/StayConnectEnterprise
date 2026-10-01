import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, waitFor, within, cleanup } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { fakeAudit, fakeSettlements, pageOf } from "./fake-paged";

// THE GROWING LISTS PAGE ON THE SERVER: Activity, Active sessions, PMS activity, guest sign-in attempts, guest
// sign-in checks, DHCP leases and reservations, and package payments.
//
// Each used to load a fixed window (500 or 200 rows) and search, filter and count it in the browser. What is
// pinned here is the shared behaviour: the page parameters reach edged, Next asks for the next page, a new
// filter or search starts at page 1, and the search text travels in a header -- never in the URL, which edged
// writes to its request log.

const get = vi.fn();
vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return { ...actual, api: { get: (...a: any[]) => get(...a), post: vi.fn(), put: vi.fn(), del: vi.fn() } };
});
vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn(), replace: vi.fn() }),
  useSearchParams: () => new URLSearchParams(""),
}));

// A block, not an arrow returning mockReset(): vitest runs a function returned from beforeEach as teardown, and
// that would call the mock -- which, in the ordering test below, never resolves.
beforeEach(() => { get.mockReset(); });
afterEach(() => { cleanup(); vi.resetModules(); });

/** Calls to one list, as [path, headers]. */
const callsTo = (prefix: string) =>
  get.mock.calls.filter(([p]) => typeof p === "string" && p.startsWith(prefix)) as [string, Record<string, string> | undefined][];
const lastCall = (prefix: string) => {
  const c = callsTo(prefix);
  return c[c.length - 1];
};
const param = (path: string, name: string) => new URL("http://x" + path).searchParams.get(name);

function serve(map: Record<string, (path: string, headers?: Record<string, string>) => unknown>) {
  get.mockImplementation((path: string, headers?: Record<string, string>) => {
    if (typeof path !== "string") return Promise.resolve({});
    if (path === "/auth/whoami") return Promise.resolve({ roles: ["site_admin"], operator_id: "me" });
    for (const [prefix, fn] of Object.entries(map)) {
      if (path === prefix || path.startsWith(prefix + "?")) return Promise.resolve(fn(path, headers));
    }
    return Promise.resolve({ data: [], meta: { has_more: false } });
  });
}

// ------------------------------------------------------------------------------------------------ Activity
const auditRows = Array.from({ length: 120 }, (_, i) => ({
  ts: new Date(Date.UTC(2026, 8, 20, 10, 0, 0) - i * 60_000).toISOString(),
  actor_type: "system",
  action: i % 4 === 0 ? "backup.downloaded" : "health.recheck",
  target_id: "t-" + i,
}));

describe("Activity — server paging", () => {
  it("pages the whole period, counts it on the server, and starts again at page 1 on a new filter", async () => {
    serve({ "/audit": fakeAudit(auditRows), "/operators": () => ({ data: [] }) });
    const { default: Page } = await import("@/app/(app)/audit/page");
    render(<Page />);

    expect(await screen.findByText("120 entries")).toBeTruthy();
    expect(screen.getByText("Showing 1–50 of 120")).toBeTruthy();
    // The filter counts are the server's count of the whole period, not of the page on screen.
    const chips = screen.getByRole("radiogroup", { name: "Show" });
    expect(within(chips).getByRole("radio", { name: /Security/ }).textContent).toContain("30");
    // The old "first 500 only" limitation is gone.
    expect(screen.queryByText(/Only the first/)).toBeNull();

    await userEvent.click(screen.getByRole("button", { name: /Next/ }));
    expect(await screen.findByText("Showing 51–100 of 120")).toBeTruthy();
    expect(param(lastCall("/audit")[0], "page")).toBe("2");

    // A filter sends the exact action codes the server filters by, and starts at page 1.
    await userEvent.click(within(chips).getByRole("radio", { name: /Security/ }));
    expect(await screen.findByText("30 entries")).toBeTruthy();
    const [path] = lastCall("/audit");
    expect(param(path, "action")).toBe("backup.downloaded");
    expect(param(path, "page")).toBe("1");
  });

  it("searches on the server with the text in a header, never the URL", async () => {
    serve({ "/audit": fakeAudit(auditRows), "/operators": () => ({ data: [] }) });
    const { default: Page } = await import("@/app/(app)/audit/page");
    render(<Page />);
    await screen.findByText("120 entries");

    await userEvent.type(screen.getByRole("searchbox", { name: "Search the activity trail" }), "downloaded a");
    await waitFor(() => {
      const [path, headers] = lastCall("/audit");
      expect(headers?.["X-Audit-Search"]).toBe(encodeURIComponent("downloaded a"));
      // The readable title "Downloaded a backup" lives here, so the code it names goes along with the search.
      expect(headers?.["X-Audit-Search-Actions"]).toBe("backup.downloaded");
      expect(path).not.toContain("downloaded");
      expect(param(path, "page")).toBe("1");
    });
  });
});

// ------------------------------------------------------------------------------------------ Active sessions
const session = (i: number) => ({
  id: "s" + i, ip: "10.0.0." + i, mac: "02:00:00:00:00:" + String(i).padStart(2, "0"),
  state: "active", started_at: new Date().toISOString(), bytes_down: 1, bytes_up: 1, subject_kind: "voucher",
});
const sessions = Array.from({ length: 130 }, (_, i) => session(i + 1));
const sessionsServer = (path: string) =>
  pageOf(path, param(path, "kind") === "room" ? [] : sessions, {
    summary: { devices_online: 130, clients_online: 90, rooms_online: 0, bytes_total: 260, kinds: { voucher: 130 } },
  });

describe("Active sessions — server paging", () => {
  it("shows the server's totals, pages, and sends the search in a header", async () => {
    serve({ "/sessions": sessionsServer });
    const { default: Page } = await import("@/app/(app)/sessions/page");
    render(<Page />);

    expect(await screen.findByText("Showing 1–50 of 130")).toBeTruthy();
    // The tiles describe every session online, not the 50 on screen.
    expect(screen.getByText("90")).toBeTruthy();
    expect(param(lastCall("/sessions")[0], "state")).toBe("active");

    await userEvent.click(screen.getByRole("button", { name: /Next/ }));
    expect(await screen.findByText("Showing 51–100 of 130")).toBeTruthy();
    expect(param(lastCall("/sessions")[0], "page")).toBe("2");

    await userEvent.type(screen.getByRole("searchbox", { name: "Search sessions" }), "Room 412");
    await waitFor(() => {
      const [path, headers] = lastCall("/sessions");
      expect(headers).toEqual({ "X-Session-Search": encodeURIComponent("Room 412") });
      expect(path).not.toContain("412");
      expect(param(path, "page")).toBe("1");
    });
  });

  it("filters by sign-in type on the server and starts at page 1", async () => {
    serve({ "/sessions": sessionsServer });
    const { default: Page } = await import("@/app/(app)/sessions/page");
    render(<Page />);
    await screen.findByText("Showing 1–50 of 130");
    await userEvent.click(screen.getByRole("button", { name: /Next/ }));
    await screen.findByText("Showing 51–100 of 130");

    await userEvent.selectOptions(screen.getByLabelText(/Filter by how the client signed in/), "voucher");
    await waitFor(() => {
      const [path] = lastCall("/sessions");
      expect(param(path, "kind")).toBe("voucher");
      expect(param(path, "page")).toBe("1");
    });
    expect(await screen.findByText("Showing 1–50 of 130")).toBeTruthy();
  });
});

// --------------------------------------------------------------------------------------------- PMS activity
const event = (i: number) => ({
  id: "e" + i, pms_interface_id: "i1", external_event_identity: "EV-" + i, event_type: "GI",
  processing_status: "APPLIED", received_at: new Date().toISOString(), room: String(100 + i), stay_id: "st" + i,
});
const events = Array.from({ length: 75 }, (_, i) => event(i + 1));
const eventsServer = (path: string) =>
  pageOf(path, param(path, "processing_status") === "REJECTED" ? [] : events, {
    summary: { applied: 75, pending: 0, manual_review: 0, rejected: 0, unmatched: 0, newest_received_at: events[0].received_at },
  });

describe("PMS activity — server paging", () => {
  it("pages, counts every message, filters on the server and searches in a header", async () => {
    serve({ "/pms-events": eventsServer });
    const { default: Page } = await import("@/app/(app)/stay-events/page");
    render(<Page />);

    expect(await screen.findByText("Showing 1–50 of 75")).toBeTruthy();
    expect(screen.getByText("75")).toBeTruthy(); // the Applied tile counts every message

    await userEvent.click(screen.getByRole("button", { name: /Next/ }));
    expect(await screen.findByText("Showing 51–75 of 75")).toBeTruthy();
    expect(param(lastCall("/pms-events")[0], "page")).toBe("2");

    // A status filter goes to the server and starts again at page 1.
    await userEvent.selectOptions(screen.getByLabelText(/Filter by what happened to the message/), "REJECTED");
    await waitFor(() => {
      const [path] = lastCall("/pms-events");
      expect(param(path, "processing_status")).toBe("REJECTED");
      expect(param(path, "page")).toBe("1");
    });
    expect(await screen.findByText("No messages in this state")).toBeTruthy();

    await userEvent.type(screen.getByRole("searchbox", { name: "Search PMS activity" }), "Müller");
    await waitFor(() => {
      const [path, headers] = lastCall("/pms-events");
      // Percent-encoded, because a browser refuses a header value outside ISO-8859-1.
      expect(headers).toEqual({ "X-Pms-Event-Search": encodeURIComponent("Müller") });
      expect(path).not.toContain("ller");
    });
  });
});

// -------------------------------------------------------------------------------- guest sign-in attempts
const attempt = (i: number) => ({
  id: "a" + i, occurred_at: new Date().toISOString(), room: String(100 + i), guest_network: "Lobby",
  result: "VERIFIED", result_label: "Verified", succeeded: true, verifier_kind: "FULL_NAME",
});
const attempts = Array.from({ length: 60 }, (_, i) => attempt(i + 1));

describe("Guest sign-in attempts — server paging", () => {
  it("pages over the server filters, and searches in a header", async () => {
    serve({
      "/guest-signin-attempts": (path) => pageOf(path, attempts, { summary: { total: 60, failed: 0, mismatch: 0, systemic: 0 } }),
    });
    const { default: Page } = await import("@/app/(app)/guest-signin-attempts/page");
    render(<Page />);
    expect(await screen.findByText("Showing 1–50 of 60")).toBeTruthy();
    expect(screen.getByText("60")).toBeTruthy(); // the Attempts tile is the server's count

    await userEvent.click(screen.getByRole("button", { name: /Next/ }));
    expect(await screen.findByText("Showing 51–60 of 60")).toBeTruthy();
    expect(param(lastCall("/guest-signin-attempts")[0], "page")).toBe("2");

    await userEvent.selectOptions(screen.getByLabelText("Filter by result"), "CREDENTIAL_MISMATCH");
    await waitFor(() => {
      const [path] = lastCall("/guest-signin-attempts");
      expect(param(path, "result")).toBe("CREDENTIAL_MISMATCH");
      expect(param(path, "page")).toBe("1");
    });

    await userEvent.type(screen.getByRole("searchbox", { name: "Search the attempts" }), "aa:bb");
    await waitFor(() => {
      const [path, headers] = lastCall("/guest-signin-attempts");
      expect(headers).toEqual({ "X-Signin-Search": encodeURIComponent("aa:bb") });
      expect(path).not.toMatch(/aa(:|%3A)bb/i);
    });
  });
});

// ------------------------------------------------------------------------------- guest sign-in checks
describe("Guest sign-in checks — server paging", () => {
  it("pages the table and keeps the server's summary of the recent window", async () => {
    const rs = Array.from({ length: 70 }, (_, i) => ({
      id: "r" + i, guest_network_id: "gn1", guest_network_name: "Lobby", outcome_code: "NO_MATCH", resolved: false,
      resolved_at: new Date().toISOString(),
    }));
    serve({
      "/pms-resolutions": (path) => pageOf(path, rs, {
        summary: { window: 200, total: 70, verified: 0, outcomes: [{ outcome_code: "NO_MATCH", count: 70 }],
          networks: [{ guest_network_id: "gn1", guest_network_name: "Lobby", total: 70, verified: 0 }] },
      }),
    });
    const { default: Page } = await import("@/app/(app)/pms-resolutions/page");
    render(<Page />);
    expect(await screen.findByText("Showing 1–50 of 70")).toBeTruthy();
    await userEvent.click(screen.getByRole("button", { name: /Next/ }));
    expect(await screen.findByText("Showing 51–70 of 70")).toBeTruthy();
    expect(param(lastCall("/pms-resolutions")[0], "page")).toBe("2");
    expect(screen.getByText("0 of 70 let online")).toBeTruthy();
  });
});

// ------------------------------------------------------------------------------------------------ DHCP
describe("DHCP & leases — server paging", () => {
  it("pages leases and reservations in edged and searches both in a header", async () => {
    const leases = Array.from({ length: 55 }, (_, i) => ({ "ip-address": "10.20.0." + (i + 1), "hw-address": "02:00:00:00:00:" + i }));
    serve({
      "/network/dhcp/leases": (path) => {
        const p = pageOf(path, leases);
        return { leases: p.data, meta: p.meta, page: p.page, page_size: p.page_size, total: p.total };
      },
      "/network/dhcp/reservations": (path) => pageOf(path, []),
      "/network/guest-networks": () => ({ data: [] }),
    });
    const { default: Page } = await import("@/app/(app)/network/dhcp/page");
    render(<Page />);
    expect(await screen.findByText("Showing 1–50 of 55")).toBeTruthy();
    await userEvent.click(screen.getByRole("button", { name: /Next/ }));
    expect(await screen.findByText("Showing 51–55 of 55")).toBeTruthy();
    expect(param(lastCall("/network/dhcp/leases")[0], "page")).toBe("2");

    await userEvent.type(screen.getByRole("searchbox", { name: "Search IP, MAC or hostname" }), "10.20.0.7");
    await waitFor(() => {
      for (const list of ["/network/dhcp/leases", "/network/dhcp/reservations"]) {
        const [path, headers] = lastCall(list);
        expect(headers).toEqual({ "X-Dhcp-Search": encodeURIComponent("10.20.0.7") });
        expect(param(path, "page")).toBe("1");
      }
    });
  });
});

// ----------------------------------------------------------------------------------------- package payments
describe("Package payments — server paging", () => {
  it("asks the server for a view, from page 1, and keeps the counts over every payment", async () => {
    const rows = Array.from({ length: 60 }, (_, i) => ({
      settlement_id: "s" + i, purchase_id: "p" + i, method: i % 2 ? "ONLINE_PAYMENT" : "NOT_REQUIRED",
      status: i % 2 ? "SETTLED" : "NOT_REQUIRED", purchase_state: "GRANTED", amount_minor: 100, currency: "USD",
      currency_exponent: 2, package_name: "Day " + i, at: new Date().toISOString(), source: "ACCOUNT", room: null,
    }));
    serve({ "/financial-ops/settlements": fakeSettlements(rows) });
    const { SettlementsView } = await import("@/components/phase4/settlements-view");
    render(<SettlementsView />);
    expect(await screen.findByText("Showing 1–50 of 60")).toBeTruthy();
    await userEvent.click(screen.getByRole("button", { name: /Next/ }));
    expect(await screen.findByText("Showing 51–60 of 60")).toBeTruthy();

    const chips = screen.getByRole("radiogroup", { name: /^show$/i });
    expect(within(chips).getByRole("radio", { name: /Card and room charge/ }).textContent).toContain("30");
    await userEvent.click(within(chips).getByRole("radio", { name: /Card and room charge/ }));
    await waitFor(() => {
      const [path] = lastCall("/financial-ops/settlements");
      expect(param(path, "view")).toBe("paid");
      expect(param(path, "page")).toBe("1");
    });
    // Thirty paid packages fit on one page: no pager.
    await waitFor(() => expect(screen.queryByText(/^Showing/)).toBeNull());
    expect(screen.getByText("30.00 USD")).toBeTruthy(); // collected over every payment, not the page
  });
});

// ------------------------------------------------------------------------------------------ the shared hook
describe("useServerPage", () => {
  it("applies only the newest question's answer, and a new question starts at page 1 on the same render", async () => {
    const { useServerPage } = await import("@/lib/use-server-page");
    const pending: { path: string; resolve: (v: unknown) => void }[] = [];
    get.mockImplementation((path: string) => new Promise((resolve) => pending.push({ path, resolve })));
    function Probe({ q }: { q: string }) {
      const l = useServerPage<{ data?: string[] }>({ path: "/x?q=" + q });
      return <div>{l.current && l.resp ? `rows:${(l.resp.data ?? []).join(",")}` : "loading"} offset:{l.offset}
        <button onClick={() => l.setOffset(50)}>p2</button></div>;
    }
    const view = render(<Probe q="a" />);
    await waitFor(() => expect(pending.length).toBe(1));
    pending[0].resolve({ data: ["a1"], meta: { has_more: true } });
    expect(await screen.findByText(/rows:a1/)).toBeTruthy();

    await userEvent.click(screen.getByRole("button", { name: "p2" }));
    await waitFor(() => expect(pending.some((p) => p.path === "/x?q=a&page=2&page_size=50")).toBe(true));

    // A new question: the very next request is page 1 -- the old offset is never asked with the new question.
    view.rerender(<Probe q="b" />);
    await waitFor(() => expect(pending[pending.length - 1].path).toBe("/x?q=b&page=1&page_size=50"));
    expect(pending.some((p) => p.path === "/x?q=b&page=2&page_size=50")).toBe(false);
    expect(screen.getByText(/offset:0/)).toBeTruthy();

    // The newest answers first; the older page-2 answer arrives late and must not replace it.
    pending[pending.length - 1].resolve({ data: ["b1"], meta: { has_more: false } });
    expect(await screen.findByText(/rows:b1/)).toBeTruthy();
    pending.find((p) => p.path.includes("page=2"))!.resolve({ data: ["a51"], meta: { has_more: false } });
    await new Promise((r) => setTimeout(r, 30));
    expect(screen.getByText(/rows:b1/)).toBeTruthy();
    for (const p of pending) p.resolve({ data: [] }); // nothing left hanging for teardown
    view.unmount();
  });
});
