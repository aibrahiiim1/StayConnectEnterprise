import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor, fireEvent, within } from "@testing-library/react";
import { readFileSync } from "node:fs";
import { join } from "node:path";

// THE OVERVIEW: every block says why it is empty instead of drawing a zero, the attention list routes to the
// screen that fixes each item, the range selector actually asks for the range, and nothing claims a figure that
// is not recorded anywhere.

const get = vi.fn();
vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return { ...actual, api: { get: (...a: any[]) => get(...a) } };
});

import DashboardPage from "@/app/(app)/dashboard/page";
import { normalizeOverview } from "@/lib/api/dashboard";

const HOURS = Array.from({ length: 24 }, (_, i) => new Date(Date.UTC(2026, 8, 24, i)).toISOString());
const zeros = () => HOURS.map(() => 0);

function healthy(): any {
  return {
    generated_at: "2026-09-24T14:00:00Z", range: "24h", window_start: HOURS[0], window_end: "2026-09-25T00:00:00Z",
    bucket_seconds: 3600, buckets: HOURS, timezone: "Africa/Cairo",
    guests: {
      available: true, devices_online: 7, guests_online: 4, sign_ins: 31, unique_devices: 12,
      sign_ins_series: HOURS.map((_, i) => (i === 9 ? 20 : i === 10 ? 11 : 0)),
      sign_ins_by_method: [
        { method: "PMS", total: 20, series: HOURS.map((_, i) => (i === 9 ? 20 : 0)) },
        { method: "VOUCHER", total: 11, series: HOURS.map((_, i) => (i === 10 ? 11 : 0)) },
      ],
      concurrency: { available: true, definition: "peak and average", peak: HOURS.map(() => 3), average: HOURS.map(() => 2), peak_in_range: 9 },
      heatmap: { rows: ["Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"], columns: [], values: [] },
    },
    traffic: {
      available: true, bytes_down: 5_000_000_000, bytes_up: 400_000_000, series_down: zeros(), series_up: zeros(),
      top_networks: [{ key: "br-guest", name: "Guest", bytes_down: 5_000_000_000, bytes_up: 400_000_000 }],
      top_packages: [], sessions_with_traffic: 9, source: "accounting_samples",
    },
    sign_in_outcomes: {
      available: true, total: 25, verified: 20,
      by_result: [{ result: "VERIFIED", count: 20 }, { result: "CREDENTIAL_MISMATCH", count: 5 }],
      series_verified: zeros(), series_failed: zeros(),
      latency: { samples: 25, p50_ms: 140, p95_ms: 410 },
      covered: "Room sign-in attempts",
      not_recorded: ["VOUCHER failures", "ACCOUNT failures", "OTP failures"],
    },
    packages: {
      available: true, active_now: [{ package_id: "p1", name: "Free Wi-Fi", code: "FREE", count: 4 }],
      grants: [{ package_id: "p1", name: "Free Wi-Fi", count: 9 }], terminations: [{ reason: "EXPIRED", count: 2 }],
    },
    pms: {
      available: true, active_interfaces: 1, ready_interfaces: 1, events_today: 40, events_applied_today: 40,
      events_needing_review: 0, historical_exceptions: 0,
      interfaces: [{
        pms_interface_id: "i1", display_label: "Opera", lifecycle_state: "ACTIVE", published: true,
        transport_status: "CONNECTED", sync_status: "IN_SYNC", room_auth_ready: true, in_house_stays: 120,
        pending_events: 0, review_events: 0, last_heartbeat_at: "2026-09-24T13:59:00Z",
      }],
      occupancy: { available: true, in_house: 120, with_internet: 60, arrivals_today: 12, departures_today: 9, posting_allowed: 0 },
      postings: { available: false, reason: "charges_not_enabled", posted_today: 0, failed_today: 0, pending: 0, review_open: 0, unknown_open: 0 },
    },
    networks: {
      available: true,
      rows: [{
        id: "g", name: "Guest", enabled: true, vlan_id: 20, subnet_cidr: "10.20.0.0/22", bridge_name: "br-guest",
        dhcp_mode: "local", dns_mode: "appliance", dns_servers: [], captive_portal_enabled: true,
        internet_access_enabled: true, pool_size: 1009, devices_online: 7, leases_known: true, active_leases: 30,
        leases_in_pool: 28, expiring_soon: 2, utilisation_pct: 2.8, traffic_known: true, bytes_down: 5_000_000_000, bytes_up: 400_000_000,
      }],
    },
    dhcp: {
      available: true, server_configured: true, server_healthy: true, leases_available: true, active_leases: 30,
      unmatched_leases: 0, expiring_soon: 2, expiring_within_seconds: 600, pools: [], captive_option_networks: ["Guest"],
      local_dhcp_networks: 1,
    },
    dns: { available: true, resolver_state: "healthy", resolver_healthy: true, networks: [{ network: "Guest", mode: "appliance", servers: [] }], statistics_collected: false },
    services: {
      available: true, overall: "healthy", counts: { healthy: 2 },
      services: [
        { name: "scd", label: "Session controller", state: "healthy", health_ok: true, critical: true, restart_count: 0, failures_in_range: 0, manual_restarts_in_range: 0 },
        { name: "kea", label: "DHCP server", state: "healthy", health_ok: true, critical: true, restart_count: 0, failures_in_range: 1, manual_restarts_in_range: 0 },
      ],
    },
    appliance: {
      version: "0.1.0-edge",
      license: { available: true, state: "Active", installed: true, valid_until: "2027-01-01T00:00:00Z" },
      network: { available: true, wan_address: "172.21.60.25/24", wan_mode: "static", lan_address: "10.10.0.1/24", internet_reachable: true },
      revisions: { available: true, latest_active: { seq: 12, state: "active", confirmed_at: "2026-09-20T10:00:00Z" } },
      resources: {
        available: true, uptime_seconds: 90000,
        load: { one: 0.4, five: 0.3, fifteen: 0.2, cpus: 4, one_per_cpu: 0.1 },
        memory: { total_bytes: 8e9, available_bytes: 6e9, used_bytes: 2e9, used_pct: 25 },
        disks: [{ path: "/", total_bytes: 1e11, used_bytes: 4e10, available_bytes: 6e10, used_pct: 40 }],
      },
    },
    attention: [],
  };
}

