import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, waitFor, within, cleanup } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

// THE SYSTEM SCREENS (Diagnostics, Alerts, Activity, Appliance & licence, Backups, Operators), redesigned.
//
// What is asserted is behaviour an operator relies on, not markup: every page carries the menu label as its
// title under the "System" eyebrow; write actions are offered only to a role the server will let use them;
// the confirmations that exist on the server (reason + password for a restart, typed name + password for a
// restore, password for a backup) are collected and sent exactly as before; and the licence screen says in
// words that existing guest sessions survive a licence problem.

const get = vi.fn();
const post = vi.fn();
const put = vi.fn();
const del = vi.fn();
vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return {
    ...actual,
    api: {
      get: (...a: any[]) => get(...a),
      post: (...a: any[]) => post(...a),
      put: (...a: any[]) => put(...a),
      del: (...a: any[]) => del(...a),
      postRaw: vi.fn(),
    },
  };
});
vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn(), replace: vi.fn() }),
  useSearchParams: () => new URLSearchParams(""),
}));

beforeEach(() => { get.mockReset(); post.mockReset(); put.mockReset(); del.mockReset(); });
afterEach(() => { cleanup(); vi.resetModules(); });

function routes(map: Record<string, unknown>, roles: string[]) {
  get.mockImplementation((path: string) => {
    if (path === "/auth/whoami") return Promise.resolve({ roles, operator_id: "me" });
    for (const [prefix, body] of Object.entries(map)) {
      if (path === prefix || path.startsWith(prefix + "?")) return Promise.resolve(body);
    }
    return Promise.resolve({});
  });
}

const SERVICE = {
  service: "scd", state: "healthy", process_state: "running", health_ok: true, health_detail: "ok",
  consecutive_failures: 0, restart_count: 0, restarts_in_window: 0, restart_window_secs: 300,
  backoff_level: 0, backoff_ms: 0, next_retry_at: null, first_failure_at: null, last_failure_at: null,
  last_failure_reason: "", last_exit_code: null, last_exit_signal: "", last_healthy_at: new Date().toISOString(),
  last_recovery_at: null, time_since_healthy_s: 0, degraded_dependency: "", critical: true,
  updated_at: new Date().toISOString(),
};
const SUMMARY = {
  overall: "healthy", counts: { healthy: 1 }, services: [SERVICE],
  boot: { converged: false, alert_open: false, pending: ["kea"] }, generated_at: new Date().toISOString(),
};

