import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, cleanup, fireEvent } from "@testing-library/react";

const get = vi.fn();
vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return { ...actual, api: { get: (...a: any[]) => get(...a), post: vi.fn(), put: vi.fn(), del: vi.fn(), postRaw: vi.fn() } };
});
vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn(), replace: vi.fn() }),
  useSearchParams: () => new URLSearchParams(""),
  usePathname: () => "/system-health",
}));

beforeEach(() => get.mockReset());
afterEach(() => { cleanup(); vi.resetModules(); });

const now = new Date().toISOString();
const SNAPSHOT = {
  at: now, mode: "deep", overall: "WARN", counts: { PASS: 40, WARN: 1, CRITICAL: 0, NOT_VERIFIED: 1 },
  metrics: { online: 42, netd_sessions: 42, cpu: 7, mem: 2100, disk: 31, deadlocks_1h: 0, signin: "5/0/6", rejoined: 3, recut: 0, stuck: "0(0 system)" },
  checks: [
    { key: "svc:x", area: "services", status: "WARN", text: "stayconnect-pmsd restarted 3 times in 1 h" },
    { key: "backup:fresh", area: "background jobs", status: "NOT_VERIFIED", text: "no database backup found" },
    { key: "res:cpu", area: "resources", status: "PASS", text: "CPU 7%" },
  ],
  incidents: [{ key: "svc:x", area: "services", status: "WARN", text: "stayconnect-pmsd restarted 3 times in 1 h", first: now, count: 4 }],
  resolved: [{ key: "res:disk:/", text: "disk / 91% used", first: now, cleared: now, status: "WARN" }],
  deep: {
    at: now, analytics: null,
    breakdowns: { ends_24h: [["USER_LOGOUT", "4"]], methods_24h: [], results_24h: [["VERIFIED", "6"]], networks_24h: [["br-guest", "9", "12", "5", "3", "1", "8"]],
      hourly: [["11-06 10:00", "8", "2", "1", "1", "0"]], daily: [["11-06 Fri", "9", "5", "3", "1", "0"]], online: ["42", "40"] },
    devices: [{ severity: "MEDIUM", status: "ONGOING", device: "ab12cd34", mac: "…:ee:ff", network: "br-guest", issue: "IP conflict", cause: "device used an address DHCP had leased to another device",
      confidence: "confirmed", first: "11-06 10:00", last: "11-06 11:00", count: 2, action: "renew" }],
    deadlocks_daily: [["2026-11-06", 0]], lock_waits: {},
  },
};

async function page(answer: unknown) {
  get.mockImplementation((path: string) => (path === "/diagnostics/monitor" ? Promise.resolve(answer) : Promise.resolve({})));
  const Page = (await import("@/app/(app)/system-health/page")).default;
  render(<Page />);
}

describe("System health", () => {
  it("shows the monitor's verdict, its active issues and the unverified count", async () => {
    await page({ available: true, stale: false, age_seconds: 30, snapshot: SNAPSHOT });
    expect(screen.getByRole("heading", { level: 1, name: "System health" })).toBeTruthy();
    expect(await screen.findByText("Appliance: Warning")).toBeTruthy();
    expect(screen.getAllByText("stayconnect-pmsd restarted 3 times in 1 h").length).toBeGreaterThan(0);
    expect(screen.getByText("1 not verified")).toBeTruthy();
    expect(screen.queryByText(/out of date/)).toBeNull();
  });

  it("says when the report is stale instead of presenting it as current", async () => {
    await page({ available: true, stale: true, age_seconds: 3600, snapshot: SNAPSHOT });
    expect(await screen.findByText("This report is out of date")).toBeTruthy();
  });

  it("never invents a report when the monitor has not written one", async () => {
    await page({ available: false, reason: "not_installed" });
    expect(await screen.findByText("No monitor report yet")).toBeTruthy();
    expect(screen.queryByText(/Appliance:/)).toBeNull();
  });

  it("lists device findings with the masked hardware address and no client address", async () => {
    await page({ available: true, stale: false, snapshot: SNAPSHOT });
    const tab = await screen.findByRole("tab", { name: "Devices" });
    fireEvent.mouseDown(tab);
    fireEvent.click(tab);
    expect(await screen.findByText("IP conflict")).toBeTruthy();
    expect(screen.getByText("…:ee:ff")).toBeTruthy();
  });
});