function allUnavailable(): any {
  const denied = { available: false, reason: "not_permitted_for_role" };
  const s = healthy();
  s.guests = { ...s.guests, available: false, reason: "sessions_unreadable" };
  s.traffic = { ...s.traffic, available: false, reason: "range_too_large_for_time_budget" };
  s.sign_in_outcomes = { ...s.sign_in_outcomes, available: false, reason: "sign_in_attempts_unreadable" };
  s.packages = { ...s.packages, available: false, reason: "entitlements_unreadable" };
  s.networks = { available: false, reason: "guest_networks_unreadable", rows: [] };
  s.dhcp = { ...s.dhcp, ...denied };
  s.dns = { ...s.dns, ...denied };
  s.services = { ...denied, services: [] };
  s.appliance = {
    version: "0.1.0-edge",
    license: { ...denied, installed: false },
    network: { ...denied },
    revisions: { ...denied },
    resources: { available: false, reason: "unavailable_on_this_platform", disks: [] },
  };
  return s;
}

const HEALTH = { service: "edged", version: "0.1.0-edge", site_id: "s", status: "ok", db: true, scd: true, license_state: "Active", license_installed: true, sync_outbox: { enabled: false, mode: "LICENSING_ONLY" } };

function route(overview: (range: string) => any, health: any = HEALTH) {
  get.mockImplementation((path: string) => {
    if (path.startsWith("/reports/overview")) {
      const range = new URL(`http://x${path}`).searchParams.get("range") ?? "24h";
      return Promise.resolve(overview(range));
    }
    if (path === "/health") return Promise.resolve(health);
    if (path === "/capabilities") return Promise.resolve({ surfaces: [] });
    if (path === "/setup/status") return Promise.resolve({ assignment: { site_name: "Coral Sea" }, hardware: { hostname: "sc-01" } });
    return Promise.reject(new Error(`unexpected ${path}`));
  });
}

beforeEach(() => {
  get.mockReset();
});