describe("Diagnostics", () => {
  it("is titled by its menu label and says when the appliance is still starting", async () => {
    routes({ "/diagnostics/services": SUMMARY }, ["site_admin"]);
    const Page = (await import("@/app/(app)/health/page")).default;
    render(<Page />);
    expect(screen.getByRole("heading", { level: 1, name: "Diagnostics" })).toBeTruthy();
    expect(screen.getByText("System")).toBeTruthy();
    expect(await screen.findByText(/still starting after boot/i)).toBeTruthy();
  });

  it("restart asks for the impact, a reason and a password, and sends exactly those", async () => {
    routes({ "/diagnostics/services": SUMMARY }, ["site_admin"]);
    post.mockResolvedValue({});
    const Page = (await import("@/app/(app)/health/page")).default;
    render(<Page />);
    await userEvent.click(await screen.findByRole("button", { name: "Restart scd" }));

    const dialog = await screen.findByRole("dialog");
    // The per-service impact is the decision the operator is making.
    expect(within(dialog).getByText(/Every guest currently online is disconnected/)).toBeTruthy();
    const confirm = within(dialog).getByRole("button", { name: "Restart now" }) as HTMLButtonElement;
    expect(confirm.disabled).toBe(true);

    await userEvent.type(within(dialog).getByLabelText(/Why are you restarting it/), "stuck");
    await userEvent.type(within(dialog).getByLabelText(/Confirm your password/), "pw-123");
    await userEvent.click(confirm);

    await waitFor(() => expect(post).toHaveBeenCalledWith("/diagnostics/services/scd/restart", { reason: "stuck", password: "pw-123" }));
  });

  it("offers a read-only role the logs but not Recheck or Restart, which the server refuses", async () => {
    routes({ "/diagnostics/services": SUMMARY }, ["site_viewer"]);
    const Page = (await import("@/app/(app)/health/page")).default;
    render(<Page />);
    expect(await screen.findByRole("button", { name: "Logs for scd" })).toBeTruthy();
    await screen.findByText(/can view diagnostics and read logs/i);
    expect(screen.queryByRole("button", { name: "Restart scd" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Recheck scd" })).toBeNull();
  });
});

describe("Alerts", () => {
  it("is titled Alerts and says why an empty queue is good news", async () => {
    get.mockResolvedValue({ data: [], meta: { has_more: false } });
    const { OperationalAlertsView } = await import("@/components/phase3/operational-alerts-view");
    render(<OperationalAlertsView />);
    expect(screen.getByRole("heading", { level: 1, name: "Alerts" })).toBeTruthy();
    expect(await screen.findByText("No open alerts")).toBeTruthy();
    expect(screen.getByText("Every checkout was handled with the configured policy.")).toBeTruthy();
  });
});

describe("Activity", () => {
  const ROWS = {
    data: [
      { ts: "2026-09-20T10:00:00Z", actor_type: "operator", actor_id: "op-1", action: "backup.downloaded", payload: { file: "db-1.sql.gz" } },
      { ts: "2026-09-20T09:00:00Z", actor_type: "system", action: "health.recheck" },
    ],
  };

  it("filters to security events with a count, and keeps the raw payload behind a disclosure", async () => {
    routes({ "/audit": ROWS, "/operators": { data: [{ id: "op-1", display_name: "Mona" }] } }, ["site_admin"]);
    const Page = (await import("@/app/(app)/audit/page")).default;
    render(<Page />);
    expect(screen.getByRole("heading", { level: 1, name: "Activity" })).toBeTruthy();
    await screen.findByText("2 entries");

    const chips = screen.getByRole("radiogroup", { name: "Show" });
    const security = within(chips).getByRole("radio", { name: /Security/ });
    await userEvent.click(security);
    expect(security.getAttribute("aria-checked")).toBe("true");
    expect(await screen.findByText("1 entry")).toBeTruthy();

    // Expand the row: the exact record is there, and the payload sits behind "Raw payload".
    // (The page's Tips lightbulb is also a collapsed disclosure; the first ROW is what is wanted here.)
    const row = screen
      .getAllByRole("button", { expanded: false })
      .filter((b) => !(b.getAttribute("aria-label") ?? "").startsWith("Tips"))[0];
    await userEvent.click(row);
    expect(screen.getByText("backup.downloaded")).toBeTruthy();
    const disclosure = screen.getByText("Raw payload");
    expect(disclosure.closest("details")).toBeTruthy();
  });
});

describe("Backups", () => {
  const ART = {
    data: [
      { name: "db-20260921T095526Z.sql.gz", size_bytes: 1024, modified_at: "2026-09-21T09:55:26Z", verified_at: "2026-09-21T10:00:00Z", verified_tables: 90 },
      { name: "db-20260920T095526Z.sql.gz", size_bytes: 1024, modified_at: "2026-09-20T09:55:26Z", verified_at: null },
    ],
  };
  const base = {
    "/backups/artifacts": ART,
    "/backups/maintenance": { active: false },
    "/backups/last-restore": { present: false },
    "/backups/health": { retention_readable: false, timer_active: true, timer_readable: true, database_backups: 2 },
    "/backups/settings": { retention: [], schedule_known: false, config_present: false },
  };

  it("offers Restore only for a verified backup", async () => {
    routes(base, ["site_admin"]);
    const Page = (await import("@/app/(app)/backups/page")).default;
    render(<Page />);
    expect(await screen.findByRole("button", { name: "Restore db-20260921T095526Z.sql.gz" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Restore db-20260920T095526Z.sql.gz" })).toBeNull();
    expect(screen.getByText("This property can be recovered from its latest backup")).toBeTruthy();
  });

  it("restore requires the backup's name typed out and a password, and sends both", async () => {
    routes(base, ["site_admin"]);
    post.mockResolvedValue({});
    const Page = (await import("@/app/(app)/backups/page")).default;
    render(<Page />);
    const name = "db-20260921T095526Z.sql.gz";
    await userEvent.click(await screen.findByRole("button", { name: `Restore ${name}` }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText(/current data will be replaced/)).toBeTruthy();
    expect(within(dialog).getByText(/Take a fresh safety copy/)).toBeTruthy();

    const go = within(dialog).getByRole("button", { name: "Restore this backup" }) as HTMLButtonElement;
    await userEvent.type(within(dialog).getByLabelText(/Confirm your password/), "pw");
    expect(go.disabled, "a password alone must not be enough").toBe(true);
    await userEvent.type(within(dialog).getByLabelText(/to confirm/), name);
    expect(go.disabled).toBe(false);
    await userEvent.click(go);

    await waitFor(() => expect(post).toHaveBeenCalledWith(
      `/backups/artifacts/${encodeURIComponent(name)}/restore`, { password: "pw", confirm: name }));
    // Progress stays on screen until the appliance reports the outcome.
    expect(await within(dialog).findByText(/this window stays open until it finishes/i)).toBeTruthy();
  });

  it("Back up now collects the password in a dialog and sends it", async () => {
    routes(base, ["site_admin"]);
    post.mockResolvedValue({});
    const Page = (await import("@/app/(app)/backups/page")).default;
    render(<Page />);
    await screen.findByText("Available backups");
    await userEvent.click(await screen.findByRole("button", { name: /Back up now/ }));
    const dialog = await screen.findByRole("dialog");
    await userEvent.type(within(dialog).getByLabelText(/Confirm your password/), "pw");
    await userEvent.click(within(dialog).getByRole("button", { name: "Back up now" }));
    await waitFor(() => expect(post).toHaveBeenCalledWith("/backups/run", { password: "pw" }));
  });

  it("shows a read-only role no write action", async () => {
    routes(base, ["site_viewer"]);
    const Page = (await import("@/app/(app)/backups/page")).default;
    render(<Page />);
    await screen.findByText(/can see and download backups/);
    expect(screen.queryByRole("button", { name: /Back up now/ })).toBeNull();
    expect(screen.queryByRole("button", { name: /^Restore / })).toBeNull();
    expect(screen.getAllByRole("link", { name: /^Download / }).length).toBe(2);
  });
});

describe("Operators", () => {
  const OPS = {
    data: [
      { id: "me", email: "me@hotel", status: "active", created_at: "2026-01-01T00:00:00Z", roles: ["site_admin"] },
      { id: "o2", email: "desk@hotel", status: "active", created_at: "2026-01-01T00:00:00Z", roles: ["front_office_operator"] },
    ],
  };

  it("marks the signed-in operator and does not promise a re-enable the API does not offer", async () => {
    routes({ "/operators": OPS }, ["site_admin"]);
    const Page = (await import("@/app/(app)/operators/page")).default;
    render(<Page />);
    expect(await screen.findByText("You")).toBeTruthy();
    await userEvent.click(screen.getByRole("button", { name: "Disable desk@hotel" }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).queryByText(/can be re-enabled/)).toBeNull();
    expect(within(dialog).getByText(/no way to re-enable/i)).toBeTruthy();
  });

  it("adding a role is confirmed before the request is sent, and sends the same body", async () => {
    routes({ "/operators": OPS }, ["site_admin"]);
    post.mockResolvedValue({});
    const Page = (await import("@/app/(app)/operators/page")).default;
    render(<Page />);
    const select = await screen.findByLabelText("Give desk@hotel another role");
    await userEvent.selectOptions(select, "site_viewer");
    // Nothing is granted by the choice alone.
    expect(post).not.toHaveBeenCalled();
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText(/Give this role\?/)).toBeTruthy();
    await userEvent.click(within(dialog).getByRole("button", { name: "Add role" }));
    await waitFor(() => expect(post).toHaveBeenCalledWith("/operators/o2/roles", { role: "site_viewer" }));
  });

  it("new operators default to Site viewer", async () => {
    routes({ "/operators": OPS }, ["site_admin"]);
    const Page = (await import("@/app/(app)/operators/page")).default;
    render(<Page />);
    await userEvent.click(await screen.findByRole("button", { name: /Add operator/ }));
    const dialog = await screen.findByRole("dialog");
    expect((within(dialog).getByLabelText("Role") as HTMLSelectElement).value).toBe("site_viewer");
  });
});

describe("Licence", () => {
  const STATUS = {
    serial: "VN-0001",
    hardware: { serial: "VN-0001", wan_mac: "aa:bb:cc:dd:ee:ff" },
    activation_status: "activated",
    enrolled: true,
    api_mtls: { mtls_ready: true },
    nats_mtls: { connected: false },
    network: { central_https_443: true },
    assignment: { assigned: true, tenant_name: "Acme Hotels", site_name: "Nile" },
    license: { state: "Expired", valid_until: "2026-09-01T00:00:00Z", max_concurrent_online_guests: 100, current_online_guests: 85, usage_percent: 85 },
  };

  it("says existing guest sessions survive, shows the capacity in words and never calls the cloud link broken", async () => {
    routes({ "/setup/status": STATUS, "/license": { state: "Expired" } }, ["site_admin"]);
    const { LicenseSection } = await import("@/app/(app)/appliance/license-section");
    render(<LicenseSection />);
    expect(await screen.findByText(/New guest logins are refused; existing guest sessions are not dropped/)).toBeTruthy();
    expect(screen.getByText("VN-0001")).toBeTruthy();
    expect(screen.getByText("aa:bb:cc:dd:ee:ff")).toBeTruthy();
    expect(screen.getByText(/85% in use/)).toBeTruthy();
    expect(screen.getByText(/nearly full/)).toBeTruthy();
    expect(screen.getByText("Licensing only")).toBeTruthy();
    expect(screen.getByRole("button", { name: /Upload licence file/ })).toBeTruthy();
    // Retired words never appear as current.
    const text = document.body.textContent ?? "";
    for (const retired of ["Subscription", "Trial", "Plan limits", "Fleet telemetry"]) {
      expect(text, `"${retired}" is a retired word`).not.toContain(retired);
    }
  });
});