describe("the overview", () => {
  it("renders the measured figures and the property context", async () => {
    route(() => healthy());
    render(<DashboardPage />);
    expect(await screen.findByText("Coral Sea · sc-01")).toBeInTheDocument();
    expect((await screen.findAllByText("5.40 GB")).length).toBeGreaterThan(0);
    expect(screen.getByText("80%")).toBeInTheDocument(); // 20 of 25 room checks
    expect(screen.getByText("Name or reservation did not match")).toBeInTheDocument();
    expect(screen.getByText(/median 140 ms/)).toBeInTheDocument();
    expect(screen.getByTestId("resources")).toBeInTheDocument();
  });

  it("says all is normal when nothing needs attention, and does not show the list", async () => {
    route(() => healthy());
    render(<DashboardPage />);
    expect(await screen.findByTestId("all-normal")).toHaveTextContent("All systems normal");
    expect(screen.queryByTestId("attention")).toBeNull();
  });

  it("lists attention items, each linking to the screen that fixes it", async () => {
    route(() => ({
      ...healthy(),
      attention: [
        { id: "pms-not-ready:Opera", severity: "err", title: "Opera is not ready for room sign-in", href: "/pms-interfaces", action: "Open PMS connection" },
        { id: "pool:Guest", severity: "warn", title: "Address pool on Guest is 91% full", href: "/network/dhcp", action: "Open DHCP" },
      ],
    }));
    render(<DashboardPage />);
    const list = await screen.findByTestId("attention");
    const pms = within(list).getByRole("link", { name: "Open PMS connection" });
    const dhcp = within(list).getByRole("link", { name: "Open DHCP" });
    expect(pms).toHaveAttribute("href", "/pms-interfaces");
    expect(dhcp).toHaveAttribute("href", "/network/dhcp");
    expect(within(list).getByText("Opera is not ready for room sign-in")).toBeInTheDocument();
    expect(screen.queryByTestId("all-normal")).toBeNull();
  });

  it("asks the server for the chosen range when the range changes", async () => {
    route(() => healthy());
    render(<DashboardPage />);
    await screen.findAllByText("5.40 GB");
    fireEvent.click(screen.getByRole("radio", { name: "7 days" }));
    await waitFor(() => expect(get).toHaveBeenCalledWith("/reports/overview?range=7d"));
    fireEvent.click(screen.getByRole("radio", { name: "30 days" }));
    await waitFor(() => expect(get).toHaveBeenCalledWith("/reports/overview?range=30d"));
  });

  it("names what is not recorded instead of implying none happened", async () => {
    route(() => healthy());
    render(<DashboardPage />);
    const note = await screen.findByTestId("not-recorded");
    expect(note).toHaveTextContent("failed voucher sign-ins");
    expect(note).toHaveTextContent("failed guest-account sign-ins");
    expect(note).toHaveTextContent("failed one-time-code sign-ins");
    expect(screen.getByText(/Query and cache statistics are not collected/)).toBeInTheDocument();
  });
});

describe("unavailable blocks", () => {
  it("each block renders its own reason", async () => {
    route(() => allUnavailable());
    render(<DashboardPage />);
    await screen.findAllByText(/traffic history for this range is too large/);
    const notes = screen.getAllByTestId("block-unavailable").map((n) => n.textContent ?? "");
    const text = notes.join("\n");
    for (const phrase of [
      "Session records could not be read.",
      "The room sign-in attempt log could not be read.",
      "Package records could not be read.",
      "Your role does not include this information.",
      "Resource figures are only available on the appliance itself.",
    ]) {
      expect(text).toContain(phrase);
    }
    expect(screen.getByText("Guest network configuration could not be read.")).toBeInTheDocument();
  });

  it("draws no fabricated figures when the figures could not be read", async () => {
    route(() => allUnavailable());
    const { container } = render(<DashboardPage />);
    await screen.findAllByText(/traffic history for this range is too large/);
    const body = container.textContent ?? "";
    // No byte figure, and no zero standing in for an unknown count.
    expect(body).not.toMatch(/\b0 B\b/);
    expect(body).not.toMatch(/\d+(\.\d+)? (KB|MB|GB|TB)\b/);
    expect(body).not.toContain("0 different devices");
    // The four headline tiles show a dash, not a number.
    expect(screen.getAllByText("—").length).toBeGreaterThanOrEqual(3);
    expect(screen.queryByTestId("resources")).toBeNull();
    expect(screen.queryByTestId("services-grid")).toBeNull();
  });

  it("survives the generic list envelope the browser specs answer every request with", async () => {
    // e2e/sidebar-collapse and e2e/sweep-guest-accounts-and-nav mock every edged path as {data:[],meta}.
    // The page must render its unavailable surfaces there, not throw a client-side exception.
    get.mockImplementation(() => Promise.resolve({ data: [], meta: { has_more: false } }));
    render(<DashboardPage />);
    expect((await screen.findAllByTestId("block-unavailable")).length).toBeGreaterThan(3);
    expect(screen.getByRole("heading", { name: "Overview" })).toBeInTheDocument();
  });

  it("a response missing whole blocks degrades to unavailable, not to a crash or zeros", () => {
    const n = normalizeOverview({ buckets: HOURS } as any);
    for (const b of [n.guests, n.traffic, n.sign_in_outcomes, n.packages, n.pms, n.networks, n.dhcp, n.dns, n.services, n.appliance.resources]) {
      expect(b.available).toBe(false);
    }
    expect(n.sign_in_outcomes.not_recorded).toHaveLength(3);
  });
});

describe("the source", () => {
  const read = (p: string) => readFileSync(join(process.cwd(), p), "utf8");
  const src = read("app/(app)/dashboard/page.tsx") + read("app/(app)/dashboard/overview-blocks.tsx") + read("lib/api/dashboard.ts");

  it("no longer claims the usage records cannot be read", () => {
    // They can (edged was granted SELECT on them), and the traffic chart is built from them.
    expect(src).not.toMatch(/not permitted to read/i);
    expect(src).not.toMatch(/cannot read (the )?(usage|accounting)/i);
    expect(src).not.toContain("Why not data per hour?");
  });

  it("uses no internal vocabulary in operator text", () => {
    expect(src).not.toMatch(/iam_v2|Phase-?\d|T0\d{3}|D41/);
  });
});
